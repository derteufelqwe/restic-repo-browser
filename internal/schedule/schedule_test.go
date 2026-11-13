package schedule

import (
	"testing"
	"time"
)

func TestParseRejectsInvalid(t *testing.T) {
	for _, spec := range []string{"", "* * * *", "61 * * * *", "a b c d e", "@daily"} {
		if _, err := Parse(spec); err == nil {
			t.Errorf("Parse(%q) should fail", spec)
		}
	}
}

func TestDescribe(t *testing.T) {
	cases := map[string]string{
		"30 3 * * *":   "Every day at 03:30",
		"0 4 * * 0":    "Every Sunday at 04:00",
		"0 5 1 * *":    "On day 1 of every month at 05:00",
		"0 6 1 */3 *":  "On day 1, every 3 months, at 06:00",
		"15 * * * *":   "Every hour at 15 minutes past",
		"*/10 * * * *": "Every 10 minutes",
	}
	for spec, want := range cases {
		s, err := Parse(spec)
		if err != nil {
			t.Fatalf("Parse(%q): %v", spec, err)
		}
		if got := s.Describe(); got != want {
			t.Errorf("Describe(%q) = %q, want %q", spec, got, want)
		}
	}
}

func TestDescribeGeneral(t *testing.T) {
	cases := map[string]string{
		"0 3 * * 1-5":       "At 03:00, on Monday through Friday",
		"*/15 9-17 * * 1-5": "Every 15 minutes, between 09:00 and 17:59, on Monday through Friday",
		"0 */6 * * *":       "At minute 0 past every 6th hour",
		"0 3,15 * * *":      "At 03:00 and 15:00",
		"30 2 1,15 * *":     "At 02:30, on days 1 and 15 of the month",
		"0 4 * * mon,fri":   "At 04:00, on Monday and Friday",
		"0 0 13 * fri":      "At 00:00, on day 13 of the month or on Friday",
		"15 3 * jan,jul *":  "At 03:15, in January and July",
		"0 5 1 */3 *":       "On day 1, every 3 months, at 05:00",
		"* * * * *":         "Every minute",
		"10,40 * * * *":     "At minutes 10 and 40 past every hour",
	}
	for spec, want := range cases {
		s, err := Parse(spec)
		if err != nil {
			t.Fatalf("Parse(%q): %v", spec, err)
		}
		if got := s.Describe(); got != want {
			t.Errorf("Describe(%q) = %q, want %q", spec, got, want)
		}
	}
}

func TestNextRuns(t *testing.T) {
	s, _ := Parse("30 3 * * *")
	from := time.Date(2026, 11, 11, 14, 20, 0, 0, time.Local)
	runs := s.NextRuns(from, 3)
	if len(runs) != 3 {
		t.Fatalf("got %d runs", len(runs))
	}
	want := time.Date(2026, 11, 12, 3, 30, 0, 0, time.Local)
	if !runs[0].Equal(want) || !runs[1].Equal(want.AddDate(0, 0, 1)) {
		t.Errorf("unexpected runs: %v", runs)
	}
}
