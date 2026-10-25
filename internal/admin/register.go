package admin

import (
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/bitflipp/whitegopher/internal/auth"
	"github.com/bitflipp/whitegopher/internal/store"
)

const inviteLifetime = 7 * 24 * time.Hour

// invitesOff reports whether invite links are disabled altogether.
func (s *Server) invitesOff() bool { return s.Invites == "off" }

// mayInvite reports whether u may create invite links.
func (s *Server) mayInvite(u store.User) bool {
	if u.ID == 0 || s.invitesOff() {
		return false
	}
	return u.IsAdmin || s.Invites == "everyone"
}

type invitesData struct {
	Invites []store.SignupInvite
	// All is set for administrators, who see everyone's invites.
	All bool
	// Link is an invite just created; it is shown once.
	Link string
}

func (s *Server) invitesPageWith(w http.ResponseWriter, r *http.Request, sess *session, link string) {
	scope := sess.User.ID
	if sess.User.IsAdmin {
		scope = 0
	}
	list, err := s.Store.ListSignupInvites(scope)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.render(w, r, "invites", http.StatusOK, page{Title: "Invites", Nav: "invites",
		Data: invitesData{Invites: list, All: sess.User.IsAdmin, Link: link}})
}

func (s *Server) invitesPage(w http.ResponseWriter, r *http.Request, sess *session) {
	if !s.mayInvite(sess.User) {
		notFound(w)
		return
	}
	s.invitesPageWith(w, r, sess, "")
}

func (s *Server) createInvite(w http.ResponseWriter, r *http.Request, sess *session) {
	if !s.mayInvite(sess.User) {
		notFound(w)
		return
	}
	token := randomHex(32)
	if err := s.Store.CreateSignupInvite(sess.User.ID, hashToken(token), time.Now().Add(inviteLifetime)); err != nil {
		s.fail(w, err)
		return
	}
	slog.Info("invite created", "user", sess.User.Username)
	s.invitesPageWith(w, r, sess, s.origin(r)+"/admin/register?token="+url.QueryEscape(token))
}

func (s *Server) revokeInvite(w http.ResponseWriter, r *http.Request, sess *session) {
	if !s.mayInvite(sess.User) {
		notFound(w)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		notFound(w)
		return
	}
	scope := sess.User.ID
	if sess.User.IsAdmin {
		scope = 0
	}
	if err := s.Store.DeleteSignupInvite(id, scope); err != nil {
		if isNotFound(err) {
			notFound(w)
		} else {
			s.fail(w, err)
		}
		return
	}
	http.Redirect(w, r, "/admin/invites?ok=revoked", http.StatusSeeOther)
}

const badInvite = "This invite link is invalid, has expired or was already used. Ask for a new one."

type registerData struct {
	Token, Username, DisplayName, Email string
}

func (s *Server) registerForm(w http.ResponseWriter, r *http.Request) {
	if s.invitesOff() {
		notFound(w)
		return
	}
	ip := s.clientIP(r)
	if !s.registerLimiter.Allowed(ip) {
		w.Header().Set("Retry-After", "3600")
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return
	}
	token := r.URL.Query().Get("token")
	if ok, err := s.Store.SignupInviteValid(hashToken(token)); err != nil || !ok {
		if err == nil {
			s.registerLimiter.Fail(ip)
		}
		s.render(w, r, "register", http.StatusBadRequest, page{Title: "Register", Error: badInvite})
		return
	}
	s.render(w, r, "register", http.StatusOK, page{Title: "Register", Data: registerData{Token: token}})
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	if s.invitesOff() {
		notFound(w)
		return
	}
	ip := s.clientIP(r)
	if !s.registerLimiter.Allowed(ip) {
		w.Header().Set("Retry-After", "3600")
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return
	}
	d := registerData{
		Token:       r.PostFormValue("token"),
		Username:    strings.TrimSpace(r.PostFormValue("username")),
		DisplayName: strings.TrimSpace(r.PostFormValue("display_name")),
		Email:       strings.TrimSpace(r.PostFormValue("email")),
	}
	h := hashToken(d.Token)
	if ok, err := s.Store.SignupInviteValid(h); err != nil || !ok {
		if err != nil {
			s.fail(w, err)
			return
		}
		s.registerLimiter.Fail(ip)
		s.render(w, r, "register", http.StatusBadRequest, page{Title: "Register", Error: badInvite})
		return
	}
	redo := func(msg string) {
		s.render(w, r, "register", http.StatusUnprocessableEntity, page{Title: "Register", Error: msg, Data: d})
	}
	pw := r.PostFormValue("password")
	switch {
	case !auth.ValidUsername(d.Username):
		redo("Username may contain letters, digits and . _ @ - (max 64).")
		return
	case len(pw) < minPasswordLen:
		redo("Password must be at least 8 characters.")
		return
	case pw != r.PostFormValue("password2"):
		redo("Passwords do not match.")
		return
	case s.emailTaken(d.Email, 0):
		redo("That email address belongs to another user.")
		return
	}
	hash, err := auth.HashPassword(pw)
	if err != nil {
		s.fail(w, err)
		return
	}
	_, err = s.Store.RegisterWithSignupInvite(h, store.User{Username: d.Username, DisplayName: d.DisplayName, Email: d.Email, PasswordHash: hash})
	switch {
	case err == store.ErrExists:
		redo("That username is taken.")
		return
	case err == store.ErrNotFound:
		s.render(w, r, "register", http.StatusBadRequest, page{Title: "Register", Error: badInvite})
		return
	case err != nil:
		s.fail(w, err)
		return
	}
	slog.Info("registered by invite", "user", d.Username, "ip", ip)
	http.Redirect(w, r, "/admin/login?ok=registered", http.StatusSeeOther)
}
