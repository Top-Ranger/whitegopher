package dav_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bitflipp/galdav/internal/auth"
	"github.com/bitflipp/galdav/internal/dav"
	"github.com/bitflipp/galdav/internal/store"
	"github.com/bitflipp/galdav/internal/storetest"
)

func newServer(t *testing.T) *httptest.Server {
	t.Helper()
	st := storetest.New(t)
	// Imported-style account: only an HA1, no argon2 hash.
	if _, err := st.CreateUser(store.User{Username: "alice", DigestHA1: auth.HA1("alice", "R", "secret")}); err != nil {
		t.Fatal(err)
	}
	s := &dav.Server{Store: st, Auth: &auth.Authenticator{Store: st, Realm: "R"}, BasePath: "/dav"}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func do(t *testing.T, method, url, user, pass, body string, hdr map[string]string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	if user != "" {
		req.SetBasicAuth(user, pass)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(b)
}

func TestWellKnown(t *testing.T) {
	ts := newServer(t)
	for _, p := range []string{"/.well-known/caldav", "/.well-known/caldav/", "/.well-known/carddav", "/.well-known/carddav/"} {
		resp, _ := do(t, "GET", ts.URL+p, "", "", "", nil)
		if resp.StatusCode != 301 || resp.Header.Get("Location") != "/dav/" {
			t.Fatalf("%s: got %d %q", p, resp.StatusCode, resp.Header.Get("Location"))
		}
	}
}

func TestAuth(t *testing.T) {
	ts := newServer(t)
	if resp, _ := do(t, "PROPFIND", ts.URL+"/dav/", "", "", "", nil); resp.StatusCode != 401 {
		t.Fatalf("no creds: %d", resp.StatusCode)
	}
	if resp, _ := do(t, "PROPFIND", ts.URL+"/dav/", "alice", "wrong", "", nil); resp.StatusCode != 401 {
		t.Fatalf("bad creds: %d", resp.StatusCode)
	}
	// Works twice: first via HA1 (then upgraded), second via argon2id.
	for i := 0; i < 2; i++ {
		if resp, _ := do(t, "PROPFIND", ts.URL+"/dav/", "alice", "secret", "", map[string]string{"Depth": "0"}); resp.StatusCode != 207 {
			t.Fatalf("login %d: %d", i, resp.StatusCode)
		}
	}
}

func TestDiscovery(t *testing.T) {
	ts := newServer(t)
	body := `<propfind xmlns="DAV:"><prop><current-user-principal/></prop></propfind>`
	resp, out := do(t, "PROPFIND", ts.URL+"/dav/", "alice", "secret", body, map[string]string{"Depth": "0"})
	if resp.StatusCode != 207 || !strings.Contains(out, "/dav/principals/alice/") {
		t.Fatalf("%d %s", resp.StatusCode, out)
	}
	body = `<propfind xmlns="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav"><prop><c:calendar-home-set/><getetag/></prop></propfind>`
	_, out = do(t, "PROPFIND", ts.URL+"/dav/principals/alice/", "alice", "secret", body, map[string]string{"Depth": "0"})
	if !strings.Contains(out, "/dav/calendars/alice/") || !strings.Contains(out, "404 Not Found") {
		t.Fatalf("%s", out)
	}
	if resp, _ := do(t, "PROPFIND", ts.URL+"/dav/principals/bob/", "alice", "secret", "", nil); resp.StatusCode != 403 {
		t.Fatalf("other user: %d", resp.StatusCode)
	}
}

func TestSiteRoot(t *testing.T) {
	ts := newServer(t)
	body := `<propfind xmlns="DAV:"><prop><current-user-principal/></prop></propfind>`
	if resp, _ := do(t, "PROPFIND", ts.URL+"/", "", "", body, map[string]string{"Depth": "0"}); resp.StatusCode != 401 {
		t.Fatalf("anonymous: %d", resp.StatusCode)
	}
	resp, out := do(t, "PROPFIND", ts.URL+"/", "alice", "secret", body, map[string]string{"Depth": "0"})
	if resp.StatusCode != 207 || !strings.Contains(out, "/dav/principals/alice/") || !strings.Contains(out, "<D:href>/</D:href>") {
		t.Fatalf("%d %s", resp.StatusCode, out)
	}
	resp, out = do(t, "PROPFIND", ts.URL+"/", "alice", "secret", "", map[string]string{"Depth": "1"})
	if resp.StatusCode != 207 || !strings.Contains(out, "<D:href>/dav/</D:href>") {
		t.Fatalf("depth 1: %d %s", resp.StatusCode, out)
	}
	resp, _ = do(t, "OPTIONS", ts.URL+"/", "alice", "secret", "", nil)
	if resp.StatusCode != 204 || !strings.Contains(resp.Header.Get("DAV"), "calendar-access") {
		t.Fatalf("options: %d %v", resp.StatusCode, resp.Header)
	}
	if resp, _ := do(t, "GET", ts.URL+"/", "alice", "secret", "", nil); resp.StatusCode != 404 {
		t.Fatalf("GET /: %d", resp.StatusCode)
	}
}
