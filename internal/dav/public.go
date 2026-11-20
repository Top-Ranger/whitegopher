package dav

import (
	"bytes"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/emersion/go-ical"

	"github.com/Top-Ranger/whitegopher/internal/index"
	"github.com/Top-Ranger/whitegopher/internal/sched"
	"github.com/Top-Ranger/whitegopher/internal/store"
)

// textEscaper escapes an iCalendar TEXT value (RFC 5545 3.3.11).
var textEscaper = strings.NewReplacer(`\`, `\\`, ";", `\;`, ",", `\,`, "\r\n", `\n`, "\n", `\n`)

var publicRE = regexp.MustCompile(`^([0-9a-f]{32})(\.ics|\.vcf)?$`)

// servePublic serves a collection by its public link (/public/<token>[.ics|.vcf]):
// a calendar as one iCalendar feed to subscribe to, an address book as one
// vCard file. No login; the unguessable token is the credential.
func (s *Server) servePublic(w http.ResponseWriter, r *http.Request) {
	s.once.Do(s.initDefaults)
	m := publicRE.FindStringSubmatch(r.PathValue("token"))
	ip := s.Auth.Proxies.ClientIP(r)
	if m == nil || !s.publicLimiter.Allowed(ip) {
		if m != nil {
			w.Header().Set("Retry-After", "900")
			http.Error(w, "too many requests", http.StatusTooManyRequests)
			return
		}
		http.NotFound(w, r)
		return
	}
	c, err := s.Store.CollectionByPublicToken(m[1])
	if errors.Is(err, store.ErrNotFound) {
		s.publicLimiter.Fail(ip)
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	isCal := c.Kind == store.KindCalendar
	if (m[2] == ".ics" && !isCal) || (m[2] == ".vcf" && isCal) || c.Special != "" {
		http.NotFound(w, r)
		return
	}
	seq, err := s.Store.SyncSeq(c.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	etag := `"` + index.ETag([]byte(c.Slug+"|"+c.DisplayName+"|"+syncToken(seq))) + `"`
	h := w.Header()
	h.Set("ETag", etag)
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Robots-Tag", "noindex")
	h.Set("Referrer-Policy", "no-referrer")
	if weakETagMatcher(r.Header.Get("If-None-Match"))(strings.Trim(etag, `"`)) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	objs, err := s.Store.QueryObjects(c.ID, "", nil, nil)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var out []byte
	if isCal {
		out = publicCalendar(c, objs)
		h.Set("Content-Type", "text/calendar; charset=utf-8")
		h.Set("Content-Disposition", `inline; filename="`+c.Slug+`.ics"`)
	} else {
		var b bytes.Buffer
		for _, o := range objs {
			b.Write(bytes.TrimRight(o.Data, "\r\n"))
			b.WriteString("\r\n")
		}
		out = b.Bytes()
		h.Set("Content-Type", "text/vcard; charset=utf-8")
		h.Set("Content-Disposition", `inline; filename="`+c.Slug+`.vcf"`)
	}
	if r.Method != http.MethodHead {
		w.Write(out)
	}
}

// publicCalendar merges a calendar's objects into one VCALENDAR: every
// event/task/journal, and each time zone definition once.
func publicCalendar(c store.Collection, objs []store.Object) []byte {
	cal := ical.NewCalendar()
	cal.Props.SetText(ical.PropVersion, "2.0")
	cal.Props.SetText(ical.PropProductID, "-//whitegopher//CalDAV//EN")
	name := c.DisplayName
	if name == "" {
		name = c.Slug
	}
	setText := func(prop, value string) {
		cal.Props.Set(&ical.Prop{Name: prop, Value: textEscaper.Replace(value), Params: ical.Params{}})
	}
	setText("X-WR-CALNAME", name)
	if c.Color != "" {
		setText("X-APPLE-CALENDAR-COLOR", c.Color)
	}
	zones := map[string]bool{}
	for _, o := range objs {
		parsed, err := ical.NewDecoder(bytes.NewReader(o.Data)).Decode()
		if err != nil {
			continue
		}
		for _, ch := range parsed.Children {
			if ch.Name == ical.CompTimezone {
				id := ""
				if p := ch.Props.Get(ical.PropTimezoneID); p != nil {
					id = p.Value
				}
				if zones[id] {
					continue
				}
				zones[id] = true
			}
			cal.Children = append(cal.Children, sched.CloneComponent(ch))
		}
	}
	return sched.Encode(cal)
}
