package admin_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/bitflipp/whitegopher/internal/admin"
	"github.com/bitflipp/whitegopher/internal/auth"
	"github.com/bitflipp/whitegopher/internal/store"
	"github.com/bitflipp/whitegopher/internal/storetest"
)

type fakeMail struct {
	mu   sync.Mutex
	sent []struct{ to, subject, body string }
}

func (f *fakeMail) SendText(to, subject, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, struct{ to, subject, body string }{to, subject, body})
	return nil
}

func TestPasswordResetByEmail(t *testing.T) {
	st := storetest.New(t)
	a := &auth.Authenticator{Store: st, Realm: "R"}
	hash, _ := auth.HashPassword("old-password1")
	st.CreateUser(store.User{Username: "bob", Email: "Bob@Example.com", PasswordHash: hash})
	st.CreateUser(store.User{Username: "noemail", PasswordHash: hash})
	mail := &fakeMail{}

	build := func(m admin.TextMailer, base string) *env {
		ui, err := (&admin.Server{Store: st, Auth: a, DAVPath: "/dav", Mailer: m, BaseURL: base}).Handler()
		if err != nil {
			t.Fatal(err)
		}
		mux := http.NewServeMux()
		mux.Handle("/admin/", ui)
		ts := httptest.NewServer(mux)
		t.Cleanup(ts.Close)
		return &env{t: t, ts: ts, st: st, cli: newClient()}
	}

	form := func(e *env, path string, kv ...string) (*http.Response, string) {
		v := url.Values{}
		for i := 0; i+1 < len(kv); i += 2 {
			v.Set(kv[i], kv[i+1])
		}
		return e.do(newClient(), "POST", path, v)
	}

	// Off without mail or a public URL: no routes, no link on the sign-in page.
	off := build(nil, "")
	resp, b := off.do(off.cli, "GET", "/admin/forgot", nil)
	status(t, resp, 404, b)
	resp, b = off.do(off.cli, "GET", "/admin/login", nil)
	if strings.Contains(b, "/admin/forgot") {
		t.Fatal("forgot link shown while disabled")
	}
	e := build(mail, "https://cal.example.com/")
	resp, b = e.do(e.cli, "GET", "/admin/login", nil)
	contains(t, b, "/admin/forgot")

	// Unknown names and accounts without email get the same answer and no mail.
	for _, ident := range []string{"nobody", "noemail"} {
		resp, b = form(e, "/admin/forgot", "identifier", ident)
		status(t, resp, 200, b)
		contains(t, b, "reset link is on its way")
	}
	if len(mail.sent) != 0 {
		t.Fatalf("mail sent: %+v", mail.sent)
	}

	// Lookup by email works case-insensitively and the link points at base_url.
	resp, b = form(e, "/admin/forgot", "identifier", "bob@example.com")
	status(t, resp, 200, b)
	if len(mail.sent) != 1 || mail.sent[0].to != "bob@example.com" {
		t.Fatalf("mail: %+v", mail.sent)
	}
	m := regexp.MustCompile(`https://cal\.example\.com/admin/reset\?token=([0-9a-f]{64})`).FindStringSubmatch(mail.sent[0].body)
	if m == nil {
		t.Fatalf("no link in %q", mail.sent[0].body)
	}
	token := m[1]

	// Bad tokens are refused; the good one shows the form.
	resp, b = e.do(e.cli, "GET", "/admin/reset?token=deadbeef", nil)
	status(t, resp, 400, b)
	resp, b = e.do(e.cli, "GET", "/admin/reset?token="+token, nil)
	status(t, resp, 200, b)
	contains(t, b, "Choose a new password")

	// Validation errors keep the link usable.
	resp, b = form(e, "/admin/reset", "token", token, "password", "short", "password2", "short")
	status(t, resp, 422, b)
	resp, b = form(e, "/admin/reset", "token", token, "password", "new-password1", "password2", "different1")
	status(t, resp, 422, b)
	if _, ok := a.Verify("bob", "old-password1"); !ok {
		t.Fatal("password changed by a failed reset")
	}

	// A live session dies with the reset; the link works once.
	bc := newClient()
	resp, b = e.do(bc, "POST", "/admin/login", url.Values{"username": {"bob"}, "password": {"old-password1"}})
	status(t, resp, 303, b)
	resp, b = form(e, "/admin/reset", "token", token, "password", "new-password1", "password2", "new-password1")
	status(t, resp, 303, b)
	if _, ok := a.Verify("bob", "new-password1"); !ok {
		t.Fatal("new password does not work")
	}
	if _, ok := a.Verify("bob", "old-password1"); ok {
		t.Fatal("old password still works")
	}
	resp, b = e.do(bc, "GET", "/admin/", nil)
	if resp.Header.Get("Location") != "/admin/login" {
		t.Fatal("session survived the reset")
	}
	resp, b = form(e, "/admin/reset", "token", token, "password", "third-password1", "password2", "third-password1")
	status(t, resp, 400, b)

	// A new request replaces the old link; per-account requests are capped.
	form(e, "/admin/forgot", "identifier", "bob")
	form(e, "/admin/forgot", "identifier", "bob")
	first := regexp.MustCompile(`token=([0-9a-f]{64})`).FindStringSubmatch(mail.sent[1].body)[1]
	resp, b = e.do(e.cli, "GET", "/admin/reset?token="+first, nil)
	status(t, resp, 400, b)
	form(e, "/admin/forgot", "identifier", "bob") // 4th for the account: no mail
	if len(mail.sent) != 4-1 {
		t.Fatalf("sent %d mails, want 3", len(mail.sent))
	}
}
