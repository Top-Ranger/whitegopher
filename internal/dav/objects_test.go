package dav_test

import (
	"strings"
	"testing"
)

const (
	alice = "alice"
	pw    = "secret"

	ics = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//t//EN\r\nBEGIN:VEVENT\r\nUID:ev1\r\n" +
		"DTSTAMP:20260101T000000Z\r\nDTSTART:20260301T100000Z\r\nDTEND:20260301T110000Z\r\nSUMMARY:Hi\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	vcf = "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:c1\r\nFN:Bob\r\nN:;Bob;;;\r\nEND:VCARD\r\n"
)

func (c *client) req(method, path, body string, hdr ...string) (int, string, map[string][]string) {
	c.t.Helper()
	h := map[string]string{}
	for i := 0; i+1 < len(hdr); i += 2 {
		h[hdr[i]] = hdr[i+1]
	}
	resp, out := do(c.t, method, c.url+path, alice, pw, body, h)
	return resp.StatusCode, out, resp.Header
}

type client struct {
	t   *testing.T
	url string
}

func expect(t *testing.T, got, want int, body string) {
	t.Helper()
	if got != want {
		t.Fatalf("status %d, want %d; body: %s", got, want, body)
	}
}

func TestCalendarLifecycle(t *testing.T) {
	c := &client{t, newServer(t).URL}
	st, b, _ := c.req("MKCALENDAR", "/dav/calendars/alice/work/", `<c:mkcalendar xmlns:c="urn:ietf:params:xml:ns:caldav" xmlns:d="DAV:" xmlns:i="http://apple.com/ns/ical/"><d:set><d:prop>
		<d:displayname>Work</d:displayname><i:calendar-color>#ff0000</i:calendar-color>
		<c:supported-calendar-component-set><c:comp name="VEVENT"/></c:supported-calendar-component-set></d:prop></d:set></c:mkcalendar>`)
	expect(t, st, 201, b)
	st, b, _ = c.req("MKCALENDAR", "/dav/calendars/alice/work/", "")
	expect(t, st, 405, b)

	// Create, conditional create, update, read.
	st, b, h := c.req("PUT", "/dav/calendars/alice/work/ev1.ics", ics, "Content-Type", "text/calendar", "If-None-Match", "*")
	expect(t, st, 201, b)
	etag := h["Etag"][0]
	st, b, _ = c.req("PUT", "/dav/calendars/alice/work/ev1.ics", ics, "If-None-Match", "*")
	expect(t, st, 412, b)
	st, b, _ = c.req("PUT", "/dav/calendars/alice/work/ev1.ics", ics, "If-Match", `"nope"`)
	expect(t, st, 412, b)
	upd := strings.Replace(ics, "SUMMARY:Hi", "SUMMARY:Changed", 1)
	st, b, h = c.req("PUT", "/dav/calendars/alice/work/ev1.ics", upd, "If-Match", etag)
	expect(t, st, 204, b)
	if h["Etag"][0] == etag {
		t.Fatal("etag did not change")
	}
	st, b, _ = c.req("GET", "/dav/calendars/alice/work/ev1.ics", "")
	expect(t, st, 200, b)
	if !strings.Contains(b, "Changed") {
		t.Fatalf("body %q", b)
	}

	// Validation.
	st, b, _ = c.req("PUT", "/dav/calendars/alice/work/bad.ics", "garbage")
	expect(t, st, 403, b)
	if !strings.Contains(b, "valid-calendar-data") {
		t.Fatal(b)
	}
	todo := strings.NewReplacer("VEVENT", "VTODO", "ev1", "t1").Replace(ics)
	st, b, _ = c.req("PUT", "/dav/calendars/alice/work/t.ics", todo)
	expect(t, st, 403, b)
	if !strings.Contains(b, "supported-calendar-component") {
		t.Fatal(b)
	}
	st, b, _ = c.req("PUT", "/dav/calendars/alice/work/dup.ics", ics)
	expect(t, st, 409, b)
	if !strings.Contains(b, "no-uid-conflict") || !strings.Contains(b, "/dav/calendars/alice/work/ev1.ics") {
		t.Fatal(b)
	}
	st, b, _ = c.req("PUT", "/dav/calendars/alice/work/x.ics", vcf, "Content-Type", "text/vcard")
	expect(t, st, 403, b)
	st, b, _ = c.req("PUT", "/dav/calendars/alice/missing/x.ics", ics)
	expect(t, st, 409, b)

	// PROPFIND listing and ctag movement.
	body := `<propfind xmlns="DAV:" xmlns:cs="http://calendarserver.org/ns/"><prop><displayname/><getetag/><cs:getctag/></prop></propfind>`
	st, b, _ = c.req("PROPFIND", "/dav/calendars/alice/work/", body, "Depth", "1")
	expect(t, st, 207, b)
	for _, want := range []string{"<D:displayname>Work</D:displayname>", "/dav/calendars/alice/work/ev1.ics", "urn:whitegopher:sync:2"} {
		if !strings.Contains(b, want) {
			t.Fatalf("missing %q in %s", want, b)
		}
	}
	st, b, _ = c.req("PROPFIND", "/dav/calendars/alice/", `<propfind xmlns="DAV:"><allprop/></propfind>`, "Depth", "1")
	expect(t, st, 207, b)
	if !strings.Contains(b, "/dav/calendars/alice/work/") || !strings.Contains(b, `<C:comp name="VEVENT"/>`) {
		t.Fatal(b)
	}

	// PROPPATCH: atomic.
	pp := func(inner string) (int, string) {
		st, b, _ := c.req("PROPPATCH", "/dav/calendars/alice/work/", `<propertyupdate xmlns="DAV:" xmlns:i="http://apple.com/ns/ical/"><set><prop>`+inner+`</prop></set></propertyupdate>`)
		return st, b
	}
	st2, b := pp(`<displayname>Renamed</displayname><i:calendar-order>3</i:calendar-order>`)
	expect(t, st2, 207, b)
	if !strings.Contains(b, "200 OK") {
		t.Fatal(b)
	}
	st2, b = pp(`<displayname>X</displayname><resourcetype/>`)
	expect(t, st2, 207, b)
	if !strings.Contains(b, "403") || !strings.Contains(b, "424") {
		t.Fatal(b)
	}
	_, b, _ = c.req("PROPFIND", "/dav/calendars/alice/work/", `<propfind xmlns="DAV:"><prop><displayname/></prop></propfind>`, "Depth", "0")
	if !strings.Contains(b, "Renamed") {
		t.Fatalf("proppatch not atomic: %s", b)
	}

	// Delete object, then collection.
	st, b, _ = c.req("DELETE", "/dav/calendars/alice/work/ev1.ics", "", "If-Match", `"stale"`)
	expect(t, st, 412, b)
	st, b, _ = c.req("DELETE", "/dav/calendars/alice/work/ev1.ics", "")
	expect(t, st, 204, b)
	st, b, _ = c.req("GET", "/dav/calendars/alice/work/ev1.ics", "")
	expect(t, st, 404, b)
	st, b, _ = c.req("DELETE", "/dav/calendars/alice/work/", "")
	expect(t, st, 204, b)
	st, b, _ = c.req("PROPFIND", "/dav/calendars/alice/work/", "", "Depth", "0")
	expect(t, st, 404, b)
}

func TestAddressBook(t *testing.T) {
	c := &client{t, newServer(t).URL}
	st, b, _ := c.req("MKCOL", "/dav/addressbooks/alice/book/", "")
	expect(t, st, 403, b) // must declare a resource type
	st, b, _ = c.req("MKCOL", "/dav/addressbooks/alice/book/", `<mkcol xmlns="DAV:" xmlns:a="urn:ietf:params:xml:ns:carddav"><set><prop>
		<resourcetype><collection/><a:addressbook/></resourcetype><displayname>Book</displayname></prop></set></mkcol>`)
	expect(t, st, 201, b)
	st, b, _ = c.req("PUT", "/dav/addressbooks/alice/book/c1.vcf", vcf, "Content-Type", "text/vcard")
	expect(t, st, 201, b)
	st, b, _ = c.req("PUT", "/dav/addressbooks/alice/book/c2.vcf", vcf)
	expect(t, st, 409, b)
	st, b, _ = c.req("PUT", "/dav/addressbooks/alice/book/c3.vcf", vcf+vcf)
	expect(t, st, 403, b)
	st, b, _ = c.req("PUT", "/dav/addressbooks/alice/book/n.vcf", strings.Replace(vcf, "UID:c1\r\n", "", 1))
	expect(t, st, 201, b) // no UID: uri is used
	st, b, _ = c.req("PROPFIND", "/dav/addressbooks/alice/book/c1.vcf", `<propfind xmlns="DAV:"><prop><getcontenttype/></prop></propfind>`)
	expect(t, st, 207, b)
	if !strings.Contains(b, "text/vcard") {
		t.Fatal(b)
	}
}

func TestLastModified(t *testing.T) {
	c := &client{t, newServer(t).URL}
	c.req("MKCALENDAR", "/dav/calendars/alice/work/", "")
	st, b, _ := c.req("PUT", "/dav/calendars/alice/work/ev1.ics", ics)
	expect(t, st, 201, b)
	st, b, h := c.req("GET", "/dav/calendars/alice/work/ev1.ics", "")
	expect(t, st, 200, b)
	lm := h["Last-Modified"]
	if len(lm) != 1 {
		t.Fatalf("no Last-Modified: %v", h)
	}
	st, b, _ = c.req("GET", "/dav/calendars/alice/work/ev1.ics", "", "If-Modified-Since", lm[0])
	expect(t, st, 304, b)
	st, b, _ = c.req("GET", "/dav/calendars/alice/work/ev1.ics", "", "If-Modified-Since", "Mon, 01 Jan 2001 00:00:00 GMT")
	expect(t, st, 200, b)
	// If-None-Match takes precedence over If-Modified-Since.
	st, b, _ = c.req("GET", "/dav/calendars/alice/work/ev1.ics", "", "If-Modified-Since", lm[0], "If-None-Match", `"other"`)
	expect(t, st, 200, b)
	st, b, _ = c.req("PROPFIND", "/dav/calendars/alice/work/", `<d:propfind xmlns:d="DAV:"><d:prop><d:getlastmodified/></d:prop></d:propfind>`, "Depth", "1")
	expect(t, st, 207, b)
	has(t, b, "<D:getlastmodified>"+lm[0]+"</D:getlastmodified>")
}
