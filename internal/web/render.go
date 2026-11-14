package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"

	"restic-repo-browser/internal/domain"
	"restic-repo-browser/internal/format"
)

// pageTemplate is the template set of one page: the shared layout and partials
// plus the page's own "content" block.
type pageTemplate struct{ *template.Template }

// loadTemplates parses layout + partials into a base set and clones it once per
// page, so every page can define its own "content" block.
func loadTemplates(funcs template.FuncMap) (map[string]*pageTemplate, error) {
	base := template.New("base").Funcs(funcs)
	for _, pattern := range []string{"templates/layout.html", "templates/partials/*.html"} {
		if _, err := base.ParseFS(assets, pattern); err != nil {
			return nil, fmt.Errorf("web: parse %s: %w", pattern, err)
		}
	}
	files, err := fs.Glob(assets, "templates/pages/*.html")
	if err != nil {
		return nil, err
	}
	pages := map[string]*pageTemplate{}
	for _, f := range files {
		clone, err := base.Clone()
		if err != nil {
			return nil, err
		}
		if _, err := clone.ParseFS(assets, f); err != nil {
			return nil, fmt.Errorf("web: parse %s: %w", f, err)
		}
		pages[strings.TrimSuffix(path.Base(f), ".html")] = &pageTemplate{clone}
	}
	return pages, nil
}

func (s *Server) funcs() template.FuncMap {
	now := s.Now
	return template.FuncMap{
		"bytes":    func(n int64) string { return format.Size(n) },
		"num":      func(n int) string { return format.Int(int64(n)) },
		"num64":    func(n int64) string { return format.Int(n) },
		"date":     func(t time.Time) string { return format.Date(t, now(), true) },
		"day":      func(t time.Time) string { return format.Date(t, now(), false) },
		"rel":      func(t time.Time) string { return format.Rel(t, now()) },
		"future":   func(t time.Time) string { return format.Future(t, now()) },
		"runtime":  format.Run,
		"duration": format.Duration,
		"clock":    func(t time.Time) string { return t.Format("15:04") },
		"plural": func(n int, one, many string) string {
			if n == 1 {
				return one
			}
			return many
		},
		"json": func(v any) (string, error) {
			b, err := json.Marshal(v)
			return string(b), err
		},
		"runtime_of": func(r domain.Run) string { return format.Duration(r.Duration(now())) },
		"download":   downloadURL,
		"pq":         pathQuery,
		"add":        func(a, b int) int { return a + b },
		"sub":        func(a, b int) int { return a - b },
		"first": func(s []string) string {
			if len(s) == 0 {
				return ""
			}
			return s[0]
		},
		"dict": func(kv ...any) map[string]any {
			m := make(map[string]any, len(kv)/2)
			for i := 0; i+1 < len(kv); i += 2 {
				m[fmt.Sprint(kv[i])] = kv[i+1]
			}
			return m
		},
		"trunc": truncate,
		"join":  strings.Join,
		"title": func(s string) string { return strings.ToUpper(s[:1]) + s[1:] },
	}
}

// page is the data every full-page template receives.
type page struct {
	Title  string
	Active string // rail entry to highlight: "overview", "jobs", "settings" or "repo:<id>"
	Rail   rail
	Banner taskBanner
	Data   any
}

// renderPage writes a full HTML page.
func (s *Server) renderPage(w http.ResponseWriter, r *http.Request, name, title, active string, data any) {
	rl, err := s.loadRail(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.execute(w, r, name, "layout", page{Title: title, Active: active, Rail: rl, Banner: s.banner(), Data: data}, http.StatusOK)
}

// renderFragment writes a named partial (an htmx swap target).
func (s *Server) renderFragment(w http.ResponseWriter, r *http.Request, partial string, data any) {
	s.execute(w, r, "overview", partial, data, http.StatusOK)
}

// renderFragmentStatus is renderFragment with an explicit status code (422 for form errors).
func (s *Server) renderFragmentStatus(w http.ResponseWriter, r *http.Request, partial string, data any, status int) {
	s.execute(w, r, "overview", partial, data, status)
}

// execute renders into a buffer first so a template error never leaves a half-written page.
func (s *Server) execute(w http.ResponseWriter, r *http.Request, set, name string, data any, status int) {
	t, ok := s.pages[set]
	if !ok {
		s.serverError(w, r, fmt.Errorf("unknown template set %q", set))
		return
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, name, data); err != nil {
		s.serverError(w, r, fmt.Errorf("render %s/%s: %w", set, name, err))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

func (s *Server) serverError(w http.ResponseWriter, r *http.Request, err error) {
	s.Log.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	http.Error(w, "Something went wrong. Check the server log for details.", http.StatusInternalServerError)
}

func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	s.execute(w, r, "message", "layout", page{Title: "Not found", Data: message{
		Heading: "Not found", Text: "This page or repository does not exist.",
	}}, http.StatusNotFound)
}

// message is the data of the simple message page.
type message struct{ Heading, Text string }

// toast describes a transient notice shown by the client, sent in an HX-Trigger header.
type toast struct {
	Title string `json:"title"`
	Sub   string `json:"sub,omitempty"`
	Done  bool   `json:"done,omitempty"`
	Fail  bool   `json:"fail,omitempty"`
}

func sendToast(w http.ResponseWriter, t toast) {
	b, _ := json.Marshal(map[string]toast{"toast": t})
	w.Header().Set("HX-Trigger", string(b))
}

// sendTaskStarted shows a toast and makes the page refresh its task banner at once.
func sendTaskStarted(w http.ResponseWriter, t toast) {
	b, _ := json.Marshal(map[string]any{"toast": t, "task-changed": true})
	w.Header().Set("HX-Trigger", string(b))
}

// truncate shortens s to at most n characters and marks the cut with an ellipsis.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
