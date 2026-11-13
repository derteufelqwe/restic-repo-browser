package web

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"restic-repo-browser/internal/domain"
	"restic-repo-browser/internal/format"
)

type sortOption struct{ Key, Label string }

var overviewSorts = []sortOption{
	{"attention", "Needs attention first"},
	{"name", "Name"},
	{"last", "Last backup"},
	{"size", "Repository size"},
	{"count", "Snapshot count"},
}

// repoCard is one repository in the overview list.
type repoCard struct {
	S     domain.Summary
	Spark string // JSON for the sparkline, empty when there is not enough history
}

type overviewData struct {
	Cards    []repoCard
	Sort     string
	Sorts    []sortOption
	Summary  string
	LastSync time.Time // most recent sync attempt of any repository
}

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	sums, err := s.summaries(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	key := r.URL.Query().Get("sort")
	if !validSort(key) {
		key = "attention"
	}
	sortSummaries(sums, key)

	data := overviewData{Sort: key, Sorts: overviewSorts, Summary: overviewSummary(sums)}
	for _, sum := range sums {
		if sum.Repo.RefreshedAt.After(data.LastSync) {
			data.LastSync = sum.Repo.RefreshedAt
		}
		data.Cards = append(data.Cards, repoCard{S: sum, Spark: sparkJSON(sum.Snapshots)})
	}
	if r.Header.Get("HX-Target") == "repo-cards" {
		s.renderFragment(w, r, "repo_cards", data)
		return
	}
	s.renderPage(w, r, "overview", "Repositories", "overview", data)
}

func validSort(key string) bool {
	for _, o := range overviewSorts {
		if o.Key == key {
			return true
		}
	}
	return false
}

func sortSummaries(sums []domain.Summary, key string) {
	lastTime := func(s domain.Summary) time.Time {
		if l := s.Last(); l != nil {
			return l.Time
		}
		return time.Time{}
	}
	less := map[string]func(a, b domain.Summary) bool{
		"attention": func(a, b domain.Summary) bool {
			if sa, sb := a.State.Severity(), b.State.Severity(); sa != sb {
				return sa < sb
			}
			return strings.ToLower(a.Repo.Name) < strings.ToLower(b.Repo.Name)
		},
		"name":  func(a, b domain.Summary) bool { return strings.ToLower(a.Repo.Name) < strings.ToLower(b.Repo.Name) },
		"last":  func(a, b domain.Summary) bool { return lastTime(a).After(lastTime(b)) },
		"size":  func(a, b domain.Summary) bool { return a.Repo.SizeBytes > b.Repo.SizeBytes },
		"count": func(a, b domain.Summary) bool { return a.Count() > b.Count() },
	}[key]
	sort.SliceStable(sums, func(i, j int) bool { return less(sums[i], sums[j]) })
}

func overviewSummary(sums []domain.Summary) string {
	if len(sums) == 0 {
		return "No repositories yet."
	}
	var total int64
	need := 0
	for _, s := range sums {
		total += s.Repo.SizeBytes
		if s.State == domain.StateOverdue || s.State == domain.StateLate || s.State == domain.StateUnreachable {
			need++
		}
	}
	text := format.Int(int64(len(sums))) + " repositories, " + format.Size(total) + " stored. "
	switch need {
	case 0:
		return text + "Everything is on schedule."
	case 1:
		return text + "1 needs attention."
	}
	return text + format.Int(int64(need)) + " need attention."
}
