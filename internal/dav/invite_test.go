package dav_test

import (
	"regexp"
	"strings"
	"testing"
)

func notificationURIs(body string) []string {
	var out []string
	for _, h := range hrefs(body) {
		if strings.HasSuffix(h, ".xml") {
			out = append(out, h)
		}
	}
	return out
}

func TestShareInvitationsAndNotifications(t *testing.T) {
	e := newSchedEnvOpts(t, nil, true)
	a, b := e.alice, e.bob
	inviteProps := `<propfind xmlns="DAV:" xmlns:cs="http://calendarserver.org/ns/"><prop><cs:invite/></prop></propfind>`
	const bobNotes = "/dav/calendars/bob/notification/"
	notes := `<propfind xmlns="DAV:" xmlns:cs="http://calendarserver.org/ns/"><prop><getetag/><getcontenttype/><resourcetype/><cs:notificationtype/></prop></propfind>`

	st, body, _ := a.req("PUT", "/dav/calendars/alice/main/ev.ics", invite("ev", "Standup", "0", ""))
	expect(t, st, 201, body)

	// The principal points at the notification collection.
	_, body, _ = b.req("PROPFIND", "/dav/principals/bob/", `<propfind xmlns="DAV:" xmlns:cs="http://calendarserver.org/ns/"><prop><cs:notification-URL/></prop></propfind>`, "Depth", "0")
	has(t, body, "notification-URL>", bobNotes)

	// With acceptance required, the share does not appear until bob answers.
	st, body, _ = a.req("POST", "/dav/calendars/alice/main/", shareBody("set", "mailto:bob@example.com", "<CS:read-write/>"), "Content-Type", "application/xml")
	expect(t, st, 200, body)
	_, body, _ = a.req("PROPFIND", "/dav/calendars/alice/main/", inviteProps, "Depth", "0")
	has(t, body, "<S:invite-noresponse/>")
	lacks(t, body, "<S:invite-accepted/>")
	_, body, _ = b.req("PROPFIND", "/dav/calendars/bob/", `<propfind xmlns="DAV:"><prop><resourcetype/></prop></propfind>`, "Depth", "1")
	lacks(t, body, "/dav/calendars/bob/main/")
	st, body, _ = b.req("GET", "/dav/calendars/bob/main/ev.ics", "")
	expect(t, st, 404, body)

	// The invitation is in bob's notification collection.
	st, body, _ = b.req("PROPFIND", bobNotes, notes, "Depth", "1")
	expect(t, st, 207, body)
	has(t, body, "<S:notification/>", `<S:invite-notification shared-type="calendar"/>`, "text/xml")
	uris := notificationURIs(body)
	if len(uris) != 1 {
		t.Fatalf("notifications: %v\n%s", uris, body)
	}
	st, note, h := b.req("GET", uris[0], "")
	expect(t, st, 200, note)
	if !strings.HasPrefix(h["Content-Type"][0], "text/xml") {
		t.Fatal(h["Content-Type"])
	}
	has(t, note, "<CS:invite-noresponse/>", "<CS:read-write/>", "<D:href>/dav/calendars/alice/main/</D:href>",
		"<CS:summary>main</CS:summary>", "<D:href>mailto:bob@example.com</D:href>", "<CS:common-name>alice</CS:common-name>")
	uid := regexp.MustCompile(`<CS:uid>([0-9a-f]+)</CS:uid>`).FindStringSubmatch(note)[1]

	// Bob accepts through the home set; the calendar appears.
	reply := func(answer, extra string) (int, string) {
		st, out, _ := b.req("POST", "/dav/calendars/bob/", `<CS:invite-reply `+csNS+`><CS:`+answer+`/>`+extra+`</CS:invite-reply>`, "Content-Type", "application/xml")
		return st, out
	}
	st, body = reply("invite-accepted", `<CS:in-reply-to>`+uid+`</CS:in-reply-to>`)
	expect(t, st, 200, body)
	st, body, _ = b.req("GET", "/dav/calendars/bob/main/ev.ics", "")
	expect(t, st, 200, body)
	has(t, body, "Standup")
	_, note, _ = b.req("GET", uris[0], "")
	has(t, note, "<CS:invite-accepted/>")

	// Alice is told, and her invite list shows the answer.
	_, body, _ = a.req("PROPFIND", "/dav/calendars/alice/main/", inviteProps, "Depth", "0")
	has(t, body, "<S:invite-accepted/>")
	_, body, _ = a.req("PROPFIND", "/dav/calendars/alice/notification/", notes, "Depth", "1")
	has(t, body, "<S:invite-reply/>")
	aliceNotes := notificationURIs(body)
	if len(aliceNotes) != 1 {
		t.Fatalf("alice notifications: %v", aliceNotes)
	}
	_, note, _ = a.req("GET", aliceNotes[0], "")
	has(t, note, "<CS:invite-reply>", "<CS:invite-accepted/>", "<CS:in-reply-to>"+uid+"</CS:in-reply-to>", "<D:href>mailto:bob@example.com</D:href>")

	// Dismissing a notification is a DELETE; sync-collection reports it.
	_, body, _ = b.req("REPORT", bobNotes, `<d:sync-collection xmlns:d="DAV:"><d:sync-token/><d:sync-level>1</d:sync-level><d:prop><d:getetag/></d:prop></d:sync-collection>`)
	has(t, body, uris[0], "urn:whitegopher:sync:")
	st, body, _ = b.req("DELETE", uris[0], "")
	expect(t, st, 204, body)
	st, body, _ = b.req("GET", uris[0], "")
	expect(t, st, 404, body)
	st, body, _ = b.req("PUT", bobNotes+"x.xml", "<x/>")
	expect(t, st, 403, body)

	// Removing the share with DAV:share-resource; re-inviting asks again, and
	// a decline (named by hosturl alone) keeps it out of bob's home.
	srBody := func(access string) string {
		return `<D:share-resource xmlns:D="DAV:"><D:sharee><D:href>mailto:bob@example.com</D:href><D:share-access><D:` + access + `/></D:share-access></D:sharee></D:share-resource>`
	}
	st, body, _ = a.req("POST", "/dav/calendars/alice/main/", srBody("no-access"), "Content-Type", "application/xml")
	expect(t, st, 200, body)
	st, body, _ = b.req("GET", "/dav/calendars/bob/main/ev.ics", "")
	expect(t, st, 404, body)
	st, body, _ = a.req("POST", "/dav/calendars/alice/main/", srBody("read"), "Content-Type", "application/xml")
	expect(t, st, 200, body)
	_, body, _ = a.req("PROPFIND", "/dav/calendars/alice/main/", inviteProps, "Depth", "0")
	has(t, body, "<S:invite-noresponse/>", "<S:read/>")
	st, body = reply("invite-declined", `<CS:hosturl><D:href>/dav/calendars/alice/main/</D:href></CS:hosturl>`)
	expect(t, st, 200, body)
	_, body, _ = a.req("PROPFIND", "/dav/calendars/alice/main/", inviteProps, "Depth", "0")
	has(t, body, "<S:invite-declined/>")
	st, body, _ = b.req("GET", "/dav/calendars/bob/main/ev.ics", "")
	expect(t, st, 404, body)
	st, body = reply("invite-accepted", `<CS:in-reply-to>nonsense</CS:in-reply-to>`)
	expect(t, st, 404, body)
	st, body, _ = a.req("POST", "/dav/calendars/alice/main/", srBody("shared-owner"), "Content-Type", "application/xml")
	expect(t, st, 403, body)

	// The server advertises both sharing protocols.
	resp, _ := do(t, "OPTIONS", e.url+"/dav/", "alice", "alicepw", "", nil)
	has(t, resp.Header.Get("DAV"), "calendarserver-sharing", "resource-sharing")
}
