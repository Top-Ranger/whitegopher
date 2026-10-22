package auth_test

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/bitflipp/galdav/internal/auth"
	"github.com/bitflipp/galdav/internal/netx"
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

func TestDAVLoginLimitAndLogging(t *testing.T) {
	var logs strings.Builder
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })

	st := storetest.New(t)
	hash, _ := auth.HashPassword("secret")
	st.CreateUser(store.User{Username: "alice", PasswordHash: hash})
	a := &auth.Authenticator{
		Store: st, Realm: "R",
		Proxies: netx.Proxies{netip.MustParsePrefix("127.0.0.0/8")},
		Limiter: netx.NewLimiter(3, time.Minute),
	}
	ts := httptest.NewServer(a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})))
	t.Cleanup(ts.Close)
	try := func(pass, xff string) int {
		req, _ := http.NewRequest("GET", ts.URL, nil)
		req.SetBasicAuth("alice", pass)
		req.Header.Set("X-Forwarded-For", xff)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	for i := 0; i < 3; i++ {
		if c := try("wrong", "203.0.113.5"); c != 401 {
			t.Fatalf("attempt %d: %d", i, c)
		}
	}
	if c := try("secret", "203.0.113.5"); c != 429 {
		t.Fatalf("not limited: %d", c)
	}
	if c := try("secret", "203.0.113.6"); c != 200 {
		t.Fatalf("other client blocked: %d", c)
	}
	out := logs.String()
	if !strings.Contains(out, "login failed") || !strings.Contains(out, "ip=203.0.113.5") || !strings.Contains(out, "user=alice") {
		t.Fatalf("failed login not logged with the forwarded address:\n%s", out)
	}
}

func TestAppPasswords(t *testing.T) {
	st := storetest.New(t)
	hash, _ := auth.HashPassword("secret")
	aid, _ := st.CreateUser(store.User{Username: "alice", PasswordHash: hash})
	bid, _ := st.CreateUser(store.User{Username: "bob", PasswordHash: hash})
	a := &auth.Authenticator{Store: st, Realm: "R"}

	token, h, err := auth.NewAppPassword()
	if err != nil || !strings.HasPrefix(token, auth.AppPasswordPrefix) || len(token) != len("gdp-")+20+3 {
		t.Fatalf("token %q: %v", token, err)
	}
	if h != auth.HashAppPassword(token) || strings.Contains(h, token) {
		t.Fatal("hash mismatch")
	}
	other, _, _ := auth.NewAppPassword()
	if other == token {
		t.Fatal("tokens repeat")
	}
	id, err := st.CreateAppPassword(aid, "phone", h)
	if err != nil {
		t.Fatal(err)
	}

	if u, ok := a.VerifyDAV("alice", token); !ok || u.Username != "alice" {
		t.Fatal("app password rejected")
	}
	if _, ok := a.VerifyDAV("alice", "secret"); !ok {
		t.Fatal("account password rejected on DAV")
	}
	if _, ok := a.VerifyDAV("bob", token); ok {
		t.Fatal("alice's app password works for bob")
	}
	if _, ok := a.VerifyDAV("alice", other); ok {
		t.Fatal("unknown app password accepted")
	}
	// An app password is not an account password (admin UI sign-in).
	if _, ok := a.Verify("alice", token); ok {
		t.Fatal("app password accepted as account password")
	}
	list, _ := st.ListAppPasswords(aid)
	if len(list) != 1 || list[0].Name != "phone" || list[0].LastUsedAt == nil {
		t.Fatalf("%+v", list)
	}
	if err := st.DeleteAppPassword(bid, id); err == nil {
		t.Fatal("bob revoked alice's app password")
	}
	if err := st.DeleteAppPassword(aid, id); err != nil {
		t.Fatal(err)
	}
	if _, ok := a.VerifyDAV("alice", token); ok {
		t.Fatal("revoked app password still works")
	}
}

func TestTOTPVector(t *testing.T) {
	// RFC 6238 appendix B: secret "12345678901234567890", T=59 -> 94287082
	// (8 digits), i.e. 287082 in 6 digits.
	secret := "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	at := time.Unix(59, 0)
	step, ok := auth.ValidTOTP(secret, "287082", at, 0)
	if !ok || step != 1 {
		t.Fatalf("step=%d ok=%v", step, ok)
	}
	if _, ok := auth.ValidTOTP(secret, "287082", at, step); ok {
		t.Fatal("same step accepted twice")
	}
	if _, ok := auth.ValidTOTP(secret, "287082", at.Add(30*time.Second), 0); !ok {
		t.Fatal("one step of drift should pass")
	}
	if _, ok := auth.ValidTOTP(secret, "287082", at.Add(120*time.Second), 0); ok {
		t.Fatal("stale code accepted")
	}
	if _, ok := auth.ValidTOTP(secret, "28708", at, 0); ok {
		t.Fatal("short code accepted")
	}
	s, err := auth.NewTOTPSecret()
	if err != nil || len(s) != 32 {
		t.Fatal(s, err)
	}
}

func TestSecondFactor(t *testing.T) {
	st := storetest.New(t)
	hash, _ := auth.HashPassword("secret")
	id, _ := st.CreateUser(store.User{Username: "alice", PasswordHash: hash})
	a := &auth.Authenticator{Store: st, Realm: "R"}

	secret, _ := auth.NewTOTPSecret()
	if err := st.BeginTOTP(id, secret); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := a.VerifyLogin("alice", "secret", ""); !ok {
		t.Fatal("not enabled yet: password alone should do")
	}
	codes, hashes, _ := auth.NewRecoveryCodes(3)
	if err := st.EnableTOTP(id, 0, hashes); err != nil {
		t.Fatal(err)
	}

	if _, ok, pw := a.VerifyLogin("alice", "secret", ""); ok || !pw {
		t.Fatalf("missing code: ok=%v pw=%v", ok, pw)
	}
	if _, ok, pw := a.VerifyLogin("alice", "wrong", "123456"); ok || pw {
		t.Fatal("wrong password accepted")
	}
	code, _ := auth.TOTPCode(secret, time.Now())
	if _, ok, _ := a.VerifyLogin("alice", "secret", code); !ok {
		t.Fatal("valid code rejected")
	}
	if _, ok, _ := a.VerifyLogin("alice", "secret", code); ok {
		t.Fatal("code replayed")
	}
	if _, ok, _ := a.VerifyLogin("alice", "secret", strings.ToUpper(codes[0])); !ok {
		t.Fatal("recovery code rejected")
	}
	if _, ok, _ := a.VerifyLogin("alice", "secret", codes[0]); ok {
		t.Fatal("recovery code reused")
	}

	// DAV: account password no longer works; app passwords do.
	if _, ok := a.VerifyDAV("alice", "secret"); ok {
		t.Fatal("account password accepted on DAV with two-factor on")
	}
	token, h, _ := auth.NewAppPassword()
	st.CreateAppPassword(id, "phone", h)
	if _, ok := a.VerifyDAV("alice", token); !ok {
		t.Fatal("app password rejected")
	}
	if err := st.DisableTOTP(id); err != nil {
		t.Fatal(err)
	}
	if _, ok := a.VerifyDAV("alice", "secret"); !ok {
		t.Fatal("password rejected after disabling")
	}
}
