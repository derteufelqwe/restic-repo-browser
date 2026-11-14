package retention

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	"restic-repo-browser/internal/domain"
	"restic-repo-browser/internal/restic"
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

func TestKeepLast(t *testing.T) {
	plan := Apply(daily(10, end), domain.KeepPolicy{Last: 3})
	if got := keptIDs(plan); !reflect.DeepEqual(got, []string{"s07", "s08", "s09"}) {
		t.Errorf("kept = %v", got)
	}
	if len(plan.Forgotten()) != 7 {
		t.Errorf("forgotten = %d", len(plan.Forgotten()))
	}
}

func TestDailyKeepsOnePerDay(t *testing.T) {
	// Two snapshots per day for three days; keep-daily 2 keeps the newest of each of the last two days.
	var snaps []domain.Snapshot
	for d := 0; d < 3; d++ {
		day := end.AddDate(0, 0, -d)
		snaps = append(snaps, snap(fmt.Sprintf("d%dam", d), day.Add(-6*time.Hour), "h"), snap(fmt.Sprintf("d%dpm", d), day, "h"))
	}
	plan := Apply(snaps, domain.KeepPolicy{Daily: 2})
	if got := keptIDs(plan); !reflect.DeepEqual(got, []string{"d0pm", "d1pm"}) {
		t.Errorf("kept = %v", got)
	}
}

func TestWeeklyMonthlyYearly(t *testing.T) {
	snaps := daily(400, end)
	plan := Apply(snaps, domain.KeepPolicy{Weekly: 4, Monthly: 3, Yearly: 2})
	counts := plan.ReasonCounts()
	if counts[ReasonWeekly] != 4 || counts[ReasonMonthly] != 3 || counts[ReasonYearly] != 2 {
		t.Errorf("counts = %v", counts)
	}
}

func TestGroupsAreIndependent(t *testing.T) {
	var snaps []domain.Snapshot
	for i := 0; i < 4; i++ {
		snaps = append(snaps, snap(fmt.Sprintf("a%d", i), end.Add(-time.Duration(i)*time.Hour), "hostA"))
		snaps = append(snaps, snap(fmt.Sprintf("b%d", i), end.Add(-time.Duration(i)*time.Hour), "hostB"))
	}
	plan := Apply(snaps, domain.KeepPolicy{Last: 1})
	if got := keptIDs(plan); !reflect.DeepEqual(got, []string{"a0", "b0"}) {
		t.Errorf("kept = %v", got)
	}
}

func TestReasonOrderAndCounts(t *testing.T) {
	plan := Apply(daily(3, end), domain.KeepPolicy{Last: 1, Daily: 1})
	newest := plan.Decisions[0]
	if !reflect.DeepEqual(newest.Reasons, []Reason{ReasonLatest, ReasonDaily}) {
		t.Errorf("reasons = %v", newest.Reasons)
	}
}

func TestEmptyPolicyKeepsEverything(t *testing.T) {
	plan := Apply(daily(4, end), domain.KeepPolicy{})
	if len(plan.Forgotten()) != 0 {
		t.Errorf("plan = %+v", plan)
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
	write := func(n int) {
		must(t, writeFile(filepath.Join(src, "f.txt"), fmt.Sprint(n)))
	}
	times := []string{
		"2026-01-05 08:00:00", "2026-01-05 20:00:00", "2026-01-06 08:00:00", "2026-01-12 09:00:00",
		"2026-02-01 07:00:00", "2026-02-02 07:00:00", "2026-02-20 07:00:00", "2026-03-15 07:00:00",
		"2026-03-16 07:00:00", "2026-03-16 19:00:00", "2026-04-30 07:00:00", "2026-05-01 07:00:00",
	}
	for i, ts := range times {
		write(i)
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

	client := restic.New(bin, filepath.Join(dir, "cache"))
	repo := restic.Repo{Path: repoPath, Password: "pw"}
	rs, err := client.Snapshots(context.Background(), repo)
	must(t, err)
	var snaps []domain.Snapshot
	for _, s := range rs {
		snaps = append(snaps, domain.Snapshot{ID: s.ID, Time: s.Time, Host: s.Hostname, Paths: s.Paths, Tags: s.Tags})
	}

	policies := []domain.KeepPolicy{
		{Last: 3},
		{Daily: 4},
		{Daily: 5, Weekly: 2},
		{Weekly: 3, Monthly: 2},
		{Monthly: 2, Yearly: 1},
		{Last: 1, Daily: 2},
		{Last: 2, Daily: 3, Weekly: 2, Monthly: 3, Yearly: 1},
	}
	for _, pol := range policies {
		plan := Apply(snaps, pol)
		res, err := client.Forget(context.Background(), repo, pol, true)
		must(t, err)
		var want []string
		for _, g := range res.Groups {
			for _, s := range g.Keep {
				want = append(want, s.ID)
			}
		}
		sort.Strings(want)
		if got := keptIDs(plan); !reflect.DeepEqual(got, want) {
			t.Errorf("policy %+v:\n ours   %v\n restic %v", pol, short(got), short(want))
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
