package dav_test

import (
	"strings"
	"testing"
)

const csNS = `xmlns:D="DAV:" xmlns:CS="http://calendarserver.org/ns/"`

func shareBody(op, who, access string) string {
	return `<CS:share ` + csNS + `><CS:` + op + `><D:href>` + who + `</D:href>` + access + `</CS:` + op + `></CS:share>`
}

func TestSharedCalendar(t *testing.T) {
	e := newSchedEnv(t, nil)
	a, b := e.alice, e.bob
	home := `<propfind xmlns="DAV:"><prop><resourcetype/><displayname/></prop></propfind>`

	st, body, _ := a.req("PUT", "/dav/calendars/alice/main/ev1.ics", invite("ev1", "Team lunch", "0", ""))
	expect(t, st, 201, body)

	// Not visible before sharing.
	st, body, _ = b.req("GET", "/dav/calendars/bob/main/ev1.ics", "")
	expect(t, st, 404, body)
	st, body, _ = b.req("GET", "/dav/calendars/alice/main/ev1.ics", "")
	expect(t, st, 403, body)

	// Alice shares read-only; the CalendarServer sharing properties appear.
	st, body, _ = a.req("POST", "/dav/calendars/alice/main/", shareBody("set", "mailto:bob@example.com", "<CS:read/>"), "Content-Type", "application/xml")
	expect(t, st, 200, body)
	_, body, _ = a.req("PROPFIND", "/dav/calendars/alice/main/", `<propfind xmlns="DAV:" xmlns:cs="http://calendarserver.org/ns/"><prop><resourcetype/><cs:invite/><cs:allowed-sharing-modes/></prop></propfind>`, "Depth", "0")
	has(t, body, "<S:shared-owner/>", "<S:can-be-shared/>", "<D:href>mailto:bob@example.com</D:href>", "<S:invite-accepted/>", "<S:read/>")

	// Bob sees it in his home set and can read it every way.
	st, body, _ = b.req("PROPFIND", "/dav/calendars/bob/", home, "Depth", "1")
	expect(t, st, 207, body)
	has(t, body, "/dav/calendars/bob/main/", "/dav/calendars/bob/bobcal/", "<S:shared/>")
	st, body, _ = b.req("GET", "/dav/calendars/bob/main/ev1.ics", "")
	expect(t, st, 200, body)
	has(t, body, "Team lunch")
	_, body, _ = b.req("PROPFIND", "/dav/calendars/bob/main/", `<propfind xmlns="DAV:"><prop><getetag/><owner/><current-user-privilege-set/></prop></propfind>`, "Depth", "1")
	has(t, body, "/dav/calendars/bob/main/ev1.ics", "<D:href>/dav/principals/alice/</D:href>", "<D:read/>")
	lacks(t, body, "<D:write/>", "/dav/calendars/alice/")
	st, body, _ = b.req("REPORT", "/dav/calendars/bob/main/", `<c:calendar-query `+calNS+`><d:prop><d:getetag/><c:calendar-data/></d:prop>
		<c:filter><c:comp-filter name="VCALENDAR"><c:comp-filter name="VEVENT"/></c:comp-filter></c:filter></c:calendar-query>`)
	expect(t, st, 207, body)
	has(t, body, "Team lunch", "/dav/calendars/bob/main/ev1.ics")
	st, body, _ = b.req("REPORT", "/dav/calendars/bob/main/", `<d:sync-collection xmlns:d="DAV:"><d:sync-token/><d:sync-level>1</d:sync-level><d:prop><d:getetag/></d:prop></d:sync-collection>`)
	expect(t, st, 207, body)
	has(t, body, "ev1.ics", "urn:galdav:sync:")

	// Read-only means read-only.
	st, body, _ = b.req("PUT", "/dav/calendars/bob/main/new.ics", invite("new", "Sneaky", "0", ""))
	expect(t, st, 403, body)
	has(t, body, "need-privileges")
	st, body, _ = b.req("DELETE", "/dav/calendars/bob/main/ev1.ics", "")
	expect(t, st, 403, body)
	st, body, _ = a.req("GET", "/dav/calendars/alice/main/ev1.ics", "")
	expect(t, st, 200, body)

	// Sharees customise their own view without touching the owner's.
	st, body, _ = b.req("PROPPATCH", "/dav/calendars/bob/main/", `<propertyupdate xmlns="DAV:" xmlns:i="http://apple.com/ns/ical/"><set><prop><displayname>Alices events</displayname><i:calendar-color>#abcdef</i:calendar-color></prop></set></propertyupdate>`)
	expect(t, st, 207, body)
	has(t, body, "200 OK")
	_, body, _ = b.req("PROPFIND", "/dav/calendars/bob/main/", `<propfind xmlns="DAV:" xmlns:i="http://apple.com/ns/ical/"><prop><displayname/><i:calendar-color/></prop></propfind>`, "Depth", "0")
	has(t, body, "Alices events", "#abcdef")
	_, body, _ = a.req("PROPFIND", "/dav/calendars/alice/main/", `<propfind xmlns="DAV:" xmlns:i="http://apple.com/ns/ical/"><prop><displayname/><i:calendar-color/></prop></propfind>`, "Depth", "0")
	lacks(t, body, "Alices events", "#abcdef")
	st, body, _ = b.req("PROPPATCH", "/dav/calendars/bob/main/", `<propertyupdate xmlns="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav"><set><prop><c:calendar-description>mine now</c:calendar-description></prop></set></propertyupdate>`)
	expect(t, st, 207, body)
	has(t, body, "403")
	// A sharee cannot reshare, nor create a collection under a mounted name.
	st, body, _ = b.req("POST", "/dav/calendars/bob/main/", shareBody("set", "mailto:alice@example.com", "<CS:read/>"), "Content-Type", "application/xml")
	expect(t, st, 403, body)
	st, body, _ = b.req("MKCALENDAR", "/dav/calendars/bob/main/", "")
	expect(t, st, 405, body)

	// Upgrade to read-write: Bob's edits land in Alice's calendar and sync to her.
	st, body, _ = a.req("POST", "/dav/calendars/alice/main/", shareBody("set", "mailto:bob@example.com", "<CS:read-write/>"), "Content-Type", "application/xml")
	expect(t, st, 200, body)
	_, body, _ = b.req("PROPFIND", "/dav/calendars/bob/main/", `<propfind xmlns="DAV:"><prop><current-user-privilege-set/></prop></propfind>`, "Depth", "0")
	has(t, body, "<D:write/>")
	st, body, _ = b.req("PUT", "/dav/calendars/bob/main/by-bob.ics", invite("by-bob", "Bob was here", "0", ""))
	expect(t, st, 201, body)
	st, body, _ = a.req("GET", "/dav/calendars/alice/main/by-bob.ics", "")
	expect(t, st, 200, body)
	has(t, body, "Bob was here")
	st, body, _ = b.req("DELETE", "/dav/calendars/bob/main/ev1.ics", "")
	expect(t, st, 204, body)
	st, body, _ = a.req("GET", "/dav/calendars/alice/main/ev1.ics", "")
	expect(t, st, 404, body)

	// Shared events do not count against the sharee's availability.
	req := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//t//EN\r\nMETHOD:REQUEST\r\nBEGIN:VFREEBUSY\r\nUID:q\r\nDTSTAMP:20260101T000000Z\r\n" +
		"DTSTART:20260310T000000Z\r\nDTEND:20260311T000000Z\r\nORGANIZER:mailto:alice@example.com\r\nATTENDEE:mailto:bob@example.com\r\nEND:VFREEBUSY\r\nEND:VCALENDAR\r\n"
	_, body, _ = a.req("POST", "/dav/calendars/alice/outbox/", req, "Content-Type", "text/calendar")
	has(t, body, "2.0;Success")
	lacks(t, unfold(body), "FBTYPE=BUSY:")

	// Stopping the share removes it from Bob's side; Alice keeps her data.
	st, body, _ = a.req("POST", "/dav/calendars/alice/main/", shareBody("remove", "mailto:bob@example.com", ""), "Content-Type", "application/xml")
	expect(t, st, 200, body)
	st, body, _ = b.req("GET", "/dav/calendars/bob/main/by-bob.ics", "")
	expect(t, st, 404, body)
	st, body, _ = a.req("GET", "/dav/calendars/alice/main/by-bob.ics", "")
	expect(t, st, 200, body)
	_, body, _ = a.req("PROPFIND", "/dav/calendars/alice/main/", `<propfind xmlns="DAV:"><prop><resourcetype/></prop></propfind>`, "Depth", "0")
	lacks(t, body, "shared-owner")
}

func TestLeavingAndBadShares(t *testing.T) {
	e := newSchedEnv(t, nil)
	a, b := e.alice, e.bob
	a.req("PUT", "/dav/calendars/alice/main/e.ics", invite("e", "Keep me", "0", ""))
	a.req("POST", "/dav/calendars/alice/main/", shareBody("set", "/dav/principals/bob/", "<CS:read-write/>"), "Content-Type", "application/xml") // principal URL form

	// DELETE on a mounted collection leaves the share and spares the data.
	st, body, _ := b.req("DELETE", "/dav/calendars/bob/main/", "")
	expect(t, st, 204, body)
	st, body, _ = b.req("GET", "/dav/calendars/bob/main/e.ics", "")
	expect(t, st, 404, body)
	st, body, _ = a.req("GET", "/dav/calendars/alice/main/e.ics", "")
	expect(t, st, 200, body)

	// Bad requests change nothing.
	for name, tc := range map[string]string{
		"unknown user": shareBody("set", "mailto:nobody@example.com", "<CS:read/>"),
		"self":         shareBody("set", "mailto:alice@example.com", "<CS:read/>"),
	} {
		st, body, _ = a.req("POST", "/dav/calendars/alice/main/", tc, "Content-Type", "application/xml")
		if st != 400 {
			t.Errorf("%s: %d %s", name, st, body)
		}
	}
	st, body, _ = a.req("POST", "/dav/calendars/alice/main/", `<x/>`, "Content-Type", "application/xml")
	expect(t, st, 403, body)
	st, body, _ = a.req("POST", "/dav/calendars/alice/inbox/", shareBody("set", "mailto:bob@example.com", "<CS:read/>"), "Content-Type", "application/xml")
	expect(t, st, 403, body)
	if m, _ := e.st.ListMounts(mustUser(t, e, "bob"), "calendar"); len(m) != 0 {
		t.Fatalf("bad requests created mounts: %+v", m)
	}
}

func mustUser(t *testing.T, e *env, name string) int64 {
	t.Helper()
	u, err := e.st.UserByName(name)
	if err != nil {
		t.Fatal(err)
	}
	return u.ID
}

func TestSharedAddressBookAndNameCollision(t *testing.T) {
	e := newSchedEnv(t, nil)
	a, b := e.alice, e.bob
	mk := `<mkcol xmlns="DAV:" xmlns:a="urn:ietf:params:xml:ns:carddav"><set><prop><resourcetype><collection/><a:addressbook/></resourcetype><displayname>Contacts</displayname></prop></set></mkcol>`
	for _, w := range []struct {
		who  string
		path string
	}{{"alice", "/dav/addressbooks/alice/contacts/"}, {"bob", "/dav/addressbooks/bob/contacts/"}} {
		who := a
		if w.who == "bob" {
			who = b
		}
		st, body, _ := who.req("MKCOL", w.path, mk)
		expect(t, st, 201, body)
	}
	st, body, _ := a.req("PUT", "/dav/addressbooks/alice/contacts/c.vcf", vcf)
	expect(t, st, 201, body)
	st, body, _ = a.req("POST", "/dav/addressbooks/alice/contacts/", shareBody("set", "mailto:bob@example.com", "<CS:read/>"), "Content-Type", "application/xml")
	expect(t, st, 200, body)

	// Bob already has "contacts", so the mount gets a distinct name.
	_, body, _ = b.req("PROPFIND", "/dav/addressbooks/bob/", `<propfind xmlns="DAV:"><prop><displayname/></prop></propfind>`, "Depth", "1")
	has(t, body, "/dav/addressbooks/bob/contacts/", "/dav/addressbooks/bob/alice-contacts/")
	st, body, _ = b.req("GET", "/dav/addressbooks/bob/alice-contacts/c.vcf", "")
	expect(t, st, 200, body)
	st, body, _ = b.req("REPORT", "/dav/addressbooks/bob/alice-contacts/", `<a:addressbook-query xmlns:a="urn:ietf:params:xml:ns:carddav" xmlns:d="DAV:"><d:prop><d:getetag/><a:address-data/></d:prop><a:filter><a:prop-filter name="FN"><a:text-match>bob</a:text-match></a:prop-filter></a:filter></a:addressbook-query>`)
	expect(t, st, 207, body)
	has(t, body, "c.vcf")
	st, body, _ = b.req("PUT", "/dav/addressbooks/bob/alice-contacts/x.vcf", strings.Replace(vcf, "c1", "x1", 1))
	expect(t, st, 403, body)
	// Bob's own "contacts" is untouched and writable.
	st, body, _ = b.req("PUT", "/dav/addressbooks/bob/contacts/own.vcf", strings.Replace(vcf, "c1", "own", 1))
	expect(t, st, 201, body)
}

func TestSharedCalendarNoScheduling(t *testing.T) {
	// Edits through a read-write share are plain writes: no iTIP is sent on
	// behalf of the owner.
	e := newSchedEnv(t, nil)
	a, b := e.alice, e.bob
	a.req("POST", "/dav/calendars/alice/main/", shareBody("set", "mailto:bob@example.com", "<CS:read-write/>"), "Content-Type", "application/xml")
	carol := "ATTENDEE;PARTSTAT=NEEDS-ACTION:mailto:alice@example.com"
	st, body, h := b.req("PUT", "/dav/calendars/bob/main/s.ics", invite("s", "Delegated", "0", "", carol))
	expect(t, st, 201, body)
	if len(h["Etag"]) == 0 {
		t.Fatal("object was rewritten by scheduling")
	}
	_, body, _ = a.req("PROPFIND", "/dav/calendars/alice/inbox/", `<propfind xmlns="DAV:"><prop><getetag/></prop></propfind>`, "Depth", "1")
	lacks(t, body, "itip-")
}
