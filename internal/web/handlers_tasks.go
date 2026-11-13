package web

import (
	"errors"
	"net/http"
	"strings"

	"restic-repo-browser/internal/tasks"
)

// taskBanner is the progress strip shown on every page while a task runs.
type taskBanner struct {
	Running bool
	Title   string
	Text    string // current phase and detail
	Percent int    // -1 when progress cannot be measured
	LastID  int64  // id of the most recently finished task; pages reload when it changes
}

func (s *Server) banner() taskBanner {
	b := taskBanner{Percent: -1}
	if last := s.Tasks.Last(); last != nil {
		b.LastID = last.Info.ID
	}
	info, ok := s.Tasks.Current()
	if !ok {
		return b
	}
	var parts []string
	for _, p := range []string{info.Step, info.Detail} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	b.Running, b.Title, b.Text, b.Percent = true, info.Title, strings.Join(parts, " — "), info.Percent()
	return b
}

func (s *Server) handleBanner(w http.ResponseWriter, r *http.Request) {
	s.renderFragment(w, r, "task_banner", s.banner())
}

// refuseIfBusy answers a request that tried to start a task while another runs.
// It reports whether err was such a refusal.
func (s *Server) refuseIfBusy(w http.ResponseWriter, err error) bool {
	var busy *tasks.BusyError
	if !errors.As(err, &busy) {
		return false
	}
	sendToast(w, toast{Title: "Another task is running", Sub: busy.Current.Title + ". Wait until it has finished.", Fail: true})
	w.WriteHeader(http.StatusConflict)
	return true
}

func (s *Server) handleSync(w http.ResponseWriter, r *http.Request) {
	sum, ok := s.repoFromPath(w, r)
	if !ok {
		return
	}
	switch err := s.Cache.Sync(sum.Repo.ID); {
	case err == nil:
		sendTaskStarted(w, toast{Title: "Sync started", Sub: sum.Repo.Name})
		w.WriteHeader(http.StatusNoContent)
	case s.refuseIfBusy(w, err):
	default:
		s.serverError(w, r, err)
	}
}

func (s *Server) handleSyncAll(w http.ResponseWriter, r *http.Request) {
	switch err := s.Cache.SyncAll(); {
	case err == nil:
		sendTaskStarted(w, toast{Title: "Sync started", Sub: "All repositories, one after the other."})
		w.WriteHeader(http.StatusNoContent)
	case s.refuseIfBusy(w, err):
	default:
		s.serverError(w, r, err)
	}
}
