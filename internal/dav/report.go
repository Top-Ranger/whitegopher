package dav

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-vcard"

	"github.com/bitflipp/galdav/internal/index"
	"github.com/bitflipp/galdav/internal/sched"
	"github.com/bitflipp/galdav/internal/store"
)

type propList struct {
	Items []struct {
		XMLName xml.Name
		// Children of calendar-data (ignored for other properties).
		Comp   *compXML `xml:"urn:ietf:params:xml:ns:caldav comp"`
		Expand *spanXML `xml:"urn:ietf:params:xml:ns:caldav expand"`
		Limit  *spanXML `xml:"urn:ietf:params:xml:ns:caldav limit-recurrence-set"`
	} `xml:",any"`
}

func (p *propList) propfind() propfind {
	var pf propfind
	if p != nil {
		for _, it := range p.Items {
			pf.props = append(pf.props, it.XMLName)
			if it.XMLName == cal("calendar-data") {
				pf.calData = newCalData(it.Comp, it.Expand, it.Limit)
			}
		}
	}
	return pf
}

type multigetReq struct {
	Prop  *propList `xml:"DAV: prop"`
	Hrefs []string  `xml:"DAV: href"`
}

type syncReq struct {
	Token string    `xml:"DAV: sync-token"`
	Level string    `xml:"DAV: sync-level"`
	Prop  *propList `xml:"DAV: prop"`
}

func (s *Server) report(w http.ResponseWriter, r *http.Request, u store.User, t target) {
	if t.n != 3 {
		writeError(w, http.StatusForbidden, "<D:supported-report/>")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	root, err := rootElement(body)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	c, ok := s.lookupCollection(w, r, u, t)
	if !ok {
		return
	}
	isCal := c.Kind == store.KindCalendar
	switch {
	case root == cal("calendar-multiget") && isCal, root == card("addressbook-multiget") && !isCal:
		s.multiget(w, r, u, c, body)
	case root == cal("free-busy-query") && isCal && c.Special == "":
		s.freeBusyQuery(w, r, u, c, body)
	case root == cal("calendar-query") && isCal:
		s.calendarQuery(w, r, u, c, body)
	case root == card("addressbook-query") && !isCal:
		s.addressbookQuery(w, r, u, c, body)
	case root == dav("sync-collection"):
		s.syncCollection(w, r, u, c, body)
	default:
		writeError(w, http.StatusForbidden, "<D:supported-report/>")
	}
}

func rootElement(body []byte) (xml.Name, error) {
	dec := xml.NewDecoder(bytes.NewReader(body))
	for {
		tok, err := dec.Token()
		if err != nil {
			return xml.Name{}, err
		}
		if se, ok := tok.(xml.StartElement); ok {
			return se.Name, nil
		}
	}
}

func (s *Server) multiget(w http.ResponseWriter, r *http.Request, u store.User, c store.Collection, body []byte) {
	var req multigetReq
	if err := xml.Unmarshal(body, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	pf := req.Prop.propfind()
	base := s.collHref(areaOf(c.Kind), u.Username, c.Slug)
	var out []resource
	for _, h := range req.Hrefs {
		pu, err := url.Parse(strings.TrimSpace(h))
		if err != nil || !strings.HasPrefix(pu.Path, base) || strings.Contains(strings.TrimPrefix(pu.Path, base), "/") {
			out = append(out, resource{href: h, status: http.StatusNotFound})
			continue
		}
		o, err := s.Store.GetObject(c.ID, strings.TrimPrefix(pu.Path, base))
		if errors.Is(err, store.ErrNotFound) {
			out = append(out, resource{href: pu.Path, status: http.StatusNotFound})
			continue
		}
		if err != nil {
			s.fail(w, r, err)
			return
		}
		out = append(out, withData(s.objectRes(u, c, o), c, o, pf))
	}
	writeMultistatus(w, pf, out, "")
}

func (s *Server) syncCollection(w http.ResponseWriter, r *http.Request, u store.User, c store.Collection, body []byte) {
	var req syncReq
	if err := xml.Unmarshal(body, &req); err != nil || (req.Level != "" && req.Level != "1") {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	seq, err := s.Store.SyncSeq(c.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var since int64
	if tok := strings.TrimSpace(req.Token); tok != "" {
		n, ok := strings.CutPrefix(tok, "urn:galdav:sync:")
		since, err = strconv.ParseInt(n, 10, 64)
		if !ok || err != nil || since < 0 || since > seq {
			writeError(w, http.StatusForbidden, "<D:valid-sync-token/>")
			return
		}
	}
	pf := req.Prop.propfind()
	wantData := false
	for _, n := range pf.props {
		wantData = wantData || n == cal("calendar-data") || n == card("address-data")
	}

	var out []resource
	base := s.collHref(areaOf(c.Kind), u.Username, c.Slug)
	emit := func(o store.Object) error {
		if wantData && o.Data == nil {
			full, err := s.Store.GetObject(c.ID, o.URI)
			if err != nil {
				return err
			}
			o = full
		}
		res := s.objectRes(u, c, o)
		if wantData {
			res = withData(res, c, o, pf)
		}
		out = append(out, res)
		return nil
	}
	objs, err := s.Store.ListObjects(c.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if since == 0 {
		for _, o := range objs {
			if err := emit(o); err != nil {
				s.fail(w, r, err)
				return
			}
		}
	} else {
		uris, err := s.Store.ChangedURIs(c.ID, since, seq)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		byURI := make(map[string]store.Object, len(objs))
		for _, o := range objs {
			byURI[o.URI] = o
		}
		for _, uri := range uris {
			if o, ok := byURI[uri]; ok {
				if err := emit(o); err != nil {
					s.fail(w, r, err)
					return
				}
			} else {
				out = append(out, resource{href: base + uri, status: http.StatusNotFound})
			}
		}
	}
	writeMultistatus(w, pf, out, "<D:sync-token>"+syncToken(seq)+"</D:sync-token>")
}

// respondObjects writes the multistatus for query results.
func (s *Server) respondObjects(w http.ResponseWriter, u store.User, c store.Collection, pf propfind, objs []store.Object) {
	out := make([]resource, 0, len(objs))
	for _, o := range objs {
		out = append(out, withData(s.objectRes(u, c, o), c, o, pf))
	}
	writeMultistatus(w, pf, out, "")
}

func (s *Server) calendarQuery(w http.ResponseWriter, r *http.Request, u store.User, c store.Collection, body []byte) {
	var req struct {
		Prop   *propList `xml:"DAV: prop"`
		Filter struct {
			Comp compFilter `xml:"urn:ietf:params:xml:ns:caldav comp-filter"`
		} `xml:"urn:ietf:params:xml:ns:caldav filter"`
	}
	if err := xml.Unmarshal(body, &req); err != nil || !strings.EqualFold(req.Filter.Comp.Name, ical.CompCalendar) {
		writeError(w, http.StatusBadRequest, "<C:valid-filter/>")
		return
	}
	top := req.Filter.Comp
	// SQL prefilter: valid only for a single, positive, direct child filter.
	var component string
	var from, to *int64
	if len(top.CompFilters) == 1 && top.CompFilters[0].IsNotDefined == nil && len(top.PropFilters) == 0 {
		cf := top.CompFilters[0]
		component = strings.ToUpper(cf.Name)
		if cf.TimeRange != nil {
			rs, re, err := cf.TimeRange.bounds()
			if err != nil {
				writeError(w, http.StatusBadRequest, "<C:valid-filter/>")
				return
			}
			if !rs.IsZero() {
				v := rs.Unix()
				from = &v
			}
			if !re.IsZero() {
				v := re.Unix()
				to = &v
			}
		}
	}
	cands, err := s.Store.QueryObjects(c.ID, component, from, to)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var hits []store.Object
	for _, o := range cands {
		parsed, err := ical.NewDecoder(bytes.NewReader(o.Data)).Decode()
		if err != nil {
			continue
		}
		if matchComp(parsed.Component, top) {
			hits = append(hits, o)
		}
	}
	s.respondObjects(w, u, c, req.Prop.propfind(), hits)
}

func (s *Server) addressbookQuery(w http.ResponseWriter, r *http.Request, u store.User, c store.Collection, body []byte) {
	var req struct {
		Prop   *propList      `xml:"DAV: prop"`
		Filter cardFilterSpec `xml:"urn:ietf:params:xml:ns:carddav filter"`
		Limit  *struct {
			NResults int `xml:"urn:ietf:params:xml:ns:carddav nresults"`
		} `xml:"urn:ietf:params:xml:ns:carddav limit"`
	}
	if err := xml.Unmarshal(body, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	cands, err := s.Store.QueryObjects(c.ID, "", nil, nil)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var hits []store.Object
	for _, o := range cands {
		card, err := vcard.NewDecoder(bytes.NewReader(o.Data)).Decode()
		if err != nil {
			continue
		}
		if req.Filter.match(card) {
			hits = append(hits, o)
		}
	}
	pf := req.Prop.propfind()
	if req.Limit != nil && req.Limit.NResults > 0 && len(hits) > req.Limit.NResults {
		// RFC 6352 8.6.1: send the first nresults matches and flag the
		// request URI as truncated.
		hits = hits[:req.Limit.NResults]
		out := make([]resource, 0, len(hits)+1)
		for _, o := range hits {
			out = append(out, withData(s.objectRes(u, c, o), c, o, pf))
		}
		out = append(out, resource{
			href:     s.collHref(areaOf(c.Kind), u.Username, c.Slug),
			status:   http.StatusInsufficientStorage,
			errorXML: "<D:number-of-matches-within-limits/>",
		})
		writeMultistatus(w, pf, out, "")
		return
	}
	s.respondObjects(w, u, c, pf, hits)
}

// freeBusyQuery answers a free-busy-query REPORT (RFC 4791 7.10) for one
// calendar with a VFREEBUSY.
func (s *Server) freeBusyQuery(w http.ResponseWriter, r *http.Request, u store.User, c store.Collection, body []byte) {
	var req struct {
		TimeRange timeRange `xml:"urn:ietf:params:xml:ns:caldav time-range"`
	}
	if err := xml.Unmarshal(body, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	start, end, err := req.TimeRange.bounds()
	if err != nil || start.IsZero() || end.IsZero() || !end.After(start) {
		writeError(w, http.StatusForbidden, "<C:valid-filter/>")
		return
	}
	c.Transparent = false // asked for explicitly
	busy, tent, err := s.sched().FreeBusy(u, []store.Collection{c}, start, end)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	uid := "fb-" + index.ETag([]byte(start.String()+end.String()+c.Slug))
	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.Write(sched.RenderFreeBusy("", uid, "", "", start, end, time.Now(), busy, tent))
}
