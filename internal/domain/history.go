package domain

import (
	"fmt"
	"time"
)

// Range selects how much history the repository chart shows.
type Range string

// The chart ranges offered in the UI.
const (
	Range30Days  Range = "30d"
	Range6Months Range = "6m"
	RangeAll     Range = "all"
)

// ParseRange returns r when known, otherwise def.
func ParseRange(r string, def Range) Range {
	switch Range(r) {
	case Range30Days, Range6Months, RangeAll:
		return Range(r)
	}
	return def
}

// Bucket groups the snapshots of one day, week or month for the history chart.
type Bucket struct {
	Key       string
	Label     string // tooltip title
	Short     string // axis label
	From, To  time.Time
	Snapshots []Snapshot // oldest first
	Added     int64
	Size      int64 // size of the newest snapshot in the bucket
}

// Slab is data that is left out of the chart so the smaller bars stay readable:
// the initial backup (range "all") or everything older than the range.
type Slab struct {
	Label string
	Added int64
	Count int
}

// History is the bucketed data for the repository chart, oldest bucket first.
type History struct {
	Unit    string // "day", "week" or "month"
	Buckets []Bucket
	Slab    *Slab
}

// BuildHistory buckets snaps (oldest first) for the given range.
func BuildHistory(snaps []Snapshot, rng Range, now time.Time) History {
	unit := "month"
	var from time.Time
	switch rng {
	case Range30Days:
		unit, from = "day", now.AddDate(0, 0, -30)
	case Range6Months:
		unit, from = "week", now.AddDate(0, 0, -183)
	}

	var slabSnaps, rest []Snapshot
	switch {
	case rng == RangeAll && len(snaps) > 0:
		slabSnaps, rest = snaps[:1], snaps[1:]
	case rng == RangeAll:
	default:
		for _, s := range snaps {
			if s.Time.Before(from) {
				slabSnaps = append(slabSnaps, s)
			} else {
				rest = append(rest, s)
			}
		}
	}

	h := History{Unit: unit}
	index := map[string]int{}
	for _, s := range rest {
		start, end := bucketSpan(s.Time, unit)
		key := start.Format("2006-01-02")
		i, ok := index[key]
		if !ok {
			i = len(h.Buckets)
			index[key] = i
			h.Buckets = append(h.Buckets, Bucket{
				Key: key, From: start, To: end,
				Label: bucketLabel(start, unit, now), Short: bucketShort(start, unit),
			})
		}
		b := &h.Buckets[i]
		b.Snapshots = append(b.Snapshots, s)
		b.Added += s.AddedBytes()
		b.Size = s.SizeBytes()
	}

	if len(slabSnaps) > 0 {
		slab := &Slab{Label: "Earlier snapshots", Count: len(slabSnaps)}
		if rng == RangeAll {
			slab.Label = "Initial backup"
		}
		for _, s := range slabSnaps {
			slab.Added += s.AddedBytes()
		}
		h.Slab = slab
	}
	return h
}

// bucketSpan returns the start (inclusive) and end (exclusive) of the bucket t falls in.
func bucketSpan(t time.Time, unit string) (time.Time, time.Time) {
	y, m, d := t.Date()
	loc := t.Location()
	switch unit {
	case "day":
		start := time.Date(y, m, d, 0, 0, 0, 0, loc)
		return start, start.AddDate(0, 0, 1)
	case "week":
		start := time.Date(y, m, d-int((t.Weekday()+6)%7), 0, 0, 0, 0, loc) // Monday
		return start, start.AddDate(0, 0, 7)
	default:
		start := time.Date(y, m, 1, 0, 0, 0, 0, loc)
		return start, start.AddDate(0, 1, 0)
	}
}

func bucketLabel(start time.Time, unit string, now time.Time) string {
	date := start.Format("Jan 2")
	if start.Year() != now.Year() {
		date = start.Format("Jan 2, 2006")
	}
	switch unit {
	case "week":
		return "Week of " + date
	case "month":
		return start.Format("Jan 2006")
	}
	return date
}

func bucketShort(start time.Time, unit string) string {
	if unit == "month" {
		return start.Format("Jan 2006")
	}
	return fmt.Sprintf("%s %d", start.Format("Jan"), start.Day())
}
