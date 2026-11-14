package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"restic-repo-browser/internal/backend"
	"restic-repo-browser/internal/cache"
	"restic-repo-browser/internal/domain"
	"restic-repo-browser/internal/format"
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
			jv := newJobView(*sum.Job, sum.Repo.ReadOnly, s.Now())
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
	// OpenDialog is the dialog fragment the page loads (and so opens) right away:
	// links from other pages use /settings?add=1 and /settings?edit=<id>.
	OpenDialog string
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
	data.OpenDialog = dialogToOpen(r.URL.Query(), data.Repos)
	s.renderPage(w, r, "settings", "Settings", "settings", data)
}

// dialogToOpen maps ?add=1 and ?edit=<id> to the repository form fragment to load
// when the settings page opens. An unknown id opens nothing.
func dialogToOpen(q url.Values, repos []settingsRepo) string {
	if q.Get("add") != "" {
		return "/settings/repos/new"
	}
	if id, err := strconv.ParseInt(q.Get("edit"), 10, 64); err == nil {
		for _, r := range repos {
			if r.ID == id {
				return fmt.Sprintf("/settings/repos/%d/edit", id)
			}
		}
	}
	return ""
}

type repoForm struct {
	ID         int64 // 0 for a new repository
	Name       string
	Kind       domain.BackendKind
	ReadOnly   bool
	Location   string
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

// invalidScheduleMsg is shown next to every cron input that cannot be parsed.
const invalidScheduleMsg = "This is not a valid schedule. Use five fields: minute, hour, day of month, month and weekday."

func validSchedule(spec string) bool {
	_, err := schedule.Parse(spec)
	return err == nil
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
	f := repoForm{Kind: domain.BackendLocal}
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
		f = repoForm{ID: repo.ID, Name: repo.Name, Kind: repo.Backend, ReadOnly: repo.ReadOnly, Location: repo.Location, BackupCron: repo.BackupCron}
	}
	f.Cron = s.explainCron(f.BackupCron)
	s.renderFragment(w, r, "repo_form", f)
}

func (s *Server) handleRepoSave(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	f := repoForm{
		Name: strings.TrimSpace(r.PostFormValue("name")), Kind: domain.BackendKind(r.PostFormValue("kind")), ReadOnly: r.PostFormValue("read_only") != "", Location: strings.TrimSpace(r.PostFormValue("path")),
		BackupCron: strings.Join(strings.Fields(r.PostFormValue("backup_cron")), " "),
	}
	if f.Kind == "" {
		f.Kind = domain.BackendLocal
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
	case !f.Kind.Valid():
		f.Error = "Choose where the repository is."
		f.Kind = domain.BackendLocal
		fail(f)
		return
	case f.Location == "":
		f.Error = "Enter the folder that holds the repository."
		if f.Kind == domain.BackendRclone {
			f.Error = "Enter the rclone remote and folder, for example gdrive:Backups/my-repo."
		}
		fail(f)
		return
	case f.Kind == domain.BackendRclone && !validRcloneLocation(f.Location):
		f.Error = "An rclone location has the form remote:folder, for example gdrive:Backups/my-repo."
		fail(f)
		return
	case f.BackupCron != "" && !validSchedule(f.BackupCron):
		f.CronErr = invalidScheduleMsg
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
			f.Error = backend.UserMessage(err)
			f.CanForce = true
			fail(f)
			return
		}
	}

	var id int64
	var err error
	n := store.NewRepo{Name: f.Name, Kind: f.Kind, ReadOnly: f.ReadOnly, Location: f.Location, Password: password, BackupCron: f.BackupCron}
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

// validRcloneLocation checks the "remote:folder" shape; rclone itself judges the rest.
func validRcloneLocation(loc string) bool {
	i := strings.Index(loc, ":")
	return i > 0
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
	return s.Explorer.Probe(ctx, backend.Ref{Kind: f.Kind, Location: f.Location, Password: password})
}

func (s *Server) handleRepoTest(w http.ResponseWriter, r *http.Request) {
	sum, ok := s.repoFromPath(w, r)
	if !ok {
		return
	}
	ref, err := backend.Resolve(r.Context(), s.Store, sum.Repo)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if err := s.Explorer.Probe(r.Context(), ref); err != nil {
		sendToast(w, toast{Title: "Cannot open the repository", Sub: backend.UserMessage(err), Fail: true})
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
