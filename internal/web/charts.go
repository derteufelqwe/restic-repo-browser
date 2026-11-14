package web

import (
	"encoding/json"
	"fmt"
	"time"

	"restic-repo-browser/internal/domain"
	"restic-repo-browser/internal/format"
	"restic-repo-browser/internal/retention"
)

// Chart data is computed here and handed to Chart.js as JSON in a data attribute.

type sparkSeries struct {
	Times []int64  `json:"t"` // unix milliseconds
	Added []int64  `json:"a"` // bytes
	IDs   []string `json:"s"` // short snapshot ids
}

// sparkJSON is the added-data series of the last 30 snapshots. The first
// snapshot holds the whole initial backup and is left out. Returns "" when
// there is too little history to draw.
func sparkJSON(snaps []domain.Snapshot) string {
	if len(snaps) < 3 {
		return ""
	}
	rest := snaps[1:]
	if len(rest) > 30 {
		rest = rest[len(rest)-30:]
	}
	var s sparkSeries
	for _, sn := range rest {
		s.Times = append(s.Times, sn.Time.UnixMilli())
		s.Added = append(s.Added, sn.AddedBytes())
		s.IDs = append(s.IDs, sn.ShortID)
	}
	b, _ := json.Marshal(s)
	return string(b)
}

type historySeries struct {
	Keys   []string `json:"keys"`
	Short  []string `json:"short"`
	Titles []string `json:"titles"`
	Added  []int64  `json:"added"`  // bytes
	Sizes  []int64  `json:"sizes"`  // bytes, size of the newest snapshot in each bucket
	Counts []int    `json:"counts"` // snapshots per bucket
	From   []int64  `json:"from"`   // unix seconds, inclusive
	To     []int64  `json:"to"`     // unix seconds, exclusive
}

// chartPanel is the data of the repository history chart block.
type chartPanel struct {
	RepoID int64
	Range  domain.Range
	Unit   string
	JSON   string
	Legend string
	Note   string
	Empty  bool
}

func buildChartPanel(repoID int64, snaps []domain.Snapshot, rng domain.Range, now time.Time) chartPanel {
	h := domain.BuildHistory(snaps, rng, now)
	var hs historySeries
	for _, b := range h.Buckets {
		hs.Keys = append(hs.Keys, b.Key)
		hs.Short = append(hs.Short, b.Short)
		hs.Titles = append(hs.Titles, b.Label)
		hs.Added = append(hs.Added, b.Added)
		hs.Sizes = append(hs.Sizes, b.Size)
		hs.Counts = append(hs.Counts, len(b.Snapshots))
		hs.From = append(hs.From, b.From.Unix())
		hs.To = append(hs.To, b.To.Unix())
	}
	raw, _ := json.Marshal(hs)
	p := chartPanel{
		RepoID: repoID, Range: rng, Unit: h.Unit, JSON: string(raw), Empty: len(h.Buckets) == 0,
		Legend: fmt.Sprintf("Each bar is one %s. Click a bar to list only the snapshots from that %s.", h.Unit, h.Unit),
	}
	if h.Slab != nil {
		p.Note = fmt.Sprintf("%s (%s%s) is not shown, so smaller changes stay readable.", h.Slab.Label, format.Size(h.Slab.Added), snapshotCount(h.Slab.Count))
	}
	return p
}

func snapshotCount(n int) string {
	if n <= 1 {
		return ""
	}
	return fmt.Sprintf(", %s snapshots", format.Int(int64(n)))
}

type retentionSeries struct {
	Labels []string `json:"labels"`
	Titles []string `json:"titles"`
	Kept   []int    `json:"kept"`
	Gone   []int    `json:"gone"`
	Unit   string   `json:"unit"`
}

// retentionJSON counts kept and forgotten snapshots per period for the cleanup preview.
func retentionJSON(plan retention.Plan, now time.Time) string {
	if len(plan.Decisions) == 0 {
		return ""
	}
	// Decisions are newest first.
	newest := plan.Decisions[0].Snapshot.Time
	oldest := plan.Decisions[len(plan.Decisions)-1].Snapshot.Time
	spanDays := newest.Sub(oldest).Hours() / 24
	step := 91
	for _, d := range []int{1, 7, 14, 30, 91} {
		if spanDays/float64(d) <= 60 {
			step = d
			break
		}
	}
	stepDur := time.Duration(step) * 24 * time.Hour
	n := int(newest.Sub(oldest)/stepDur) + 1
	s := retentionSeries{
		Labels: make([]string, n), Titles: make([]string, n),
		Kept: make([]int, n), Gone: make([]int, n),
		Unit: map[int]string{1: "day", 7: "week", 14: "two weeks", 30: "month", 91: "quarter"}[step],
	}
	for i := 0; i < n; i++ {
		start := oldest.Add(time.Duration(i) * stepDur)
		s.Titles[i] = "From " + format.Date(start, now, false)
		if spanDays > 300 {
			s.Labels[i] = start.Format("Jan '06")
		} else {
			s.Labels[i] = start.Format("Jan 2")
		}
	}
	for _, d := range plan.Decisions {
		i := int(d.Snapshot.Time.Sub(oldest) / stepDur)
		if d.Kept() {
			s.Kept[i]++
		} else {
			s.Gone[i]++
		}
	}
	b, _ := json.Marshal(s)
	return string(b)
}
