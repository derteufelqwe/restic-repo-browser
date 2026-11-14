// Package web serves the htmx-driven user interface. Pages and fragments are
// rendered from embedded templates; all data except the snapshot explorer and
// downloads comes from the SQLite cache.
package web

import (
	"context"
	"embed"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"restic-repo-browser/internal/auth"
	"restic-repo-browser/internal/restic"
	"restic-repo-browser/internal/store"
	"restic-repo-browser/internal/tasks"
)

//go:embed static templates
var assets embed.FS

// Explorer is the live access to repository contents: the only part of the UI
// that talks to restic while a request is handled.
type Explorer interface {
	Probe(ctx context.Context, repo restic.Repo) error
	List(ctx context.Context, repo restic.Repo, snapshotID, dir string) ([]restic.Node, error)
	Node(ctx context.Context, repo restic.Repo, snapshotID, path string) (restic.Node, error)
	Dump(ctx context.Context, repo restic.Repo, snapshotID, path string, asTar bool, w io.Writer) error
}

// Cache syncs the SQLite copy of repository data. Sync and SyncAll start a
// background task and fail with *tasks.BusyError when another task is running.
type Cache interface {
	Sync(repoID int64) error
	SyncAll() error
	Reschedule()
}

// Jobs starts cleanup runs. RunNow fails with *tasks.BusyError when another task is running.
type Jobs interface {
	RunNow(ctx context.Context, repoID int64) error
	Running(repoID int64) bool
}

// Deps are the collaborators of a Server.
type Deps struct {
	Store    *store.Store
	Explorer Explorer
	Cache    Cache
	Jobs     Jobs
	Tasks    *tasks.Manager
	Auth     *auth.Service
	Log      *slog.Logger
	Now      func() time.Time // defaults to time.Now
	// SecureCookies marks the session cookie Secure (set when served over HTTPS).
	SecureCookies bool
	// BrowseDir is the folder the folder picker opens first. Empty means the home folder.
	BrowseDir string
}

// Server is the HTTP handler of the application.
type Server struct {
	Deps
	pages map[string]*pageTemplate
	mux   *http.ServeMux
}

// New builds a Server and parses its templates.
func New(d Deps) (*Server, error) {
	if d.Now == nil {
		d.Now = time.Now
	}
	s := &Server{Deps: d}
	pages, err := loadTemplates(s.funcs())
	if err != nil {
		return nil, err
	}
	s.pages = pages
	s.mux = http.NewServeMux()
	s.routes()
	return s, nil
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.recoverer(s.logRequests(s.mux)).ServeHTTP(w, r)
}

func (s *Server) routes() {
	static, _ := fs.Sub(assets, "static")
	s.mux.Handle("GET /static/", http.StripPrefix("/static/", cacheStatic(http.FileServerFS(static))))

	// Public.
	s.mux.HandleFunc("GET /login", s.handleLoginForm)
	s.mux.HandleFunc("POST /login", s.handleLogin)

	// Everything below requires a session.
	app := func(pattern string, h http.HandlerFunc) {
		s.mux.Handle(pattern, s.requireSession(s.sameOrigin(h)))
	}
	app("POST /logout", s.handleLogout)
	app("GET /{$}", s.handleOverview)

	app("GET /repos/{id}", s.handleRepo)
	app("GET /repos/{id}/chart", s.handleChart)
	app("GET /repos/{id}/snapshots", s.handleSnapshotList)
	app("POST /repos/{id}/sync", s.handleSync)
	app("POST /sync", s.handleSyncAll)
	app("GET /tasks/banner", s.handleBanner)
	app("GET /fs", s.handleFS)

	app("GET /repos/{id}/cleanup", s.handleCleanupView)
	app("GET /repos/{id}/cleanup/edit", s.handleCleanupEdit)
	app("POST /repos/{id}/cleanup", s.handleCleanupSave)
	app("POST /repos/{id}/cleanup/preview", s.handleCleanupPreview)
	app("POST /repos/{id}/cleanup/run", s.handleCleanupRun)
	app("GET /repos/{id}/cleanup/runs", s.handleCleanupRuns)

	app("GET /repos/{id}/snapshots/{snap}", s.handleExplorer)
	app("GET /repos/{id}/snapshots/{snap}/listing", s.handleListing)
	app("GET /repos/{id}/snapshots/{snap}/tree", s.handleTree)
	app("GET /repos/{id}/snapshots/{snap}/details", s.handleDetails)
	app("GET /repos/{id}/snapshots/{snap}/preview", s.handlePreview)
	app("GET /repos/{id}/snapshots/{snap}/download", s.handleDownload)
	app("POST /repos/{id}/snapshots/{snap}/download", s.handleDownload)

	app("GET /jobs", s.handleJobs)

	app("GET /settings", s.handleSettings)
	app("GET /settings/backup-cron", s.handleBackupCron)
	app("GET /settings/repos/new", s.handleRepoForm)
	app("GET /settings/repos/{id}/edit", s.handleRepoForm)
	app("POST /settings/repos", s.handleRepoSave)
	app("POST /settings/repos/{id}", s.handleRepoSave)
	app("POST /settings/repos/{id}/test", s.handleRepoTest)
	app("POST /settings/repos/{id}/delete", s.handleRepoDelete)
	app("POST /settings/cache", s.handleCacheSettings)
}
