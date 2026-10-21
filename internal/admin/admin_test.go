package admin_test

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/bitflipp/galdav/internal/admin"
	"github.com/bitflipp/galdav/internal/auth"
	"github.com/bitflipp/galdav/internal/dav"
	"github.com/bitflipp/galdav/internal/store"
)

type env struct {
	t   *testing.T
	ts  *httptest.Server
	st  *store.Store
	cli *http.Client
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	a := &auth.Authenticator{Store: st, Realm: "R"}
	ui, err := (&admin.Server{Store: st, Auth: a, DAVPath: "/dav"}).Handler()
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/", (&dav.Server{Store: st, Auth: a, BasePath: "/dav"}).Handler())
	mux.Handle("/admin/", ui)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return &env{t: t, ts: ts, st: st, cli: newClient()}
}

func newClient() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func (e *env) do(c *http.Client, method, path string, form url.Values, hdr ...string) (*http.Response, string) {
	e.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, _ := http.NewRequest(method, e.ts.URL+path, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := c.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(b)
}

var csrfRE = regexp.MustCompile(`name="csrf" value="([^"]+)"`)

func (e *env) csrf(c *http.Client) string {
	e.t.Helper()
	_, body := e.do(c, "GET", "/admin/account", nil)
	m := csrfRE.FindStringSubmatch(body)
	if m == nil {
		e.t.Fatalf("no csrf token in %s", body)
	}
	return m[1]
}

func (e *env) post(c *http.Client, path string, kv ...string) (*http.Response, string) {
	e.t.Helper()
	f := url.Values{"csrf": {e.csrf(c)}}
	for i := 0; i+1 < len(kv); i += 2 {
		f.Set(kv[i], kv[i+1])
	}
	return e.do(c, "POST", path, f)
}

func status(t *testing.T, resp *http.Response, want int, body string) {
	t.Helper()
	if resp.StatusCode != want {
		t.Fatalf("status %d, want %d (Location %q); body: %.400s", resp.StatusCode, want, resp.Header.Get("Location"), body)
	}
}

func contains(t *testing.T, body string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(body, w) {
			t.Fatalf("missing %q in %.600s", w, body)
		}
	}
}

func TestSetupLoginAndCollections(t *testing.T) {
	e := newEnv(t)
	c := e.cli

	// Everything redirects to setup while there are no users.
	resp, b := e.do(c, "GET", "/admin/", nil)
	status(t, resp, 303, b)
	if resp.Header.Get("Location") != "/admin/setup" {
		t.Fatal(resp.Header.Get("Location"))
	}
	resp, b = e.do(c, "POST", "/admin/setup", url.Values{"username": {"root"}, "password": {"short"}, "password2": {"short"}})
	status(t, resp, 422, b)
	resp, b = e.do(c, "POST", "/admin/setup", url.Values{"username": {"root"}, "password": {"hunter2hunter2"}, "password2": {"hunter2hunter2"}})
	status(t, resp, 303, b)
	// Setup is closed once a user exists.
	resp, b = e.do(newClient(), "POST", "/admin/setup", url.Values{"username": {"evil"}, "password": {"hunter2hunter2"}, "password2": {"hunter2hunter2"}})
	status(t, resp, 303, b)
	if _, err := e.st.UserByName("evil"); err == nil {
		t.Fatal("second setup created a user")
	}

	// Logged in after setup.
	resp, b = e.do(c, "GET", "/admin/", nil)
	status(t, resp, 200, b)
	contains(t, b, "No calendars yet", "http://127.0.0.1")

	// Create a calendar and an address book, then use them over DAV.
	resp, b = e.post(c, "/admin/collections", "kind", "calendar", "name", "Work Stuff", "color", "#ff0000", "comp_VEVENT", "1")
	status(t, resp, 303, b)
	resp, b = e.post(c, "/admin/collections", "kind", "calendar", "name", "Work Stuff", "comp_VEVENT", "1") // slug collision → suffix
	status(t, resp, 303, b)
	resp, b = e.post(c, "/admin/collections", "kind", "addressbook", "name", "People")
	status(t, resp, 303, b)
	resp, b = e.post(c, "/admin/collections", "kind", "calendar", "name", "Bad", "color", "red", "comp_VEVENT", "1")
	status(t, resp, 422, b)
	resp, b = e.post(c, "/admin/collections", "kind", "calendar", "name", "NoComp")
	status(t, resp, 422, b)
	_, b = e.do(c, "GET", "/admin/", nil)
	contains(t, b, "work-stuff", "work-stuff-2", "people", `fill="#ff0000"`)

	req, _ := http.NewRequest("PROPFIND", e.ts.URL+"/dav/calendars/root/work-stuff/", nil)
	req.SetBasicAuth("root", "hunter2hunter2")
	req.Header.Set("Depth", "0")
	r2, err := http.DefaultClient.Do(req)
	if err != nil || r2.StatusCode != 207 {
		t.Fatalf("DAV sees the UI-created calendar: %v %v", err, r2)
	}
	// Digest HA1 was stored, so Digest-only clients can authenticate.
	u, _ := e.st.UserByName("root")
	if u.DigestHA1 != auth.HA1("root", "R", "hunter2hunter2") {
		t.Fatal("digest hash missing")
	}

	// Edit and delete.
	cols, _ := e.st.ListCollections(u.ID, store.KindCalendar)
	id := strconv.FormatInt(cols[0].ID, 10)
	resp, b = e.post(c, "/admin/collections/"+id, "name", "Renamed", "color", "#00ff00", "order", "4")
	status(t, resp, 303, b)
	_, b = e.do(c, "GET", "/admin/collections/"+id, nil)
	contains(t, b, "Renamed", "#00ff00", "/dav/calendars/root/work-stuff/")
	resp, b = e.post(c, "/admin/collections/"+id+"/delete", "confirm", "nope")
	status(t, resp, 422, b)
	resp, b = e.post(c, "/admin/collections/"+id+"/delete", "confirm", "work-stuff")
	status(t, resp, 303, b)
	if _, err := e.st.GetCollection(u.ID, store.KindCalendar, "work-stuff"); err == nil {
		t.Fatal("not deleted")
	}
}

func TestSecurity(t *testing.T) {
	e := newEnv(t)
	a := &auth.Authenticator{Store: e.st, Realm: "R"}
	hash, ha1, _ := a.NewCredentials("root", "hunter2hunter2")
	e.st.CreateUser(store.User{Username: "root", PasswordHash: hash, DigestHA1: ha1, IsAdmin: true})

	// Anonymous requests go to login.
	resp, b := e.do(e.cli, "GET", "/admin/users", nil)
	status(t, resp, 303, b)
	if resp.Header.Get("Location") != "/admin/login" {
		t.Fatal(resp.Header.Get("Location"))
	}
	// Wrong password.
	resp, b = e.do(e.cli, "POST", "/admin/login", url.Values{"username": {"root"}, "password": {"nope"}})
	status(t, resp, 401, b)
	// Cross-origin POST is rejected even with correct credentials.
	resp, b = e.do(e.cli, "POST", "/admin/login", url.Values{"username": {"root"}, "password": {"hunter2hunter2"}}, "Origin", "https://evil.example")
	status(t, resp, 403, b)
	resp, b = e.do(e.cli, "POST", "/admin/login", url.Values{"username": {"root"}, "password": {"hunter2hunter2"}})
	status(t, resp, 303, b)
	var sessCookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == "galdav_session" {
			sessCookie = c
		}
	}
	if sessCookie == nil || !sessCookie.HttpOnly || sessCookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("cookie: %+v", sessCookie)
	}
	if resp.Header.Get("Content-Security-Policy") == "" {
		t.Fatal("no CSP")
	}
	// POST without CSRF token is rejected.
	resp, b = e.do(e.cli, "POST", "/admin/collections", url.Values{"kind": {"addressbook"}, "name": {"x"}})
	status(t, resp, 403, b)

	// Logout invalidates the session server-side.
	resp, b = e.post(e.cli, "/admin/logout")
	status(t, resp, 303, b)
	old := newClient()
	u, _ := url.Parse(e.ts.URL)
	old.Jar.SetCookies(u, []*http.Cookie{sessCookie})
	resp, b = e.do(old, "GET", "/admin/", nil)
	status(t, resp, 303, b)
	if resp.Header.Get("Location") != "/admin/login" {
		t.Fatal("old session cookie still works")
	}
}

func TestUserManagement(t *testing.T) {
	e := newEnv(t)
	a := &auth.Authenticator{Store: e.st, Realm: "R"}
	hash, ha1, _ := a.NewCredentials("root", "hunter2hunter2")
	rootID, _ := e.st.CreateUser(store.User{Username: "root", PasswordHash: hash, DigestHA1: ha1, IsAdmin: true})
	login := func(name, pw string) *http.Client {
		c := newClient()
		resp, b := e.do(c, "POST", "/admin/login", url.Values{"username": {name}, "password": {pw}})
		status(t, resp, 303, b)
		return c
	}
	admin := login("root", "hunter2hunter2")

	resp, b := e.post(admin, "/admin/users", "username", "bad name!", "password", "longenough")
	status(t, resp, 422, b)
	resp, b = e.post(admin, "/admin/users", "username", "bob", "password", "short")
	status(t, resp, 422, b)
	resp, b = e.post(admin, "/admin/users", "username", "bob", "password", "bobs-password", "display_name", "Bob", "email", "bob@example.com")
	status(t, resp, 303, b)
	resp, b = e.post(admin, "/admin/users", "username", "bob", "password", "bobs-password")
	status(t, resp, 422, b)
	contains(t, b, "taken")
	bob, err := e.st.UserByName("bob")
	if err != nil || bob.IsAdmin || bob.DisplayName != "Bob" {
		t.Fatalf("%+v %v", bob, err)
	}

	// A regular user cannot reach user management, and only sees own collections.
	bc := login("bob", "bobs-password")
	resp, b = e.do(bc, "GET", "/admin/users", nil)
	status(t, resp, 403, b)
	resp, b = e.post(bc, "/admin/users", "username", "mallory", "password", "longenough")
	status(t, resp, 403, b)
	resp, b = e.post(admin, "/admin/collections", "kind", "addressbook", "name", "Root book")
	status(t, resp, 303, b)
	rootCols, _ := e.st.ListCollections(rootID, store.KindAddressBook)
	resp, b = e.do(bc, "GET", "/admin/collections/"+strconv.FormatInt(rootCols[0].ID, 10), nil)
	status(t, resp, 404, b)
	resp, b = e.post(bc, "/admin/collections/"+strconv.FormatInt(rootCols[0].ID, 10)+"/delete", "confirm", rootCols[0].Slug)
	status(t, resp, 404, b)

	// Bob changes his own password; old sessions die, new password works.
	resp, b = e.post(bc, "/admin/account/password", "current", "wrong", "password", "new-password1", "password2", "new-password1")
	status(t, resp, 422, b)
	resp, b = e.post(bc, "/admin/account/password", "current", "bobs-password", "password", "new-password1", "password2", "new-password1")
	status(t, resp, 303, b)
	resp, b = e.do(bc, "GET", "/admin/", nil)
	if resp.Header.Get("Location") != "/admin/login" {
		t.Fatal("session survived password change")
	}
	if _, ok := a.Verify("bob", "bobs-password"); ok {
		t.Fatal("old password still valid")
	}
	login("bob", "new-password1")
	if u, _ := e.st.UserByName("bob"); u.DigestHA1 != auth.HA1("bob", "R", "new-password1") {
		t.Fatal("digest hash not updated")
	}

	// Admin edits, resets and deletes; last-admin and self protections hold.
	bid := strconv.FormatInt(bob.ID, 10)
	resp, b = e.post(admin, "/admin/users/"+bid, "display_name", "Robert", "is_admin", "1")
	status(t, resp, 303, b)
	resp, b = e.post(admin, "/admin/users/"+bid, "display_name", "Robert") // demote again
	status(t, resp, 303, b)
	rid := strconv.FormatInt(rootID, 10)
	resp, b = e.post(admin, "/admin/users/"+rid, "display_name", "") // demote the only admin
	status(t, resp, 422, b)
	resp, b = e.post(admin, "/admin/users/"+rid+"/delete", "confirm", "root")
	status(t, resp, 422, b)
	contains(t, b, "own account")
	resp, b = e.post(admin, "/admin/users/"+bid+"/password", "password", "reset-by-admin")
	status(t, resp, 303, b)
	login("bob", "reset-by-admin")
	resp, b = e.post(admin, "/admin/users/"+bid+"/delete", "confirm", "wrong")
	status(t, resp, 422, b)
	resp, b = e.post(admin, "/admin/users/"+bid+"/delete", "confirm", "bob")
	status(t, resp, 303, b)
	if _, err := e.st.UserByName("bob"); err == nil {
		t.Fatal("bob not deleted")
	}
}
