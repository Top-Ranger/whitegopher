package dav

import (
	"encoding/xml"
	"fmt"
	"strconv"
	"strings"

	"github.com/bitflipp/galdav/internal/store"
)

func dav(local string) xml.Name  { return xml.Name{Space: nsDAV, Local: local} }
func cal(local string) xml.Name  { return xml.Name{Space: nsCal, Local: local} }
func card(local string) xml.Name { return xml.Name{Space: nsCard, Local: local} }
func cs(local string) xml.Name   { return xml.Name{Space: nsCS, Local: local} }
func apl(local string) xml.Name  { return xml.Name{Space: nsApl, Local: local} }

func (s *Server) principalHref(user string) string { return s.BasePath + "/principals/" + user + "/" }
func (s *Server) homeHref(area, user string) string {
	return s.BasePath + "/" + area + "/" + user + "/"
}
func (s *Server) collHref(area, user, slug string) string {
	return s.homeHref(area, user) + slug + "/"
}

func areaOf(kind string) string {
	if kind == store.KindCalendar {
		return "calendars"
	}
	return "addressbooks"
}

func syncToken(seq int64) string { return fmt.Sprintf("urn:galdav:sync:%d", seq) }

const (
	privAll = "<D:privilege><D:read/></D:privilege><D:privilege><D:write/></D:privilege>" +
		"<D:privilege><D:read-current-user-privilege-set/></D:privilege>"
)

func (s *Server) rootRes(u store.User) resource {
	return resource{href: s.BasePath + "/", props: map[xml.Name]string{
		dav("resourcetype"):           "<D:collection/>",
		dav("displayname"):            "root",
		dav("current-user-principal"): hrefXML(s.principalHref(u.Username)),
	}}
}

func (s *Server) containerRes(area string) resource {
	return resource{href: s.BasePath + "/" + area + "/", props: map[xml.Name]string{
		dav("resourcetype"): "<D:collection/>",
		dav("displayname"):  esc(area),
	}}
}

func (s *Server) principalRes(u store.User) resource {
	name := u.DisplayName
	if name == "" {
		name = u.Username
	}
	return resource{href: s.principalHref(u.Username), props: map[xml.Name]string{
		dav("resourcetype"):              "<D:collection/><D:principal/>",
		dav("displayname"):               esc(name),
		dav("current-user-principal"):    hrefXML(s.principalHref(u.Username)),
		dav("principal-URL"):             hrefXML(s.principalHref(u.Username)),
		cal("calendar-home-set"):         hrefXML(s.homeHref("calendars", u.Username)),
		cal("schedule-inbox-URL"):        hrefXML(s.collHref("calendars", u.Username, "inbox")),
		cal("schedule-outbox-URL"):       hrefXML(s.collHref("calendars", u.Username, "outbox")),
		cal("calendar-user-type"):        "INDIVIDUAL",
		cal("calendar-user-address-set"): addressSet(s.sched().Addresses(u)),
		card("addressbook-home-set"):     hrefXML(s.homeHref("addressbooks", u.Username)),
	}}
}

func addressSet(addrs []string) string {
	var b strings.Builder
	for _, a := range addrs {
		b.WriteString("<D:href>" + esc(a) + "</D:href>")
	}
	return b.String()
}

const (
	privInbox  = privAll + "<D:privilege><C:schedule-deliver/></D:privilege><D:privilege><C:schedule-deliver-invite/></D:privilege><D:privilege><C:schedule-deliver-reply/></D:privilege><D:privilege><C:schedule-query-freebusy/></D:privilege>"
	privOutbox = privAll + "<D:privilege><C:schedule-send/></D:privilege><D:privilege><C:schedule-send-invite/></D:privilege><D:privilege><C:schedule-send-reply/></D:privilege><D:privilege><C:schedule-send-freebusy/></D:privilege>"
)

// outboxRes is the virtual scheduling outbox: it holds nothing and only
// accepts POSTs.
func (s *Server) outboxRes(u store.User) resource {
	return resource{href: s.collHref("calendars", u.Username, "outbox"), props: map[xml.Name]string{
		dav("resourcetype"):               "<D:collection/><C:schedule-outbox/>",
		dav("displayname"):                "Outbox",
		dav("owner"):                      hrefXML(s.principalHref(u.Username)),
		dav("current-user-principal"):     hrefXML(s.principalHref(u.Username)),
		dav("current-user-privilege-set"): privOutbox,
	}}
}

func (s *Server) homeRes(area string, u store.User) resource {
	name := "Calendars"
	if area == "addressbooks" {
		name = "Address books"
	}
	return resource{href: s.homeHref(area, u.Username), props: map[xml.Name]string{
		dav("resourcetype"):           "<D:collection/>",
		dav("displayname"):            name,
		dav("current-user-principal"): hrefXML(s.principalHref(u.Username)),
		dav("owner"):                  hrefXML(s.principalHref(u.Username)),
	}}
}

func (s *Server) collectionRes(u store.User, c store.Collection, seq int64) resource {
	name := c.DisplayName
	if name == "" {
		name = c.Slug
	}
	p := map[xml.Name]string{
		dav("displayname"):                esc(name),
		dav("owner"):                      hrefXML(s.principalHref(u.Username)),
		dav("current-user-principal"):     hrefXML(s.principalHref(u.Username)),
		dav("current-user-privilege-set"): privAll,
		dav("sync-token"):                 syncToken(seq),
		cs("getctag"):                     syncToken(seq),
	}
	if c.Special == store.SpecialInbox {
		p[dav("resourcetype")] = "<D:collection/><C:schedule-inbox/>"
		p[dav("current-user-privilege-set")] = privInbox
		p[dav("supported-report-set")] = reportSet("<C:calendar-multiget/>", "<C:calendar-query/>")
		if def, err := s.Store.DefaultCalendar(u, "VEVENT"); err == nil {
			p[cal("schedule-default-calendar-URL")] = hrefXML(s.collHref("calendars", u.Username, def.Slug))
		}
	} else if c.Kind == store.KindCalendar {
		p[dav("resourcetype")] = "<D:collection/><C:calendar/>"
		if c.Transparent {
			p[cal("schedule-calendar-transp")] = "<C:transparent/>"
		} else {
			p[cal("schedule-calendar-transp")] = "<C:opaque/>"
		}
		p[cal("calendar-description")] = esc(c.Description)
		p[apl("calendar-order")] = strconv.Itoa(c.SortOrder)
		if c.Color != "" {
			p[apl("calendar-color")] = esc(c.Color)
		}
		if c.Timezone != "" {
			p[cal("calendar-timezone")] = esc(c.Timezone)
		}
		var comps strings.Builder
		for _, n := range strings.Split(c.Components, ",") {
			comps.WriteString(`<C:comp name="` + esc(n) + `"/>`)
		}
		p[cal("supported-calendar-component-set")] = comps.String()
		p[dav("supported-report-set")] = reportSet("<C:calendar-multiget/>", "<C:calendar-query/>", "<C:free-busy-query/>")
		p[cal("supported-calendar-data")] = `<C:calendar-data content-type="text/calendar" version="2.0"/>`
	} else {
		p[dav("resourcetype")] = "<D:collection/><A:addressbook/>"
		p[dav("supported-report-set")] = reportSet("<A:addressbook-multiget/>", "<A:addressbook-query/>")
		p[card("addressbook-description")] = esc(c.Description)
		p[card("supported-address-data")] = `<A:address-data-type content-type="text/vcard" version="3.0"/>` +
			`<A:address-data-type content-type="text/vcard" version="4.0"/>`
	}
	return resource{href: s.collHref(areaOf(c.Kind), u.Username, c.Slug), props: p}
}

func contentType(c store.Collection, o store.Object) string {
	if c.Kind == store.KindCalendar {
		return "text/calendar; charset=utf-8; component=" + o.Component
	}
	return "text/vcard; charset=utf-8"
}

func (s *Server) objectRes(u store.User, c store.Collection, o store.Object) resource {
	return resource{href: s.collHref(areaOf(c.Kind), u.Username, c.Slug) + o.URI, props: map[xml.Name]string{
		dav("resourcetype"):               "",
		dav("getetag"):                    `"` + o.ETag + `"`,
		dav("getcontenttype"):             esc(contentType(c, o)),
		dav("getcontentlength"):           strconv.FormatInt(o.Size, 10),
		dav("owner"):                      hrefXML(s.principalHref(u.Username)),
		dav("current-user-privilege-set"): privAll,
	}}
}

// applyProp sets or removes one live-settable collection property. known
// is false for properties we don't store; ok is false for invalid values.
func applyProp(c *store.Collection, it propItem, remove bool) (known, ok bool) {
	text := strings.TrimSpace(it.Text)
	if remove {
		text = ""
	}
	isCal := c.Kind == store.KindCalendar
	switch it.XMLName {
	case dav("displayname"):
		c.DisplayName = text
	case cal("calendar-description"):
		if !isCal {
			return false, false
		}
		c.Description = text
	case card("addressbook-description"):
		if isCal {
			return false, false
		}
		c.Description = text
	case apl("calendar-color"):
		if !isCal {
			return false, false
		}
		c.Color = text
	case apl("calendar-order"):
		if !isCal {
			return false, false
		}
		n := 0
		if text != "" {
			var err error
			if n, err = strconv.Atoi(text); err != nil {
				return true, false
			}
		}
		c.SortOrder = n
	case cal("calendar-timezone"):
		if !isCal {
			return false, false
		}
		c.Timezone = strings.TrimSpace(it.Text)
		if remove {
			c.Timezone = ""
		}
	case cal("schedule-calendar-transp"):
		if !isCal || c.Special != "" {
			return false, false
		}
		c.Transparent = false
		if !remove && len(it.Children) > 0 {
			switch it.Children[0].XMLName.Local {
			case "transparent":
				c.Transparent = true
			case "opaque":
			default:
				return true, false
			}
		}
	default:
		return false, false
	}
	return true, true
}

func reportSet(reports ...string) string {
	reports = append(reports, "<D:sync-collection/>")
	var b strings.Builder
	for _, r := range reports {
		b.WriteString("<D:supported-report><D:report>" + r + "</D:report></D:supported-report>")
	}
	return b.String()
}

// withData adds the calendar-data/address-data property to an object resource.
func withData(r resource, c store.Collection, o store.Object) resource {
	if c.Kind == store.KindCalendar {
		r.props[cal("calendar-data")] = esc(string(o.Data))
	} else {
		r.props[card("address-data")] = esc(string(o.Data))
	}
	return r
}
