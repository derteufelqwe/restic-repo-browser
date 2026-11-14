// Package jobs runs each repository's cleanup job (forget, then optionally
// prune) on its cron schedule or on demand, and records every run with its log.
// Cleanup runs share the single task slot with syncs, so only one runs at a time.
package jobs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"restic-repo-browser/internal/backend"
	"restic-repo-browser/internal/domain"
	"restic-repo-browser/internal/schedule"
	"restic-repo-browser/internal/tasks"
)

// Errors returned by RunNow.
var (
	ErrNoJob      = errors.New("this repository has no cleanup job")
	ErrNotStarted = errors.New("the job runner is not running")
)

// Store is the part of the persistence layer the runner needs.
type Store interface {
	Repo(ctx context.Context, id int64) (domain.Repo, error)
	RepoPassword(ctx context.Context, id int64) (string, error)
	Job(ctx context.Context, repoID int64) (*domain.Job, error)
	Jobs(ctx context.Context) (map[int64]domain.Job, error)
	StartRun(ctx context.Context, repoID int64, at time.Time) (int64, error)
	FinishRun(ctx context.Context, id int64, at time.Time, status domain.RunStatus, summary, log string) error
	AbandonRunningRuns(ctx context.Context, at time.Time) error
}

// Backend is the part of the repository backend the runner needs.
type Backend interface {
	Forget(ctx context.Context, ref backend.Ref, keep domain.KeepPolicy) (backend.ForgetResult, error)
	Prune(ctx context.Context, ref backend.Ref, log io.Writer) (backend.PruneResult, error)
}

// Syncer refreshes the cached data of a repository after a run changed it.
type Syncer interface {
	Refresh(ctx context.Context, id int64) error
}

// Runner executes cleanup jobs.
type Runner struct {
	store   Store
	backend Backend
	syncer  Syncer
	tasks   *tasks.Manager
	log     *slog.Logger
	now     func() time.Time
	tick    time.Duration

	mu   sync.Mutex
	base context.Context // set by Start; runs started by RunNow inherit it
	wg   sync.WaitGroup
}

// New creates a Runner.
func New(st Store, be Backend, sy Syncer, tm *tasks.Manager, log *slog.Logger) *Runner {
	return &Runner{store: st, backend: be, syncer: sy, tasks: tm, log: log, now: time.Now, tick: 15 * time.Second}
}

// Start launches the scheduler. It returns immediately; the scheduler and any
// running jobs stop when ctx ends. Call Wait to block until they have.
func (r *Runner) Start(ctx context.Context) {
	r.mu.Lock()
	r.base = ctx
	r.mu.Unlock()

	if err := r.store.AbandonRunningRuns(ctx, r.now()); err != nil {
		r.log.Error("cleaning up interrupted runs", "err", err)
	}
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		r.schedule(ctx)
	}()
}

// Wait blocks until the scheduler and all runs have finished.
func (r *Runner) Wait() { r.wg.Wait() }

// Running reports whether a cleanup run for the repository is in progress.
func (r *Runner) Running(repoID int64) bool {
	info, ok := r.tasks.Current()
	return ok && info.Kind == tasks.KindCleanup && info.RepoID == repoID
}

// RunNow starts the repository's cleanup job in the background. It fails with a
// *tasks.BusyError when another task (a sync or any cleanup) is running.
func (r *Runner) RunNow(ctx context.Context, repoID int64) error {
	job, err := r.store.Job(ctx, repoID)
	if err != nil {
		return err
	}
	if job == nil {
		return ErrNoJob
	}
	r.mu.Lock()
	base := r.base
	r.mu.Unlock()
	if base == nil {
		return ErrNotStarted
	}
	title, err := r.title(ctx, repoID)
	if err != nil {
		return err
	}
	task, err := r.tasks.TryStart(tasks.KindCleanup, title, repoID)
	if err != nil {
		return err
	}
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		r.execute(base, *job, task)
	}()
	return nil
}

func (r *Runner) title(ctx context.Context, repoID int64) (string, error) {
	repo, err := r.store.Repo(ctx, repoID)
	if err != nil {
		return "", err
	}
	return "Cleaning up " + repo.Name, nil
}

// schedule starts jobs whose cron time passed since the previous tick. A run
// that was due while the server was down is skipped, not made up. A run that is
// due while another task runs waits for the slot.
func (r *Runner) schedule(ctx context.Context) {
	ticker := time.NewTicker(r.tick)
	defer ticker.Stop()
	last := r.now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		now := r.now()
		jobs, err := r.store.Jobs(ctx)
		if err != nil {
			r.log.Error("loading jobs", "err", err)
			continue
		}
		for _, job := range jobs {
			if !job.Enabled {
				continue
			}
			sch, err := schedule.Parse(job.Cron)
			if err != nil {
				r.log.Warn("job has an invalid schedule", "repo", job.RepoID, "err", err)
				continue
			}
			if due := sch.Next(last); !due.After(now) {
				r.wg.Add(1)
				go func() {
					defer r.wg.Done()
					r.runScheduled(ctx, job)
				}()
			}
		}
		last = now
	}
}

func (r *Runner) runScheduled(ctx context.Context, job domain.Job) {
	title, err := r.title(ctx, job.RepoID)
	if err != nil {
		r.log.Warn("scheduled cleanup skipped", "repo", job.RepoID, "err", err)
		return
	}
	task, err := r.tasks.Start(ctx, tasks.KindCleanup, title, job.RepoID)
	if err != nil {
		return // shutting down
	}
	r.execute(ctx, job, task)
}

// execute performs one run inside task and records it.
func (r *Runner) execute(ctx context.Context, job domain.Job, task *tasks.Task) {
	started := r.now()
	ctx = tasks.WithTask(ctx, task)
	runID, err := r.store.StartRun(ctx, job.RepoID, started)
	if err != nil {
		r.log.Error("recording run start", "repo", job.RepoID, "err", err)
		task.Finish(err)
		return
	}
	log := &safeBuffer{}
	status, summary := domain.RunOK, ""
	runErr := r.perform(ctx, job, log, &summary)
	if runErr != nil {
		status = domain.RunFailed
		summary = backend.UserMessage(runErr)
		fmt.Fprintf(log, "\nError: %v\nJob stopped. It will try again at the next scheduled run.\n", runErr)
	}
	// Record the outcome even when the server is shutting down.
	rec, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := r.store.FinishRun(rec, runID, r.now(), status, summary, log.String()); err != nil {
		r.log.Error("recording run result", "repo", job.RepoID, "err", err)
	}
	r.log.Info("cleanup finished", "repo", job.RepoID, "status", status, "summary", summary)

	// The repository changed: bring the saved data up to date within the same task.
	tasks.Step(ctx, "Syncing the saved data")
	if err := r.syncer.Refresh(ctx, job.RepoID); err != nil && ctx.Err() == nil {
		r.log.Warn("sync after cleanup failed", "repo", job.RepoID, "err", err)
	}
	task.Finish(runErr)
}

// perform runs the steps of a job and sets summary on success.
func (r *Runner) perform(ctx context.Context, job domain.Job, log *safeBuffer, summary *string) error {
	repo, err := r.store.Repo(ctx, job.RepoID)
	if err != nil {
		return err
	}
	ref, err := backend.Resolve(ctx, r.store, repo)
	if err != nil {
		return err
	}

	tasks.Step(ctx, "Forgetting snapshots that the keep rules do not match")
	fmt.Fprintf(log, "Forgetting snapshots (keep rules: %s)\n", keepRules(job.Keep))
	res, err := r.backend.Forget(ctx, ref, job.Keep)
	if err != nil {
		return err
	}
	writeForgetReport(log, res)
	parts := []string{fmt.Sprintf("Forgot %d snapshot%s", len(res.Removed), plural(len(res.Removed)))}

	if job.Prune {
		tasks.Step(ctx, "Pruning unused data (this can take a while)")
		fmt.Fprint(log, "\nPruning unused data\n")
		pruned, err := r.backend.Prune(ctx, ref, log)
		if err != nil {
			return err
		}
		if pruned.Freed != "" {
			parts = append(parts, "freed "+pruned.Freed)
		}
	}
	*summary = strings.Join(parts, ", ")
	return nil
}

// keepRules lists the rules of a policy for the run log ("last 7, daily 14").
func keepRules(k domain.KeepPolicy) string {
	var parts []string
	for _, r := range []struct {
		name string
		n    int
	}{{"last", k.Last}, {"daily", k.Daily}, {"weekly", k.Weekly}, {"monthly", k.Monthly}, {"yearly", k.Yearly}} {
		if r.n > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", r.name, r.n))
		}
	}
	return strings.Join(parts, ", ")
}

func writeForgetReport(w io.Writer, res backend.ForgetResult) {
	fmt.Fprintf(w, "keep %d snapshots\nremove %d snapshots", res.Kept, len(res.Removed))
	if len(res.Removed) == 0 {
		fmt.Fprintln(w)
		return
	}
	fmt.Fprint(w, ":\nID        Time                 Host        Paths\n")
	for _, s := range res.Removed {
		fmt.Fprintf(w, "%-9s %-20s %-11s %s\n", s.ShortID, s.Time.Format("2006-01-02 15:04:05"), s.Host, strings.Join(s.Paths, ", "))
	}
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// safeBuffer is a bytes.Buffer that tolerates writes from several goroutines
// (restic's stdout and stderr copiers).
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
