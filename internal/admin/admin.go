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
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bitflipp/galdav/internal/auth"
	"github.com/bitflipp/galdav/internal/store"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static/*
var staticFS embed.FS

const (
	cookieName      = "galdav_session"
	sessionLifetime = 24 * time.Hour
	minPasswordLen  = 8
)

type Server struct {
	Store *store.Store
	Auth  *auth.Authenticator
	// DAVPath is the URL prefix of the DAV tree (shown to users).
	DAVPath string

	pages   map[string]*template.Template
	limiter *limiter
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
}

var flashes = map[string]string{
	"saved":    "Changes saved.",
	"created":  "Created.",
	"deleted":  "Deleted.",
	"password": "Password changed.",
}

func (s *Server) init() error {
	s.pages = map[string]*template.Template{}
	s.limiter = newLimiter(10, 15*time.Minute)
	entries, err := fs.ReadDir(templateFS, "templates")
	if err != nil {
		return err
	}
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), ".html")
		if name == "base" {
			continue
		}
		t, err := template.ParseFS(templateFS, "templates/base.html", "templates/"+e.Name())
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
	mux.HandleFunc("GET /admin/setup", s.setupForm)
	mux.HandleFunc("POST /admin/setup", s.setup)
	mux.HandleFunc("POST /admin/logout", s.authed(s.logout))

	mux.HandleFunc("GET /admin/{$}", s.authed(s.dashboard))
	mux.HandleFunc("POST /admin/collections", s.authed(s.createCollection))
	mux.HandleFunc("GET /admin/collections/{id}", s.authed(s.collectionForm))
	mux.HandleFunc("POST /admin/collections/{id}", s.authed(s.updateCollection))
	mux.HandleFunc("POST /admin/collections/{id}/delete", s.authed(s.deleteCollection))
	mux.HandleFunc("POST /admin/collections/{id}/shares", s.authed(s.addShare))
	mux.HandleFunc("POST /admin/collections/{id}/shares/{uid}/remove", s.authed(s.removeShare))
	mux.HandleFunc("GET /admin/shared/{id}", s.authed(s.mountForm))
	mux.HandleFunc("POST /admin/shared/{id}", s.authed(s.updateMount))
	mux.HandleFunc("POST /admin/shared/{id}/leave", s.authed(s.leaveMount))
	mux.HandleFunc("GET /admin/account", s.authed(s.accountForm))
	mux.HandleFunc("POST /admin/account/password", s.authed(s.changeOwnPassword))
	mux.HandleFunc("POST /admin/account/default-calendar", s.authed(s.setDefaultCalendar))

	mux.HandleFunc("GET /admin/users", s.adminOnly(s.listUsers))
	mux.HandleFunc("GET /admin/users/new", s.adminOnly(s.newUserForm))
	mux.HandleFunc("POST /admin/users", s.adminOnly(s.createUser))
	mux.HandleFunc("GET /admin/users/{id}", s.adminOnly(s.userForm))
	mux.HandleFunc("POST /admin/users/{id}", s.adminOnly(s.updateUser))
	mux.HandleFunc("POST /admin/users/{id}/password", s.adminOnly(s.resetPassword))
	mux.HandleFunc("POST /admin/users/{id}/delete", s.adminOnly(s.deleteUser))

	return securityHeaders(sameOrigin(mux)), nil
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; img-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
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

func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func (s *Server) startSession(w http.ResponseWriter, r *http.Request, u store.User) error {
	tok := randomHex(32)
	exp := time.Now().Add(sessionLifetime)
	if err := s.Store.CreateSession(store.Session{TokenHash: hashToken(tok), UserID: u.ID, CSRF: randomHex(16), ExpiresAt: exp}); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: tok, Path: "/admin/", Expires: exp,
		HttpOnly: true, Secure: isHTTPS(r), SameSite: http.SameSiteLaxMode,
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

func (s *Server) davURL(r *http.Request) string {
	scheme := "http"
	if isHTTPS(r) {
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

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func isNotFound(err error) bool { return errors.Is(err, store.ErrNotFound) }
