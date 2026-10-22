package dav_test

import (
	"regexp"
	"strings"
	"testing"
)

func event(uid, summary, dtstart, dtend, extra string) string {
	return "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//t//EN\r\nBEGIN:VEVENT\r\nUID:" + uid +
		"\r\nDTSTAMP:20260101T000000Z\r\nDTSTART:" + dtstart + "\r\nDTEND:" + dtend + "\r\nSUMMARY:" + summary + "\r\n" + extra +
		"END:VEVENT\r\nEND:VCALENDAR\r\n"
}

func hrefs(body string) []string {
	var out []string
	for _, m := range regexp.MustCompile(`<D:href>([^<]*)</D:href>`).FindAllStringSubmatch(body, -1) {
		out = append(out, m[1])
	}
	return out
}

func has(t *testing.T, body string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(body, w) {
			t.Fatalf("missing %q in %s", w, body)
		}
	}
}

func lacks(t *testing.T, body string, bad ...string) {
	t.Helper()
	for _, w := range bad {
		if strings.Contains(body, w) {
			t.Fatalf("unexpected %q in %s", w, body)
		}
	}
}

const calNS = `xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav"`

func TestCalendarReports(t *testing.T) {
	c := &client{t, newServer(t).URL}
	st, b, _ := c.req("MKCALENDAR", "/dav/calendars/alice/w/", "")
	expect(t, st, 201, b)
	put := func(name, data string) {
		st, b, _ := c.req("PUT", "/dav/calendars/alice/w/"+name, data)
		expect(t, st, 201, b)
	}
	put("a.ics", event("a", "Lunch with Bob", "20260301T120000Z", "20260301T130000Z", ""))
	put("b.ics", event("b", "Standup", "20260305T090000Z", "20260305T091500Z", "RRULE:FREQ=DAILY;COUNT=3\r\n"))
	put("c.ics", event("c", "Weekly", "20260101T090000Z", "20260101T100000Z", "RRULE:FREQ=WEEKLY\r\n"))
	put("d.ics", event("d", "Late", "20270101T090000Z", "20270101T100000Z", ""))

	query := func(inner string) string {
		st, b, _ := c.req("REPORT", "/dav/calendars/alice/w/", `<c:calendar-query `+calNS+`><d:prop><d:getetag/><c:calendar-data/></d:prop>
			<c:filter><c:comp-filter name="VCALENDAR"><c:comp-filter name="VEVENT">`+inner+`</c:comp-filter></c:comp-filter></c:filter></c:calendar-query>`)
		expect(t, st, 207, b)
		return b
	}
	b = query(`<c:time-range start="20260301T000000Z" end="20260302T000000Z"/>`)
	has(t, b, "a.ics", "SUMMARY:Lunch with Bob")
	lacks(t, b, "b.ics", "d.ics")
	// The weekly event started 2026-01-01 (Thursday); 2026-03-05 is one too.
	b = query(`<c:time-range start="20260305T000000Z" end="20260306T000000Z"/>`)
	has(t, b, "b.ics", "c.ics")
	lacks(t, b, "a.ics", "d.ics")
	// Third daily occurrence is 2026-03-07; fourth would be the 8th.
	b = query(`<c:time-range start="20260307T000000Z" end="20260308T000000Z"/>`)
	has(t, b, "b.ics")
	b = query(`<c:time-range start="20260308T000000Z" end="20260309T000000Z"/>`)
	lacks(t, b, "b.ics")
	b = query(`<c:time-range start="20260301T000000Z"/>`) // open end
	has(t, b, "a.ics", "d.ics", "c.ics")
	b = query(`<c:prop-filter name="SUMMARY"><c:text-match>STANDUP</c:text-match></c:prop-filter>`)
	has(t, b, "b.ics")
	lacks(t, b, "a.ics")
	b = query(`<c:prop-filter name="SUMMARY"><c:text-match negate-condition="yes">e</c:text-match></c:prop-filter>`)
	has(t, b, "a.ics", "b.ics") // neither "Lunch with Bob" nor "Standup" contains an "e"
	lacks(t, b, "c.ics", "d.ics")
	b = query(`<c:prop-filter name="RRULE"><c:is-not-defined/></c:prop-filter>`)
	has(t, b, "a.ics", "d.ics")
	lacks(t, b, "b.ics", "c.ics")
	// VTODO filter matches nothing.
	st, b, _ = c.req("REPORT", "/dav/calendars/alice/w/", `<c:calendar-query `+calNS+`><d:prop><d:getetag/></d:prop>
		<c:filter><c:comp-filter name="VCALENDAR"><c:comp-filter name="VTODO"/></c:comp-filter></c:filter></c:calendar-query>`)
	expect(t, st, 207, b)
	lacks(t, b, ".ics")

	// multiget, with one unknown href.
	st, b, _ = c.req("REPORT", "/dav/calendars/alice/w/", `<c:calendar-multiget `+calNS+`><d:prop><d:getetag/><c:calendar-data/></d:prop>
		<d:href>/dav/calendars/alice/w/a.ics</d:href><d:href>/dav/calendars/alice/w/zzz.ics</d:href></c:calendar-multiget>`)
	expect(t, st, 207, b)
	has(t, b, "Lunch with Bob", "404 Not Found", "zzz.ics")

	// Wrong report type for this collection.
	st, b, _ = c.req("REPORT", "/dav/calendars/alice/w/", `<a:addressbook-query xmlns:a="urn:ietf:params:xml:ns:carddav"/>`)
	expect(t, st, 403, b)
}

func TestSyncCollection(t *testing.T) {
	c := &client{t, newServer(t).URL}
	c.req("MKCALENDAR", "/dav/calendars/alice/s/", "")
	sync := func(token string) (string, string) {
		st, b, _ := c.req("REPORT", "/dav/calendars/alice/s/", `<d:sync-collection xmlns:d="DAV:"><d:sync-token>`+token+`</d:sync-token><d:sync-level>1</d:sync-level><d:prop><d:getetag/></d:prop></d:sync-collection>`)
		expect(t, st, 207, b)
		m := regexp.MustCompile(`<D:sync-token>([^<]+)</D:sync-token>`).FindStringSubmatch(b)
		if m == nil {
			t.Fatal("no sync-token in " + b)
		}
		return m[1], b
	}
	c.req("PUT", "/dav/calendars/alice/s/1.ics", event("1", "one", "20260301T120000Z", "20260301T130000Z", ""))
	c.req("PUT", "/dav/calendars/alice/s/2.ics", event("2", "two", "20260301T120000Z", "20260301T130000Z", ""))
	tok, b := sync("")
	has(t, b, "1.ics", "2.ics")

	_, b = sync(tok)
	lacks(t, b, ".ics")

	c.req("PUT", "/dav/calendars/alice/s/1.ics", event("1", "one!", "20260301T120000Z", "20260301T130000Z", ""))
	c.req("DELETE", "/dav/calendars/alice/s/2.ics", "")
	c.req("PUT", "/dav/calendars/alice/s/3.ics", event("3", "three", "20260301T120000Z", "20260301T130000Z", ""))
	c.req("DELETE", "/dav/calendars/alice/s/3.ics", "") // created and deleted since the token
	tok2, b := sync(tok)
	has(t, b, "1.ics", "2.ics", "3.ics")
	if strings.Count(b, "404 Not Found") != 2 || strings.Count(b, "200 OK") != 1 {
		t.Fatal(b)
	}
	if tok2 == tok {
		t.Fatal("token did not advance")
	}
	_, b = sync(tok2)
	lacks(t, b, ".ics")

	st, b, _ := c.req("REPORT", "/dav/calendars/alice/s/", `<d:sync-collection xmlns:d="DAV:"><d:sync-token>urn:galdav:sync:999999</d:sync-token><d:sync-level>1</d:sync-level><d:prop/></d:sync-collection>`)
	expect(t, st, 403, b)
	has(t, b, "valid-sync-token")
}

func TestAddressbookQuery(t *testing.T) {
	c := &client{t, newServer(t).URL}
	c.req("MKCOL", "/dav/addressbooks/alice/b/", `<mkcol xmlns="DAV:" xmlns:a="urn:ietf:params:xml:ns:carddav"><set><prop><resourcetype><collection/><a:addressbook/></resourcetype></prop></set></mkcol>`)
	card := func(uid, fn, email string) string {
		return "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:" + uid + "\r\nFN:" + fn + "\r\nN:;" + fn + ";;;\r\nEMAIL;TYPE=work:" + email + "\r\nEND:VCARD\r\n"
	}
	c.req("PUT", "/dav/addressbooks/alice/b/1.vcf", card("1", "Alice Doe", "alice@example.com"))
	c.req("PUT", "/dav/addressbooks/alice/b/2.vcf", card("2", "Bob Roe", "bob@example.org"))
	q := func(filter string) string {
		st, b, _ := c.req("REPORT", "/dav/addressbooks/alice/b/", `<a:addressbook-query xmlns:a="urn:ietf:params:xml:ns:carddav" xmlns:d="DAV:"><d:prop><d:getetag/><a:address-data/></d:prop>`+filter+`</a:addressbook-query>`)
		expect(t, st, 207, b)
		return b
	}
	b := q(`<a:filter><a:prop-filter name="FN"><a:text-match match-type="starts-with">bob</a:text-match></a:prop-filter></a:filter>`)
	has(t, b, "2.vcf", "Bob Roe")
	lacks(t, b, "1.vcf")
	b = q(`<a:filter test="anyof"><a:prop-filter name="FN"><a:text-match>alice</a:text-match></a:prop-filter><a:prop-filter name="EMAIL"><a:text-match match-type="ends-with">.org</a:text-match></a:prop-filter></a:filter>`)
	has(t, b, "1.vcf", "2.vcf")
	b = q(`<a:filter test="allof"><a:prop-filter name="FN"><a:text-match>alice</a:text-match></a:prop-filter><a:prop-filter name="EMAIL"><a:text-match match-type="ends-with">.org</a:text-match></a:prop-filter></a:filter>`)
	lacks(t, b, ".vcf")
	b = q(`<a:filter><a:prop-filter name="TEL"><a:is-not-defined/></a:prop-filter></a:filter>`)
	has(t, b, "1.vcf", "2.vcf")
	b = q(``)
	has(t, b, "1.vcf", "2.vcf")
}

func TestAddressbookQueryLimit(t *testing.T) {
	c := &client{t, newServer(t).URL}
	c.req("MKCOL", "/dav/addressbooks/alice/b/", `<mkcol xmlns="DAV:" xmlns:a="urn:ietf:params:xml:ns:carddav"><set><prop><resourcetype><collection/><a:addressbook/></resourcetype></prop></set></mkcol>`)
	for _, n := range []string{"1", "2", "3"} {
		c.req("PUT", "/dav/addressbooks/alice/b/"+n+".vcf", "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:"+n+"\r\nFN:P"+n+"\r\nN:;P"+n+";;;\r\nEND:VCARD\r\n")
	}
	q := func(limit string) string {
		st, b, _ := c.req("REPORT", "/dav/addressbooks/alice/b/", `<a:addressbook-query xmlns:a="urn:ietf:params:xml:ns:carddav" xmlns:d="DAV:"><d:prop><d:getetag/></d:prop>`+limit+`</a:addressbook-query>`)
		expect(t, st, 207, b)
		return b
	}
	b := q(`<a:limit><a:nresults>2</a:nresults></a:limit>`)
	if n := strings.Count(b, ".vcf"); n != 2 {
		t.Fatalf("%d objects: %s", n, b)
	}
	has(t, b, "507 Insufficient Storage", "number-of-matches-within-limits", "/dav/addressbooks/alice/b/</D:href>")
	b = q(`<a:limit><a:nresults>3</a:nresults></a:limit>`)
	has(t, b, "1.vcf", "2.vcf", "3.vcf")
	lacks(t, b, "507")
}
