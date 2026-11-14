// Package cache keeps the SQLite copy of every repository's data up to date.
// The web UI only reads that copy; this package is the one place that asks
// the backend for snapshots, sizes and locks. A "sync" runs once a day, after each
// cleanup run and on demand, and only one task runs at a time.
package cache

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"restic-repo-browser/internal/backend"
	"restic-repo-browser/internal/domain"
	"restic-repo-browser/internal/store"
	"restic-repo-browser/internal/tasks"
)

// Store is the part of the persistence layer the refresher needs.
type Store interface {
	ListRepos(ctx context.Context) ([]domain.Repo, error)
	Repo(ctx context.Context, id int64) (domain.Repo, error)
	RepoPassword(ctx context.Context, id int64) (string, error)
	SyncSnapshots(ctx context.Context, repoID int64, snaps []domain.Snapshot) error
	SnapshotsWithoutSize(ctx context.Context, repoID int64) ([]string, error)
	SetSnapshotSize(ctx context.Context, repoID int64, id string, size, files int64) error
	ReplaceLocks(ctx context.Context, repoID int64, locks []domain.Lock) error
	SaveRefresh(ctx context.Context, id int64, r store.RefreshResult) error
	Setting(ctx context.Context, key, def string) (string, error)
}

// Backend is the part of the repository backend the refresher needs.
type Backend interface {
	Snapshots(ctx context.Context, ref backend.Ref) ([]domain.Snapshot, error)
	SnapshotSize(ctx context.Context, ref backend.Ref, snapshotID string) (backend.SnapshotSize, error)
	Locks(ctx context.Context, ref backend.Ref) ([]domain.Lock, error)
	RepoSize(ctx context.Context, ref backend.Ref) (int64, error)
}

// DefaultRefreshTime is the daily sync time until the user changes it.
const DefaultRefreshTime = "03:00"

// maxAge is how old the cache may be at startup before it is synced at once.
const maxAge = 24 * time.Hour

// ErrNotStarted is returned by Sync before Run has been called.
var ErrNotStarted = errors.New("the sync service is not running")

// Refresher syncs repositories into the cache.
type Refresher struct {
	store   Store
	backend Backend
	tasks   *tasks.Manager
	log     *slog.Logger
	now     func() time.Time

	mu      sync.Mutex
	base    context.Context // set by Run; user-started syncs run under it
	resched chan struct{}   // the daily time changed
	wg      sync.WaitGroup
}

// New creates a Refresher.
func New(st Store, be Backend, tm *tasks.Manager, log *slog.Logger) *Refresher {
	return &Refresher{store: st, backend: be, tasks: tm, log: log, now: time.Now, resched: make(chan struct{}, 1)}
}

// Reschedule tells the daily timer to re-read the sync time setting.
func (r *Refresher) Reschedule() {
	select {
	case r.resched <- struct{}{}:
	default:
	}
}

// Run does the daily sync (and one at startup when the cache is older than a
// day) until ctx ends.
func (r *Refresher) Run(ctx context.Context) {
	r.mu.Lock()
	r.base = ctx
	r.mu.Unlock()
	defer r.wg.Wait()

	if r.cacheIsStale(ctx) {
		r.runAsScheduled(ctx, "Syncing all repositories")
	}
	for ctx.Err() == nil {
		timer := time.NewTimer(time.Until(r.nextDaily(ctx)))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-r.resched:
			timer.Stop()
		case <-timer.C:
			r.log.Info("daily sync starting")
			r.runAsScheduled(ctx, "Syncing all repositories")
		}
	}
}

// runAsScheduled waits for a free slot, then syncs every repository.
func (r *Refresher) runAsScheduled(ctx context.Context, title string) {
	task, err := r.tasks.Start(ctx, tasks.KindSync, title, 0)
	if err != nil {
		return
	}
	r.syncAll(ctx, task)
}

func (r *Refresher) cacheIsStale(ctx context.Context) bool {
	repos, err := r.store.ListRepos(ctx)
	if err != nil {
		r.log.Error("startup sync check", "err", err)
		return false
	}
	for _, repo := range repos {
		if r.now().Sub(repo.RefreshedAt) > maxAge {
			return true
		}
	}
	return false
}

// SyncAll starts a sync of every repository in the background. It fails with a
// *tasks.BusyError when another task is running.
func (r *Refresher) SyncAll() error { return r.startSync(0) }

// Sync starts a sync of one repository in the background. It fails with a
// *tasks.BusyError when another task is running.
func (r *Refresher) Sync(repoID int64) error { return r.startSync(repoID) }

func (r *Refresher) startSync(repoID int64) error {
	r.mu.Lock()
	base := r.base
	r.mu.Unlock()
	if base == nil {
		return ErrNotStarted
	}
	title := "Syncing all repositories"
	if repoID != 0 {
		repo, err := r.store.Repo(base, repoID)
		if err != nil {
			return fmt.Errorf("load repository %d: %w", repoID, err)
		}
		title = "Syncing " + repo.Name
	}
	task, err := r.tasks.TryStart(tasks.KindSync, title, repoID)
	if err != nil {
		return err
	}
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		if repoID == 0 {
			r.syncAll(base, task)
			return
		}
		ctx := tasks.WithTask(base, task)
		err := r.Refresh(ctx, repoID)
		task.Finish(err)
	}()
	return nil
}

// syncAll refreshes every repository one after the other and finishes task.
func (r *Refresher) syncAll(ctx context.Context, task *tasks.Task) {
	repos, err := r.store.ListRepos(ctx)
	if err != nil {
		task.Finish(err)
		return
	}
	var failed []string
	for i, repo := range repos {
		if ctx.Err() != nil {
			task.Finish(ctx.Err())
			return
		}
		task.Step(fmt.Sprintf("Repository %d of %d", i+1, len(repos)))
		task.Progress(i, len(repos))
		rctx := tasks.WithTask(ctx, task)
		if err := r.Refresh(rctx, repo.ID); err != nil && ctx.Err() == nil {
			r.log.Warn("sync failed", "repo", repo.Name, "err", err)
			failed = append(failed, repo.Name)
		}
	}
	if len(failed) > 0 {
		task.Finish(fmt.Errorf("%d of %d repositories could not be read", len(failed), len(repos)))
		return
	}
	task.Finish(nil)
}

// nextDaily is the next occurrence of the configured sync time.
func (r *Refresher) nextDaily(ctx context.Context) time.Time {
	setting, err := r.store.Setting(ctx, store.SettingCacheRefreshTime, DefaultRefreshTime)
	if err != nil {
		r.log.Warn("read sync time", "err", err)
		setting = DefaultRefreshTime
	}
	next, err := NextDaily(r.now(), setting)
	if err != nil {
		r.log.Warn("invalid sync time, using default", "value", setting, "err", err)
		next, _ = NextDaily(r.now(), DefaultRefreshTime)
	}
	return next
}

// NextDaily returns the first time after now that has the given "HH:MM" clock time.
func NextDaily(now time.Time, hhmm string) (time.Time, error) {
	clock, err := time.Parse("15:04", hhmm)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid time %q, expected HH:MM", hhmm)
	}
	next := time.Date(now.Year(), now.Month(), now.Day(), clock.Hour(), clock.Minute(), 0, 0, now.Location())
	if !next.After(now) {
		next = next.AddDate(0, 0, 1)
	}
	return next, nil
}

// Refresh syncs one repository and records the outcome. It does not take the
// task slot itself: callers run it inside a task and pass a context carrying
// that task so progress can be reported. A failure to open the repository is
// stored with the repository (and returned); the previously cached snapshots
// are kept so the UI can still show them.
func (r *Refresher) Refresh(ctx context.Context, id int64) error {
	repo, err := r.store.Repo(ctx, id)
	if err != nil {
		return fmt.Errorf("load repository %d: %w", id, err)
	}
	ref, err := backend.Resolve(ctx, r.store, repo)
	if err != nil {
		return err
	}

	started := r.now()
	size, err := r.fetch(ctx, repo, ref)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	result := store.RefreshResult{At: started, Size: size}
	if err != nil {
		result.Err = backend.UserMessage(err)
		r.log.Warn("repository unreachable", "repo", repo.Name, "err", err)
	}
	if saveErr := r.store.SaveRefresh(ctx, id, result); saveErr != nil {
		return saveErr
	}
	if err != nil {
		return err
	}
	r.log.Info("synced", "repo", repo.Name, "took", r.now().Sub(started).Round(time.Millisecond))
	return nil
}

// fetch reads everything from the repository into the store and returns its size on disk.
func (r *Refresher) fetch(ctx context.Context, repo domain.Repo, ref backend.Ref) (int64, error) {
	tasks.Detail(ctx, repo.Name+": reading the snapshot list")
	snaps, err := r.backend.Snapshots(ctx, ref)
	if err != nil {
		return 0, err
	}
	if err := r.store.SyncSnapshots(ctx, repo.ID, snaps); err != nil {
		return 0, err
	}
	r.fillMissingSizes(ctx, repo, ref)

	tasks.Detail(ctx, repo.Name+": checking for locks")
	locks, err := r.backend.Locks(ctx, ref)
	if err != nil {
		r.log.Warn("reading locks failed", "repo", repo.Name, "err", err)
	} else if err := r.store.ReplaceLocks(ctx, repo.ID, locks); err != nil {
		return 0, err
	}
	tasks.Detail(ctx, repo.Name+": measuring the repository size")
	size, err := r.backend.RepoSize(ctx, ref)
	if err != nil {
		if !errors.Is(err, backend.ErrUnsupported) {
			r.log.Warn("measuring repository size failed", "repo", repo.Name, "err", err)
		}
		return repo.SizeBytes, nil // keep the last known size
	}
	return size, nil
}

// fillMissingSizes computes the size of snapshots that carry no summary
// (made by restic before 0.17). Snapshots never change, so this happens once per snapshot.
func (r *Refresher) fillMissingSizes(ctx context.Context, repo domain.Repo, ref backend.Ref) {
	ids, err := r.store.SnapshotsWithoutSize(ctx, repo.ID)
	if err != nil {
		r.log.Warn("listing snapshots without size", "err", err)
		return
	}
	for i, id := range ids {
		if ctx.Err() != nil {
			return
		}
		tasks.Detail(ctx, fmt.Sprintf("%s: computing snapshot sizes (%d of %d)", repo.Name, i+1, len(ids)))
		size, err := r.backend.SnapshotSize(ctx, ref, id)
		if err != nil {
			r.log.Warn("computing snapshot size failed", "snapshot", id, "err", err)
			continue
		}
		if err := r.store.SetSnapshotSize(ctx, repo.ID, id, size.Bytes, size.Files); err != nil {
			r.log.Warn("saving snapshot size", "snapshot", id, "err", err)
		}
	}
}
