package dav_test

import (
	"strings"
	"testing"
)

func TestPublicLinks(t *testing.T) {
	e := newSchedEnv(t, nil)
	a := e.alice
	tzEvent := func(uid string) string {
		return "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//t//EN\r\nBEGIN:VTIMEZONE\r\nTZID:Europe/Berlin\r\nBEGIN:STANDARD\r\nDTSTART:19701025T030000\r\nTZOFFSETFROM:+0200\r\nTZOFFSETTO:+0100\r\nEND:STANDARD\r\nEND:VTIMEZONE\r\n" +
			"BEGIN:VEVENT\r\nUID:" + uid + "\r\nDTSTAMP:20260101T000000Z\r\nDTSTART;TZID=Europe/Berlin:20260310T100000\r\nDTEND;TZID=Europe/Berlin:20260310T110000\r\nSUMMARY:" + uid + "\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	}
	for _, uid := range []string{"one", "two"} {
		st, body, _ := a.req("PUT", "/dav/calendars/alice/main/"+uid+".ics", tzEvent(uid))
		expect(t, st, 201, body)
	}
	st, body, _ := a.req("MKCOL", "/dav/addressbooks/alice/people/", `<D:mkcol xmlns:D="DAV:" xmlns:A="urn:ietf:params:xml:ns:carddav"><D:set><D:prop><D:resourcetype><D:collection/><A:addressbook/></D:resourcetype></D:prop></D:set></D:mkcol>`)
	expect(t, st, 201, body)
	st, body, _ = a.req("PUT", "/dav/addressbooks/alice/people/bob.vcf", "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Bob B\r\nN:B;Bob;;;\r\nUID:bob\r\nEND:VCARD\r\n")
	expect(t, st, 201, body)

	alice, _ := e.st.UserByName("alice")
	cal, _ := e.st.GetCollection(alice.ID, "calendar", "main")
	book, _ := e.st.GetCollection(alice.ID, "addressbook", "people")
	anon := who{t, e.url, "", ""}
	get := func(path string, hdr ...string) (int, string, map[string][]string) {
		return anon.req("GET", path, "", hdr...)
	}

	// No link, no access.
	st, _, _ = get("/public/" + strings.Repeat("a", 32) + ".ics")
	expect(t, st, 404, "")

	tok, err := e.st.NewPublicLink(cal.ID)
	if err != nil {
		t.Fatal(err)
	}
	st, feed, h := get("/public/" + tok + ".ics")
	expect(t, st, 200, feed)
	if !strings.HasPrefix(h["Content-Type"][0], "text/calendar") {
		t.Fatal(h["Content-Type"])
	}
	feed = unfold(feed)
	has(t, feed, "X-WR-CALNAME:main", "SUMMARY:one", "SUMMARY:two", "TZID=Europe/Berlin:20260310T100000")
	if strings.Count(feed, "BEGIN:VCALENDAR") != 1 || strings.Count(feed, "BEGIN:VTIMEZONE") != 1 {
		t.Fatalf("not merged into one calendar with one zone:\n%s", feed)
	}
	// The extension is optional, but must match the kind.
	st, _, _ = get("/public/" + tok)
	expect(t, st, 200, "")
	st, _, _ = get("/public/" + tok + ".vcf")
	expect(t, st, 404, "")

	// Conditional requests.
	etag := h["Etag"][0]
	st, _, _ = get("/public/"+tok+".ics", "If-None-Match", etag)
	expect(t, st, 304, "")
	// If-None-Match uses weak comparison, so W/ validators and lists match too.
	st, _, _ = get("/public/"+tok+".ics", "If-None-Match", "W/"+etag)
	expect(t, st, 304, "")
	st, _, _ = get("/public/"+tok+".ics", "If-None-Match", `"other", W/`+etag)
	expect(t, st, 304, "")
	st, feed, _ = get("/public/"+tok+".ics", "If-None-Match", `W/"other"`)
	expect(t, st, 200, feed)
	a.req("PUT", "/dav/calendars/alice/main/three.ics", tzEvent("three"))
	st, feed, _ = get("/public/"+tok+".ics", "If-None-Match", etag)
	expect(t, st, 200, feed)
	has(t, feed, "SUMMARY:three")

	// Address books come out as one vCard file.
	btok, _ := e.st.NewPublicLink(book.ID)
	st, vcf, h := get("/public/" + btok + ".vcf")
	expect(t, st, 200, vcf)
	has(t, vcf, "FN:Bob B")
	if !strings.HasPrefix(h["Content-Type"][0], "text/vcard") {
		t.Fatal(h["Content-Type"])
	}

	// A new address replaces the old one; turning it off ends access.
	tok2, _ := e.st.NewPublicLink(cal.ID)
	if tok2 == tok {
		t.Fatal("token not renewed")
	}
	st, _, _ = get("/public/" + tok + ".ics")
	expect(t, st, 404, "")
	st, _, _ = get("/public/" + tok2 + ".ics")
	expect(t, st, 200, "")
	e.st.DeletePublicLink(cal.ID)
	st, _, _ = get("/public/" + tok2 + ".ics")
	expect(t, st, 404, "")

	// Guessing is slowed down.
	limited := false
	for i := 0; i < 40 && !limited; i++ {
		st, _, _ = get("/public/" + strings.Repeat("b", 31) + string(rune('a'+i%6)) + ".ics")
		limited = st == 429
	}
	if !limited {
		t.Fatal("token guessing is not rate limited")
	}
}
