package domain

import (
	"strings"
	"time"
)

// SnapshotFilter narrows the snapshot list shown on the repository page.
type SnapshotFilter struct {
	Query string
	Host  string
	Tag   string
	From  time.Time // inclusive; zero means unbounded
	To    time.Time // exclusive; zero means unbounded
}

// Matches reports whether s passes the filter.
func (f SnapshotFilter) Matches(s Snapshot) bool {
	if f.Host != "" && s.Host != f.Host {
		return false
	}
	if f.Tag != "" && !s.HasTag(f.Tag) {
		return false
	}
	if !f.From.IsZero() && s.Time.Before(f.From) {
		return false
	}
	if !f.To.IsZero() && !s.Time.Before(f.To) {
		return false
	}
	if q := strings.ToLower(strings.TrimSpace(f.Query)); q != "" {
		hay := strings.ToLower(strings.Join(append(append([]string{s.ShortID, s.ID, s.Host}, s.Paths...), s.Tags...), " "))
		if !strings.Contains(hay, q) {
			return false
		}
	}
	return true
}

// Active reports whether any criterion is set.
func (f SnapshotFilter) Active() bool {
	return f.Query != "" || f.Host != "" || f.Tag != "" || !f.From.IsZero() || !f.To.IsZero()
}
