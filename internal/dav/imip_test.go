package dav_test

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/bitflipp/galdav/internal/sched"
)

func imipMail(from, authResults, method, ics string) string {
	var b strings.Builder
	b.WriteString("From: " + from + "\r\nTo: galdav@example.com\r\nSubject: x\r\n")
	if authResults != "" {
		b.WriteString("Authentication-Results: mx.example.com; " + authResults + "\r\n")
	}
	b.WriteString("MIME-Version: 1.0\r\nContent-Type: multipart/alternative; boundary=XX\r\n\r\n")
	b.WriteString("--XX\r\nContent-Type: text/plain\r\n\r\nSee attachment\r\n")
	b.WriteString("--XX\r\nContent-Type: text/calendar; method=" + method + "\r\nContent-Transfer-Encoding: base64\r\n\r\n")
	enc := base64.StdEncoding.EncodeToString([]byte(ics))
	for len(enc) > 60 {
		b.WriteString(enc[:60] + "\r\n")
		enc = enc[60:]
	}
	b.WriteString(enc + "\r\n--XX--\r\n")
	return b.String()
}

func itip(method, uid, organizer, attendee, partstat, extra string) string {
	return "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//ext//EN\r\nMETHOD:" + method + "\r\nBEGIN:VEVENT\r\nUID:" + uid +
		"\r\nDTSTAMP:20260101T000000Z\r\nDTSTART:20260310T100000Z\r\nDTEND:20260310T110000Z\r\nSUMMARY:Sync\r\nSEQUENCE:0\r\n" +
		"ORGANIZER:mailto:" + organizer + "\r\nATTENDEE;PARTSTAT=" + partstat + ":mailto:" + attendee + "\r\n" + extra + "END:VEVENT\r\nEND:VCALENDAR\r\n"
}

func TestInboundIMIP(t *testing.T) {
	e := newSchedEnv(t, nil)
	a, b := e.alice, e.bob
	svc := sched.New(e.st, "/dav", nil)
	receive := func(msg string, auth bool) sched.InboundResult {
		t.Helper()
		res, err := svc.ReceiveMail(strings.NewReader(msg), auth)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	const dkim = "dkim=pass header.d=external.org"

	// Alice invites Carol (external, no outbound mail here); Carol's answer arrives by email.
	path := "/dav/calendars/alice/main/meet.ics"
	st, body, _ := a.req("PUT", path, invite("meet", "Planning", "0", "", carolAtt))
	expect(t, st, 201, body)
	reply := itip("REPLY", "meet", "alice@example.com", "carol@external.org", "ACCEPTED", "")

	// Forged or unvouched mail changes nothing.
	if r := receive(imipMail("Mallory <mallory@evil.org>", dkim, "REPLY", reply), true); r.Ignored == "" || len(r.Delivered) != 0 {
		t.Fatalf("reply from a stranger accepted: %+v", r)
	}
	if r := receive(imipMail("carol@external.org", "", "REPLY", reply), true); r.Ignored == "" || !strings.Contains(r.Ignored, "authenticated") {
		t.Fatalf("unauthenticated reply accepted: %+v", r)
	}
	_, got, _ := a.req("GET", path, "")
	lacks(t, unfold(got), "PARTSTAT=ACCEPTED")

	// The real reply is applied to the organizer's copy and lands in the inbox.
	r := receive(imipMail("Carol <Carol@External.org>", dkim, "REPLY", reply), true)
	if r.Ignored != "" || len(r.Delivered) != 1 || r.Delivered[0] != "alice" {
		t.Fatalf("%+v", r)
	}
	_, got, _ = a.req("GET", path, "")
	has(t, unfold(got), "PARTSTAT=ACCEPTED", "mailto:carol@external.org")
	st, body, _ = a.req("PROPFIND", "/dav/calendars/alice/inbox/", `<propfind xmlns="DAV:"><prop><resourcetype/></prop></propfind>`, "Depth", "1")
	expect(t, st, 207, body)
	has(t, body, "itip-")

	// A reply naming someone else's attendee line is dropped, not trusted.
	other := itip("REPLY", "meet", "alice@example.com", "dave@external.org", "DECLINED", "")
	if r := receive(imipMail("carol@external.org", dkim, "REPLY", other), true); len(r.Delivered) != 0 {
		t.Fatalf("reply for another attendee accepted: %+v", r)
	}

	// An external organizer invites Bob: the event appears in his calendar.
	inv := itip("REQUEST", "ext-1", "boss@external.org", "bob@example.com", "NEEDS-ACTION", "")
	if r := receive(imipMail("mallory@evil.org", dkim, "REQUEST", inv), true); len(r.Delivered) != 0 {
		t.Fatalf("invitation from a non-organizer accepted: %+v", r)
	}
	r = receive(imipMail("boss@external.org", dkim, "REQUEST", inv), true)
	if len(r.Delivered) != 1 || r.Delivered[0] != "bob" {
		t.Fatalf("%+v", r)
	}
	st, mine, _ := b.req("GET", "/dav/calendars/bob/bobcal/ext-1.ics", "")
	expect(t, st, 200, mine)
	has(t, mine, "SUMMARY:Sync")

	// The same organizer may update or cancel it; somebody else may not.
	cancel := strings.Replace(inv, "METHOD:REQUEST", "METHOD:CANCEL", 1)
	if r := receive(imipMail("evil@evil.org", dkim, "CANCEL", strings.Replace(cancel, "boss@external.org", "evil@evil.org", 1)), true); len(r.Delivered) != 0 {
		t.Fatalf("takeover of another organizer's event: %+v", r)
	}
	if r := receive(imipMail("boss@external.org", dkim, "CANCEL", cancel), true); len(r.Delivered) != 1 {
		t.Fatalf("%+v", r)
	}
	_, mine, _ = b.req("GET", "/dav/calendars/bob/bobcal/ext-1.ics", "")
	has(t, mine, "STATUS:CANCELLED")

	// An invitation cannot overwrite an event Bob organizes himself.
	st, body, _ = b.req("PUT", "/dav/calendars/bob/bobcal/own.ics", invite("own", "Mine", "0", "", "ATTENDEE;PARTSTAT=NEEDS-ACTION:mailto:zed@external.org"))
	expect(t, st, 201, body)
	hijack := itip("REQUEST", "own", "boss@external.org", "bob@example.com", "NEEDS-ACTION", "")
	if r := receive(imipMail("boss@external.org", dkim, "REQUEST", hijack), true); len(r.Delivered) != 0 {
		t.Fatalf("overwrote the user's own event: %+v", r)
	}
	_, own, _ := b.req("GET", "/dav/calendars/bob/bobcal/own.ics", "")
	has(t, own, "SUMMARY:Mine")

	// Mail without a calendar part is not an error.
	_, err := svc.ReceiveMail(strings.NewReader("From: x@y.org\r\nSubject: hi\r\n\r\nhello\r\n"), false)
	if err != sched.ErrNotITIP {
		t.Fatalf("plain mail: %v", err)
	}
}
