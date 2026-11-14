package web

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"restic-repo-browser/internal/cache"
	"restic-repo-browser/internal/domain"
	"restic-repo-browser/internal/format"
	"restic-repo-browser/internal/restic"
	"restic-repo-browser/internal/schedule"
	"restic-repo-browser/internal/store"
)

// ---- jobs overview ----

type jobRow struct {
	Repo domain.Repo
	Job  *jobView
	Last *domain.Run
}

type jobsPage struct {
	Rows    []jobRow
	Failing []jobRow
	Runs    runsData
}

func (s *Server) handleJobs(w http.ResponseWriter, r *http.Request) {
	sums, err := s.summaries(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	data := jobsPage{}
	for _, sum := range sums {
		row := jobRow{Repo: sum.Repo, Last: sum.LastRun}
		if sum.Job != nil {
			jv := newJobView(*sum.Job, s.Now())
			row.Job = &jv
		}
		data.Rows = append(data.Rows, row)
		if sum.LastRun != nil && sum.LastRun.Status == domain.RunFailed {
			data.Failing = append(data.Failing, row)
		}
	}
	runs, err := s.Store.RecentRuns(r.Context(), 12)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	data.Runs = runsData{Runs: runs}
	s.renderPage(w, r, "jobs", "Jobs", "jobs", data)
}

// ---- settings ----

type settingsPage struct {
	Repos       []settingsRepo
	RefreshTime string
	LastUpdate  time.Time
}

type settingsRepo struct {
	domain.Repo
	State domain.State
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	sums, err := s.summaries(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	data := settingsPage{}
	for _, sum := range sums {
		data.Repos = append(data.Repos, settingsRepo{Repo: sum.Repo, State: sum.State})
		if sum.Repo.RefreshOKAt.After(data.LastUpdate) {
			data.LastUpdate = sum.Repo.RefreshOKAt
		}
	}
	data.RefreshTime, err = s.Store.Setting(r.Context(), store.SettingCacheRefreshTime, cache.DefaultRefreshTime)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderPage(w, r, "settings", "Settings", "settings", data)
}

type repoForm struct {
	ID         int64 // 0 for a new repository
	Name       string
	Path       string
	BackupCron string
	Error      string
	NameErr    string
	CronErr    string
	CanForce   bool // the repository could not be opened; allow saving anyway
	Cron       cronEcho
}

// cronEcho is the plain-English explanation shown under a cron input. The
// cron_echo template reads CronOK, CronEcho and NextRuns.
type cronEcho struct {
	Empty    bool // no schedule entered
	CronOK   bool
	CronEcho string
	NextRuns []string
}

func (s *Server) explainCron(spec string) cronEcho {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return cronEcho{Empty: true}
	}
	sch, err := schedule.Parse(spec)
	if err != nil {
		return cronEcho{}
	}
	e := cronEcho{CronOK: true, CronEcho: sch.Describe()}
	for _, t := range sch.NextRuns(s.Now(), 3) {
		e.NextRuns = append(e.NextRuns, format.Run(t))
	}
	return e
}

// handleBackupCron explains the expected-backups cron expression while it is typed.
func (s *Server) handleBackupCron(w http.ResponseWriter, r *http.Request) {
	s.renderFragment(w, r, "backup_cron_echo", s.explainCron(r.URL.Query().Get("backup_cron")))
}

func (s *Server) handleRepoForm(w http.ResponseWriter, r *http.Request) {
	f := repoForm{}
	if idStr := r.PathValue("id"); idStr != "" {
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			s.notFound(w, r)
			return
		}
		repo, err := s.Store.Repo(r.Context(), id)
		if errors.Is(err, store.ErrNotFound) {
			s.notFound(w, r)
			return
		} else if err != nil {
			s.serverError(w, r, err)
			return
		}
		f = repoForm{ID: repo.ID, Name: repo.Name, Path: repo.Path, BackupCron: repo.BackupCron}
	}
	f.Cron = s.explainCron(f.BackupCron)
	s.renderFragment(w, r, "repo_form", f)
}

func (s *Server) handleRepoSave(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	f := repoForm{
		Name: strings.TrimSpace(r.PostFormValue("name")), Path: strings.TrimSpace(r.PostFormValue("path")),
		BackupCron: strings.Join(strings.Fields(r.PostFormValue("backup_cron")), " "),
	}
	password := r.PostFormValue("password")
	force := r.PostFormValue("force") != ""
	if idStr := r.PathValue("id"); idStr != "" {
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			s.notFound(w, r)
			return
		}
		f.ID = id
	}
	fail := func(f repoForm) {
		f.Cron = s.explainCron(f.BackupCron)
		s.renderFragmentStatus(w, r, "repo_form", f, http.StatusUnprocessableEntity)
	}

	switch {
	case f.Name == "":
		f.NameErr = "Give the repository a name."
		fail(f)
		return
	case f.Path == "":
		f.Error = "Enter the folder that holds the repository."
		fail(f)
		return
	case f.BackupCron != "" && !s.explainCron(f.BackupCron).CronOK:
		f.CronErr = "This is not a valid schedule. Use five fields: minute, hour, day of month, month and weekday."
		fail(f)
		return
	case f.ID == 0 && password == "":
		f.Error = "Enter the repository password."
		fail(f)
		return
	}

	// Check that the repository opens with these settings before saving them.
	if !force {
		if err := s.probe(ctx, f, password); err != nil {
			f.Error = restic.UserMessage(err)
			f.CanForce = true
			fail(f)
			return
		}
	}

	var id int64
	var err error
	n := store.NewRepo{Name: f.Name, Path: f.Path, Password: password, BackupCron: f.BackupCron}
	if f.ID == 0 {
		id, err = s.Store.CreateRepo(ctx, n, s.Now())
	} else {
		id, err = f.ID, s.Store.UpdateRepo(ctx, f.ID, n)
	}
	switch {
	case errors.Is(err, store.ErrDuplicate):
		f.NameErr = "A repository with this name already exists."
		fail(f)
		return
	case errors.Is(err, store.ErrNotFound):
		s.notFound(w, r)
		return
	case err != nil:
		s.serverError(w, r, err)
		return
	}
	// Read the new repository right away; when another task is running it is picked up by the next daily sync.
	if err := s.Cache.Sync(id); err != nil {
		s.Log.Info("initial sync of a new repository not started", "repo", id, "reason", err)
	}
	w.Header().Set("HX-Redirect", "/settings")
}

// probe opens the repository with the form's path and password (or the stored
// password when editing without entering a new one).
func (s *Server) probe(ctx context.Context, f repoForm, password string) error {
	if password == "" && f.ID != 0 {
		stored, err := s.Store.RepoPassword(ctx, f.ID)
		if err != nil {
			return err
		}
		password = stored
	}
	return s.Explorer.Probe(ctx, restic.Repo{Path: f.Path, Password: password})
}

func (s *Server) handleRepoTest(w http.ResponseWriter, r *http.Request) {
	sum, ok := s.repoFromPath(w, r)
	if !ok {
		return
	}
	rr, err := s.resticRepo(r.Context(), sum.Repo)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if err := s.Explorer.Probe(r.Context(), rr); err != nil {
		sendToast(w, toast{Title: "Cannot open the repository", Sub: restic.UserMessage(err), Fail: true})
	} else {
		sendToast(w, toast{Title: "Repository opens", Sub: sum.Repo.Name + " is reachable and the password works.", Done: true})
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRepoDelete(w http.ResponseWriter, r *http.Request) {
	sum, ok := s.repoFromPath(w, r)
	if !ok {
		return
	}
	if err := s.Store.DeleteRepo(r.Context(), sum.Repo.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	w.Header().Set("HX-Redirect", "/settings")
}

func (s *Server) handleCacheSettings(w http.ResponseWriter, r *http.Request) {
	value := strings.TrimSpace(r.PostFormValue("refresh_time"))
	if _, err := cache.NextDaily(s.Now(), value); err != nil {
		sendToast(w, toast{Title: "Enter a time like 03:00", Fail: true})
		w.WriteHeader(http.StatusUnprocessableEntity)
		return
	}
	if err := s.Store.SetSetting(r.Context(), store.SettingCacheRefreshTime, value); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.Cache.Reschedule()
	sendToast(w, toast{Title: "Sync time saved", Sub: "Repositories are synced every day at " + value + ".", Done: true})
	w.WriteHeader(http.StatusNoContent)
}
