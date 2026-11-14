package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"restic-repo-browser/internal/domain"
	"restic-repo-browser/internal/format"
	"restic-repo-browser/internal/jobs"
	"restic-repo-browser/internal/retention"
)

const runsShown = 8

// ---- read-only view ----

type runsData struct {
	RepoID   int64
	Runs     []domain.Run
	Running  bool
	OOB      bool   // also send the status line as an out-of-band swap
	ShowRepo bool   // name the repository in each row (Jobs page)
	Status   string // status line text
}

type cleanupView struct {
	S          domain.Summary
	Job        *jobView
	Items      []fact
	FailStreak int
	Runs       runsData
	Reachable  bool
}

// TabLabel is the short state shown on the Cleanup tab.
func (c cleanupView) TabLabel() string {
	if c.S.Repo.ReadOnly {
		return "Read-only"
	}
	if c.Job == nil {
		return "Not set up"
	}
	return c.Job.StatusText(c.S.LastRun)
}

func (s *Server) buildCleanupView(ctx context.Context, sum domain.Summary) (cleanupView, error) {
	runs, err := s.Store.RepoRuns(ctx, sum.Repo.ID, runsShown)
	if err != nil {
		return cleanupView{}, err
	}
	v := cleanupView{S: sum, Reachable: !sum.Repo.Unreachable()}
	for _, r := range runs {
		if r.Status != domain.RunFailed {
			break
		}
		v.FailStreak++
	}
	v.Runs = runsData{RepoID: sum.Repo.ID, Runs: runs, Running: s.Jobs.Running(sum.Repo.ID)}
	if sum.Job != nil {
		jv := newJobView(*sum.Job, sum.Repo.ReadOnly, s.Now())
		v.Job = &jv
		v.Items = s.cleanupItems(sum, jv)
		v.Runs.Status = s.statusLine(jv, runs)
	}
	return v, nil
}

func (s *Server) statusLine(jv jobView, runs []domain.Run) string {
	now := s.Now()
	if jv.ReadOnly {
		return "Read-only: this job is kept but never runs."
	}
	var parts []string
	switch {
	case len(runs) == 0:
		parts = append(parts, "Never run")
	case runs[0].Status == domain.RunRunning:
		parts = append(parts, "Running now")
	case runs[0].Status == domain.RunFailed:
		parts = append(parts, "Last run failed "+format.Rel(runs[0].StartedAt, now))
	default:
		parts = append(parts, "Last run finished "+format.Rel(runs[0].FinishedAt, now))
	}
	if !jv.Next.IsZero() {
		parts = append(parts, "Next run "+format.Future(jv.Next, now))
	} else if !jv.Enabled {
		parts = append(parts, "Paused")
	}
	return strings.Join(parts, ". ") + "."
}

func (s *Server) cleanupItems(sum domain.Summary, jv jobView) []fact {
	effect := fact{Label: "Effect", Value: "No snapshots yet"}
	if len(sum.Snapshots) > 0 {
		kept := len(retention.Apply(sum.Snapshots, jv.Keep).Kept())
		effect = fact{"Effect", fmt.Sprintf("Keeps %s of %s snapshots", format.Int(int64(kept)), format.Int(int64(len(sum.Snapshots)))),
			fmt.Sprintf("Forgets %s on the next run", format.Int(int64(len(sum.Snapshots)-kept)))}
	}
	after := "Forget only"
	if jv.Prune {
		after = "Prune the repository"
	}
	active := "Paused"
	switch {
	case jv.ReadOnly:
		active = "Kept, never run (read-only)"
	case jv.Enabled:
		active = "Active"
	}
	return []fact{
		{"Schedule", jv.Describe, jv.Cron},
		{"Keep rules", jv.Keeps(), ""},
		effect,
		{"After forgetting", after, ""},
		{"Job", active, ""},
	}
}

// refuseIfReadOnly answers a request that would set up or start a cleanup on a
// read-only repository. It reports whether it did.
func (s *Server) refuseIfReadOnly(w http.ResponseWriter, sum domain.Summary) bool {
	if !sum.Repo.ReadOnly {
		return false
	}
	sendToast(w, toast{Title: "This repository is read-only", Sub: "Cleanups cannot be set up or run.", Fail: true})
	w.WriteHeader(http.StatusForbidden)
	return true
}

func (s *Server) handleCleanupView(w http.ResponseWriter, r *http.Request) {
	sum, ok := s.repoFromPath(w, r)
	if !ok {
		return
	}
	v, err := s.buildCleanupView(r.Context(), sum)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderFragment(w, r, "cleanup_body", v)
}

// ---- edit form ----

type cleanupForm struct {
	RepoID    int64
	IsNew     bool
	Cron      string
	Keep      domain.KeepPolicy
	Prune     bool
	Enabled   bool
	Errors    map[string]string
	Status    string
	Snapshots int
	Preview   previewData
}

func defaultJob(repoID int64) domain.Job {
	return domain.Job{
		RepoID: repoID, Cron: "30 3 * * *", Prune: true, Enabled: true,
		Keep: domain.KeepPolicy{Last: 7, Daily: 14, Weekly: 8, Monthly: 12, Yearly: 2},
	}
}

func newCleanupForm(job domain.Job, isNew bool, status string, snapshots int) cleanupForm {
	return cleanupForm{
		RepoID: job.RepoID, IsNew: isNew, Cron: job.Cron, Keep: job.Keep,
		Prune: job.Prune, Enabled: job.Enabled, Errors: map[string]string{},
		Status: status, Snapshots: snapshots,
	}
}

func (s *Server) handleCleanupEdit(w http.ResponseWriter, r *http.Request) {
	sum, ok := s.repoFromPath(w, r)
	if !ok {
		return
	}
	if s.refuseIfReadOnly(w, sum) {
		return
	}
	job, isNew, status := defaultJob(sum.Repo.ID), true, "Not set up."
	if sum.Job != nil {
		job, isNew = *sum.Job, false
		runs, err := s.Store.RepoRuns(r.Context(), sum.Repo.ID, 1)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		status = s.statusLine(newJobView(job, sum.Repo.ReadOnly, s.Now()), runs)
	}
	f := newCleanupForm(job, isNew, status, sum.Count())
	f.Preview = s.buildPreview(sum, job, nil)
	s.renderFragment(w, r, "cleanup_edit", f)
}

// jobFromForm reads the edit form. The returned map holds one message per invalid field.
func jobFromForm(r *http.Request, repoID int64) (domain.Job, map[string]string) {
	errs := map[string]string{}
	num := func(name string) int {
		n := atoiDefault(strings.TrimSpace(r.PostFormValue(name)), 0)
		return min(max(n, 0), 9999)
	}
	j := domain.Job{
		RepoID: repoID, Cron: strings.Join(strings.Fields(r.PostFormValue("cron")), " "),
		Keep: domain.KeepPolicy{
			Last: num("last"), Daily: num("daily"), Weekly: num("weekly"),
			Monthly: num("monthly"), Yearly: num("yearly"),
		},
		Prune: r.PostFormValue("prune") != "", Enabled: r.PostFormValue("enabled") != "",
	}
	if !validSchedule(j.Cron) {
		errs["cron"] = invalidScheduleMsg
	}
	if j.Keep.Empty() {
		errs["keep"] = "Add at least one keep rule. A job without keep rules would forget every snapshot."
	}
	return j, errs
}

func (s *Server) handleCleanupSave(w http.ResponseWriter, r *http.Request) {
	sum, ok := s.repoFromPath(w, r)
	if !ok {
		return
	}
	if s.refuseIfReadOnly(w, sum) {
		return
	}
	job, errs := jobFromForm(r, sum.Repo.ID)
	if len(errs) > 0 {
		f := newCleanupForm(job, sum.Job == nil, "", sum.Count())
		f.Errors = errs
		f.Preview = s.buildPreview(sum, job, errs)
		s.renderFragmentStatus(w, r, "cleanup_edit", f, http.StatusUnprocessableEntity)
		return
	}
	created := sum.Job == nil
	if err := s.Store.SaveJob(r.Context(), job); err != nil {
		s.serverError(w, r, err)
		return
	}
	sum.Job = &job
	v, err := s.buildCleanupView(r.Context(), sum)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	title := "Cleanup job saved"
	if created {
		title = "Cleanup job created"
	}
	sendToast(w, toast{Title: title, Done: true})
	s.renderFragment(w, r, "cleanup_body", v)
}

// ---- live preview ----

type reasonRow struct {
	Label string
	Count int
}

type previewData struct {
	Ready       bool // false: nothing to compute yet
	NoRule      bool
	NoSnapshots bool
	Kept, Gone  int
	Total       int
	Freed       string
	PruneOn     bool
	Reasons     []reasonRow
	ChartJSON   string
	CronEcho    string
	CronOK      bool
	NextRuns    []string
	SaveBlocked bool
	IsNew       bool
	Errors      map[string]string
	OOB         bool
}

var reasonLabels = map[retention.Reason]string{
	retention.ReasonLatest: "Latest snapshots", retention.ReasonDaily: "Daily",
	retention.ReasonWeekly: "Weekly", retention.ReasonMonthly: "Monthly", retention.ReasonYearly: "Yearly",
}

func (s *Server) buildPreview(sum domain.Summary, job domain.Job, errs map[string]string) previewData {
	p := previewData{IsNew: sum.Job == nil, Errors: errs, PruneOn: job.Prune, Total: len(sum.Snapshots)}
	if errs == nil {
		p.Errors = map[string]string{}
	}
	echo := s.explainCron(job.Cron)
	p.CronOK, p.CronEcho, p.NextRuns = echo.CronOK, echo.CronEcho, echo.NextRuns
	p.NoRule = job.Keep.Empty()
	p.SaveBlocked = p.NoRule || !p.CronOK
	switch {
	case len(sum.Snapshots) == 0:
		p.NoSnapshots = true
		return p
	case p.NoRule:
		return p
	}
	plan := retention.Apply(sum.Snapshots, job.Keep)
	p.Ready = true
	kept, gone := plan.Kept(), plan.Forgotten()
	p.Kept, p.Gone = len(kept), len(gone)
	// The oldest snapshot's data is shared by every later one, so it never counts as freed.
	oldest := sum.Snapshots[0].ID
	var freed int64
	for _, g := range gone {
		if g.ID != oldest {
			freed += g.AddedBytes()
		}
	}
	p.Freed = format.Size(freed)
	counts := plan.ReasonCounts()
	for _, r := range retention.Reasons {
		if counts[r] > 0 {
			p.Reasons = append(p.Reasons, reasonRow{Label: reasonLabels[r], Count: counts[r]})
		}
	}
	p.ChartJSON = retentionJSON(plan, s.Now())
	return p
}

func (s *Server) handleCleanupPreview(w http.ResponseWriter, r *http.Request) {
	sum, ok := s.repoFromPath(w, r)
	if !ok {
		return
	}
	if s.refuseIfReadOnly(w, sum) {
		return
	}
	job, errs := jobFromForm(r, sum.Repo.ID)
	delete(errs, "keep") // shown by the preview itself
	p := s.buildPreview(sum, job, errs)
	p.OOB, p.IsNew = true, sum.Job == nil
	s.renderFragment(w, r, "cleanup_preview", p)
}

// ---- run now and history ----

func (s *Server) runsFragment(w http.ResponseWriter, r *http.Request, sum domain.Summary, oob bool) {
	v, err := s.buildCleanupView(r.Context(), sum)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	v.Runs.OOB = oob
	s.renderFragment(w, r, "cleanup_runs", v.Runs)
}

func (s *Server) handleCleanupRun(w http.ResponseWriter, r *http.Request) {
	sum, ok := s.repoFromPath(w, r)
	if !ok {
		return
	}
	if s.refuseIfReadOnly(w, sum) {
		return
	}
	switch err := s.Jobs.RunNow(r.Context(), sum.Repo.ID); {
	case err == nil:
		sendTaskStarted(w, toast{Title: "Cleanup started", Sub: sum.Repo.Name + " is being cleaned up."})
	case s.refuseIfBusy(w, err):
		return
	case errors.Is(err, jobs.ErrReadOnly): // switched to read-only while this request was on its way
		sendToast(w, toast{Title: "This repository is read-only", Sub: "Cleanups cannot be run.", Fail: true})
	case errors.Is(err, jobs.ErrNoJob):
		sendToast(w, toast{Title: "Set up a cleanup job first", Fail: true})
	default:
		s.Log.Error("starting cleanup", "repo", sum.Repo.ID, "err", err)
		sendToast(w, toast{Title: "Cleanup could not start", Sub: err.Error(), Fail: true})
	}
	s.runsFragment(w, r, sum, true)
}

func (s *Server) handleCleanupRuns(w http.ResponseWriter, r *http.Request) {
	sum, ok := s.repoFromPath(w, r)
	if !ok {
		return
	}
	s.runsFragment(w, r, sum, true)
}
