package web

import (
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"restic-repo-browser/internal/cache"
	"restic-repo-browser/internal/domain"
	"restic-repo-browser/internal/format"
	"restic-repo-browser/internal/schedule"
	"restic-repo-browser/internal/store"
)

// fact is one cell of the stat strip.
type fact struct{ Label, Value, Sub string }

type notice struct {
	Kind    string // "error" or "warn"
	Title   string
	Body    string
	Details string // shown in monospace below the body
}

type repoPage struct {
	S         domain.Summary
	Facts     []fact
	Notices   []notice
	Chart     chartPanel
	Snapshots snapshotList
	Cleanup   cleanupView
	TabLabel  string // state of the cleanup job shown on the tab
}

func (s *Server) handleRepo(w http.ResponseWriter, r *http.Request) {
	sum, ok := s.repoFromPath(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	cleanup, err := s.buildCleanupView(ctx, sum)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	refreshTime, err := s.Store.Setting(ctx, store.SettingCacheRefreshTime, cache.DefaultRefreshTime)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	data := repoPage{
		S:         sum,
		Facts:     s.facts(sum, refreshTime),
		Notices:   s.notices(sum),
		Chart:     buildChartPanel(sum.Repo.ID, sum.Snapshots, domain.Range30Days, s.Now()),
		Snapshots: s.buildSnapshotList(sum, parseSnapshotQuery(r)),
		Cleanup:   cleanup,
		TabLabel:  cleanup.TabLabel(),
	}
	s.renderPage(w, r, "repo", sum.Repo.Name, fmt.Sprintf("repo:%d", sum.Repo.ID), data)
}

func (s *Server) facts(sum domain.Summary, refreshTime string) []fact {
	now := s.Now()
	f := []fact{}
	if last := sum.Last(); last != nil {
		f = append(f,
			fact{"Last backup", format.Rel(last.Time, now), format.Date(last.Time, now, true)},
			fact{"Snapshots", format.Int(int64(sum.Count())), strings.Join(sum.Hosts(), ", ")},
			fact{"Repository size", format.Size(sum.Repo.SizeBytes), ""},
			fact{"Latest snapshot", sizeOrDash(last.Size), filesText(last.Files)},
			fact{"Added in 30 days", format.Size(sum.Added30Days()), ""},
		)
	} else {
		f = append(f,
			fact{"Last backup", "Never", ""},
			fact{"Snapshots", "0", ""},
			fact{"Repository size", format.Size(sum.Repo.SizeBytes), ""},
		)
	}
	f = append(f, syncFact(sum.Repo, refreshTime, now))
	if sum.Job != nil {
		jv := newJobView(*sum.Job, sum.Repo.ReadOnly, now)
		next := "Paused"
		if jv.ReadOnly {
			next = "Never (read-only)"
		} else if !jv.Next.IsZero() {
			next = format.Run(jv.Next)
		}
		f = append(f, fact{"Next cleanup", next, ""})
	} else {
		f = append(f, fact{"Next cleanup", "Not set up", ""})
	}
	return f
}

func sizeOrDash(p *int64) string {
	if p == nil {
		return "Unknown"
	}
	return format.Size(*p)
}

func filesText(p *int64) string {
	if p == nil {
		return ""
	}
	return format.Int(*p) + " files"
}

func (s *Server) notices(sum domain.Summary) []notice {
	now := s.Now()
	var out []notice
	if sum.Repo.Unreachable() {
		saved := "no data was ever saved"
		if l := sum.Last(); l != nil {
			saved = "showing the data saved on " + format.Date(sum.Repo.RefreshOKAt, now, false)
		}
		out = append(out, notice{Kind: "error", Title: "This repository cannot be reached",
			Body:    "Browsing and downloads are unavailable until it is back (" + saved + ").",
			Details: sum.Repo.RefreshError})
	}
	for _, l := range sum.Locks {
		kind := "a lock (usually a running backup)"
		if l.Exclusive {
			kind = "an exclusive lock (forget, prune or check)"
		}
		out = append(out, notice{Kind: "warn", Title: l.Host + " holds " + kind,
			Body: fmt.Sprintf("The lock was created %s (as of the last data update). You can keep browsing. The cleanup job waits until the lock is gone.", format.Rel(l.Time, now))})
	}
	if !sum.Repo.Unreachable() && sum.Last() != nil {
		last := sum.Last()
		title := "No backup for " + strings.TrimSuffix(format.Rel(last.Time, now), " ago")
		switch {
		case sum.Scheduled && sum.Missed > 0:
			kind := "warn"
			if sum.State == domain.StateOverdue {
				kind = "error"
			}
			expected := "on its schedule"
			if sch, err := schedule.Parse(sum.Repo.BackupCron); err == nil {
				expected = strings.ToLower(sch.Describe())
			}
			out = append(out, notice{Kind: kind, Title: title,
				Body: fmt.Sprintf("The last snapshot is from %s. Backups are expected %s. %s missed since then.",
					format.Date(last.Time, now, true), expected, missedText(sum.Missed))})
		case !sum.Scheduled && sum.State == domain.StateOverdue:
			out = append(out, notice{Kind: "warn", Title: title,
				Body: fmt.Sprintf("The last snapshot is from %s. This repository normally gets one every %s.", format.Date(last.Time, now, true), intervalText(sum.Interval))})
		}
	}
	return out
}

func missedText(n int) string {
	if n == 1 {
		return "1 scheduled backup was"
	}
	return fmt.Sprintf("%d scheduled backups were", n)
}

func intervalText(d time.Duration) string {
	switch {
	case d >= 36*time.Hour:
		return fmt.Sprintf("%d days", int((d+12*time.Hour)/(24*time.Hour)))
	case d >= 20*time.Hour:
		return "day"
	case d >= 90*time.Minute:
		return fmt.Sprintf("%d hours", int((d+30*time.Minute)/time.Hour))
	}
	return "hour"
}

// ---- chart fragment ----

func (s *Server) handleChart(w http.ResponseWriter, r *http.Request) {
	sum, ok := s.repoFromPath(w, r)
	if !ok {
		return
	}
	rng := domain.ParseRange(r.URL.Query().Get("range"), domain.Range30Days)
	s.renderFragment(w, r, "chart_panel", buildChartPanel(sum.Repo.ID, sum.Snapshots, rng, s.Now()))
}

// ---- snapshot list ----

type snapshotQuery struct {
	Filter domain.SnapshotFilter
	Label  string // chip text for a date filter
	Page   int
	Per    int
}

var perPageOptions = []int{defaultPerPage, 30, 50}

func parseSnapshotQuery(r *http.Request) snapshotQuery {
	q := r.URL.Query()
	sq := snapshotQuery{
		Filter: domain.SnapshotFilter{Query: q.Get("q"), Host: q.Get("host"), Tag: q.Get("tag")},
		Label:  q.Get("label"), Page: atoiDefault(q.Get("page"), 1), Per: defaultPerPage,
	}
	if n := atoiDefault(q.Get("per"), defaultPerPage); slices.Contains(perPageOptions, n) {
		sq.Per = n
	}
	if v, err := strconv.ParseInt(q.Get("from"), 10, 64); err == nil && v > 0 {
		sq.Filter.From = time.Unix(v, 0)
	}
	if v, err := strconv.ParseInt(q.Get("to"), 10, 64); err == nil && v > 0 {
		sq.Filter.To = time.Unix(v, 0)
	}
	return sq
}

func atoiDefault(s string, def int) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

type snapshotList struct {
	S        domain.Summary
	Query    snapshotQuery
	Page     Page[domain.Snapshot]
	Links    []int
	Hosts    []string
	Tags     []string
	PerOpts  []int
	Browsing bool // false when the repository is unreachable
	From, To int64
	Chip     bool
	OOB      bool // render the chip as an out-of-band swap
}

func (s *Server) buildSnapshotList(sum domain.Summary, q snapshotQuery) snapshotList {
	var matches []domain.Snapshot
	for i := len(sum.Snapshots) - 1; i >= 0; i-- { // newest first
		if q.Filter.Matches(sum.Snapshots[i]) {
			matches = append(matches, sum.Snapshots[i])
		}
	}
	page := Paginate(matches, q.Page, q.Per)
	l := snapshotList{
		S: sum, Query: q, Page: page, Links: page.PageLinks(),
		Hosts: sum.Hosts(), Tags: sum.Tags(), PerOpts: perPageOptions,
		Browsing: !sum.Repo.Unreachable(),
		Chip:     !q.Filter.From.IsZero() && !q.Filter.To.IsZero() && q.Label != "",
	}
	if l.Chip {
		l.From, l.To = q.Filter.From.Unix(), q.Filter.To.Unix()
	}
	return l
}

func (s *Server) handleSnapshotList(w http.ResponseWriter, r *http.Request) {
	sum, ok := s.repoFromPath(w, r)
	if !ok {
		return
	}
	l := s.buildSnapshotList(sum, parseSnapshotQuery(r))
	l.OOB = true
	s.renderFragment(w, r, "snapshot_results", l)
}

// syncFact is the "Last sync" cell of the stat strip.
func syncFact(repo domain.Repo, dailyAt string, now time.Time) fact {
	switch {
	case repo.RefreshedAt.IsZero():
		return fact{"Last sync", "Never", "Syncs daily at " + dailyAt}
	case repo.Unreachable():
		return fact{"Last sync", "Failed " + format.Rel(repo.RefreshedAt, now), "Syncs daily at " + dailyAt}
	}
	return fact{"Last sync", format.Rel(repo.RefreshedAt, now), "Succeeded. Syncs daily at " + dailyAt}
}
