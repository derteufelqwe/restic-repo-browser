package cache

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"restic-repo-browser/internal/backend"
	"restic-repo-browser/internal/domain"
	"restic-repo-browser/internal/store"
	"restic-repo-browser/internal/tasks"
)

// fakeStore is an in-memory Store.
type fakeStore struct {
	repo    domain.Repo
	snaps   []domain.Snapshot
	sizes   map[string]int64
	locks   []domain.Lock
	refresh store.RefreshResult
	setting string
}

func (f *fakeStore) ListRepos(context.Context) ([]domain.Repo, error) {
	return []domain.Repo{f.repo}, nil
}
func (f *fakeStore) Repo(context.Context, int64) (domain.Repo, error)    { return f.repo, nil }
func (f *fakeStore) RepoPassword(context.Context, int64) (string, error) { return "pw", nil }
func (f *fakeStore) SyncSnapshots(_ context.Context, _ int64, s []domain.Snapshot) error {
	f.snaps = s
	return nil
}
func (f *fakeStore) SnapshotsWithoutSize(context.Context, int64) ([]string, error) {
	var ids []string
	for _, s := range f.snaps {
		if s.Size == nil {
			if _, ok := f.sizes[s.ID]; !ok {
				ids = append(ids, s.ID)
			}
		}
	}
	return ids, nil
}
func (f *fakeStore) SetSnapshotSize(_ context.Context, _ int64, id string, size, _ int64) error {
	if f.sizes == nil {
		f.sizes = map[string]int64{}
	}
	f.sizes[id] = size
	return nil
}
func (f *fakeStore) ReplaceLocks(_ context.Context, _ int64, l []domain.Lock) error {
	f.locks = l
	return nil
}
func (f *fakeStore) SaveRefresh(_ context.Context, _ int64, r store.RefreshResult) error {
	f.refresh = r
	return nil
}
func (f *fakeStore) Setting(context.Context, string, string) (string, error) { return f.setting, nil }

type fakeBackend struct {
	snaps   []domain.Snapshot
	err     error
	sizeFor []string
	gate    chan struct{} // when set, Snapshots blocks until it is closed
}

func (f *fakeBackend) Snapshots(context.Context, backend.Ref) ([]domain.Snapshot, error) {
	if f.gate != nil {
		<-f.gate
	}
	return f.snaps, f.err
}
func (f *fakeBackend) SnapshotSize(_ context.Context, _ backend.Ref, id string) (backend.SnapshotSize, error) {
	f.sizeFor = append(f.sizeFor, id)
	return backend.SnapshotSize{Bytes: 777, Files: 3}, nil
}
func (f *fakeBackend) Locks(context.Context, backend.Ref) ([]domain.Lock, error) {
	return []domain.Lock{{ID: "l1", Host: "docker-01", Exclusive: true}}, nil
}
func (f *fakeBackend) RepoSize(context.Context, backend.Ref) (int64, error) { return 1234, nil }

func newRefresher(st *fakeStore, be *fakeBackend) (*Refresher, *tasks.Manager) {
	tm := tasks.NewManager()
	return New(st, be, tm, slog.New(slog.NewTextHandler(io.Discard, nil))), tm
}

var notFound = &backend.Error{Kind: backend.KindNotFound}

func i64(v int64) *int64 { return &v }

func TestRefreshStoresEverything(t *testing.T) {
	st := &fakeStore{repo: domain.Repo{ID: 1, Name: "r", Backend: domain.BackendLocal, Location: "/r"}}
	be := &fakeBackend{snaps: []domain.Snapshot{
		{ID: "new", ShortID: "n", Host: "h", Size: i64(50), Added: i64(5), Files: i64(2)},
		{ID: "old", ShortID: "o", Host: "h"}, // made by restic < 0.17: no summary
	}}
	r, _ := newRefresher(st, be)

	if err := r.Refresh(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if len(st.snaps) != 2 || st.snaps[0].SizeBytes() != 50 {
		t.Errorf("snapshots = %+v", st.snaps)
	}
	if len(be.sizeFor) != 1 || be.sizeFor[0] != "old" || st.sizes["old"] != 777 {
		t.Errorf("size calls = %v, sizes = %v", be.sizeFor, st.sizes)
	}
	if len(st.locks) != 1 || st.locks[0].Host != "docker-01" || !st.locks[0].Exclusive {
		t.Errorf("locks = %+v", st.locks)
	}
	if st.refresh.Err != "" || st.refresh.Size != 1234 {
		t.Errorf("refresh result = %+v", st.refresh)
	}

	// A second refresh must not recompute the already known size.
	be.sizeFor = nil
	if err := r.Refresh(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if len(be.sizeFor) != 0 {
		t.Errorf("size recomputed: %v", be.sizeFor)
	}
}

func TestFailedRefreshKeepsCachedSnapshotsAndSyncAllReportsIt(t *testing.T) {
	st := &fakeStore{repo: domain.Repo{ID: 1, Name: "r", Location: "/r"}, snaps: []domain.Snapshot{{ID: "cached"}}}
	r, tm := newRefresher(st, &fakeBackend{err: notFound})

	if err := r.Refresh(context.Background(), 1); !errors.Is(err, backend.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
	if len(st.snaps) != 1 || st.snaps[0].ID != "cached" {
		t.Errorf("cached snapshots were touched: %+v", st.snaps)
	}
	if st.refresh.Err != backend.UserMessage(notFound) {
		t.Errorf("stored error = %q", st.refresh.Err)
	}

	task, _ := tm.TryStart(tasks.KindSync, "Syncing all repositories", 0)
	r.syncAll(context.Background(), task)
	if last := tm.Last(); last == nil || !strings.Contains(last.Err, "1 of 1 repositories could not be read") {
		t.Errorf("last = %+v", last)
	}
}

func TestSyncOnlyOneAtATime(t *testing.T) {
	st := &fakeStore{repo: domain.Repo{ID: 1, Name: "r", Location: "/r", RefreshedAt: time.Now()}}
	gate := make(chan struct{})
	r, tm := newRefresher(st, &fakeBackend{snaps: []domain.Snapshot{{ID: "a"}}, gate: gate})

	if err := r.Sync(1); !errors.Is(err, ErrNotStarted) {
		t.Fatalf("before Run: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan struct{})
	go func() { r.Run(ctx); close(finished) }()
	waitFor(t, func() bool { r.mu.Lock(); defer r.mu.Unlock(); return r.base != nil })

	if err := r.Sync(1); err != nil {
		t.Fatal(err)
	}
	var busy *tasks.BusyError
	if err := r.Sync(1); !errors.As(err, &busy) || busy.Current.Title != "Syncing r" {
		t.Errorf("second Sync: %v", err)
	}
	if err := r.SyncAll(); !errors.As(err, &busy) {
		t.Errorf("SyncAll while busy: %v", err)
	}
	close(gate)
	waitFor(t, func() bool { _, running := tm.Current(); return !running })
	if last := tm.Last(); last == nil || last.Err != "" {
		t.Errorf("last = %+v", last)
	}
	cancel()
	<-finished
}

func TestStartupSyncWhenCacheIsStale(t *testing.T) {
	st := &fakeStore{repo: domain.Repo{ID: 1, Name: "r", Location: "/r"}, setting: "03:00"} // never refreshed
	r, tm := newRefresher(st, &fakeBackend{})
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan struct{})
	go func() { r.Run(ctx); close(finished) }()
	waitFor(t, func() bool { return tm.Last() != nil })
	cancel()
	<-finished
	if st.refresh.At.IsZero() {
		t.Error("stale cache must be synced at startup")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestNextDaily(t *testing.T) {
	loc := time.UTC
	now := time.Date(2026, 11, 11, 14, 20, 0, 0, loc)
	next, err := NextDaily(now, "03:00")
	if err != nil || !next.Equal(time.Date(2026, 11, 12, 3, 0, 0, 0, loc)) {
		t.Errorf("next = %v, %v", next, err)
	}
	next, _ = NextDaily(now, "18:30")
	if !next.Equal(time.Date(2026, 11, 11, 18, 30, 0, 0, loc)) {
		t.Errorf("next = %v", next)
	}
	if _, err := NextDaily(now, "25:00"); err == nil {
		t.Error("expected an error for an invalid time")
	}
}
