// Package admin implements the web UI for managing users, calendars and
// address books.
package admin

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"errors"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/bitflipp/whitegopher/internal/auth"
	"github.com/bitflipp/whitegopher/internal/netx"
	"github.com/bitflipp/whitegopher/internal/sharing"
	"github.com/bitflipp/whitegopher/internal/store"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static/*
var staticFS embed.FS

const (
	cookieName      = "whitegopher_session"
	sessionLifetime = 24 * time.Hour
	minPasswordLen  = 8
)

type Server struct {
	Store *store.Store
	Auth  *auth.Authenticator
	// DAVPath is the URL prefix of the DAV tree (shown to users).
	DAVPath string
	// TrustedProxies are the peers whose X-Forwarded-For/-Proto headers are
	// believed; for anyone else those headers are ignored.
	TrustedProxies []netip.Prefix
	// Limiter throttles failed logins per client address. Defaults to 10
	// failures per 15 minutes.
	Limiter *netx.Limiter
	// Mailer and BaseURL (the public address of this server, e.g.
	// "https://cal.example.com") enable password reset by email; without both
	// the feature is off.
	Mailer  TextMailer
	BaseURL string
	// Sharing manages share invitations. Defaults to accepting shares at once.
	Sharing *sharing.Service

	pages map[string]*template.Template
	// codeLimiter slows guessing of second-factor codes once the password is
	// known, per account.
	codeLimiter *netx.Limiter
	// resetLimiter bounds reset requests per client address, userResetLimiter
	// per account (so nobody's inbox can be flooded).
	resetLimiter, userResetLimiter *netx.Limiter
}

type session struct {
	User      store.User
	CSRF      string
	tokenHash string
}

// page is the data passed to every template.
type page struct {
	Title   string
	Nav     string
	User    store.User
	CSRF    string
	Flash   string
	Error   string
	DAVURL  string
	Data    any
	IsSetup bool
	// CanReset offers "forgot password" on the sign-in page.
	CanReset bool
}

var flashes = map[string]string{
	"saved":    "Changes saved.",
	"created":  "Created.",
	"deleted":  "Deleted.",
	"password": "Password changed.",
	"reset":    "Password changed. Sign in with the new one.",
}

var funcs = template.FuncMap{
	// quotaMiB renders a byte quota as whole MiB.
	"quotaMiB": func(b *int64) int64 { return *b >> 20 },
}

func (s *Server) init() error {
	if s.Limiter == nil {
		s.Limiter = netx.NewLimiter(10, 15*time.Minute)
	}
	if s.Sharing == nil {
		s.Sharing = &sharing.Service{Store: s.Store, BasePath: s.DAVPath}
	}
	s.codeLimiter = netx.NewLimiter(5, 15*time.Minute)
	s.resetLimiter = netx.NewLimiter(10, time.Hour)
	s.userResetLimiter = netx.NewLimiter(3, time.Hour)
	s.pages = map[string]*template.Template{}
	entries, err := fs.ReadDir(templateFS, "templates")
	if err != nil {
		return err
	}
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), ".html")
		if name == "base" {
			continue
		}
		t, err := template.New("").Funcs(funcs).ParseFS(templateFS, "templates/base.html", "templates/"+e.Name())
		if err != nil {
			return err
		}
		s.pages[name] = t
	}
	return nil
}

// Handler returns the admin UI, to be mounted at /admin/.
func (s *Server) Handler() (http.Handler, error) {
	if err := s.init(); err != nil {
		return nil, err
	}
	static, err := fs.Sub(staticFS, "static")
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.Handle("GET /admin/static/", http.StripPrefix("/admin/static/", http.FileServerFS(static)))

	mux.HandleFunc("GET /admin/login", s.loginForm)
	mux.HandleFunc("POST /admin/login", s.login)
	mux.HandleFunc("GET /admin/forgot", s.forgotForm)
	mux.HandleFunc("POST /admin/forgot", s.forgot)
	mux.HandleFunc("GET /admin/reset", s.resetForm)
	mux.HandleFunc("POST /admin/reset", s.reset)
	mux.HandleFunc("GET /admin/setup", s.setupForm)
	mux.HandleFunc("POST /admin/setup", s.setup)
	mux.HandleFunc("POST /admin/logout", s.authed(s.logout))

	mux.HandleFunc("GET /admin/{$}", s.authed(s.dashboard))
	mux.HandleFunc("POST /admin/collections", s.authed(s.createCollection))
	mux.HandleFunc("GET /admin/collections/{id}", s.authed(s.collectionForm))
	mux.HandleFunc("POST /admin/collections/{id}", s.authed(s.updateCollection))
	mux.HandleFunc("POST /admin/collections/{id}/delete", s.authed(s.deleteCollection))
	mux.HandleFunc("POST /admin/collections/{id}/public", s.authed(s.setPublicLink))
	mux.HandleFunc("POST /admin/collections/{id}/public/delete", s.authed(s.removePublicLink))
	mux.HandleFunc("POST /admin/collections/{id}/shares", s.authed(s.addShare))
	mux.HandleFunc("POST /admin/collections/{id}/shares/{uid}/remove", s.authed(s.removeShare))
	mux.HandleFunc("POST /admin/invites/{id}/accept", s.authed(s.answerInvite(true)))
	mux.HandleFunc("POST /admin/invites/{id}/decline", s.authed(s.answerInvite(false)))
	mux.HandleFunc("GET /admin/shared/{id}", s.authed(s.mountForm))
	mux.HandleFunc("POST /admin/shared/{id}", s.authed(s.updateMount))
	mux.HandleFunc("POST /admin/shared/{id}/leave", s.authed(s.leaveMount))
	mux.HandleFunc("GET /admin/account", s.authed(s.accountForm))
	mux.HandleFunc("POST /admin/account/password", s.authed(s.changeOwnPassword))
	mux.HandleFunc("POST /admin/account/default-calendar", s.authed(s.setDefaultCalendar))
	mux.HandleFunc("POST /admin/account/privacy", s.authed(s.setPrivacy))
	mux.HandleFunc("POST /admin/account/2fa/start", s.authed(s.twoFactorStart))
	mux.HandleFunc("POST /admin/account/2fa/enable", s.authed(s.twoFactorEnable))
	mux.HandleFunc("POST /admin/account/2fa/disable", s.authed(s.twoFactorDisable))
	mux.HandleFunc("POST /admin/account/app-passwords", s.authed(s.createAppPassword))
	mux.HandleFunc("POST /admin/account/app-passwords/{id}/delete", s.authed(s.deleteAppPassword))

	mux.HandleFunc("GET /admin/users", s.adminOnly(s.listUsers))
	mux.HandleFunc("GET /admin/users/new", s.adminOnly(s.newUserForm))
	mux.HandleFunc("POST /admin/users", s.adminOnly(s.createUser))
	mux.HandleFunc("GET /admin/users/{id}", s.adminOnly(s.userForm))
	mux.HandleFunc("POST /admin/users/{id}", s.adminOnly(s.updateUser))
	mux.HandleFunc("POST /admin/users/{id}/password", s.adminOnly(s.resetPassword))
	mux.HandleFunc("POST /admin/users/{id}/2fa/disable", s.adminOnly(s.adminDisableTwoFactor))
	mux.HandleFunc("POST /admin/users/{id}/delete", s.adminOnly(s.deleteUser))

	return securityHeaders(sameOrigin(mux)), nil
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// sameOrigin rejects cross-origin state-changing requests. Browsers always
// send Origin on cross-origin POSTs; requests without it (non-browsers) pass.
func sameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			origin := r.Header.Get("Origin")
			if origin == "" {
				origin = r.Header.Get("Referer")
			}
			if origin != "" {
				u, err := url.Parse(origin)
				if err != nil || u.Host != r.Host {
					http.Error(w, "cross-origin request rejected", http.StatusForbidden)
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

// --- sessions -------------------------------------------------------------

func hashToken(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func (s *Server) startSession(w http.ResponseWriter, r *http.Request, u store.User) error {
	tok := randomHex(32)
	exp := time.Now().Add(sessionLifetime)
	if err := s.Store.CreateSession(store.Session{TokenHash: hashToken(tok), UserID: u.ID, CSRF: randomHex(16), ExpiresAt: exp}); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: tok, Path: "/admin/", Expires: exp,
		HttpOnly: true, Secure: s.proxies().IsHTTPS(r), SameSite: http.SameSiteLaxMode,
	})
	return nil
}

func (s *Server) currentSession(r *http.Request) (*session, bool) {
	c, err := r.Cookie(cookieName)
	if err != nil || c.Value == "" {
		return nil, false
	}
	h := hashToken(c.Value)
	sess, u, err := s.Store.SessionUser(h)
	if err != nil {
		return nil, false
	}
	return &session{User: u, CSRF: sess.CSRF, tokenHash: h}, true
}

type handler func(http.ResponseWriter, *http.Request, *session)

// authed requires a session and, for POSTs, a valid CSRF token.
func (s *Server) authed(h handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, ok := s.currentSession(r)
		if !ok {
			if n, _ := s.Store.CountUsers(); n == 0 {
				http.Redirect(w, r, "/admin/setup", http.StatusSeeOther)
				return
			}
			http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
			return
		}
		if r.Method == http.MethodPost {
			if err := r.ParseForm(); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			if subtle.ConstantTimeCompare([]byte(r.PostFormValue("csrf")), []byte(sess.CSRF)) != 1 {
				http.Error(w, "invalid CSRF token", http.StatusForbidden)
				return
			}
		}
		h(w, r, sess)
	}
}

func (s *Server) adminOnly(h handler) http.HandlerFunc {
	return s.authed(func(w http.ResponseWriter, r *http.Request, sess *session) {
		if !sess.User.IsAdmin {
			http.Error(w, "administrators only", http.StatusForbidden)
			return
		}
		h(w, r, sess)
	})
}

// --- rendering ------------------------------------------------------------

// origin is the public scheme://host of this server: the configured BaseURL,
// else what the request came in on.
func (s *Server) origin(r *http.Request) string {
	if s.BaseURL != "" {
		return strings.TrimRight(s.BaseURL, "/")
	}
	scheme := "http"
	if s.proxies().IsHTTPS(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func (s *Server) davURL(r *http.Request) string {
	scheme := "http"
	if s.proxies().IsHTTPS(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host + s.DAVPath + "/"
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, name string, status int, p page) {
	if sess, ok := s.currentSession(r); ok && p.User.ID == 0 {
		p.User, p.CSRF = sess.User, sess.CSRF
	}
	if p.Flash == "" {
		p.Flash = flashes[r.URL.Query().Get("ok")]
	}
	p.DAVURL = s.davURL(r)
	p.CanReset = s.resetEnabled()
	t, ok := s.pages[name]
	if !ok {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := t.ExecuteTemplate(w, "base", p); err != nil {
		slog.Error("render", "page", name, "err", err)
	}
}

func (s *Server) fail(w http.ResponseWriter, err error) {
	slog.Error("admin", "err", err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}

func notFound(w http.ResponseWriter) { http.Error(w, "not found", http.StatusNotFound) }

func (s *Server) clientIP(r *http.Request) string { return s.proxies().ClientIP(r) }

func (s *Server) proxies() netx.Proxies { return netx.Proxies(s.TrustedProxies) }

func isNotFound(err error) bool { return errors.Is(err, store.ErrNotFound) }
