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

func TestChallengeIsBasicOnly(t *testing.T) {
	ts := setup(t)
	resp, _ := get(t, ts, "")
	h := resp.Header.Values("WWW-Authenticate")
	if resp.StatusCode != 401 || len(h) != 1 || !strings.HasPrefix(h[0], "Basic ") {
		t.Fatalf("challenge: %d %v", resp.StatusCode, h)
	}
	if resp, _ := get(t, ts, `Digest username="alice", realm="R"`); resp.StatusCode != 401 {
		t.Fatalf("digest accepted: %d", resp.StatusCode)
	}
}

func TestImportedHA1IsUpgradedAndCleared(t *testing.T) {
	st := storetest.New(t)
	st.CreateUser(store.User{Username: "alice", DigestHA1: auth.HA1("alice", "R", "secret")})
	a := &auth.Authenticator{Store: st, Realm: "R"}
	if _, ok := a.Verify("alice", "secret"); !ok {
		t.Fatal("imported HA1 login failed")
	}
	u, _ := st.UserByName("alice")
	if u.DigestHA1 != "" || !strings.HasPrefix(u.PasswordHash, "$argon2id$") {
		t.Fatalf("not upgraded: %+v", u)
	}
	if _, ok := a.Verify("alice", "secret"); !ok {
		t.Fatal("argon2 login failed")
	}
}
