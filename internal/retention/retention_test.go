package retention

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	"restic-repo-browser/internal/domain"
	"restic-repo-browser/internal/testutil"
)

func snap(id string, t time.Time, host string, tags ...string) domain.Snapshot {
	return domain.Snapshot{ID: id, Time: t, Host: host, Paths: []string{"/data"}, Tags: tags}
}

// daily returns n snapshots, one per day at noon, the newest on `end`.
func daily(n int, end time.Time) []domain.Snapshot {
	var out []domain.Snapshot
	for i := n - 1; i >= 0; i-- {
		out = append(out, snap(fmt.Sprintf("s%02d", n-1-i), end.AddDate(0, 0, -i), "h"))
	}
	return out
}

func keptIDs(p Plan) []string {
	var ids []string
	for _, s := range p.Kept() {
		ids = append(ids, s.ID)
	}
	sort.Strings(ids)
	return ids
}

var end = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)

func TestKeepLastAndDaily(t *testing.T) {
	plan := Apply(daily(10, end), domain.KeepPolicy{Last: 3})
	if got := keptIDs(plan); !reflect.DeepEqual(got, []string{"s07", "s08", "s09"}) || len(plan.Forgotten()) != 7 {
		t.Errorf("keep-last: kept = %v", got)
	}

	// Two snapshots per day for three days; keep-daily 2 keeps the newest of each of the last two days.
	var snaps []domain.Snapshot
	for d := 0; d < 3; d++ {
		day := end.AddDate(0, 0, -d)
		snaps = append(snaps, snap(fmt.Sprintf("d%dam", d), day.Add(-6*time.Hour), "h"), snap(fmt.Sprintf("d%dpm", d), day, "h"))
	}
	if got := keptIDs(Apply(snaps, domain.KeepPolicy{Daily: 2})); !reflect.DeepEqual(got, []string{"d0pm", "d1pm"}) {
		t.Errorf("keep-daily: kept = %v", got)
	}
}

func TestWeeklyMonthlyYearly(t *testing.T) {
	plan := Apply(daily(400, end), domain.KeepPolicy{Weekly: 4, Monthly: 3, Yearly: 2})
	counts := plan.ReasonCounts()
	if counts[ReasonWeekly] != 4 || counts[ReasonMonthly] != 3 || counts[ReasonYearly] != 2 {
		t.Errorf("counts = %v", counts)
	}
}

func TestGroupsReasonsAndEmptyPolicy(t *testing.T) {
	var snaps []domain.Snapshot
	for i := 0; i < 4; i++ {
		snaps = append(snaps, snap(fmt.Sprintf("a%d", i), end.Add(-time.Duration(i)*time.Hour), "hostA"))
		snaps = append(snaps, snap(fmt.Sprintf("b%d", i), end.Add(-time.Duration(i)*time.Hour), "hostB"))
	}
	if got := keptIDs(Apply(snaps, domain.KeepPolicy{Last: 1})); !reflect.DeepEqual(got, []string{"a0", "b0"}) {
		t.Errorf("groups must be independent, kept = %v", got)
	}
	newest := Apply(daily(3, end), domain.KeepPolicy{Last: 1, Daily: 1}).Decisions[0]
	if !reflect.DeepEqual(newest.Reasons, []Reason{ReasonLatest, ReasonDaily}) {
		t.Errorf("reasons = %v", newest.Reasons)
	}
	// An empty policy keeps everything here (restic itself refuses it; callers validate).
	if len(Apply(daily(4, end), domain.KeepPolicy{}).Forgotten()) != 0 {
		t.Error("an empty policy must keep everything")
	}
}

// TestMatchesRestic compares the plan with what `restic forget --dry-run` decides
// on a real repository.
func TestMatchesRestic(t *testing.T) {
	bin := testutil.Binary(t)
	dir := t.TempDir()
	repoPath := filepath.Join(dir, "repo")
	src := filepath.Join(dir, "src")
	testutil.Restic(t, bin, repoPath, "pw", "init", "--quiet")
	times := []string{
		"2026-01-05 08:00:00", "2026-01-05 20:00:00", "2026-01-06 08:00:00", "2026-01-12 09:00:00",
		"2026-02-01 07:00:00", "2026-02-02 07:00:00", "2026-02-20 07:00:00", "2026-03-15 07:00:00",
		"2026-03-16 07:00:00", "2026-03-16 19:00:00", "2026-04-30 07:00:00", "2026-05-01 07:00:00",
	}
	for i, ts := range times {
		testutil.WriteFile(t, filepath.Join(src, "f.txt"), fmt.Sprint(i))
		host := "alpha"
		if i%4 == 3 {
			host = "beta"
		}
		args := []string{"backup", src, "--host", host, "--time", ts, "--quiet"}
		if i%5 == 0 {
			args = append(args, "--tag", "keep")
		}
		testutil.Restic(t, bin, repoPath, "pw", args...)
	}

	var snaps []domain.Snapshot
	var listed []struct {
		ID       string    `json:"id"`
		Time     time.Time `json:"time"`
		Hostname string    `json:"hostname"`
		Paths    []string  `json:"paths"`
		Tags     []string  `json:"tags"`
	}
	must(t, json.Unmarshal(testutil.Restic(t, bin, repoPath, "pw", "snapshots", "--json"), &listed))
	for _, s := range listed {
		snaps = append(snaps, domain.Snapshot{ID: s.ID, Time: s.Time, Host: s.Hostname, Paths: s.Paths, Tags: s.Tags})
	}

	policies := []struct {
		keep  domain.KeepPolicy
		flags []string
	}{
		{domain.KeepPolicy{Last: 3}, []string{"--keep-last", "3"}},
		{domain.KeepPolicy{Daily: 4}, []string{"--keep-daily", "4"}},
		{domain.KeepPolicy{Daily: 5, Weekly: 2}, []string{"--keep-daily", "5", "--keep-weekly", "2"}},
		{domain.KeepPolicy{Weekly: 3, Monthly: 2}, []string{"--keep-weekly", "3", "--keep-monthly", "2"}},
		{domain.KeepPolicy{Monthly: 2, Yearly: 1}, []string{"--keep-monthly", "2", "--keep-yearly", "1"}},
		{domain.KeepPolicy{Last: 2, Daily: 3, Weekly: 2, Monthly: 3, Yearly: 1},
			[]string{"--keep-last", "2", "--keep-daily", "3", "--keep-weekly", "2", "--keep-monthly", "3", "--keep-yearly", "1"}},
	}
	for _, pol := range policies {
		args := append([]string{"forget", "--dry-run", "--json"}, pol.flags...)
		var groups []struct {
			Keep []struct {
				ID string `json:"id"`
			} `json:"keep"`
		}
		must(t, json.Unmarshal(testutil.Restic(t, bin, repoPath, "pw", args...), &groups))
		var want []string
		for _, g := range groups {
			for _, s := range g.Keep {
				want = append(want, s.ID)
			}
		}
		sort.Strings(want)
		if got := keptIDs(Apply(snaps, pol.keep)); !reflect.DeepEqual(got, want) {
			t.Errorf("policy %+v:\n ours   %v\n restic %v", pol.keep, short(got), short(want))
		}
	}
}

func short(ids []string) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = id[:8]
	}
	return out
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
