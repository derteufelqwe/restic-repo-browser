// Package retention evaluates restic's "keep" rules against a list of snapshots
// without touching the repository. It mirrors the policy logic of `restic
// forget` (grouping by host and paths, per-group counting of hourly, daily,
// weekly, monthly and yearly buckets) so the cleanup preview matches what a real run does.
package retention

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"restic-repo-browser/internal/domain"
)

// Reason says which rule keeps a snapshot.
type Reason string

// The rules, in the order they are reported.
const (
	ReasonLatest  Reason = "latest"
	ReasonHourly  Reason = "hourly"
	ReasonDaily   Reason = "daily"
	ReasonWeekly  Reason = "weekly"
	ReasonMonthly Reason = "monthly"
	ReasonYearly  Reason = "yearly"
)

// Reasons lists all rules in display order.
var Reasons = []Reason{ReasonLatest, ReasonHourly, ReasonDaily, ReasonWeekly, ReasonMonthly, ReasonYearly}

// Decision is the verdict for one snapshot.
type Decision struct {
	Snapshot domain.Snapshot
	Reasons  []Reason // empty means the snapshot would be forgotten
}

// Kept reports whether at least one rule keeps the snapshot.
func (d Decision) Kept() bool { return len(d.Reasons) > 0 }

// Plan is the outcome of applying a policy, newest snapshot first.
type Plan struct {
	Decisions []Decision
}

// Kept returns the snapshots that stay.
func (p Plan) Kept() []domain.Snapshot { return p.filter(true) }

// Forgotten returns the snapshots that would be forgotten.
func (p Plan) Forgotten() []domain.Snapshot { return p.filter(false) }

func (p Plan) filter(kept bool) []domain.Snapshot {
	var out []domain.Snapshot
	for _, d := range p.Decisions {
		if d.Kept() == kept {
			out = append(out, d.Snapshot)
		}
	}
	return out
}

// ReasonCounts counts how many snapshots each rule keeps (one snapshot can match several).
func (p Plan) ReasonCounts() map[Reason]int {
	counts := map[Reason]int{}
	for _, d := range p.Decisions {
		for _, r := range d.Reasons {
			counts[r]++
		}
	}
	return counts
}

// Apply decides for every snapshot whether the policy keeps it. An empty
// policy keeps everything, which differs from `restic forget` (that refuses it);
// callers validate separately.
func Apply(snaps []domain.Snapshot, p domain.KeepPolicy) Plan {
	if p.Empty() {
		plan := Plan{}
		for _, s := range sortedNewestFirst(snaps) {
			plan.Decisions = append(plan.Decisions, Decision{Snapshot: s, Reasons: []Reason{ReasonLatest}})
		}
		return plan
	}

	groups := map[string][]domain.Snapshot{}
	for _, s := range snaps {
		key := s.Host + "\x00" + strings.Join(s.Paths, "\x00")
		groups[key] = append(groups[key], s)
	}
	var plan Plan
	for _, g := range groups {
		plan.Decisions = append(plan.Decisions, applyGroup(sortedNewestFirst(g), p)...)
	}
	sort.SliceStable(plan.Decisions, func(i, j int) bool {
		return plan.Decisions[i].Snapshot.Time.After(plan.Decisions[j].Snapshot.Time)
	})
	return plan
}

// bucket counts how many distinct time buckets of one granularity were kept.
type bucket struct {
	reason Reason
	limit  int // number of buckets to keep
	key    func(t time.Time, index int) string
	last   string
	used   int
}

func applyGroup(list []domain.Snapshot, p domain.KeepPolicy) []Decision {
	buckets := []*bucket{
		{reason: ReasonLatest, limit: p.Last, key: func(_ time.Time, i int) string { return fmt.Sprint(i) }},
		{reason: ReasonHourly, limit: p.Hourly, key: func(t time.Time, _ int) string { return t.Format("2006-01-02T15") }},
		{reason: ReasonDaily, limit: p.Daily, key: func(t time.Time, _ int) string { return t.Format("2006-01-02") }},
		{reason: ReasonWeekly, limit: p.Weekly, key: func(t time.Time, _ int) string {
			y, w := t.ISOWeek()
			return fmt.Sprintf("%d-W%02d", y, w)
		}},
		{reason: ReasonMonthly, limit: p.Monthly, key: func(t time.Time, _ int) string { return t.Format("2006-01") }},
		{reason: ReasonYearly, limit: p.Yearly, key: func(t time.Time, _ int) string { return t.Format("2006") }},
	}
	out := make([]Decision, len(list))
	for i, s := range list {
		var reasons []Reason
		for _, b := range buckets {
			if b.limit <= 0 || b.used >= b.limit {
				continue
			}
			if k := b.key(s.Time, i); k != b.last {
				b.last = k
				b.used++
				reasons = append(reasons, b.reason)
			}
		}
		out[i] = Decision{Snapshot: s, Reasons: orderReasons(reasons)}
	}
	return out
}

func orderReasons(in []Reason) []Reason {
	if len(in) < 2 {
		return in
	}
	var out []Reason
	for _, r := range Reasons {
		for _, x := range in {
			if x == r {
				out = append(out, r)
			}
		}
	}
	return out
}

func sortedNewestFirst(in []domain.Snapshot) []domain.Snapshot {
	out := append([]domain.Snapshot(nil), in...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time.After(out[j].Time) })
	return out
}
