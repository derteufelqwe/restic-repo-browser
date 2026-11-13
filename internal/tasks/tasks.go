// Package tasks makes sure only one long-running task (a sync or a cleanup run)
// executes at a time, and lets the running task report progress that the UI
// can show.
package tasks

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Kind says what a task does.
type Kind string

// The task kinds.
const (
	KindSync    Kind = "sync"
	KindCleanup Kind = "cleanup"
)

// Info is a snapshot of a running task's state.
type Info struct {
	ID      int64
	Kind    Kind
	Title   string // e.g. "Syncing nas-photos"
	RepoID  int64  // the repository involved, 0 when several
	Step    string // current phase, e.g. "Repository 2 of 6"
	Detail  string // what is happening right now, e.g. "nas-photos: reading the snapshot list"
	Done    int    // completed units of Total; only meaningful when Total > 0
	Total   int    // 0 means progress is not measurable
	Started time.Time
}

// Percent is the completion in whole percent, or -1 when unknown.
func (i Info) Percent() int {
	if i.Total <= 0 {
		return -1
	}
	return min(100, i.Done*100/i.Total)
}

// Result is the outcome of a finished task.
type Result struct {
	Info     Info
	Err      string
	Finished time.Time
}

// BusyError is returned when a task cannot start because another one runs.
type BusyError struct{ Current Info }

func (e *BusyError) Error() string {
	return fmt.Sprintf("another task is running: %s", e.Current.Title)
}

// Manager owns the single task slot.
type Manager struct {
	now func() time.Time

	mu      sync.Mutex
	next    int64
	current *Task
	last    *Result
	freed   chan struct{} // closed (and replaced) whenever the slot becomes free
}

// NewManager creates a Manager.
func NewManager() *Manager {
	return &Manager{now: time.Now, freed: make(chan struct{})}
}

// TryStart takes the slot or fails with a *BusyError.
func (m *Manager) TryStart(kind Kind, title string, repoID int64) (*Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current != nil {
		return nil, &BusyError{Current: m.current.snapshot()}
	}
	return m.begin(kind, title, repoID), nil
}

// Start waits until the slot is free (or ctx ends) and takes it. Scheduled work
// uses this so that it runs after the current task instead of being dropped.
func (m *Manager) Start(ctx context.Context, kind Kind, title string, repoID int64) (*Task, error) {
	for {
		m.mu.Lock()
		if m.current == nil {
			t := m.begin(kind, title, repoID)
			m.mu.Unlock()
			return t, nil
		}
		freed := m.freed
		m.mu.Unlock()
		select {
		case <-freed:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// begin creates the task; the caller holds m.mu and has checked the slot is free.
func (m *Manager) begin(kind Kind, title string, repoID int64) *Task {
	m.next++
	t := &Task{m: m, info: Info{ID: m.next, Kind: kind, Title: title, RepoID: repoID, Started: m.now()}}
	m.current = t
	return t
}

// Current returns the running task, if any.
func (m *Manager) Current() (Info, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current == nil {
		return Info{}, false
	}
	return m.current.snapshot(), true
}

// Last returns the outcome of the most recently finished task, or nil.
func (m *Manager) Last() *Result {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.last == nil {
		return nil
	}
	r := *m.last
	return &r
}

// Task is a running task. Finish must be called exactly once.
type Task struct {
	m    *Manager
	info Info // guarded by m.mu
}

func (t *Task) snapshot() Info { return t.info }

// Step sets the current phase and resets the measured progress and detail.
func (t *Task) Step(text string) {
	t.m.mu.Lock()
	t.info.Step, t.info.Detail, t.info.Done, t.info.Total = text, "", 0, 0
	t.m.mu.Unlock()
}

// Progress reports done of total units for the current step.
func (t *Task) Progress(done, total int) {
	t.m.mu.Lock()
	t.info.Done, t.info.Total = done, total
	t.m.mu.Unlock()
}

// Detail sets the detail line without touching the phase or measured progress.
func (t *Task) Detail(text string) {
	t.m.mu.Lock()
	t.info.Detail = text
	t.m.mu.Unlock()
}

// Info returns the task's current state.
func (t *Task) Info() Info {
	t.m.mu.Lock()
	defer t.m.mu.Unlock()
	return t.info
}

// Finish frees the slot and records the outcome. A nil err means success.
func (t *Task) Finish(err error) {
	m := t.m
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current != t {
		return // already finished
	}
	res := &Result{Info: t.info, Finished: m.now()}
	if err != nil {
		res.Err = err.Error()
	}
	m.last, m.current = res, nil
	close(m.freed)
	m.freed = make(chan struct{})
}

type ctxKey struct{}

// WithTask returns a context that carries t, so deep code can report progress
// without knowing about tasks.
func WithTask(ctx context.Context, t *Task) context.Context {
	return context.WithValue(ctx, ctxKey{}, t)
}

func from(ctx context.Context) *Task {
	t, _ := ctx.Value(ctxKey{}).(*Task)
	return t
}

// Step sets the activity of the task carried by ctx (no-op without one).
func Step(ctx context.Context, text string) {
	if t := from(ctx); t != nil {
		t.Step(text)
	}
}

// Progress reports measured progress for the task carried by ctx.
func Progress(ctx context.Context, done, total int) {
	if t := from(ctx); t != nil {
		t.Progress(done, total)
	}
}

// Detail sets only the detail line of the task carried by ctx.
func Detail(ctx context.Context, text string) {
	if t := from(ctx); t != nil {
		t.Detail(text)
	}
}
