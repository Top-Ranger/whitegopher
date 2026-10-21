package dav

import (
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/bitflipp/galdav/internal/index"
	"github.com/bitflipp/galdav/internal/store"
)

// etagMatcher implements If-Match list semantics (strong comparison).
func etagMatcher(header string) func(string) bool {
	return func(etag string) bool {
		for _, v := range strings.Split(header, ",") {
			v = strings.TrimSpace(v)
			if v == "*" {
				return true
			}
			if v == `"`+etag+`"` {
				return true
			}
		}
		return false
	}
}

func (s *Server) mkcol(w http.ResponseWriter, r *http.Request, u store.User, t target) {
	kind := t.kind()
	if t.n != 3 || kind == "" {
		http.Error(w, "collections can only be created directly in a home set", http.StatusForbidden)
		return
	}
	if r.Method == "MKCALENDAR" && kind != store.KindCalendar {
		http.Error(w, "MKCALENDAR only creates calendars", http.StatusForbidden)
		return
	}
	if !slugRE.MatchString(t.slug) {
		http.Error(w, "invalid collection name", http.StatusForbidden)
		return
	}
	if _, err := s.Store.GetCollection(u.ID, kind, t.slug); err == nil {
		http.Error(w, "already exists", http.StatusMethodNotAllowed)
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		s.fail(w, r, err)
		return
	}
	pu, err := parsePropUpdate(r.Body)
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			s.fail(w, r, err)
			return
		}
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	c := store.Collection{OwnerID: u.ID, Kind: kind, Slug: t.slug}
	typed := r.Method == "MKCALENDAR" // MKCOL must state the resource type
	for _, set := range pu.Set {
		for _, it := range set.Prop.Items {
			switch it.XMLName {
			case dav("resourcetype"):
				for _, ch := range it.Children {
					typed = typed || (kind == store.KindCalendar && ch.XMLName == cal("calendar")) ||
						(kind == store.KindAddressBook && ch.XMLName == card("addressbook"))
				}
			case cal("supported-calendar-component-set"):
				if kind != store.KindCalendar {
					continue
				}
				var names []string
				for _, ch := range it.Children {
					switch n := strings.ToUpper(ch.Name); n {
					case "VEVENT", "VTODO", "VJOURNAL":
						names = append(names, n)
					}
				}
				c.Components = strings.Join(names, ",")
			default:
				applyProp(&c, it, false) // unknown properties are ignored on creation
			}
		}
	}
	if !typed {
		writeError(w, http.StatusForbidden, "<D:valid-resourcetype/>")
		return
	}
	if kind == store.KindAddressBook {
		c.Components = ""
	}
	if _, err := s.Store.CreateCollection(c); errors.Is(err, store.ErrExists) {
		http.Error(w, "already exists", http.StatusMethodNotAllowed)
		return
	} else if err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func (s *Server) proppatch(w http.ResponseWriter, r *http.Request, u store.User, t target) {
	if t.n != 3 {
		http.Error(w, "properties of this resource are read-only", http.StatusForbidden)
		return
	}
	c, ok := s.lookupCollection(w, r, u, t)
	if !ok {
		return
	}
	pu, err := parsePropUpdate(r.Body)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	type change struct {
		name   xml.Name
		status int
	}
	var changes []change
	updated, failed := c, false
	apply := func(items []propItem, remove bool) {
		for _, it := range items {
			known, ok := applyProp(&updated, it, remove)
			st := http.StatusOK
			if !known {
				st = http.StatusForbidden
			} else if !ok {
				st = http.StatusConflict
			}
			failed = failed || st != http.StatusOK
			changes = append(changes, change{it.XMLName, st})
		}
	}
	for _, x := range pu.Set {
		apply(x.Prop.Items, false)
	}
	for _, x := range pu.Remove {
		apply(x.Prop.Items, true)
	}
	if failed {
		// All-or-nothing: successful ones become 424 Failed Dependency.
		for i := range changes {
			if changes[i].status == http.StatusOK {
				changes[i].status = http.StatusFailedDependency
			}
		}
	} else if err := s.Store.UpdateCollection(updated); err != nil {
		s.fail(w, r, err)
		return
	}

	byStatus := map[int][]xml.Name{}
	var order []int
	for _, ch := range changes {
		if _, seen := byStatus[ch.status]; !seen {
			order = append(order, ch.status)
		}
		byStatus[ch.status] = append(byStatus[ch.status], ch.name)
	}
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?>` + "\n<D:multistatus" + nsDecl + "><D:response>")
	b.WriteString(hrefXML(s.collHref(areaOf(c.Kind), u.Username, c.Slug)))
	for _, st := range order {
		b.WriteString("<D:propstat><D:prop>")
		for _, n := range byStatus[st] {
			b.WriteString("<" + qname(n) + "/>")
		}
		b.WriteString("</D:prop><D:status>HTTP/1.1 " + strconv.Itoa(st) + " " + http.StatusText(st) + "</D:status></D:propstat>")
	}
	b.WriteString("</D:response></D:multistatus>")
	w.Header().Set("Content-Type", `application/xml; charset="utf-8"`)
	w.WriteHeader(http.StatusMultiStatus)
	io.WriteString(w, b.String())
}

func (s *Server) get(w http.ResponseWriter, r *http.Request, u store.User, t target) {
	if t.n != 4 {
		http.Error(w, "not an object", http.StatusMethodNotAllowed)
		return
	}
	c, ok := s.lookupCollection(w, r, u, t)
	if !ok {
		return
	}
	o, err := s.Store.GetObject(c.ID, t.uri)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	h := w.Header()
	h.Set("ETag", `"`+o.ETag+`"`)
	h.Set("Content-Type", contentType(c, o))
	h.Set("Content-Length", strconv.Itoa(len(o.Data)))
	if r.Method != http.MethodHead {
		w.Write(o.Data)
	}
}

func (s *Server) put(w http.ResponseWriter, r *http.Request, u store.User, t target) {
	if t.n != 4 {
		http.Error(w, "PUT is only allowed on objects", http.StatusMethodNotAllowed)
		return
	}
	c, ok := s.collectionForWrite(w, r, u, t)
	if !ok {
		return
	}
	isCal := c.Kind == store.KindCalendar
	ct := strings.ToLower(strings.TrimSpace(strings.SplitN(r.Header.Get("Content-Type"), ";", 2)[0]))
	if ct != "" {
		okType := (isCal && ct == "text/calendar") || (!isCal && (ct == "text/vcard" || ct == "text/x-vcard"))
		if !okType {
			if isCal {
				writeError(w, http.StatusForbidden, "<C:supported-calendar-data/>")
			} else {
				writeError(w, http.StatusForbidden, "<A:supported-address-data/>")
			}
			return
		}
	}
	data, err := io.ReadAll(r.Body)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var p index.Parsed
	var bad string
	if isCal {
		p, bad = index.Calendar(data, strings.Split(c.Components, ","))
	} else {
		p, bad = index.Card(data, t.uri)
	}
	if bad != "" {
		writeError(w, http.StatusForbidden, bad)
		return
	}

	var cond store.PutCond
	cond.IfNoneMatchAny = strings.TrimSpace(r.Header.Get("If-None-Match")) == "*"
	if h := r.Header.Get("If-Match"); h != "" {
		cond.IfMatch = etagMatcher(h)
	}
	o := store.Object{
		CollectionID: c.ID, URI: t.uri, UID: p.UID, ETag: index.ETag(data), Data: data,
		Component: p.Component, FirstOcc: p.First, LastOcc: p.Last,
	}
	created, err := s.Store.PutObject(o, cond)
	var conflict *store.UIDConflictError
	switch {
	case errors.Is(err, store.ErrPrecondition):
		w.WriteHeader(http.StatusPreconditionFailed)
		return
	case errors.As(err, &conflict):
		ns := "C"
		if !isCal {
			ns = "A"
		}
		href := s.collHref(areaOf(c.Kind), u.Username, c.Slug) + conflict.URI
		writeError(w, http.StatusConflict, "<"+ns+":no-uid-conflict>"+hrefXML(href)+"</"+ns+":no-uid-conflict>")
		return
	case err != nil:
		s.fail(w, r, err)
		return
	}
	w.Header().Set("ETag", `"`+o.ETag+`"`)
	if created {
		w.WriteHeader(http.StatusCreated)
	} else {
		w.WriteHeader(http.StatusNoContent)
	}
}

// collectionForWrite is lookupCollection with 409 for a missing parent.
func (s *Server) collectionForWrite(w http.ResponseWriter, r *http.Request, u store.User, t target) (store.Collection, bool) {
	c, err := s.Store.GetCollection(u.ID, t.kind(), t.slug)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "collection does not exist", http.StatusConflict)
		return c, false
	}
	if err != nil {
		s.fail(w, r, err)
		return c, false
	}
	return c, true
}

func (s *Server) delete(w http.ResponseWriter, r *http.Request, u store.User, t target) {
	switch t.n {
	case 3:
		c, ok := s.lookupCollection(w, r, u, t)
		if !ok {
			return
		}
		if err := s.Store.DeleteCollection(c.ID); err != nil {
			s.fail(w, r, err)
			return
		}
	case 4:
		c, ok := s.lookupCollection(w, r, u, t)
		if !ok {
			return
		}
		var m func(string) bool
		if h := r.Header.Get("If-Match"); h != "" {
			m = etagMatcher(h)
		}
		err := s.Store.DeleteObject(c.ID, t.uri, m)
		switch {
		case errors.Is(err, store.ErrNotFound):
			http.NotFound(w, r)
			return
		case errors.Is(err, store.ErrPrecondition):
			w.WriteHeader(http.StatusPreconditionFailed)
			return
		case err != nil:
			s.fail(w, r, err)
			return
		}
	default:
		http.Error(w, "cannot delete this resource", http.StatusForbidden)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
