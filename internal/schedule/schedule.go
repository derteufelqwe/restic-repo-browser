// Package schedule parses five-field cron expressions and describes them in
// plain English.
package schedule

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

var parser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

// Schedule is a parsed cron expression.
type Schedule struct {
	Spec string
	sch  cron.Schedule
}

// Parse validates spec (minute hour day-of-month month weekday).
func Parse(spec string) (Schedule, error) {
	spec = strings.Join(strings.Fields(spec), " ")
	sch, err := parser.Parse(spec)
	if err != nil {
		return Schedule{}, fmt.Errorf("invalid schedule %q: %w", spec, err)
	}
	return Schedule{Spec: spec, sch: sch}, nil
}

// Next returns the first run time strictly after t.
func (s Schedule) Next(t time.Time) time.Time { return s.sch.Next(t) }

// NextRuns returns the next n run times after from.
func (s Schedule) NextRuns(from time.Time, n int) []time.Time {
	out := make([]time.Time, 0, n)
	t := from
	for len(out) < n {
		t = s.sch.Next(t)
		if t.IsZero() {
			break
		}
		out = append(out, t)
	}
	return out
}

var (
	numRe   = regexp.MustCompile(`^\d+$`)
	weekday = [...]string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}
)

func atoi(s string) int {
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n
}
