package domain

import (
	"slices"
	"sort"
	"time"
)

// State is the freshness of a repository's backups.
type State string

// The possible repository states.
const (
	StateOK          State = "ok"          // backups arrive as usual
	StateLate        State = "late"        // a bit later than usual
	StateOverdue     State = "bad"         // far later than usual
	StateIdle        State = "idle"        // no snapshots yet
	StateUnreachable State = "unreachable" // last refresh failed
)

// CSSClass is the status-dot class the stylesheet knows for s.
func (s State) CSSClass() string {
	if s == StateUnreachable {
		return "bad"
	}
	return string(s)
}

// Label is the short status text.
func (s State) Label() string {
	switch s {
	case StateOverdue:
		return "Overdue"
	case StateLate:
		return "Running late"
	case StateIdle:
		return "No backups yet"
	case StateUnreachable:
		return "Unreachable"
	default:
		return "On schedule"
	}
}

// Hint is the longer text used as tooltip.
func (s State) Hint() string {
	switch s {
	case StateOverdue:
		return "Last backup is overdue"
	case StateLate:
		return "Last backup is later than usual"
	case StateIdle:
		return "No backups yet"
	case StateUnreachable:
		return "Repository unreachable"
	default:
		return "Backups are on schedule"
	}
}

// Severity orders states for the "needs attention first" sort (lower is worse).
func (s State) Severity() int {
	switch s {
	case StateUnreachable, StateOverdue:
		return 0
	case StateLate:
		return 1
	case StateIdle:
		return 2
	default:
		return 3
	}
}

// Summary bundles a repository with everything derived from its cached data.
type Summary struct {
	Repo      Repo
	Snapshots []Snapshot // oldest first
	Locks     []Lock
	Job       *Job
	LastRun   *Run // newest cleanup run, nil when none
	State     State
	// Interval is the typical time between snapshots, used to judge freshness
	// when the repository has no backup schedule.
	Interval time.Duration
	// Scheduled is true when the state was judged against the repository's
	// backup schedule. Missed is then the number of scheduled backups that
	// have no snapshot.
	Scheduled bool
	Missed    int

	now time.Time
}

// Schedule is a backup schedule: Next returns the first run time after t.
type Schedule interface {
	Next(t time.Time) time.Time
}

// Summarize derives the figures shown for a repository. snaps may be in any
// order. A nil sched judges freshness from the gaps between snapshots.
func Summarize(repo Repo, snaps []Snapshot, locks []Lock, job *Job, sched Schedule, now time.Time) Summary {
	sorted := slices.Clone(snaps)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Time.Before(sorted[j].Time) })
	s := Summary{Repo: repo, Snapshots: sorted, Locks: locks, Job: job, now: now}
	s.Interval = typicalInterval(sorted)
	if sched != nil && len(sorted) > 0 {
		s.Scheduled = true
		s.Missed = missedRuns(sched, sorted[len(sorted)-1].Time, s.judgedUntil())
	}
	s.State = s.computeState()
	return s
}

// Count is the number of snapshots.
func (s Summary) Count() int { return len(s.Snapshots) }

// Last is the newest snapshot, or nil when there are none.
func (s Summary) Last() *Snapshot {
	if len(s.Snapshots) == 0 {
		return nil
	}
	return &s.Snapshots[len(s.Snapshots)-1]
}

// Hosts lists the distinct hosts, sorted.
func (s Summary) Hosts() []string {
	return distinct(s.Snapshots, func(sn Snapshot) []string { return []string{sn.Host} })
}

// Tags lists the distinct tags, sorted.
func (s Summary) Tags() []string {
	return distinct(s.Snapshots, func(sn Snapshot) []string { return sn.Tags })
}

// AddedSince sums the data added by snapshots taken at or after t.
func (s Summary) AddedSince(t time.Time) int64 {
	var sum int64
	for _, sn := range s.Snapshots {
		if !sn.Time.Before(t) {
			sum += sn.AddedBytes()
		}
	}
	return sum
}

// Added30Days sums the data added in the last 30 days.
func (s Summary) Added30Days() int64 { return s.AddedSince(s.now.AddDate(0, 0, -30)) }

// HasActiveJob reports whether the repo has a cleanup job that will run: one
// that is not paused and whose repository is not read-only.
func (s Summary) HasActiveJob() bool { return s.Job != nil && s.Job.Enabled && !s.Repo.ReadOnly }

// Late and overdue thresholds, as multiples of the typical snapshot interval.
const (
	lateFactor    = 1.5
	overdueFactor = 3.0
	// defaultInterval is assumed while there are too few snapshots to measure one.
	defaultInterval = 24 * time.Hour
	minInterval     = time.Hour
	// intervalSample is how many of the newest gaps are used for the median.
	intervalSample = 30
)

func (s Summary) computeState() State {
	switch {
	case s.Repo.Unreachable():
		return StateUnreachable
	case len(s.Snapshots) == 0:
		return StateIdle
	}
	if s.Scheduled {
		switch {
		case s.Missed >= 2:
			return StateOverdue
		case s.Missed == 1:
			return StateLate
		}
		return StateOK
	}
	age := s.now.Sub(s.Last().Time)
	switch {
	case age > time.Duration(float64(s.Interval)*overdueFactor):
		return StateOverdue
	case age > time.Duration(float64(s.Interval)*lateFactor):
		return StateLate
	default:
		return StateOK
	}
}

// Tolerances for judging a backup schedule.
const (
	// earlyTolerance: a snapshot taken this long before a scheduled run still counts for it.
	earlyTolerance = time.Hour
	// maxGrace caps how long a backup may run late before the run counts as missed.
	maxGrace = 12 * time.Hour
	// maxRunsChecked bounds the work for very frequent schedules.
	maxRunsChecked = 1000
)

// judgedUntil is the point in time up to which the schedule is checked. It is
// the last successful sync, because the saved snapshots are only that recent;
// before the first sync it is the current time.
func (s Summary) judgedUntil() time.Time {
	if !s.Repo.RefreshOKAt.IsZero() && s.Repo.RefreshOKAt.Before(s.now) {
		return s.Repo.RefreshOKAt
	}
	return s.now
}

// missedRuns counts the scheduled runs after the newest snapshot (last) that
// are over by ref. A run is over once half the time to the next run has passed
// (at most maxGrace), so a backup that is still running is not counted. A
// snapshot taken shortly before a run (up to earlyTolerance, but never more
// than half the time between runs) counts for that run.
func missedRuns(sched Schedule, last, ref time.Time) int {
	missed := 0
	t := last
	for range maxRunsChecked {
		run := sched.Next(t)
		if run.IsZero() || run.After(ref) {
			break
		}
		t = run
		early, grace := earlyTolerance, maxGrace
		if next := sched.Next(run); !next.IsZero() {
			half := next.Sub(run) / 2
			early, grace = min(early, half), min(half, maxGrace)
		}
		if run.Sub(last) <= early {
			continue // the newest snapshot belongs to this run
		}
		if ref.Sub(run) >= grace {
			missed++
		}
	}
	return missed
}

// typicalInterval is the median gap between the newest snapshots (oldest-first input).
func typicalInterval(sorted []Snapshot) time.Duration {
	if len(sorted) < 3 {
		return defaultInterval
	}
	from := max(1, len(sorted)-intervalSample)
	gaps := make([]time.Duration, 0, len(sorted)-from)
	for i := from; i < len(sorted); i++ {
		gaps = append(gaps, sorted[i].Time.Sub(sorted[i-1].Time))
	}
	slices.Sort(gaps)
	return max(gaps[len(gaps)/2], minInterval)
}

func distinct(snaps []Snapshot, pick func(Snapshot) []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, sn := range snaps {
		for _, v := range pick(sn) {
			if !seen[v] {
				seen[v] = true
				out = append(out, v)
			}
		}
	}
	sort.Strings(out)
	return out
}
