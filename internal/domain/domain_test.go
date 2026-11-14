package domain

import (
	"reflect"
	"testing"
	"time"
)

var now = time.Date(2026, 11, 11, 14, 20, 0, 0, time.UTC)

func i64(v int64) *int64 { return &v }

// every returns n snapshots spaced step apart, the newest `age` before now.
func every(n int, step, age time.Duration) []Snapshot {
	out := make([]Snapshot, n)
	for i := range out {
		out[i] = Snapshot{
			ID: string(rune('a' + i%26)), Host: "h", Paths: []string{"/p"},
			Time: now.Add(-age - time.Duration(n-1-i)*step), Added: i64(100), Size: i64(1000 + int64(i)),
		}
	}
	return out
}

func TestStateFreshness(t *testing.T) {
	cases := []struct {
		name string
		repo Repo
		snap []Snapshot
		want State
	}{
		{"idle", Repo{}, nil, StateIdle},
		{"unreachable", Repo{RefreshError: "boom"}, every(5, 24*time.Hour, time.Hour), StateUnreachable},
		{"on schedule", Repo{}, every(10, 24*time.Hour, 5*time.Hour), StateOK},
		{"late", Repo{}, every(10, 24*time.Hour, 40*time.Hour), StateLate},
		{"overdue", Repo{}, every(10, 24*time.Hour, 5*24*time.Hour), StateOverdue},
		{"single fresh snapshot", Repo{}, every(1, 0, 3*time.Hour), StateOK},
	}
	for _, c := range cases {
		if got := Summarize(c.repo, c.snap, nil, nil, nil, now).State; got != c.want {
			t.Errorf("%s: state = %s, want %s", c.name, got, c.want)
		}
	}
}

func TestSummaryAggregates(t *testing.T) {
	snaps := every(40, 24*time.Hour, time.Hour)
	snaps[0].Tags, snaps[1].Tags = []string{"x"}, []string{"y", "x"}
	snaps[2].Host = "other"
	s := Summarize(Repo{}, snaps, nil, nil, nil, now)
	if !reflect.DeepEqual(s.Hosts(), []string{"h", "other"}) {
		t.Errorf("hosts = %v", s.Hosts())
	}
	if !reflect.DeepEqual(s.Tags(), []string{"x", "y"}) {
		t.Errorf("tags = %v", s.Tags())
	}
	if got := s.Added30Days(); got != 30*100 {
		t.Errorf("added in 30 days = %d", got)
	}
	if s.Last().Time != snaps[39].Time {
		t.Error("last snapshot is not the newest")
	}
}

func TestBuildHistoryDays(t *testing.T) {
	snaps := every(60, 24*time.Hour, 0)
	h := BuildHistory(snaps, Range30Days, now)
	if h.Unit != "day" || h.Slab == nil || h.Slab.Label != "Earlier snapshots" {
		t.Fatalf("unexpected history: unit=%s slab=%v", h.Unit, h.Slab)
	}
	total := 0
	for _, b := range h.Buckets {
		total += len(b.Snapshots)
		if b.Added != int64(len(b.Snapshots))*100 {
			t.Errorf("bucket %s added = %d for %d snapshots", b.Key, b.Added, len(b.Snapshots))
		}
	}
	if total+h.Slab.Count != 60 {
		t.Errorf("snapshots lost: %d in buckets + %d in slab", total, h.Slab.Count)
	}
	if !h.Buckets[0].From.Before(h.Buckets[len(h.Buckets)-1].From) {
		t.Error("buckets must be oldest first")
	}
}

func TestBuildHistoryAllSplitsInitialBackup(t *testing.T) {
	h := BuildHistory(every(5, 24*time.Hour*40, 0), RangeAll, now)
	if h.Unit != "month" || h.Slab == nil || h.Slab.Label != "Initial backup" || h.Slab.Count != 1 {
		t.Fatalf("unexpected history: %+v", h)
	}
	if len(h.Buckets) != 4 {
		t.Errorf("buckets = %d, want 4", len(h.Buckets))
	}
}

func TestWeekBucketStartsOnMonday(t *testing.T) {
	sunday := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	start, end := bucketSpan(sunday, "week")
	if start.Weekday() != time.Monday || start.Day() != 2 || end.Day() != 9 {
		t.Errorf("week span = %v to %v", start, end)
	}
}

func TestFilter(t *testing.T) {
	s := Snapshot{ID: "abcdef0123", ShortID: "abcdef01", Host: "nas", Paths: []string{"/data"}, Tags: []string{"photos"}, Time: now}
	cases := []struct {
		f    SnapshotFilter
		want bool
	}{
		{SnapshotFilter{}, true},
		{SnapshotFilter{Query: "ABCDEF"}, true},
		{SnapshotFilter{Query: "/data"}, true},
		{SnapshotFilter{Query: "nothing"}, false},
		{SnapshotFilter{Host: "nas"}, true},
		{SnapshotFilter{Host: "pi"}, false},
		{SnapshotFilter{Tag: "photos"}, true},
		{SnapshotFilter{Tag: "x"}, false},
		{SnapshotFilter{From: now, To: now.Add(time.Hour)}, true},
		{SnapshotFilter{From: now.Add(time.Second)}, false},
		{SnapshotFilter{To: now}, false}, // To is exclusive
	}
	for i, c := range cases {
		if got := c.f.Matches(s); got != c.want {
			t.Errorf("case %d: Matches = %v, want %v", i, got, c.want)
		}
	}
}

func TestPaginate(t *testing.T) {
	items := make([]int, 33)
	p := Paginate(items, 3, 15)
	if p.Pages != 3 || len(p.Items) != 3 || p.FirstIndex != 31 || p.LastIndex() != 33 {
		t.Errorf("page = %+v", p)
	}
	if p := Paginate(items, 99, 15); p.Number != 3 {
		t.Errorf("page number should clamp, got %d", p.Number)
	}
	if p := Paginate([]int(nil), 1, 15); p.Pages != 1 || p.FirstIndex != 0 || p.LastIndex() != 0 {
		t.Errorf("empty page = %+v", p)
	}
}

func TestPageLinks(t *testing.T) {
	p := Page[int]{Pages: 20, Number: 10}
	want := []int{1, 0, 9, 10, 11, 0, 20}
	if got := p.PageLinks(); !reflect.DeepEqual(got, want) {
		t.Errorf("links = %v, want %v", got, want)
	}
	p = Page[int]{Pages: 5, Number: 1}
	if got := p.PageLinks(); !reflect.DeepEqual(got, []int{1, 2, 3, 4, 5}) {
		t.Errorf("links = %v", got)
	}
}

func TestKeepPolicyEmpty(t *testing.T) {
	if !(KeepPolicy{}).Empty() || (KeepPolicy{Last: 1}).Empty() {
		t.Error("Empty() wrong")
	}
}

// cronAt is a fake schedule that runs at the anchor time and then every step.
type cronAt struct {
	anchor time.Time
	step   time.Duration
}

func (c cronAt) Next(t time.Time) time.Time {
	if t.Before(c.anchor) {
		return c.anchor
	}
	n := t.Sub(c.anchor)/c.step + 1
	return c.anchor.Add(n * c.step)
}

func TestStateWithSchedule(t *testing.T) {
	// Weekly backups on the day of "now" (Wednesday 11 Nov 2026), 03:00.
	weekly := cronAt{anchor: time.Date(2026, 11, 4, 3, 0, 0, 0, time.UTC), step: 7 * 24 * time.Hour}
	hourly := cronAt{anchor: time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC), step: time.Hour}
	snap := func(at time.Time) []Snapshot { return []Snapshot{{ID: "a", Time: at, Host: "h"}} }
	day := func(d, h, m int) time.Time { return time.Date(2026, 11, d, h, m, 0, 0, time.UTC) }

	cases := []struct {
		name   string
		sched  Schedule
		snaps  []Snapshot
		synced time.Time
		at     time.Time
		state  State
		missed int
	}{
		{"weekly, backup 7 days ago, next one not due", weekly, snap(day(4, 3, 10)), day(11, 2, 0), day(11, 2, 0), StateOK, 0},
		{"weekly, one run missed a day ago", weekly, snap(day(4, 3, 10)), day(12, 4, 0), day(12, 4, 0), StateLate, 1},
		{"weekly, two runs missed", weekly, snap(day(4, 3, 10)), day(19, 4, 0), day(19, 4, 0), StateOverdue, 2},
		{"weekly, run still within its grace time", weekly, snap(day(4, 3, 10)), day(11, 8, 0), day(11, 8, 0), StateOK, 0},
		{"weekly, one run missed", weekly, snap(day(4, 3, 10)), day(11, 16, 0), day(11, 16, 0), StateLate, 1},
		{"snapshot a little before the run counts for it", weekly, snap(day(11, 2, 30)), day(11, 16, 0), day(11, 16, 0), StateOK, 0},
		{"judged up to the last sync, not up to now", hourly, snap(day(11, 13, 5)), day(11, 13, 30), day(11, 20, 0), StateOK, 0},
		{"hourly, two hours without a backup", hourly, snap(day(11, 11, 5)), day(11, 13, 45), day(11, 13, 45), StateOverdue, 2},
	}
	for _, c := range cases {
		repo := Repo{RefreshOKAt: c.synced}
		got := Summarize(repo, c.snaps, nil, nil, c.sched, c.at)
		if got.State != c.state || got.Missed != c.missed || !got.Scheduled {
			t.Errorf("%s: state=%s missed=%d scheduled=%v, want %s / %d", c.name, got.State, got.Missed, got.Scheduled, c.state, c.missed)
		}
	}

	// Without snapshots the schedule changes nothing.
	if got := Summarize(Repo{}, nil, nil, nil, weekly, now); got.State != StateIdle || got.Scheduled {
		t.Errorf("no snapshots: state=%s scheduled=%v", got.State, got.Scheduled)
	}
}
