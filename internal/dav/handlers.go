package dav

import (
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/bitflipp/galdav/internal/index"
	"github.com/bitflipp/galdav/internal/sched"
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
	if !ValidSlug(t.slug) {
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
	var newDefault *string
	apply := func(items []propItem, remove bool) {
		for _, it := range items {
			known, ok := applyProp(&updated, it, remove)
			if c.Special == store.SpecialInbox && it.XMLName == cal("schedule-default-calendar-URL") {
				known, ok = s.inboxDefault(u, it, remove, &newDefault)
			} else if c.Special != "" {
				known = false // nothing else on the inbox is settable
			}
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
	} else {
		var err error
		if newDefault != nil {
			err = s.Store.SetDefaultCalendar(u.ID, *newDefault)
		} else if c.Special == "" {
			err = s.Store.UpdateCollection(updated)
		}
		if err != nil {
			s.fail(w, r, err)
			return
		}
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
	if c.Special != "" {
		http.Error(w, "the scheduling inbox is filled by the server", http.StatusForbidden)
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
	var oldData []byte
	if isCal {
		if prev, err := s.Store.GetObject(c.ID, t.uri); err == nil {
			oldData = prev.Data
		}
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
	if isCal {
		// Implicit scheduling. It may rewrite the stored copy to record
		// delivery status; then the ETag we would report is stale, so omit it.
		if rewritten := s.sched().ProcessPut(u, oldData, data); rewritten != nil {
			o.Data, o.ETag = rewritten, index.ETag(rewritten)
			if p, bad := index.Calendar(rewritten, strings.Split(c.Components, ",")); bad == "" {
				o.FirstOcc, o.LastOcc = p.First, p.Last
			}
			if _, err := s.Store.PutObject(o, store.PutCond{}); err != nil {
				s.fail(w, r, err)
				return
			}
			o.ETag = ""
		}
	}
	if o.ETag != "" {
		w.Header().Set("ETag", `"`+o.ETag+`"`)
	}
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
		if c.Special != "" {
			http.Error(w, "the scheduling inbox cannot be deleted", http.StatusForbidden)
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
		var oldData []byte
		if c.Kind == store.KindCalendar && c.Special == "" {
			if prev, err := s.Store.GetObject(c.ID, t.uri); err == nil {
				oldData = prev.Data
			}
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
		if oldData != nil {
			s.sched().ProcessDelete(u, oldData)
		}
	default:
		http.Error(w, "cannot delete this resource", http.StatusForbidden)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// inboxDefault validates a schedule-default-calendar-URL update; the new
// calendar slug is recorded in out and applied if the whole PROPPATCH succeeds.
func (s *Server) inboxDefault(u store.User, it propItem, remove bool, out **string) (known, ok bool) {
	slug := ""
	if !remove {
		if len(it.Children) == 0 {
			return true, false
		}
		pu, err := url.Parse(strings.TrimSpace(it.Children[0].Text))
		base := s.homeHref("calendars", u.Username)
		if err != nil || !strings.HasPrefix(pu.Path, base) {
			return true, false
		}
		slug = strings.Trim(strings.TrimPrefix(pu.Path, base), "/")
		c, err := s.Store.GetCollection(u.ID, store.KindCalendar, slug)
		if err != nil || c.Special != "" {
			return true, false
		}
	}
	*out = &slug
	return true, true
}

// outbox serves the virtual scheduling outbox at calendars/<user>/outbox/.
func (s *Server) outbox(w http.ResponseWriter, r *http.Request, u store.User, t target) {
	switch {
	case r.Method == "PROPFIND" && t.n == 3:
		pf, err := parsePropfind(r.Body)
		if err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		writeMultistatus(w, pf, []resource{s.outboxRes(u)}, "")
	case r.Method == http.MethodPost && t.n == 3:
		s.outboxPost(w, r, u)
	case t.n > 3:
		http.NotFound(w, r)
	default:
		http.Error(w, "the outbox only accepts POST and PROPFIND", http.StatusMethodNotAllowed)
	}
}

func (s *Server) outboxPost(w http.ResponseWriter, r *http.Request, u store.User) {
	if ct := strings.ToLower(r.Header.Get("Content-Type")); ct != "" && !strings.HasPrefix(ct, "text/calendar") {
		writeError(w, http.StatusForbidden, "<C:supported-calendar-data/>")
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var recipients []string
	for _, h := range r.Header.Values("Recipient") {
		for _, v := range strings.Split(h, ",") {
			if v = strings.Trim(strings.TrimSpace(v), "<>\""); v != "" {
				recipients = append(recipients, v)
			}
		}
	}
	results, err := s.sched().HandleOutbox(u, body, recipients)
	switch {
	case errors.Is(err, sched.ErrInvalidData):
		writeError(w, http.StatusForbidden, "<C:valid-calendar-data/>")
		return
	case errors.Is(err, sched.ErrInvalidMessage):
		writeError(w, http.StatusForbidden, "<C:valid-scheduling-message/>")
		return
	case errors.Is(err, sched.ErrInvalidOrganizer):
		writeError(w, http.StatusForbidden, "<C:valid-organizer/>")
		return
	case err != nil:
		s.fail(w, r, err)
		return
	}
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?>` + "\n<C:schedule-response" + nsDecl + ">")
	for _, res := range results {
		b.WriteString("<C:response><C:recipient>" + hrefXMLRaw(res.Recipient) + "</C:recipient>")
		b.WriteString("<C:request-status>" + res.Status + ";" + esc(sched.StatusText[res.Status]) + "</C:request-status>")
		if res.Data != nil {
			b.WriteString("<C:calendar-data>" + esc(string(res.Data)) + "</C:calendar-data>")
		}
		b.WriteString("</C:response>")
	}
	b.WriteString("</C:schedule-response>")
	w.Header().Set("Content-Type", `application/xml; charset="utf-8"`)
	w.WriteHeader(http.StatusOK)
	io.WriteString(w, b.String())
}
