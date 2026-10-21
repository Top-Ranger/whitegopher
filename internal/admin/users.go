package admin

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/bitflipp/galdav/internal/auth"
	"github.com/bitflipp/galdav/internal/store"
)

func (s *Server) accountForm(w http.ResponseWriter, r *http.Request, sess *session) {
	s.render(w, r, "account", http.StatusOK, page{Title: "Account", Nav: "account"})
}

func (s *Server) changeOwnPassword(w http.ResponseWriter, r *http.Request, sess *session) {
	redo := func(msg string) {
		s.render(w, r, "account", http.StatusUnprocessableEntity, page{Title: "Account", Nav: "account", Error: msg})
	}
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
	hash, ha1, err := s.Auth.NewCredentials(u.Username, pw)
	if err == nil {
		err = s.Store.SetCredentials(u.ID, hash, ha1)
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
	hash, ha1, err := s.Auth.NewCredentials(name, pw)
	if err == nil {
		_, err = s.Store.CreateUser(store.User{
			Username: name, DisplayName: strings.TrimSpace(r.PostFormValue("display_name")),
			Email: strings.TrimSpace(r.PostFormValue("email")), PasswordHash: hash, DigestHA1: ha1,
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
