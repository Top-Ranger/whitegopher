// Package dav implements the WebDAV, CalDAV and CardDAV HTTP handlers.
package dav

import (
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"

	"github.com/bitflipp/galdav/internal/auth"
	"github.com/bitflipp/galdav/internal/sched"
	"github.com/bitflipp/galdav/internal/store"
)

type Server struct {
	Store    *store.Store
	Auth     *auth.Authenticator
	BasePath string // e.g. "/dav", no trailing slash
	// Sched performs scheduling; if nil, a service without email delivery
	// is created on first use.
	Sched *sched.Service
	once  sync.Once
}

func (s *Server) sched() *sched.Service {
	s.once.Do(func() {
		if s.Sched == nil {
			s.Sched = sched.New(s.Store, s.BasePath, nil)
		}
	})
	return s.Sched
}

const maxBody = 10 << 20

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	redirect := func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, s.BasePath+"/", http.StatusMovedPermanently)
	}
	mux.HandleFunc("/.well-known/caldav", redirect)
	mux.HandleFunc("/.well-known/carddav", redirect)
	mux.Handle(s.BasePath+"/", s.Auth.Middleware(http.HandlerFunc(s.serveDAV)))
	return mux
}

// target is a parsed request path: <base>/<area>/<user>/<slug>/<uri>.
type target struct {
	n    int    // number of path segments present (0..4)
	area string // principals, calendars or addressbooks
	user string
	slug string
	uri  string
}

func (t target) kind() string {
	switch t.area {
	case "calendars":
		return store.KindCalendar
	case "addressbooks":
		return store.KindAddressBook
	}
	return ""
}

var slugRE = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9._-]{0,127}$`)

// ValidSlug reports whether s may name a collection.
func ValidSlug(s string) bool { return slugRE.MatchString(s) && !reservedSlug(s) }

// reservedSlug names the scheduling collections, which are not user-creatable.
func reservedSlug(s string) bool { return s == "inbox" || s == "outbox" }

func validURI(s string) bool {
	if s == "" || len(s) > 255 || s == "." || s == ".." {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// locate parses the path and enforces that users only reach their own tree.
// It writes the error response itself and reports ok=false on failure.
func (s *Server) locate(w http.ResponseWriter, r *http.Request, u store.User) (t target, ok bool) {
	rel := strings.Trim(strings.TrimPrefix(r.URL.Path, s.BasePath), "/")
	var parts []string
	if rel != "" {
		parts = strings.Split(rel, "/")
	}
	t.n = len(parts)
	if t.n > 4 {
		http.NotFound(w, r)
		return t, false
	}
	if t.n >= 1 {
		t.area = parts[0]
		switch t.area {
		case "principals", "calendars", "addressbooks":
		default:
			http.NotFound(w, r)
			return t, false
		}
	}
	if t.n >= 2 {
		t.user = parts[1]
		if t.user != u.Username {
			http.Error(w, "forbidden", http.StatusForbidden)
			return t, false
		}
	}
	if t.n >= 3 {
		if t.area == "principals" {
			http.NotFound(w, r)
			return t, false
		}
		t.slug = parts[2]
		if !validURI(t.slug) {
			http.NotFound(w, r)
			return t, false
		}
	}
	if t.n == 4 {
		t.uri = parts[3]
		if !validURI(t.uri) {
			http.NotFound(w, r)
			return t, false
		}
	}
	return t, true
}

func (s *Server) serveDAV(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r.Context())
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	if r.Method == http.MethodOptions {
		w.Header().Set("DAV", "1, 3, access-control, calendar-access, calendar-auto-schedule, addressbook, extended-mkcol")
		w.Header().Set("Allow", "OPTIONS, PROPFIND, PROPPATCH, REPORT, POST, MKCALENDAR, MKCOL, GET, HEAD, PUT, DELETE")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	t, ok := s.locate(w, r, u)
	if !ok {
		return
	}
	if t.n >= 3 && t.area == "calendars" && t.slug == "outbox" {
		s.outbox(w, r, u, t)
		return
	}
	switch r.Method {
	case "PROPFIND":
		s.propfind(w, r, u, t)
	case "REPORT":
		s.report(w, r, u, t)
	case http.MethodPost:
		s.postCollection(w, r, u, t)
	case "PROPPATCH":
		s.proppatch(w, r, u, t)
	case "MKCALENDAR", "MKCOL":
		s.mkcol(w, r, u, t)
	case http.MethodGet, http.MethodHead:
		s.get(w, r, u, t)
	case http.MethodPut:
		s.put(w, r, u, t)
	case http.MethodDelete:
		s.delete(w, r, u, t)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// lookupCollection resolves t's collection, writing 404 on absence.
func (s *Server) lookupCollection(w http.ResponseWriter, r *http.Request, u store.User, t target) (store.Collection, bool) {
	if t.kind() == store.KindCalendar && t.slug == store.SpecialInbox {
		if _, err := s.Store.Inbox(u.ID); err != nil { // created on first use
			s.fail(w, r, err)
			return store.Collection{}, false
		}
	}
	c, err := s.Store.ResolveCollection(u.ID, t.kind(), t.slug)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return c, false
	}
	if err != nil {
		s.fail(w, r, err)
		return c, false
	}
	return c, true
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
		return
	}
	slog.Error("dav", "method", r.Method, "path", r.URL.Path, "err", err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}

func (s *Server) propfind(w http.ResponseWriter, r *http.Request, u store.User, t target) {
	pf, err := parsePropfind(r.Body)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	depth := r.Header.Get("Depth")
	if depth == "infinity" {
		writeError(w, http.StatusForbidden, "<D:propfind-finite-depth/>")
		return
	}
	deep := depth == "1"

	var out []resource
	switch t.n {
	case 0:
		out = []resource{s.rootRes(u)}
		if deep {
			out = append(out, s.containerRes("principals"), s.containerRes("calendars"), s.containerRes("addressbooks"))
		}
	case 1:
		out = []resource{s.containerRes(t.area)}
	case 2:
		if t.area == "principals" {
			out = []resource{s.principalRes(u)}
			break
		}
		out = []resource{s.homeRes(t.area, u)}
		if deep {
			cs, err := s.Store.ListCollections(u.ID, t.kind())
			if err != nil {
				s.fail(w, r, err)
				return
			}
			mounts, err := s.Store.ListMounts(u.ID, t.kind())
			if err != nil {
				s.fail(w, r, err)
				return
			}
			cs = append(cs, mounts...)
			for _, c := range cs {
				seq, err := s.Store.SyncSeq(c.ID)
				if err != nil {
					s.fail(w, r, err)
					return
				}
				out = append(out, s.collectionRes(u, c, seq))
			}
			if t.area == "calendars" {
				inbox, err := s.Store.Inbox(u.ID)
				if err != nil {
					s.fail(w, r, err)
					return
				}
				seq, _ := s.Store.SyncSeq(inbox.ID)
				out = append(out, s.collectionRes(u, inbox, seq), s.outboxRes(u))
			}
		}
	case 3:
		c, ok := s.lookupCollection(w, r, u, t)
		if !ok {
			return
		}
		seq, err := s.Store.SyncSeq(c.ID)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		out = []resource{s.collectionRes(u, c, seq)}
		if deep {
			objs, err := s.Store.ListObjects(c.ID)
			if err != nil {
				s.fail(w, r, err)
				return
			}
			for _, o := range objs {
				out = append(out, s.objectRes(u, c, o))
			}
		}
	case 4:
		c, ok := s.lookupCollection(w, r, u, t)
		if !ok {
			return
		}
		objs, err := s.Store.ListObjects(c.ID)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		for _, o := range objs {
			if o.URI == t.uri {
				out = []resource{s.objectRes(u, c, o)}
			}
		}
		if out == nil {
			http.NotFound(w, r)
			return
		}
	}
	writeMultistatus(w, pf, out, "")
}
