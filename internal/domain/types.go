// Package domain holds the core types of the application and the pure logic
// built on them (freshness, history buckets, snapshot filtering). It depends on
// nothing else in this module.
package domain

import (
	"slices"
	"time"
)

// Repo is a restic repository on a local disk. The password is never part of
// this type; it is only handed out by the store when a restic command needs it.
type Repo struct {
	ID        int64
	Name      string
	Path      string
	CreatedAt time.Time

	// RefreshedAt is the last refresh attempt, RefreshOKAt the last successful one.
	RefreshedAt time.Time
	RefreshOKAt time.Time
	// RefreshError is empty when the last refresh worked.
	RefreshError string
	// SizeBytes is the repository's size on disk at the last successful refresh.
	SizeBytes int64
}

// Unreachable reports whether the last sync failed.
func (r Repo) Unreachable() bool { return r.RefreshError != "" }

// NeverSynced reports whether no sync has been attempted yet.
func (r Repo) NeverSynced() bool { return r.RefreshedAt.IsZero() }

// Snapshot is one restic snapshot as cached from the repository.
type Snapshot struct {
	ID       string
	ShortID  string
	Time     time.Time
	Host     string
	Paths    []string
	Tags     []string
	Size     *int64 // total size of the snapshot's files; nil when unknown
	Added    *int64 // new data this snapshot added to the repo; nil when unknown
	Files    *int64 // number of files; nil when unknown
	Username string
}

// HasTag reports whether the snapshot carries tag.
func (s Snapshot) HasTag(tag string) bool { return slices.Contains(s.Tags, tag) }

// SizeBytes returns the snapshot size, or 0 when unknown.
func (s Snapshot) SizeBytes() int64 { return deref(s.Size) }

// AddedBytes returns the data added, or 0 when unknown.
func (s Snapshot) AddedBytes() int64 { return deref(s.Added) }

// FileCount returns the file count, or 0 when unknown.
func (s Snapshot) FileCount() int64 { return deref(s.Files) }

func deref(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

// Lock is a restic lock seen in a repository.
type Lock struct {
	ID        string
	Time      time.Time
	Host      string
	Username  string
	PID       int
	Exclusive bool
}

// KeepPolicy are the "keep" rules of a cleanup job (restic forget --keep-*).
type KeepPolicy struct {
	Last    int
	Hourly  int
	Daily   int
	Weekly  int
	Monthly int
	Yearly  int
}

// Empty reports whether no keep rule is set. Such a policy would forget everything.
func (k KeepPolicy) Empty() bool {
	return k.Last == 0 && k.Hourly == 0 && k.Daily == 0 && k.Weekly == 0 &&
		k.Monthly == 0 && k.Yearly == 0
}

// Job is the single cleanup job (forget, optionally prune) of a repo.
type Job struct {
	RepoID  int64
	Cron    string
	Keep    KeepPolicy
	Prune   bool
	Enabled bool
}

// RunStatus is the outcome of a job run.
type RunStatus string

// The possible run states.
const (
	RunRunning RunStatus = "running"
	RunOK      RunStatus = "ok"
	RunFailed  RunStatus = "failed"
)

// CSSClass is the status-dot class for the stylesheet.
func (s RunStatus) CSSClass() string {
	switch s {
	case RunFailed:
		return "bad"
	case RunRunning:
		return "late"
	}
	return "ok"
}

// Label is the short status text.
func (s RunStatus) Label() string {
	switch s {
	case RunFailed:
		return "Failed"
	case RunRunning:
		return "Running"
	}
	return "Done"
}

// Run is one execution of a cleanup job.
type Run struct {
	ID         int64
	RepoID     int64
	RepoName   string // filled by queries that join the repo table
	StartedAt  time.Time
	FinishedAt time.Time
	Status     RunStatus
	Summary    string
	Log        string
}

// Duration is how long the run took (or has been taking).
func (r Run) Duration(now time.Time) time.Duration {
	if r.FinishedAt.IsZero() {
		return now.Sub(r.StartedAt)
	}
	return r.FinishedAt.Sub(r.StartedAt)
}
