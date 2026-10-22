package admin

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bitflipp/galdav/internal/store"
)

const resetLifetime = time.Hour

// TextMailer sends plain-text email, for password-reset links.
type TextMailer interface {
	SendText(to, subject, body string) error
}

// resetEnabled reports whether password reset by email is available: it needs
// an outgoing mail server and the public URL to put in the link.
func (s *Server) resetEnabled() bool { return s.Mailer != nil && s.BaseURL != "" }

func (s *Server) forgotForm(w http.ResponseWriter, r *http.Request) {
	if !s.resetEnabled() {
		notFound(w)
		return
	}
	s.render(w, r, "forgot", http.StatusOK, page{Title: "Reset password"})
}

// forgot emails a reset link. The answer is the same whether or not the
// account exists, so it cannot be used to find out who has one.
func (s *Server) forgot(w http.ResponseWriter, r *http.Request) {
	if !s.resetEnabled() {
		notFound(w)
		return
	}
	ip := s.clientIP(r)
	if !s.resetLimiter.Allowed(ip) {
		w.Header().Set("Retry-After", "3600")
		s.render(w, r, "forgot", http.StatusTooManyRequests, page{Title: "Reset password", Error: "Too many requests. Try again later."})
		return
	}
	s.resetLimiter.Fail(ip) // every request counts
	ident := strings.TrimSpace(r.PostFormValue("identifier"))
	u, err := s.Store.UserByName(ident)
	if err != nil {
		u, err = s.Store.UserByEmail(ident)
	}
	if err == nil && u.Email != "" && s.userResetLimiter.Allowed(codeKey(u.ID)) {
		s.userResetLimiter.Fail(codeKey(u.ID))
		if err := s.sendReset(u); err != nil {
			slog.Error("password reset", "user", u.Username, "err", err)
		} else {
			slog.Info("password reset requested", "user", u.Username, "ip", ip)
		}
	}
	s.render(w, r, "forgot", http.StatusOK, page{Title: "Reset password", Flash: "If that account exists and has an email address, a reset link is on its way. It works for one hour."})
}

func (s *Server) sendReset(u store.User) error {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return err
	}
	token := hex.EncodeToString(b)
	if err := s.Store.CreatePasswordReset(u.ID, hashToken(token), time.Now().Add(resetLifetime)); err != nil {
		return err
	}
	link := strings.TrimRight(s.BaseURL, "/") + "/admin/reset?token=" + url.QueryEscape(token)
	body := fmt.Sprintf("Someone asked to reset the password of the galdav account %q.\n\n"+
		"To choose a new password, open this link within one hour:\n\n%s\n\n"+
		"If you did not ask for this, ignore this message; your password stays as it is.\n", u.Username, link)
	return s.Mailer.SendText(u.Email, "Reset your galdav password", body)
}

func (s *Server) resetForm(w http.ResponseWriter, r *http.Request) {
	if !s.resetEnabled() {
		notFound(w)
		return
	}
	token := r.URL.Query().Get("token")
	if _, err := s.Store.PasswordResetUser(hashToken(token)); err != nil {
		s.render(w, r, "forgot", http.StatusBadRequest, page{Title: "Reset password", Error: "This reset link is invalid or has expired. Ask for a new one."})
		return
	}
	s.render(w, r, "reset", http.StatusOK, page{Title: "Choose a new password", Data: token})
}

func (s *Server) reset(w http.ResponseWriter, r *http.Request) {
	if !s.resetEnabled() {
		notFound(w)
		return
	}
	token := r.PostFormValue("token")
	h := hashToken(token)
	u, err := s.Store.PasswordResetUser(h)
	if err != nil {
		s.render(w, r, "forgot", http.StatusBadRequest, page{Title: "Reset password", Error: "This reset link is invalid or has expired. Ask for a new one."})
		return
	}
	redo := func(msg string) {
		s.render(w, r, "reset", http.StatusUnprocessableEntity, page{Title: "Choose a new password", Data: token, Error: msg})
	}
	pw := r.PostFormValue("password")
	switch {
	case len(pw) < minPasswordLen:
		redo("Password must be at least 8 characters.")
		return
	case pw != r.PostFormValue("password2"):
		redo("Passwords do not match.")
		return
	}
	if ok, err := s.Store.ConsumePasswordReset(h); err != nil || !ok {
		s.render(w, r, "forgot", http.StatusBadRequest, page{Title: "Reset password", Error: "This reset link is invalid or has expired. Ask for a new one."})
		return
	}
	if !s.setPassword(w, u, pw) {
		return
	}
	slog.Info("password reset", "user", u.Username, "ip", s.clientIP(r))
	http.Redirect(w, r, "/admin/login?ok=reset", http.StatusSeeOther)
}
