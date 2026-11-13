package format

import (
	"testing"
	"time"
)

var now = time.Date(2026, 11, 11, 14, 20, 0, 0, time.UTC)

func TestInt(t *testing.T) {
	cases := map[int64]string{0: "0", 999: "999", 1000: "1,000", 1462: "1,462", 1234567: "1,234,567", -4200: "-4,200"}
	for in, want := range cases {
		if got := Int(in); got != want {
			t.Errorf("Int(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestSize(t *testing.T) {
	cases := map[int64]string{
		0:                      "0 B",
		512:                    "512 B",
		13 * 1024:              "13 KB",
		7*1024*1024 + 400*1024: "7.4 MB",
		250 * 1024 * 1024:      "250 MB",
		1 << 30:                "1.0 GB",
		438 * (1 << 30):        "438.0 GB",
		2 * (1 << 40):          "2.00 TB",
	}
	for in, want := range cases {
		if got := Size(in); got != want {
			t.Errorf("Size(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestDate(t *testing.T) {
	if got := Date(now.Add(-26*time.Hour), now, true); got != "Nov 10, 12:20" {
		t.Errorf("got %q", got)
	}
	if got := Date(time.Date(2025, 3, 1, 9, 5, 0, 0, time.UTC), now, false); got != "Mar 1, 2025" {
		t.Errorf("got %q", got)
	}
}

func TestRel(t *testing.T) {
	cases := []struct {
		ago  time.Duration
		want string
	}{
		{10 * time.Second, "just now"},
		{12 * time.Minute, "12 min ago"},
		{5 * time.Hour, "5 h ago"},
		{4 * 24 * time.Hour, "4 days ago"},
		{200 * 24 * time.Hour, "7 months ago"},
	}
	for _, c := range cases {
		if got := Rel(now.Add(-c.ago), now); got != c.want {
			t.Errorf("Rel(-%v) = %q, want %q", c.ago, got, c.want)
		}
	}
}

func TestFuture(t *testing.T) {
	if got := Future(now.Add(30*time.Minute), now); got != "in 30 min" {
		t.Errorf("got %q", got)
	}
	if got := Future(now.Add(3*24*time.Hour), now); got != "in 3 days" {
		t.Errorf("got %q", got)
	}
}

func TestDuration(t *testing.T) {
	if got := Duration(2 * time.Second); got != "2 s" {
		t.Errorf("got %q", got)
	}
	if got := Duration(192 * time.Second); got != "3 min 12 s" {
		t.Errorf("got %q", got)
	}
}
