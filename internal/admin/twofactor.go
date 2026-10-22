package admin

import (
	"net/http"
	"strconv"
	"time"

	"github.com/bitflipp/whitegopher/internal/auth"
)

const recoveryCodeCount = 8

// twoFactorStart creates a secret for the user to add to an authenticator app.
// It is not active until a code from the app confirms it.
func (s *Server) twoFactorStart(w http.ResponseWriter, r *http.Request, sess *session) {
	if sess.User.TOTPEnabled {
		s.accountPage(w, r, sess, http.StatusUnprocessableEntity, "Two-factor authentication is already on.")
		return
	}
	secret, err := auth.NewTOTPSecret()
	if err == nil {
		err = s.Store.BeginTOTP(sess.User.ID, secret)
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, "/admin/account#two-factor", http.StatusSeeOther)
}

// twoFactorEnable confirms enrolment with a code and shows the recovery codes.
func (s *Server) twoFactorEnable(w http.ResponseWriter, r *http.Request, sess *session) {
	u := sess.User
	if u.TOTPEnabled || u.TOTPSecret == "" {
		s.accountPage(w, r, sess, http.StatusUnprocessableEntity, "Start two-factor setup first.")
		return
	}
	if !s.codeLimiter.Allowed(codeKey(u.ID)) {
		s.accountPage(w, r, sess, http.StatusTooManyRequests, "Too many wrong codes. Try again later.")
		return
	}
	step, ok := auth.ValidTOTP(u.TOTPSecret, r.PostFormValue("code"), time.Now(), 0)
	if !ok {
		s.codeLimiter.Fail(codeKey(u.ID))
		s.accountPage(w, r, sess, http.StatusUnprocessableEntity, "That code is not right. Check the app and your clock, then try again.")
		return
	}
	codes, hashes, err := auth.NewRecoveryCodes(recoveryCodeCount)
	if err == nil {
		err = s.Store.EnableTOTP(u.ID, step, hashes)
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	// Reload so the page shows the new state.
	if fresh, err := s.Store.UserByID(u.ID); err == nil {
		sess.User = fresh
	}
	s.accountPageWith(w, r, sess, http.StatusOK, "", func(d *accountData) { d.NewCodes = codes })
}

// twoFactorDisable turns two-factor off after re-checking the password and a
// code (or recovery code).
func (s *Server) twoFactorDisable(w http.ResponseWriter, r *http.Request, sess *session) {
	u := sess.User
	if !u.TOTPEnabled {
		s.accountPage(w, r, sess, http.StatusUnprocessableEntity, "Two-factor authentication is not on.")
		return
	}
	if !s.codeLimiter.Allowed(codeKey(u.ID)) {
		s.accountPage(w, r, sess, http.StatusTooManyRequests, "Too many wrong codes. Try again later.")
		return
	}
	if _, ok := s.Auth.Verify(u.Username, r.PostFormValue("password")); !ok || !s.Auth.CheckSecondFactor(u, r.PostFormValue("code")) {
		s.codeLimiter.Fail(codeKey(u.ID))
		s.accountPage(w, r, sess, http.StatusUnprocessableEntity, "Password or code is wrong.")
		return
	}
	if err := s.Store.DisableTOTP(u.ID); err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, "/admin/account?ok=saved", http.StatusSeeOther)
}

// adminDisableTwoFactor lets an administrator switch off a user's two-factor
// authentication, e.g. after they lost their device.
func (s *Server) adminDisableTwoFactor(w http.ResponseWriter, r *http.Request, sess *session) {
	u, ok := s.targetUser(w, r)
	if !ok {
		return
	}
	if err := s.Store.DisableTOTP(u.ID); err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, "/admin/users/"+strconv.FormatInt(u.ID, 10)+"?ok=saved", http.StatusSeeOther)
}

func codeKey(userID int64) string { return "u:" + strconv.FormatInt(userID, 10) }
