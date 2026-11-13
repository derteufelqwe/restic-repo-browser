package web

import (
	"errors"
	"net/http"
	"time"

	"restic-repo-browser/internal/auth"
)

type loginData struct {
	Error      string
	Configured bool
}

func (s *Server) renderLogin(w http.ResponseWriter, r *http.Request, data loginData, status int) {
	s.execute(w, r, "login", "login_document", data, status)
}

func (s *Server) handleLoginForm(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil && s.Auth.Valid(r.Context(), c.Value) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	configured, err := s.Auth.HasPassword(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderLogin(w, r, loginData{Configured: configured}, http.StatusOK)
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !originMatches(r) {
		http.Error(w, "cross-site request refused", http.StatusForbidden)
		return
	}
	token, expires, err := s.Auth.Login(r.Context(), r.PostFormValue("password"), clientIP(r))
	switch {
	case err == nil:
		http.SetCookie(w, &http.Cookie{
			Name: sessionCookie, Value: token, Path: "/", Expires: expires,
			HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: s.SecureCookies,
		})
		http.Redirect(w, r, "/", http.StatusSeeOther)
	case errors.Is(err, auth.ErrInvalidPassword):
		s.renderLogin(w, r, loginData{Configured: true, Error: "Wrong password. Check it and try again."}, http.StatusUnauthorized)
	case errors.Is(err, auth.ErrTooManyAttempts):
		s.renderLogin(w, r, loginData{Configured: true, Error: "Too many wrong passwords. Wait a minute and try again."}, http.StatusTooManyRequests)
	case errors.Is(err, auth.ErrNotConfigured):
		s.renderLogin(w, r, loginData{Configured: false}, http.StatusOK)
	default:
		s.serverError(w, r, err)
	}
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		if err := s.Auth.Logout(r.Context(), c.Value); err != nil {
			s.Log.Warn("logout", "err", err)
		}
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", Expires: time.Unix(0, 0), MaxAge: -1, HttpOnly: true})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}
