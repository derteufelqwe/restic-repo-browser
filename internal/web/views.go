package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"restic-repo-browser/internal/domain"
	"restic-repo-browser/internal/schedule"
	"restic-repo-browser/internal/store"
)

// railItem is a repository entry in the navigation rail.
type railItem struct {
	ID    int64
	Name  string
	Class string // status dot class
	Hint  string
}

type rail struct {
	Repos  []railItem
	Failed int // repositories whose last cleanup run failed
}

func (s *Server) loadRail(ctx context.Context) (rail, error) {
	sums, err := s.summaries(ctx)
	if err != nil {
		return rail{}, err
	}
	var rl rail
	for _, sum := range sums {
		rl.Repos = append(rl.Repos, railItem{ID: sum.Repo.ID, Name: sum.Repo.Name, Class: sum.State.CSSClass(), Hint: sum.State.Hint()})
		if sum.LastRun != nil && sum.LastRun.Status == domain.RunFailed {
			rl.Failed++
		}
	}
	return rl, nil
}

// summaries loads every repository with its cached data.
func (s *Server) summaries(ctx context.Context) ([]domain.Summary, error) {
	repos, err := s.Store.ListRepos(ctx)
	if err != nil {
		return nil, err
	}
	jobs, err := s.Store.Jobs(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Summary, 0, len(repos))
	for _, repo := range repos {
		sum, err := s.summarize(ctx, repo, jobs)
		if err != nil {
			return nil, err
		}
		out = append(out, sum)
	}
	return out, nil
}

// summary loads one repository; jobs may be nil to load its job on demand.
func (s *Server) summary(ctx context.Context, id int64) (domain.Summary, error) {
	repo, err := s.Store.Repo(ctx, id)
	if err != nil {
		return domain.Summary{}, err
	}
	job, err := s.Store.Job(ctx, id)
	if err != nil {
		return domain.Summary{}, err
	}
	jobs := map[int64]domain.Job{}
	if job != nil {
		jobs[id] = *job
	}
	return s.summarize(ctx, repo, jobs)
}

func (s *Server) summarize(ctx context.Context, repo domain.Repo, jobs map[int64]domain.Job) (domain.Summary, error) {
	snaps, err := s.Store.Snapshots(ctx, repo.ID)
	if err != nil {
		return domain.Summary{}, err
	}
	locks, err := s.Store.Locks(ctx, repo.ID)
	if err != nil {
		return domain.Summary{}, err
	}
	var job *domain.Job
	if j, ok := jobs[repo.ID]; ok {
		job = &j
	}
	var sched domain.Schedule // stays a nil interface without a valid backup schedule
	if repo.BackupCron != "" {
		if p, err := schedule.Parse(repo.BackupCron); err == nil {
			sched = p
		}
	}
	sum := domain.Summarize(repo, snaps, locks, job, sched, s.Now())
	if runs, err := s.Store.RepoRuns(ctx, repo.ID, 1); err != nil {
		return domain.Summary{}, err
	} else if len(runs) > 0 {
		sum.LastRun = &runs[0]
	}
	return sum, nil
}

// repoFromPath parses the {id} path value and loads the repository summary,
// answering 404 itself when it does not exist.
func (s *Server) repoFromPath(w http.ResponseWriter, r *http.Request) (domain.Summary, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.notFound(w, r)
		return domain.Summary{}, false
	}
	sum, err := s.summary(r.Context(), id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		s.notFound(w, r)
		return domain.Summary{}, false
	case err != nil:
		s.serverError(w, r, err)
		return domain.Summary{}, false
	}
	return sum, true
}

// jobView adds schedule information to a cleanup job.
type jobView struct {
	domain.Job
	Describe string
	ReadOnly bool      // the repository is read-only: the job is kept but never runs
	Next     time.Time // zero when paused, read-only or the schedule is invalid
}

func newJobView(j domain.Job, readOnly bool, now time.Time) jobView {
	v := jobView{Job: j, Describe: "Custom schedule", ReadOnly: readOnly}
	if sch, err := schedule.Parse(j.Cron); err == nil {
		v.Describe = sch.Describe()
		if j.Enabled && !readOnly {
			v.Next = sch.Next(now)
		}
	}
	return v
}

// StatusText summarises the job state for display: "Active", "Paused", "Read-only" or "Failing".
func (v jobView) StatusText(last *domain.Run) string {
	switch {
	case v.ReadOnly:
		return "Read-only"
	case !v.Enabled:
		return "Paused"
	case last != nil && last.Status == domain.RunFailed:
		return "Failing"
	}
	return "Active"
}

// Keeps describes the keep rules in words ("7 latest, 30 daily").
func (v jobView) Keeps() string { return keepText(v.Keep) }

func keepText(k domain.KeepPolicy) string {
	var parts []string
	add := func(n int, what string) {
		if n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, what))
		}
	}
	add(k.Last, "latest")
	add(k.Daily, "daily")
	add(k.Weekly, "weekly")
	add(k.Monthly, "monthly")
	add(k.Yearly, "yearly")
	if len(parts) == 0 {
		return "Keeps everything"
	}
	return strings.Join(parts, ", ")
}
