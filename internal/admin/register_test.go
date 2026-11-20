package admin_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"testing"

	"github.com/Top-Ranger/whitegopher/internal/admin"
	"github.com/Top-Ranger/whitegopher/internal/auth"
	"github.com/Top-Ranger/whitegopher/internal/store"
	"github.com/Top-Ranger/whitegopher/internal/storetest"
)

func TestInviteRegistration(t *testing.T) {
	st := storetest.New(t)
	a := &auth.Authenticator{Store: st, Realm: "R"}
	hash, _ := auth.HashPassword("password-1")
	st.CreateUser(store.User{Username: "root", PasswordHash: hash, IsAdmin: true})
	st.CreateUser(store.User{Username: "bob", PasswordHash: hash})

	build := func(mode string) *env {
		ui, err := (&admin.Server{Store: st, Auth: a, DAVPath: "/dav", Invites: mode}).Handler()
		if err != nil {
			t.Fatal(err)
		}
		mux := http.NewServeMux()
		mux.Handle("/admin/", ui)
		ts := httptest.NewServer(mux)
		t.Cleanup(ts.Close)
		return &env{t: t, ts: ts, st: st, cli: newClient()}
	}
	login := func(e *env, user string) *http.Client {
		c := newClient()
		resp, b := e.do(c, "POST", "/admin/login", url.Values{"username": {user}, "password": {"password-1"}})
		status(t, resp, 303, b)
		return c
	}
	linkRe := regexp.MustCompile(`/admin/register\?token=[0-9a-f]{64}`)

	// Default: administrators only.
	e := build("")
	bob, root := login(e, "bob"), login(e, "root")
	resp, b := e.do(bob, "GET", "/admin/invites", nil)
	status(t, resp, 404, b)
	resp, b = e.post(bob, "/admin/invites")
	status(t, resp, 404, b)
	resp, b = e.post(root, "/admin/invites")
	status(t, resp, 200, b)
	link := linkRe.FindString(b)
	if link == "" {
		t.Fatalf("no link in %q", b)
	}

	// Bad tokens are refused; the real one shows the form.
	resp, b = e.do(newClient(), "GET", "/admin/register?token=nope", nil)
	status(t, resp, 400, b)
	resp, b = e.do(newClient(), "GET", link, nil)
	status(t, resp, 200, b)
	token := link[len("/admin/register?token="):]

	reg := func(user, pw, pw2 string) (*http.Response, string) {
		return e.do(newClient(), "POST", "/admin/register", url.Values{"token": {token}, "username": {user}, "password": {pw}, "password2": {pw2}, "email": {"new@example.com"}})
	}
	// Validation errors and a taken username leave the link usable.
	resp, b = reg("new user!", "password-2", "password-2")
	status(t, resp, 422, b)
	resp, b = reg("newbie", "short", "short")
	status(t, resp, 422, b)
	resp, b = reg("newbie", "password-2", "different-2")
	status(t, resp, 422, b)
	resp, b = reg("bob", "password-2", "password-2")
	status(t, resp, 422, b)
	resp, b = reg("newbie", "password-2", "password-2")
	status(t, resp, 303, b)
	u, ok := a.Verify("newbie", "password-2")
	if !ok || u.IsAdmin {
		t.Fatalf("new account: %+v ok=%v", u, ok)
	}

	// Single use.
	resp, b = reg("another", "password-2", "password-2")
	status(t, resp, 400, b)
	resp, b = e.do(newClient(), "GET", link, nil)
	status(t, resp, 400, b)

	// Revoking removes the link.
	e.post(root, "/admin/invites")
	list, _ := st.ListSignupInvites(0)
	if len(list) != 1 {
		t.Fatalf("invites: %+v", list)
	}
	resp, b = e.post(bob, "/admin/invites/"+strconv.FormatInt(list[0].ID, 10)+"/revoke")
	status(t, resp, 404, b)
	resp, b = e.post(root, "/admin/invites/"+strconv.FormatInt(list[0].ID, 10)+"/revoke")
	status(t, resp, 303, b)

	// Everyone: users get their own invites and see only those.
	e = build("everyone")
	bob = login(e, "bob")
	resp, b = e.post(bob, "/admin/invites")
	status(t, resp, 200, b)
	if linkRe.FindString(b) == "" {
		t.Fatal("no link for a regular user")
	}
	e.post(login(e, "root"), "/admin/invites")
	bu, _ := st.UserByName("bob")
	if mine, _ := st.ListSignupInvites(bu.ID); len(mine) != 1 {
		t.Fatalf("%+v", mine)
	}
	if all, _ := st.ListSignupInvites(0); len(all) != 2 {
		t.Fatalf("%+v", all)
	}

	// Off: no page, no registration.
	e = build("off")
	resp, b = e.do(login(e, "root"), "GET", "/admin/invites", nil)
	status(t, resp, 404, b)
	resp, b = e.do(newClient(), "GET", link, nil)
	status(t, resp, 404, b)
	resp, b = e.do(newClient(), "POST", "/admin/register", url.Values{"token": {token}})
	status(t, resp, 404, b)
}
