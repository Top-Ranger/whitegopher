package admin

import (
	"errors"
	"html/template"
	"net/http"
	"strconv"
	"strings"

	"github.com/bitflipp/whitegopher/internal/auth"
	"github.com/bitflipp/whitegopher/internal/store"
)

type accountData struct {
	Calendars []store.Collection
	// Default is the slug of the calendar receiving invitations ("" = automatic).
	Default string
	// HideFreeBusy withholds the user's availability from other users.
	HideFreeBusy bool
	AppPasswords []store.AppPassword
	// NewToken is an app password just created; it is shown once.
	NewToken string
	// Two-factor state. Secret and URI are filled while enrolling;
	// NewCodes holds recovery codes just generated (shown once).
	TwoFactorOn  bool
	Enrolling    bool
	Secret       string
	URI          template.URL // otpauth:// link; built here, so safe in an href
	RecoveryLeft int
	NewCodes     []string
}

func (s *Server) accountPage(w http.ResponseWriter, r *http.Request, sess *session, status int, errMsg string) {
	s.accountPageWith(w, r, sess, status, errMsg, nil)
}

// accountPageWith renders the account page; mod, if set, adjusts what it shows.
func (s *Server) accountPageWith(w http.ResponseWriter, r *http.Request, sess *session, status int, errMsg string, mod func(*accountData)) {
	cals, err := s.Store.ListCollections(sess.User.ID, store.KindCalendar)
	if err != nil {
		s.fail(w, err)
		return
	}
	aps, err := s.Store.ListAppPasswords(sess.User.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	d := accountData{Calendars: cals, Default: sess.User.DefaultCalendar, HideFreeBusy: sess.User.HideFreeBusy,
		AppPasswords: aps, TwoFactorOn: sess.User.TOTPEnabled}
	if sess.User.TOTPEnabled {
		if d.RecoveryLeft, err = s.Store.CountRecoveryCodes(sess.User.ID); err != nil {
			s.fail(w, err)
			return
		}
	} else if sess.User.TOTPSecret != "" {
		d.Enrolling, d.Secret = true, sess.User.TOTPSecret
		d.URI = template.URL(auth.OTPAuthURI("whitegopher", sess.User.Username, d.Secret))
	}
	if mod != nil {
		mod(&d)
	}
	s.render(w, r, "account", status, page{Title: "Account", Nav: "account", Error: errMsg, Data: d})
}

func (s *Server) createAppPassword(w http.ResponseWriter, r *http.Request, sess *session) {
	name := strings.TrimSpace(r.PostFormValue("name"))
	if name == "" || len(name) > 100 {
		s.accountPage(w, r, sess, http.StatusUnprocessableEntity, "Give the app password a name (up to 100 characters), e.g. the device.")
		return
	}
	token, hash, err := auth.NewAppPassword()
	if err == nil {
		_, err = s.Store.CreateAppPassword(sess.User.ID, name, hash)
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	s.accountPageWith(w, r, sess, http.StatusOK, "", func(d *accountData) { d.NewToken = token })
}

func (s *Server) deleteAppPassword(w http.ResponseWriter, r *http.Request, sess *session) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		notFound(w)
		return
	}
	if err := s.Store.DeleteAppPassword(sess.User.ID, id); err != nil {
		if isNotFound(err) {
			notFound(w)
		} else {
			s.fail(w, err)
		}
		return
	}
	http.Redirect(w, r, "/admin/account?ok=deleted", http.StatusSeeOther)
}

func (s *Server) accountForm(w http.ResponseWriter, r *http.Request, sess *session) {
	s.accountPage(w, r, sess, http.StatusOK, "")
}

func (s *Server) setDefaultCalendar(w http.ResponseWriter, r *http.Request, sess *session) {
	slug := r.PostFormValue("calendar")
	if slug != "" {
		if c, err := s.Store.GetCollection(sess.User.ID, store.KindCalendar, slug); err != nil || c.Special != "" {
			s.accountPage(w, r, sess, http.StatusUnprocessableEntity, "Unknown calendar.")
			return
		}
	}
	if err := s.Store.SetDefaultCalendar(sess.User.ID, slug); err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, "/admin/account?ok=saved", http.StatusSeeOther)
}

func (s *Server) setPrivacy(w http.ResponseWriter, r *http.Request, sess *session) {
	if err := s.Store.SetHideFreeBusy(sess.User.ID, r.PostFormValue("hide_freebusy") != ""); err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, "/admin/account?ok=saved", http.StatusSeeOther)
}

// emailTaken reports whether another user already uses email.
func (s *Server) emailTaken(email string, self int64) bool {
	if email == "" {
		return false
	}
	u, err := s.Store.UserByEmail(email)
	return err == nil && u.ID != self
}

func (s *Server) changeOwnPassword(w http.ResponseWriter, r *http.Request, sess *session) {
	redo := func(msg string) { s.accountPage(w, r, sess, http.StatusUnprocessableEntity, msg) }
	if _, ok := s.Auth.Verify(sess.User.Username, r.PostFormValue("current")); !ok {
		redo("Current password is wrong.")
		return
	}
	pw := r.PostFormValue("password")
	if len(pw) < minPasswordLen {
		redo("New password must be at least 8 characters.")
		return
	}
	if pw != r.PostFormValue("password2") {
		redo("Passwords do not match.")
		return
	}
	if !s.setPassword(w, sess.User, pw) {
		return
	}
	// Setting credentials ends all sessions, including this one.
	http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
}

// setPassword stores new credentials (which also ends the user's sessions).
func (s *Server) setPassword(w http.ResponseWriter, u store.User, pw string) bool {
	hash, err := auth.HashPassword(pw)
	if err == nil {
		err = s.Store.SetCredentials(u.ID, hash)
	}
	if err != nil {
		s.fail(w, err)
		return false
	}
	return true
}

type userRow struct {
	store.User
	Self bool
}

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request, sess *session) {
	us, err := s.Store.ListUsers()
	if err != nil {
		s.fail(w, err)
		return
	}
	rows := make([]userRow, len(us))
	for i, u := range us {
		rows[i] = userRow{User: u, Self: u.ID == sess.User.ID}
	}
	s.render(w, r, "users", http.StatusOK, page{Title: "Users", Nav: "users", Data: rows})
}

func (s *Server) newUserForm(w http.ResponseWriter, r *http.Request, sess *session) {
	s.render(w, r, "user_new", http.StatusOK, page{Title: "New user", Nav: "users"})
}

func (s *Server) createUser(w http.ResponseWriter, r *http.Request, sess *session) {
	redo := func(msg string) {
		s.render(w, r, "user_new", http.StatusUnprocessableEntity, page{Title: "New user", Nav: "users", Error: msg,
			Data: store.User{Username: r.PostFormValue("username"), DisplayName: r.PostFormValue("display_name"), Email: r.PostFormValue("email"), IsAdmin: r.PostFormValue("is_admin") != ""}})
	}
	name := strings.TrimSpace(r.PostFormValue("username"))
	pw := r.PostFormValue("password")
	switch {
	case !auth.ValidUsername(name):
		redo("Username may contain letters, digits and . _ @ - (max 64).")
		return
	case len(pw) < minPasswordLen:
		redo("Password must be at least 8 characters.")
		return
	}
	if _, err := s.Store.UserByName(name); err == nil {
		redo("That username is taken.")
		return
	}
	if s.emailTaken(r.PostFormValue("email"), 0) {
		redo("That email address belongs to another user.")
		return
	}
	hash, err := auth.HashPassword(pw)
	if err == nil {
		_, err = s.Store.CreateUser(store.User{
			Username: name, DisplayName: strings.TrimSpace(r.PostFormValue("display_name")),
			Email: strings.TrimSpace(r.PostFormValue("email")), PasswordHash: hash,
			IsAdmin: r.PostFormValue("is_admin") != "",
		})
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, "/admin/users?ok=created", http.StatusSeeOther)
}

func (s *Server) targetUser(w http.ResponseWriter, r *http.Request) (store.User, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		notFound(w)
		return store.User{}, false
	}
	u, err := s.Store.UserByID(id)
	if err != nil {
		if isNotFound(err) {
			notFound(w)
		} else {
			s.fail(w, err)
		}
		return u, false
	}
	return u, true
}

func (s *Server) userPage(w http.ResponseWriter, r *http.Request, u store.User, status int, errMsg string) {
	s.render(w, r, "user", status, page{Title: u.Username, Nav: "users", Data: u, Error: errMsg})
}

func (s *Server) userForm(w http.ResponseWriter, r *http.Request, sess *session) {
	if u, ok := s.targetUser(w, r); ok {
		s.userPage(w, r, u, http.StatusOK, "")
	}
}

// lastAdmin reports whether u is the only administrator.
func (s *Server) lastAdmin(u store.User) (bool, error) {
	if !u.IsAdmin {
		return false, nil
	}
	n, err := s.Store.CountAdmins()
	return n <= 1, err
}

func (s *Server) updateUser(w http.ResponseWriter, r *http.Request, sess *session) {
	u, ok := s.targetUser(w, r)
	if !ok {
		return
	}
	u.DisplayName = strings.TrimSpace(r.PostFormValue("display_name"))
	u.Email = strings.TrimSpace(r.PostFormValue("email"))
	wantAdmin := r.PostFormValue("is_admin") != ""
	if u.IsAdmin && !wantAdmin {
		last, err := s.lastAdmin(u)
		if err != nil {
			s.fail(w, err)
			return
		}
		if last {
			s.userPage(w, r, u, http.StatusUnprocessableEntity, "At least one administrator is required.")
			return
		}
	}
	if s.emailTaken(u.Email, u.ID) {
		s.userPage(w, r, u, http.StatusUnprocessableEntity, "That email address belongs to another user.")
		return
	}
	quota, err := parseQuotaMiB(r.PostFormValue("quota_mib"))
	if err != nil {
		s.userPage(w, r, u, http.StatusUnprocessableEntity, "Quota must be a whole number of MiB (blank for the default, 0 for unlimited).")
		return
	}
	u.QuotaBytes = quota
	u.IsAdmin = wantAdmin
	if err := s.Store.UpdateUser(u); err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, "/admin/users/"+strconv.FormatInt(u.ID, 10)+"?ok=saved", http.StatusSeeOther)
}

func (s *Server) resetPassword(w http.ResponseWriter, r *http.Request, sess *session) {
	u, ok := s.targetUser(w, r)
	if !ok {
		return
	}
	pw := r.PostFormValue("password")
	if len(pw) < minPasswordLen {
		s.userPage(w, r, u, http.StatusUnprocessableEntity, "Password must be at least 8 characters.")
		return
	}
	if !s.setPassword(w, u, pw) {
		return
	}
	if u.ID == sess.User.ID {
		http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/users/"+strconv.FormatInt(u.ID, 10)+"?ok=password", http.StatusSeeOther)
}

func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request, sess *session) {
	u, ok := s.targetUser(w, r)
	if !ok {
		return
	}
	if u.ID == sess.User.ID {
		s.userPage(w, r, u, http.StatusUnprocessableEntity, "You cannot delete your own account.")
		return
	}
	if r.PostFormValue("confirm") != u.Username {
		s.userPage(w, r, u, http.StatusUnprocessableEntity, "Type the username to confirm deletion.")
		return
	}
	last, err := s.lastAdmin(u)
	if err != nil {
		s.fail(w, err)
		return
	}
	if last {
		s.userPage(w, r, u, http.StatusUnprocessableEntity, "At least one administrator is required.")
		return
	}
	if err := s.Store.DeleteUser(u.ID); err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, "/admin/users?ok=deleted", http.StatusSeeOther)
}

// parseQuotaMiB reads the quota form field: blank is the server default (nil),
// otherwise a non-negative number of MiB.
func parseQuotaMiB(v string) (*int64, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 || n > 1<<30 {
		return nil, errors.New("bad quota")
	}
	b := n << 20
	return &b, nil
}
