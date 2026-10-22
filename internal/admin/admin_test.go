package admin_test

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/bitflipp/galdav/internal/admin"
	"github.com/bitflipp/galdav/internal/auth"
	"github.com/bitflipp/galdav/internal/dav"
	"github.com/bitflipp/galdav/internal/store"
	"github.com/bitflipp/galdav/internal/storetest"
)

type env struct {
	t   *testing.T
	ts  *httptest.Server
	st  *store.Store
	cli *http.Client
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st := storetest.New(t)
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
	// New accounts get no Digest HA1.
	u, _ := e.st.UserByName("root")
	if u.DigestHA1 != "" {
		t.Fatal("HA1 stored for a new account")
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
	hash, _ := auth.HashPassword("hunter2hunter2")
	e.st.CreateUser(store.User{Username: "root", PasswordHash: hash, IsAdmin: true})

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
	hash, _ := auth.HashPassword("hunter2hunter2")
	rootID, _ := e.st.CreateUser(store.User{Username: "root", PasswordHash: hash, IsAdmin: true})
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

	// Hiding free-busy is a personal setting.
	bc = login("bob", "new-password1")
	resp, b = e.post(bc, "/admin/account/privacy", "hide_freebusy", "1")
	status(t, resp, 303, b)
	if u, _ := e.st.UserByName("bob"); !u.HideFreeBusy {
		t.Fatal("hide_freebusy not saved")
	}
	resp, b = e.post(bc, "/admin/account/privacy")
	status(t, resp, 303, b)
	if u, _ := e.st.UserByName("bob"); u.HideFreeBusy {
		t.Fatal("hide_freebusy not cleared")
	}

	// App passwords: shown once, usable for DAV, revocable, not for the UI.
	resp, b = e.post(bc, "/admin/account/app-passwords", "name", " ")
	status(t, resp, 422, b)
	resp, b = e.post(bc, "/admin/account/app-passwords", "name", "Phone")
	status(t, resp, 200, b)
	m := regexp.MustCompile(`gdp-[a-z2-7]{5}(-[a-z2-7]{5}){3}`).FindString(b)
	if m == "" {
		t.Fatalf("no token shown: %s", b)
	}
	if _, ok := a.VerifyDAV("bob", m); !ok {
		t.Fatal("new app password does not authenticate")
	}
	resp, b = e.do(bc, "GET", "/admin/account", nil)
	status(t, resp, 200, b)
	contains(t, b, "Phone")
	if strings.Contains(b, m) {
		t.Fatal("token shown again")
	}
	resp, b = e.do(newClient(), "POST", "/admin/login", url.Values{"username": {"bob"}, "password": {m}})
	status(t, resp, 401, b)
	aps, _ := e.st.ListAppPasswords(bob.ID)
	resp, b = e.post(bc, "/admin/account/app-passwords/"+strconv.FormatInt(aps[0].ID, 10)+"/delete")
	status(t, resp, 303, b)
	if _, ok := a.VerifyDAV("bob", m); ok {
		t.Fatal("revoked app password still works")
	}

	// Admin edits, resets and deletes; last-admin and self protections hold.
	bid := strconv.FormatInt(bob.ID, 10)
	resp, b = e.post(admin, "/admin/users/"+bid, "display_name", "Robert", "is_admin", "1")
	status(t, resp, 303, b)
	resp, b = e.post(admin, "/admin/users/"+bid, "display_name", "Robert") // demote again
	status(t, resp, 303, b)
	// Quota: blank is the server default, 0 unlimited, a number is MiB.
	resp, b = e.post(admin, "/admin/users/"+bid, "display_name", "Robert", "quota_mib", "5")
	status(t, resp, 303, b)
	if u, _ := e.st.UserByName("bob"); u.QuotaBytes == nil || *u.QuotaBytes != 5<<20 {
		t.Fatalf("quota = %v", u.QuotaBytes)
	}
	resp, b = e.post(admin, "/admin/users/"+bid, "display_name", "Robert", "quota_mib", "lots")
	status(t, resp, 422, b)
	resp, b = e.post(admin, "/admin/users/"+bid, "display_name", "Robert", "quota_mib", "")
	status(t, resp, 303, b)
	if u, _ := e.st.UserByName("bob"); u.QuotaBytes != nil {
		t.Fatalf("quota not reset: %v", *u.QuotaBytes)
	}
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

func TestEmailUniquenessAndDefaultCalendar(t *testing.T) {
	e := newEnv(t)
	hash, _ := auth.HashPassword("hunter2hunter2")
	rootID, _ := e.st.CreateUser(store.User{Username: "root", PasswordHash: hash, IsAdmin: true, Email: "Root@Example.com"})
	c := newClient()
	resp, b := e.do(c, "POST", "/admin/login", url.Values{"username": {"root"}, "password": {"hunter2hunter2"}})
	status(t, resp, 303, b)

	// Emails are matched case-insensitively and must be unique.
	resp, b = e.post(c, "/admin/users", "username", "bob", "password", "bobs-password", "email", "ROOT@example.com")
	status(t, resp, 422, b)
	contains(t, b, "belongs to another user")
	resp, b = e.post(c, "/admin/users", "username", "bob", "password", "bobs-password", "email", "bob@example.com")
	status(t, resp, 303, b)
	bob, _ := e.st.UserByName("bob")
	resp, b = e.post(c, "/admin/users/"+strconv.FormatInt(bob.ID, 10), "email", "root@example.com")
	status(t, resp, 422, b)
	if u, err := e.st.UserByEmail("Root@EXAMPLE.com"); err != nil || u.ID != rootID {
		t.Fatalf("lookup: %+v %v", u, err)
	}

	// Default calendar choice.
	resp, b = e.post(c, "/admin/collections", "kind", "calendar", "name", "Inbox", "comp_VEVENT", "1") // reserved name gets a suffix
	status(t, resp, 303, b)
	resp, b = e.post(c, "/admin/collections", "kind", "calendar", "name", "Work", "comp_VEVENT", "1")
	status(t, resp, 303, b)
	_, b = e.do(c, "GET", "/admin/account", nil)
	contains(t, b, `value="inbox-1"`, `value="work"`, "Automatic")
	resp, b = e.post(c, "/admin/account/default-calendar", "calendar", "work")
	status(t, resp, 303, b)
	if u, _ := e.st.UserByName("root"); u.DefaultCalendar != "work" {
		t.Fatalf("default = %q", u.DefaultCalendar)
	}
	resp, b = e.post(c, "/admin/account/default-calendar", "calendar", "nope")
	status(t, resp, 422, b)
	resp, b = e.post(c, "/admin/account/default-calendar", "calendar", "inbox") // the scheduling inbox is not a calendar choice
	status(t, resp, 422, b)
}

func TestSharingUI(t *testing.T) {
	e := newEnv(t)
	mk := func(name string, admin bool) int64 {
		hash, _ := auth.HashPassword(name + "-password")
		id, _ := e.st.CreateUser(store.User{Username: name, PasswordHash: hash, IsAdmin: admin})
		return id
	}
	mk("root", true)
	mk("bob", false)
	mk("eve", false)
	login := func(name string) *http.Client {
		c := newClient()
		resp, b := e.do(c, "POST", "/admin/login", url.Values{"username": {name}, "password": {name + "-password"}})
		status(t, resp, 303, b)
		return c
	}
	root, bob, eve := login("root"), login("bob"), login("eve")

	resp, b := e.post(root, "/admin/collections", "kind", "calendar", "name", "Team", "color", "#336699", "comp_VEVENT", "1")
	status(t, resp, 303, b)
	u, _ := e.st.UserByName("root")
	cals, _ := e.st.ListCollections(u.ID, store.KindCalendar)
	cid := strconv.FormatInt(cals[0].ID, 10)

	// Validation.
	resp, b = e.post(root, "/admin/collections/"+cid+"/shares", "username", "ghost", "access", "read")
	status(t, resp, 422, b)
	contains(t, b, "No such user")
	resp, b = e.post(root, "/admin/collections/"+cid+"/shares", "username", "root", "access", "read")
	status(t, resp, 422, b)
	resp, b = e.post(root, "/admin/collections/"+cid+"/shares", "username", "bob", "access", "admin")
	status(t, resp, 400, b)
	// Other users cannot manage the owner's sharing.
	resp, b = e.post(eve, "/admin/collections/"+cid+"/shares", "username", "eve", "access", "readwrite")
	status(t, resp, 404, b)

	// Share, see it on both sides.
	resp, b = e.post(root, "/admin/collections/"+cid+"/shares", "username", "bob", "access", "read")
	status(t, resp, 303, b)
	_, b = e.do(root, "GET", "/admin/collections/"+cid, nil)
	contains(t, b, "bob", "read only", "Stop sharing")
	_, b = e.do(bob, "GET", "/admin/", nil)
	contains(t, b, "Shared with you", "Team", "root", "read only")
	resp, b = e.post(root, "/admin/collections/"+cid+"/shares", "username", "bob", "access", "readwrite") // change access
	status(t, resp, 303, b)
	_, b = e.do(bob, "GET", "/admin/", nil)
	contains(t, b, "can edit")
	_, b = e.do(eve, "GET", "/admin/", nil)
	if strings.Contains(b, "Shared with you") {
		t.Fatal("eve sees a share that is not hers")
	}

	// Bob's own settings for the mount.
	bobUser, _ := e.st.UserByName("bob")
	mounts, _ := e.st.ListMounts(bobUser.ID, store.KindCalendar)
	sid := strconv.FormatInt(mounts[0].ShareID, 10)
	_, b = e.do(bob, "GET", "/admin/shared/"+sid, nil)
	contains(t, b, "Owned by", "root", "Leave")
	resp, b = e.post(bob, "/admin/shared/"+sid, "name", "Root's team", "color", "nope")
	status(t, resp, 422, b)
	resp, b = e.post(bob, "/admin/shared/"+sid, "name", "Rootsteam", "color", "#445566", "order", "3")
	status(t, resp, 303, b)
	m, _ := e.st.MountByID(bobUser.ID, mounts[0].ShareID)
	if m.DisplayName != "Rootsteam" || m.Color != "#445566" || m.SortOrder != 3 {
		t.Fatalf("mount: %+v", m)
	}
	if orig, _ := e.st.GetCollectionByID(cals[0].ID); orig.DisplayName != "Team" || orig.Color != "#336699" {
		t.Fatalf("owner's collection changed: %+v", orig)
	}
	// Mounts of other users are off limits.
	resp, b = e.do(eve, "GET", "/admin/shared/"+sid, nil)
	status(t, resp, 404, b)
	resp, b = e.post(eve, "/admin/shared/"+sid+"/leave")
	status(t, resp, 404, b)

	// Bob leaves; sharing again and then removing from the owner's side also works.
	resp, b = e.post(bob, "/admin/shared/"+sid+"/leave")
	status(t, resp, 303, b)
	if m, _ := e.st.ListMounts(bobUser.ID, store.KindCalendar); len(m) != 0 {
		t.Fatal("still mounted")
	}
	e.post(root, "/admin/collections/"+cid+"/shares", "username", "bob", "access", "read")
	resp, b = e.post(root, "/admin/collections/"+cid+"/shares/"+strconv.FormatInt(bobUser.ID, 10)+"/remove")
	status(t, resp, 303, b)
	if sh, _ := e.st.Sharees(cals[0].ID); len(sh) != 0 {
		t.Fatal("share not removed")
	}
	// The scheduling inbox is not reachable as a collection page.
	inbox, _ := e.st.Inbox(u.ID)
	resp, b = e.do(root, "GET", "/admin/collections/"+strconv.FormatInt(inbox.ID, 10), nil)
	status(t, resp, 404, b)
}

func TestTrustedProxies(t *testing.T) {
	st := storetest.New(t)
	a := &auth.Authenticator{Store: st, Realm: "R"}
	hash, _ := auth.HashPassword("hunter2hunter2")
	st.CreateUser(store.User{Username: "root", PasswordHash: hash, IsAdmin: true})
	build := func(trusted ...string) *httptest.Server {
		var ps []netip.Prefix
		for _, p := range trusted {
			ps = append(ps, netip.MustParsePrefix(p))
		}
		ui, err := (&admin.Server{Store: st, Auth: a, DAVPath: "/dav", TrustedProxies: ps}).Handler()
		if err != nil {
			t.Fatal(err)
		}
		ts := httptest.NewServer(ui)
		t.Cleanup(ts.Close)
		return ts
	}
	login := func(ts *httptest.Server, pass string, hdr ...string) *http.Response {
		req, _ := http.NewRequest("POST", ts.URL+"/admin/login", strings.NewReader(url.Values{"username": {"root"}, "password": {pass}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		for i := 0; i+1 < len(hdr); i += 2 {
			req.Header.Set(hdr[i], hdr[i+1])
		}
		resp, err := newClient().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}
	secure := func(resp *http.Response) bool {
		for _, c := range resp.Cookies() {
			if c.Name == "galdav_session" {
				return c.Secure
			}
		}
		t.Fatal("no session cookie")
		return false
	}

	// Behind a trusted proxy, attempts are counted per forwarded client.
	ts := build("127.0.0.0/8")
	for i := 0; i < 10; i++ {
		if r := login(ts, "wrong", "X-Forwarded-For", "203.0.113.5"); r.StatusCode != 401 {
			t.Fatalf("attempt %d: %d", i, r.StatusCode)
		}
	}
	if r := login(ts, "wrong", "X-Forwarded-For", "203.0.113.5"); r.StatusCode != 429 {
		t.Fatalf("not rate limited: %d", r.StatusCode)
	}
	if r := login(ts, "wrong", "X-Forwarded-For", "203.0.113.5, 127.0.0.1"); r.StatusCode != 429 {
		t.Fatalf("trusted hop in the chain should be skipped: %d", r.StatusCode)
	}
	r := login(ts, "hunter2hunter2", "X-Forwarded-For", "203.0.113.9", "X-Forwarded-Proto", "https")
	if r.StatusCode != 303 || !secure(r) {
		t.Fatalf("other client: %d secure=%v", r.StatusCode, secure(r))
	}
	// A client-supplied hop cannot hide the real one: the rightmost untrusted entry counts.
	if r := login(ts, "wrong", "X-Forwarded-For", "1.2.3.4, 203.0.113.5"); r.StatusCode != 429 {
		t.Fatalf("spoofed left entry evaded the limit: %d", r.StatusCode)
	}

	// Without trust the headers are ignored: everyone is the peer address.
	ts = build()
	for i := 0; i < 10; i++ {
		login(ts, "wrong", "X-Forwarded-For", "198.51.100."+strconv.Itoa(i))
	}
	if r := login(ts, "hunter2hunter2", "X-Forwarded-For", "198.51.100.77"); r.StatusCode != 429 {
		t.Fatalf("forged X-Forwarded-For escaped the limit: %d", r.StatusCode)
	}
	ts = build()
	if r := login(ts, "hunter2hunter2", "X-Forwarded-Proto", "https"); r.StatusCode != 303 || secure(r) {
		t.Fatalf("untrusted X-Forwarded-Proto honoured: %d secure=%v", r.StatusCode, secure(r))
	}
}
