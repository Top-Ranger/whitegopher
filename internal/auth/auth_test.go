package auth_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bitflipp/galdav/internal/auth"
	"github.com/bitflipp/galdav/internal/store"
	"github.com/bitflipp/galdav/internal/storetest"
)

func setup(t *testing.T) *httptest.Server {
	t.Helper()
	st := storetest.New(t)
	st.CreateUser(store.User{Username: "alice", DigestHA1: auth.HA1("alice", "R", "secret")})
	a := &auth.Authenticator{Store: st, Realm: "R"}
	ts := httptest.NewServer(a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, _ := auth.UserFrom(r.Context())
		fmt.Fprint(w, u.Username)
	})))
	t.Cleanup(ts.Close)
	return ts
}

func get(t *testing.T, ts *httptest.Server, authz string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest("PROPFIND", ts.URL+"/dav/x?y=1", nil)
	if authz != "" {
		req.Header.Set("Authorization", authz)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	buf := new(strings.Builder)
	b := make([]byte, 512)
	n, _ := resp.Body.Read(b)
	buf.Write(b[:n])
	return resp, buf.String()
}

func nonceOf(t *testing.T, resp *http.Response) string {
	t.Helper()
	for _, h := range resp.Header.Values("WWW-Authenticate") {
		if rest, ok := strings.CutPrefix(h, "Digest "); ok {
			i := strings.Index(rest, `nonce="`) + len(`nonce="`)
			return rest[i : i+strings.Index(rest[i:], `"`)]
		}
	}
	t.Fatal("no digest challenge")
	return ""
}

func digestHeader(user, ha1, nonce, nc, uri string) string {
	resp := auth.DigestResponse(ha1, "PROPFIND", uri, nonce, nc, "cn", "auth")
	return fmt.Sprintf(`Digest username=%q, realm="R", nonce=%q, uri=%q, qop=auth, nc=%s, cnonce="cn", response=%q, algorithm=MD5`,
		user, nonce, uri, nc, resp)
}

func TestDigest(t *testing.T) {
	ts := setup(t)
	resp, _ := get(t, ts, "")
	if resp.StatusCode != 401 || len(resp.Header.Values("WWW-Authenticate")) != 2 {
		t.Fatalf("challenge: %d %v", resp.StatusCode, resp.Header)
	}
	nonce := nonceOf(t, resp)
	ha1 := auth.HA1("alice", "R", "secret")
	uri := "/dav/x?y=1"

	if resp, body := get(t, ts, digestHeader("alice", ha1, nonce, "00000001", uri)); resp.StatusCode != 200 || body != "alice" {
		t.Fatalf("good digest: %d %q", resp.StatusCode, body)
	}
	if resp, _ := get(t, ts, digestHeader("alice", ha1, nonce, "00000001", uri)); resp.StatusCode != 401 {
		t.Fatalf("replayed nc accepted: %d", resp.StatusCode)
	}
	if resp, _ := get(t, ts, digestHeader("alice", ha1, nonce, "00000002", uri)); resp.StatusCode != 200 {
		t.Fatalf("nc 2: %d", resp.StatusCode)
	}
	bad := auth.HA1("alice", "R", "wrong")
	if resp, _ := get(t, ts, digestHeader("alice", bad, nonce, "00000003", uri)); resp.StatusCode != 401 {
		t.Fatalf("wrong password: %d", resp.StatusCode)
	}
	if resp, _ := get(t, ts, digestHeader("alice", ha1, nonce, "00000003", "/dav/other")); resp.StatusCode != 401 {
		t.Fatalf("uri mismatch accepted: %d", resp.StatusCode)
	}
	if resp, _ := get(t, ts, digestHeader("alice", ha1, "forged.nonce", "00000001", uri)); resp.StatusCode != 401 {
		t.Fatalf("forged nonce accepted: %d", resp.StatusCode)
	}
	if resp, _ := get(t, ts, digestHeader("mallory", ha1, nonce, "00000009", uri)); resp.StatusCode != 401 {
		t.Fatalf("unknown user: %d", resp.StatusCode)
	}
}

func TestBasicStillWorks(t *testing.T) {
	ts := setup(t)
	req, _ := http.NewRequest("GET", ts.URL, nil)
	req.SetBasicAuth("alice", "secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("%v %v", err, resp)
	}
}

func TestPasswordHashing(t *testing.T) {
	h, err := auth.HashPassword("pw")
	if err != nil || !strings.HasPrefix(h, "$argon2id$") {
		t.Fatal(h, err)
	}
}

func TestNoncesAreUnique(t *testing.T) {
	ts := setup(t)
	a, _ := get(t, ts, "")
	b, _ := get(t, ts, "")
	if nonceOf(t, a) == nonceOf(t, b) {
		t.Fatal("two challenges in the same second shared a nonce")
	}
}
