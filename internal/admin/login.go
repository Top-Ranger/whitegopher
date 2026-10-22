package admin

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/bitflipp/galdav/internal/auth"
	"github.com/bitflipp/galdav/internal/store"
)

func (s *Server) loginForm(w http.ResponseWriter, r *http.Request) {
	if n, _ := s.Store.CountUsers(); n == 0 {
		http.Redirect(w, r, "/admin/setup", http.StatusSeeOther)
		return
	}
	if _, ok := s.currentSession(r); ok {
		http.Redirect(w, r, "/admin/", http.StatusSeeOther)
		return
	}
	s.render(w, r, "login", http.StatusOK, page{Title: "Sign in"})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)
	if !s.Limiter.Allowed(ip) {
		slog.Warn("login blocked", "via", "admin", "ip", ip)
		w.Header().Set("Retry-After", "900")
		s.render(w, r, "login", http.StatusTooManyRequests, page{Title: "Sign in", Error: "Too many failed attempts. Try again later."})
		return
	}
	name, pass := r.PostFormValue("username"), r.PostFormValue("password")
	u, ok := s.Auth.Verify(name, pass)
	if !ok {
		s.Limiter.Fail(ip)
		slog.Warn("login failed", "via", "admin", "user", name, "ip", ip)
		s.render(w, r, "login", http.StatusUnauthorized, page{Title: "Sign in", Error: "Wrong username or password."})
		return
	}
	s.Limiter.Reset(ip)
	if err := s.startSession(w, r, u); err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, "/admin/", http.StatusSeeOther)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request, sess *session) {
	s.Store.DeleteSession(sess.tokenHash)
	http.SetCookie(w, &http.Cookie{Name: cookieName, Path: "/admin/", MaxAge: -1, HttpOnly: true})
	http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
}

// setup creates the first administrator when no users exist.
func (s *Server) setupForm(w http.ResponseWriter, r *http.Request) {
	if n, _ := s.Store.CountUsers(); n > 0 {
		http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
		return
	}
	s.render(w, r, "login", http.StatusOK, page{Title: "Set up", IsSetup: true})
}

func (s *Server) setup(w http.ResponseWriter, r *http.Request) {
	redo := func(msg string) {
		s.render(w, r, "login", http.StatusUnprocessableEntity, page{Title: "Set up", IsSetup: true, Error: msg})
	}
	name, pass := strings.TrimSpace(r.PostFormValue("username")), r.PostFormValue("password")
	switch {
	case !auth.ValidUsername(name):
		redo("Username may contain letters, digits and . _ @ - (max 64).")
		return
	case len(pass) < minPasswordLen:
		redo("Password must be at least 8 characters.")
		return
	case pass != r.PostFormValue("password2"):
		redo("Passwords do not match.")
		return
	}
	hash, err := auth.HashPassword(pass)
	if err != nil {
		s.fail(w, err)
		return
	}
	id, err := s.Store.CreateUserIfNone(store.User{Username: name, PasswordHash: hash, IsAdmin: true})
	if err != nil {
		if err == store.ErrExists {
			http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
			return
		}
		s.fail(w, err)
		return
	}
	u, err := s.Store.UserByID(id)
	if err == nil {
		err = s.startSession(w, r, u)
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, "/admin/", http.StatusSeeOther)
}
