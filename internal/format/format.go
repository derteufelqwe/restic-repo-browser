// Package format turns numbers and times into the short human-readable strings
// used throughout the UI. All time-relative helpers take "now" explicitly so
// output is deterministic and testable.
package format

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

var months = [...]string{"Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"}

const (
	kib = 1 << 10
	mib = 1 << 20
	gib = 1 << 30
	tib = 1 << 40
)

// Int formats n with thousands separators ("1,462").
func Int(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// Size formats a byte count with one decimal for GB and above, and fewer
// digits for smaller units ("438.2 GB", "7.4 MB", "13 KB", "512 B").
func Size(b int64) string {
	switch {
	case b >= tib:
		return fmt.Sprintf("%.2f TB", float64(b)/tib)
	case b >= gib:
		return fmt.Sprintf("%s GB", decimal(float64(b)/gib, 1))
	case b >= mib:
		v := float64(b) / mib
		if v < 10 {
			return fmt.Sprintf("%.1f MB", v)
		}
		return fmt.Sprintf("%.0f MB", v)
	case b >= kib:
		return fmt.Sprintf("%.0f KB", float64(b)/kib)
	default:
		return fmt.Sprintf("%d B", b)
	}
}

// decimal formats v with the given digits and thousands separators.
func decimal(v float64, digits int) string {
	s := strconv.FormatFloat(v, 'f', digits, 64)
	whole, frac, _ := strings.Cut(s, ".")
	n, _ := strconv.ParseInt(whole, 10, 64)
	if frac == "" {
		return Int(n)
	}
	return Int(n) + "." + frac
}

// Date formats t as "Nov 11, 14:20", adding the year when it differs from now's.
func Date(t, now time.Time, withTime bool) string {
	t = t.In(now.Location())
	s := fmt.Sprintf("%s %d", months[t.Month()-1], t.Day())
	if t.Year() != now.Year() {
		s += fmt.Sprintf(", %d", t.Year())
	}
	if withTime {
		s += fmt.Sprintf(", %02d:%02d", t.Hour(), t.Minute())
	}
	return s
}

// Rel describes a past time relative to now ("2 h ago").
func Rel(t, now time.Time) string {
	mins := int(now.Sub(t).Round(time.Minute) / time.Minute)
	switch {
	case mins < 1:
		return "just now"
	case mins < 60:
		return fmt.Sprintf("%d min ago", mins)
	}
	h := round(float64(mins) / 60)
	if h < 36 {
		return fmt.Sprintf("%d h ago", h)
	}
	days := round(float64(mins) / 1440)
	if days < 60 {
		return fmt.Sprintf("%d days ago", days)
	}
	return fmt.Sprintf("%d months ago", round(float64(days)/30))
}

// Future describes a future time relative to now ("in 3 h").
func Future(t, now time.Time) string {
	mins := int(t.Sub(now).Round(time.Minute) / time.Minute)
	if mins < 60 {
		if mins < 1 {
			mins = 1
		}
		return fmt.Sprintf("in %d min", mins)
	}
	h := round(float64(mins) / 60)
	if h < 36 {
		return fmt.Sprintf("in %d h", h)
	}
	return fmt.Sprintf("in %d days", round(float64(mins)/1440))
}

// Run formats a scheduled run time ("Sun, Nov 15, 04:00").
func Run(t time.Time) string {
	return fmt.Sprintf("%s, %s %d, %02d:%02d", t.Weekday().String()[:3], months[t.Month()-1], t.Day(), t.Hour(), t.Minute())
}

// Duration formats a run duration ("2 s", "3 min 12 s").
func Duration(d time.Duration) string {
	s := int(d.Round(time.Second) / time.Second)
	if s < 60 {
		return fmt.Sprintf("%d s", s)
	}
	return fmt.Sprintf("%d min %d s", s/60, s%60)
}

func round(f float64) int { return int(f + 0.5) }
