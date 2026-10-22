package admin_test

import (
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/bitflipp/galdav/internal/auth"
	"github.com/bitflipp/galdav/internal/store"
)

func TestTwoFactor(t *testing.T) {
	e := newEnv(t)
	a := &auth.Authenticator{Store: e.st, Realm: "R"}
	hash, _ := auth.HashPassword("hunter2hunter2")
	e.st.CreateUser(store.User{Username: "root", PasswordHash: hash, IsAdmin: true})
	bobID, _ := e.st.CreateUser(store.User{Username: "bob", PasswordHash: hash})
	login := func(name, code string) (*http.Client, *http.Response, string) {
		c := newClient()
		resp, b := e.do(c, "POST", "/admin/login", url.Values{"username": {name}, "password": {"hunter2hunter2"}, "code": {code}})
		return c, resp, b
	}
	bc, resp, b := login("bob", "")
	status(t, resp, 303, b)

	// Enrol: nothing is enforced until a code confirms the secret.
	resp, b = e.post(bc, "/admin/account/2fa/enable", "code", "123456")
	status(t, resp, 422, b)
	resp, b = e.post(bc, "/admin/account/2fa/start")
	status(t, resp, 303, b)
	resp, b = e.do(bc, "GET", "/admin/account", nil)
	status(t, resp, 200, b)
	contains(t, b, "otpauth://totp/galdav:bob")
	bob, _ := e.st.UserByName("bob")
	if bob.TOTPSecret == "" || bob.TOTPEnabled {
		t.Fatalf("%+v", bob)
	}
	contains(t, b, bob.TOTPSecret)
	if _, resp, b := login("bob", ""); resp.StatusCode != 303 {
		t.Fatalf("2FA enforced before confirmation: %d %s", resp.StatusCode, b)
	}
	resp, b = e.post(bc, "/admin/account/2fa/enable", "code", "000000")
	status(t, resp, 422, b)
	code, _ := auth.TOTPCode(bob.TOTPSecret, time.Now())
	resp, b = e.post(bc, "/admin/account/2fa/enable", "code", code)
	status(t, resp, 200, b)
	var codes []string
	for _, m := range regexp.MustCompile(`<code>([a-z2-7]{5}-[a-z2-7]{5})</code>`).FindAllStringSubmatch(b, -1) {
		codes = append(codes, m[1])
	}
	if len(codes) != 8 {
		t.Fatalf("recovery codes: %v", codes)
	}

	// Sign-in now needs a code; a used step cannot be replayed.
	_, resp, b = login("bob", "")
	status(t, resp, 401, b)
	_, resp, b = login("bob", code)
	status(t, resp, 401, b) // same step as enrolment
	next, _ := auth.TOTPCode(bob.TOTPSecret, time.Now().Add(30*time.Second))
	_, resp, b = login("bob", next)
	status(t, resp, 303, b)
	_, resp, b = login("bob", codes[0])
	status(t, resp, 303, b)
	_, resp, b = login("bob", codes[0])
	status(t, resp, 401, b)

	// DAV: the account password is out, app passwords are in.
	if _, ok := a.VerifyDAV("bob", "hunter2hunter2"); ok {
		t.Fatal("account password works on DAV with two-factor on")
	}
	_, ahash, _ := auth.NewAppPassword()
	e.st.CreateAppPassword(bobID, "phone", ahash)

	// Guessing codes with the right password locks the account, from any address.
	// Three wrong codes already counted above (the bad enrolment code, the
	// replayed code and the reused recovery code), so two more hit the limit of 5.
	for i := 0; i < 2; i++ {
		_, resp, _ = login("bob", "999999")
		if resp.StatusCode != 401 {
			t.Fatalf("attempt %d: %d", i, resp.StatusCode)
		}
	}
	_, resp, b = login("bob", codes[1])
	status(t, resp, 429, b)

	// Disabling needs the password and a code; an administrator can force it.
	c, resp, b := login("root", "")
	status(t, resp, 303, b)
	resp, b = e.post(c, "/admin/users/"+strconv.FormatInt(bobID, 10)+"/2fa/disable")
	status(t, resp, 303, b)
	if u, _ := e.st.UserByName("bob"); u.TOTPEnabled || u.TOTPSecret != "" {
		t.Fatalf("not disabled: %+v", u)
	}
	if n, _ := e.st.CountRecoveryCodes(bobID); n != 0 {
		t.Fatalf("recovery codes left: %d", n)
	}
	if _, ok := a.VerifyDAV("bob", "hunter2hunter2"); !ok {
		t.Fatal("account password rejected after disabling")
	}

	// Self-service: turning it off needs the password and a code.
	e.st.CreateUser(store.User{Username: "carol", PasswordHash: hash})
	cc, resp, b := login("carol", "")
	status(t, resp, 303, b)
	e.post(cc, "/admin/account/2fa/start")
	carol, _ := e.st.UserByName("carol")
	code, _ = auth.TOTPCode(carol.TOTPSecret, time.Now())
	resp, b = e.post(cc, "/admin/account/2fa/enable", "code", code)
	status(t, resp, 200, b)
	next, _ = auth.TOTPCode(carol.TOTPSecret, time.Now().Add(30*time.Second))
	resp, b = e.post(cc, "/admin/account/2fa/disable", "password", "wrong", "code", next)
	status(t, resp, 422, b)
	resp, b = e.post(cc, "/admin/account/2fa/disable", "password", "hunter2hunter2", "code", next)
	status(t, resp, 303, b)
	if u, _ := e.st.UserByName("carol"); u.TOTPEnabled {
		t.Fatal("carol still has two-factor on")
	}
}
