package jobs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"restic-repo-browser/internal/backend"
	"restic-repo-browser/internal/domain"
	"restic-repo-browser/internal/tasks"
)

type fakeStore struct {
	mu   sync.Mutex
	job  *domain.Job
	runs map[int64]*domain.Run
	next int64
}

func newFakeStore(job *domain.Job) *fakeStore {
	return &fakeStore{job: job, runs: map[int64]*domain.Run{}}
}

func (f *fakeStore) Repo(context.Context, int64) (domain.Repo, error) {
	return domain.Repo{ID: 1, Name: "r", Backend: domain.BackendLocal, Location: "/r"}, nil
}
func (f *fakeStore) RepoPassword(context.Context, int64) (string, error) { return "pw", nil }
func (f *fakeStore) Job(context.Context, int64) (*domain.Job, error)     { return f.job, nil }
func (f *fakeStore) Jobs(context.Context) (map[int64]domain.Job, error) {
	if f.job == nil {
		return nil, nil
	}
	return map[int64]domain.Job{f.job.RepoID: *f.job}, nil
}
func (f *fakeStore) StartRun(_ context.Context, repoID int64, at time.Time) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	f.runs[f.next] = &domain.Run{ID: f.next, RepoID: repoID, StartedAt: at, Status: domain.RunRunning}
	return f.next, nil
}
func (f *fakeStore) FinishRun(_ context.Context, id int64, at time.Time, st domain.RunStatus, summary, log string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.runs[id]
	r.FinishedAt, r.Status, r.Summary, r.Log = at, st, summary, log
	return nil
}
func (f *fakeStore) AbandonRunningRuns(context.Context, time.Time) error { return nil }
func (f *fakeStore) finished() []domain.Run {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.Run
	for _, r := range f.runs {
		if r.Status != domain.RunRunning {
			out = append(out, *r)
		}
	}
	return out
}

type fakeBackend struct {
	mu        sync.Mutex
	calls     []string
	forgetErr error
	freed     string
	gate      chan struct{} // when set, Forget blocks until it is closed
}

func (f *fakeBackend) record(s string) { f.mu.Lock(); f.calls = append(f.calls, s); f.mu.Unlock() }
func (f *fakeBackend) Forget(_ context.Context, _ backend.Ref, k domain.KeepPolicy) (backend.ForgetResult, error) {
	f.record(fmt.Sprintf("forget last=%d", k.Last))
	if f.gate != nil {
		<-f.gate
	}
	if f.forgetErr != nil {
		return backend.ForgetResult{}, f.forgetErr
	}
	return backend.ForgetResult{Kept: 5, Removed: []domain.Snapshot{
		{ShortID: "aaaa1111", Host: "h", Paths: []string{"/p"}}, {ShortID: "bbbb2222", Host: "h", Paths: []string{"/p"}}}}, nil
}
func (f *fakeBackend) Prune(_ context.Context, _ backend.Ref, log io.Writer) (backend.PruneResult, error) {
	f.record("prune")
	_, _ = io.WriteString(log, "pruning...\n")
	return backend.PruneResult{Freed: f.freed}, nil
}

// fakeSyncer records the progress text visible while the sync runs.
type fakeSyncer struct {
	mu    sync.Mutex
	ids   []int64
	steps []string
	tm    *tasks.Manager
}

func (f *fakeSyncer) Refresh(_ context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ids = append(f.ids, id)
	if info, ok := f.tm.Current(); ok {
		f.steps = append(f.steps, info.Step)
	}
	return nil
}

func newRunner(st *fakeStore, rc *fakeBackend) (*Runner, *fakeSyncer, *tasks.Manager) {
	tm := tasks.NewManager()
	sy := &fakeSyncer{tm: tm}
	return New(st, rc, sy, tm, slog.New(slog.NewTextHandler(io.Discard, nil))), sy, tm
}

func job() *domain.Job {
	return &domain.Job{RepoID: 1, Cron: "30 3 * * *", Keep: domain.KeepPolicy{Last: 7}, Prune: true, Enabled: true}
}

func execute(t *testing.T, r *Runner, tm *tasks.Manager, j *domain.Job) {
	t.Helper()
	task, err := tm.TryStart(tasks.KindCleanup, "Cleaning up r", j.RepoID)
	if err != nil {
		t.Fatal(err)
	}
	r.execute(context.Background(), *j, task)
}

func TestExecuteSuccess(t *testing.T) {
	st, rc := newFakeStore(job()), &fakeBackend{freed: "5.280 KiB"}
	r, sy, tm := newRunner(st, rc)

	execute(t, r, tm, job())

	runs := st.finished()
	if len(runs) != 1 {
		t.Fatalf("runs = %d", len(runs))
	}
	run := runs[0]
	if run.Status != domain.RunOK || run.Summary != "Forgot 2 snapshots, freed 5.280 KiB" {
		t.Errorf("run = %+v", run)
	}
	for _, want := range []string{"keep rules: last 7", "remove 2 snapshots", "aaaa1111", "pruning..."} {
		if !strings.Contains(run.Log, want) {
			t.Errorf("log lacks %q:\n%s", want, run.Log)
		}
	}
	if got := strings.Join(rc.calls, "; "); got != "forget last=7; prune" {
		t.Errorf("calls = %s", got)
	}
	if len(sy.ids) != 1 || sy.ids[0] != 1 || sy.steps[0] != "Syncing the saved data" {
		t.Errorf("saved data not synced inside the task: ids=%v steps=%v", sy.ids, sy.steps)
	}
	if _, running := tm.Current(); running {
		t.Error("the task slot must be free after a run")
	}
	if last := tm.Last(); last == nil || last.Err != "" {
		t.Errorf("last task = %+v", last)
	}

	// A job without prune only forgets.
	forgetOnly := job()
	forgetOnly.Prune = false
	rc.calls = nil
	execute(t, r, tm, forgetOnly)
	if len(rc.calls) != 1 {
		t.Errorf("calls without prune = %v", rc.calls)
	}
}

func TestExecuteFailureStopsAndRecords(t *testing.T) {
	st := newFakeStore(job())
	rc := &fakeBackend{forgetErr: &backend.Error{Kind: backend.KindLocked, Message: "repository is already locked"}}
	r, sy, tm := newRunner(st, rc)

	execute(t, r, tm, job())

	run := st.finished()[0]
	if run.Status != domain.RunFailed || run.Summary != "The repository is locked by another process." {
		t.Errorf("run = %+v", run)
	}
	if !strings.Contains(run.Log, "repository is already locked") || !strings.Contains(run.Log, "try again at the next scheduled run") {
		t.Errorf("log = %s", run.Log)
	}
	if len(rc.calls) != 1 {
		t.Errorf("prune must not run after a failed forget: %v", rc.calls)
	}
	if len(sy.ids) != 1 {
		t.Error("saved data must be synced after failed runs too")
	}
	if last := tm.Last(); last == nil || last.Err == "" {
		t.Errorf("the failed run must be reported as the task result: %+v", last)
	}
}

func TestRunNowRules(t *testing.T) {
	st := newFakeStore(nil)
	rc := &fakeBackend{gate: make(chan struct{})}
	r, _, tm := newRunner(st, rc)

	if err := r.RunNow(context.Background(), 1); !errors.Is(err, ErrNoJob) {
		t.Errorf("without a job: %v", err)
	}
	st.job = job()
	if err := r.RunNow(context.Background(), 1); !errors.Is(err, ErrNotStarted) {
		t.Errorf("before Start: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.Start(ctx)
	if err := r.RunNow(ctx, 1); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return r.Running(1) })
	var busy *tasks.BusyError
	if err := r.RunNow(ctx, 1); !errors.As(err, &busy) || busy.Current.Kind != tasks.KindCleanup {
		t.Errorf("second RunNow: %v", err)
	}
	// Another kind of task also blocks it, and the other way round.
	if _, err := tm.TryStart(tasks.KindSync, "Syncing", 0); err == nil {
		t.Error("a sync must not start during a cleanup")
	}
	close(rc.gate)
	waitFor(t, func() bool { return !r.Running(1) && len(st.finished()) == 1 })

	sync, _ := tm.TryStart(tasks.KindSync, "Syncing all repositories", 0)
	if err := r.RunNow(ctx, 1); !errors.As(err, &busy) || busy.Current.Title != "Syncing all repositories" {
		t.Errorf("RunNow during sync: %v", err)
	}
	sync.Finish(nil)
	cancel()
	r.Wait()
}

func TestSchedulerStartsDueJobsAndWaitsForTheSlot(t *testing.T) {
	now := time.Date(2026, 11, 11, 3, 29, 50, 0, time.Local)
	var mu sync.Mutex
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }

	st, rc := newFakeStore(job()), &fakeBackend{} // 03:30 daily
	r, _, tm := newRunner(st, rc)
	r.now, r.tick = clock, 10*time.Millisecond

	busy, _ := tm.TryStart(tasks.KindSync, "Syncing all repositories", 0) // the slot is taken
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.Start(ctx)
	time.Sleep(50 * time.Millisecond)
	if len(st.finished()) != 0 {
		t.Fatal("job ran before its time")
	}
	mu.Lock()
	now = time.Date(2026, 11, 11, 3, 30, 5, 0, time.Local)
	mu.Unlock()
	time.Sleep(100 * time.Millisecond)
	if len(st.finished()) != 0 {
		t.Fatal("the scheduled job must wait while another task holds the slot")
	}
	busy.Finish(nil)
	waitFor(t, func() bool { return len(st.finished()) >= 1 })
	cancel()
	r.Wait()

	if n := len(st.finished()); n != 1 {
		t.Errorf("job ran %d times, want 1", n)
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
