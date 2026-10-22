package dav_test

import (
	"bufio"
	"net"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/bitflipp/whitegopher/internal/auth"
	"github.com/bitflipp/whitegopher/internal/dav"
	"github.com/bitflipp/whitegopher/internal/sched"
	"github.com/bitflipp/whitegopher/internal/sharing"
	"github.com/bitflipp/whitegopher/internal/store"
	"github.com/bitflipp/whitegopher/internal/storetest"
)

type who struct {
	t          *testing.T
	url        string
	user, pass string
}

func (w who) req(method, path, body string, hdr ...string) (int, string, map[string][]string) {
	w.t.Helper()
	h := map[string]string{}
	for i := 0; i+1 < len(hdr); i += 2 {
		h[hdr[i]] = hdr[i+1]
	}
	resp, out := do(w.t, method, w.url+path, w.user, w.pass, body, h)
	return resp.StatusCode, out, resp.Header
}

func unfold(s string) string { return strings.ReplaceAll(s, "\r\n ", "") }

type env struct {
	t          *testing.T
	st         *store.Store
	url        string
	alice, bob who
}

func newSchedEnv(t *testing.T, mail sched.Mailer) *env { return newSchedEnvOpts(t, mail, false) }

// newSchedEnvOpts is newSchedEnv with the option to make new shares wait for
// the sharee's acceptance.
func newSchedEnvOpts(t *testing.T, mail sched.Mailer, requireAccept bool) *env {
	t.Helper()
	st := storetest.New(t)
	for _, n := range []string{"alice", "bob"} {
		if _, err := st.CreateUser(store.User{Username: n, Email: strings.ToUpper(n[:1]) + n[1:] + "@Example.com", DigestHA1: auth.HA1(n, "R", n+"pw")}); err != nil {
			t.Fatal(err)
		}
	}
	s := &dav.Server{Store: st, Auth: &auth.Authenticator{Store: st, Realm: "R"}, BasePath: "/dav", Sched: sched.New(st, "/dav", mail),
		Sharing: &sharing.Service{Store: st, BasePath: "/dav", RequireAccept: requireAccept}}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	e := &env{t: t, st: st, url: ts.URL,
		alice: who{t, ts.URL, "alice", "alicepw"}, bob: who{t, ts.URL, "bob", "bobpw"}}
	for _, p := range []struct {
		w    who
		slug string
	}{{e.alice, "main"}, {e.bob, "bobcal"}} {
		st, b, _ := p.w.req("MKCALENDAR", "/dav/calendars/"+p.w.user+"/"+p.slug+"/", "")
		expect(t, st, 201, b)
	}
	return e
}

func invite(uid, summary, seq, extra string, attendees ...string) string {
	var b strings.Builder
	b.WriteString("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//t//EN\r\nBEGIN:VEVENT\r\nUID:" + uid + "\r\nDTSTAMP:20260101T000000Z\r\n")
	b.WriteString("DTSTART:20260310T100000Z\r\nDTEND:20260310T110000Z\r\nSUMMARY:" + summary + "\r\nSEQUENCE:" + seq + "\r\n")
	b.WriteString("ORGANIZER;CN=Alice:mailto:alice@example.com\r\n")
	for _, a := range attendees {
		b.WriteString(a + "\r\n")
	}
	b.WriteString(extra + "END:VEVENT\r\nEND:VCALENDAR\r\n")
	return b.String()
}

const (
	bobAtt   = "ATTENDEE;CN=Bob;PARTSTAT=NEEDS-ACTION;RSVP=TRUE:mailto:bob@example.com"
	carolAtt = "ATTENDEE;PARTSTAT=NEEDS-ACTION;RSVP=TRUE:mailto:carol@external.org"
)

func TestInvitationLifecycle(t *testing.T) {
	e := newSchedEnv(t, nil)
	a, b := e.alice, e.bob
	path := "/dav/calendars/alice/main/meet.ics"

	// Alice invites Bob (local) and Carol (external, no mail configured).
	st, body, h := a.req("PUT", path, invite("meet", "Planning", "0", "", bobAtt, carolAtt))
	expect(t, st, 201, body)
	if len(h["Etag"]) != 0 {
		t.Fatal("ETag must be omitted when the server rewrote the object")
	}
	_, got, _ := a.req("GET", path, "")
	got = unfold(got)
	has(t, got, "SCHEDULE-STATUS=1.2:mailto:bob@example.com", "SCHEDULE-STATUS=5.3:mailto:carol@external.org")

	// Bob's inbox holds the REQUEST and his default calendar the event.
	st, body, _ = b.req("PROPFIND", "/dav/calendars/bob/inbox/", `<propfind xmlns="DAV:"><prop><resourcetype/></prop></propfind>`, "Depth", "1")
	expect(t, st, 207, body)
	has(t, body, "schedule-inbox", "itip-")
	inboxItem := hrefs(body)[1]
	_, item, _ := b.req("GET", strings.TrimPrefix(inboxItem, ""), "")
	has(t, item, "METHOD:REQUEST", "SUMMARY:Planning")
	st, mine, _ := b.req("GET", "/dav/calendars/bob/bobcal/meet.ics", "")
	expect(t, st, 200, mine)
	mine = unfold(mine)
	has(t, mine, "SUMMARY:Planning", "PARTSTAT=NEEDS-ACTION")
	lacks(t, mine, "METHOD:", "SCHEDULE-STATUS")

	// Bob accepts by editing his copy; Alice's copy and inbox update.
	accepted := strings.Replace(mine, "PARTSTAT=NEEDS-ACTION;RSVP=TRUE:mailto:bob@example.com", "PARTSTAT=ACCEPTED:mailto:bob@example.com", 1)
	if accepted == mine {
		t.Fatalf("test edit did not apply to:\n%s", mine)
	}
	st, body, _ = b.req("PUT", "/dav/calendars/bob/bobcal/meet.ics", accepted)
	expect(t, st, 204, body)
	_, got, _ = a.req("GET", path, "")
	got = unfold(got)
	has(t, got, "PARTSTAT=ACCEPTED", "mailto:bob@example.com")
	if !strings.Contains(got, "SCHEDULE-STATUS=2.0") {
		t.Fatalf("reply not recorded:\n%s", got)
	}
	_, ainbox, _ := a.req("PROPFIND", "/dav/calendars/alice/inbox/", `<propfind xmlns="DAV:"><prop><getetag/></prop></propfind>`, "Depth", "1")
	_, reply, _ := a.req("GET", hrefs(ainbox)[1], "")
	has(t, reply, "METHOD:REPLY", "PARTSTAT=ACCEPTED")

	// Alice reschedules (SEQUENCE bump): Bob's copy follows and asks again.
	moved := strings.Replace(invite("meet", "Planning v2", "1", "", bobAtt, carolAtt), "DTSTART:20260310T100000Z", "DTSTART:20260311T100000Z", 1)
	st, body, _ = a.req("PUT", path, moved)
	expect(t, st, 204, body)
	_, mine, _ = b.req("GET", "/dav/calendars/bob/bobcal/meet.ics", "")
	mine = unfold(mine)
	has(t, mine, "SUMMARY:Planning v2", "DTSTART:20260311T100000Z", "PARTSTAT=NEEDS-ACTION", "SEQUENCE:1")

	// Alice drops Carol: only Carol would get a CANCEL, Bob is untouched.
	st, body, _ = a.req("PUT", path, invite("meet", "Planning v2", "1", "", bobAtt))
	expect(t, st, 204, body)

	// Alice cancels by deleting; Bob's copy is marked cancelled.
	st, body, _ = a.req("DELETE", path, "")
	expect(t, st, 204, body)
	_, mine, _ = b.req("GET", "/dav/calendars/bob/bobcal/meet.ics", "")
	has(t, unfold(mine), "STATUS:CANCELLED")
	_, item, _ = b.req("GET", inboxItem, "")
	has(t, item, "METHOD:CANCEL")
}

func TestAttendeeDeclinesByDeleting(t *testing.T) {
	e := newSchedEnv(t, nil)
	a, b := e.alice, e.bob
	a.req("PUT", "/dav/calendars/alice/main/m.ics", invite("m", "Sync", "0", "", bobAtt))
	st, body, _ := b.req("DELETE", "/dav/calendars/bob/bobcal/m.ics", "")
	expect(t, st, 204, body)
	_, got, _ := a.req("GET", "/dav/calendars/alice/main/m.ics", "")
	has(t, unfold(got), "PARTSTAT=DECLINED")
}

func TestScheduleAgentClientIsLeftAlone(t *testing.T) {
	e := newSchedEnv(t, nil)
	att := "ATTENDEE;SCHEDULE-AGENT=CLIENT;PARTSTAT=NEEDS-ACTION:mailto:bob@example.com"
	st, body, h := e.alice.req("PUT", "/dav/calendars/alice/main/c.ics", invite("c", "Client side", "0", "", att))
	expect(t, st, 201, body)
	if len(h["Etag"]) == 0 {
		t.Fatal("nothing was rewritten, so the ETag should be present")
	}
	_, body, _ = e.bob.req("PROPFIND", "/dav/calendars/bob/inbox/", `<propfind xmlns="DAV:"><prop><getetag/></prop></propfind>`, "Depth", "1")
	lacks(t, body, "itip-")
	st, body, _ = e.bob.req("GET", "/dav/calendars/bob/bobcal/c.ics", "")
	expect(t, st, 404, body)
}

func TestNoSchedulingWithoutAttendees(t *testing.T) {
	e := newSchedEnv(t, nil)
	st, body, h := e.alice.req("PUT", "/dav/calendars/alice/main/solo.ics", invite("solo", "Alone", "0", ""))
	expect(t, st, 201, body)
	if len(h["Etag"]) == 0 {
		t.Fatal("plain event should keep its ETag")
	}
}

func TestFreeBusy(t *testing.T) {
	e := newSchedEnv(t, nil)
	a, b := e.alice, e.bob
	put := func(uri, uid, start, end, extra string) {
		st, body, _ := a.req("PUT", "/dav/calendars/alice/main/"+uri, "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//t//EN\r\nBEGIN:VEVENT\r\nUID:"+uid+
			"\r\nDTSTAMP:20260101T000000Z\r\nDTSTART:"+start+"\r\nDTEND:"+end+"\r\n"+extra+"END:VEVENT\r\nEND:VCALENDAR\r\n")
		expect(t, st, 201, body)
	}
	put("a.ics", "a", "20260310T100000Z", "20260310T110000Z", "")
	put("b.ics", "b", "20260310T103000Z", "20260310T120000Z", "")
	put("c.ics", "c", "20260310T150000Z", "20260310T160000Z", "STATUS:TENTATIVE\r\n")
	put("d.ics", "d", "20260310T170000Z", "20260310T180000Z", "TRANSP:TRANSPARENT\r\n")

	// free-busy-query REPORT on the calendar.
	st, body, h := a.req("REPORT", "/dav/calendars/alice/main/", `<c:free-busy-query `+calNS+`><c:time-range start="20260310T000000Z" end="20260311T000000Z"/></c:free-busy-query>`)
	expect(t, st, 200, body)
	if !strings.HasPrefix(h["Content-Type"][0], "text/calendar") {
		t.Fatal(h["Content-Type"])
	}
	body = unfold(body)
	has(t, body, "FREEBUSY;FBTYPE=BUSY:20260310T100000Z/20260310T120000Z", "FREEBUSY;FBTYPE=BUSY-TENTATIVE:20260310T150000Z/20260310T160000Z")
	lacks(t, body, "170000Z")
	st, body, _ = a.req("REPORT", "/dav/calendars/alice/main/", `<c:free-busy-query `+calNS+`><c:time-range start="20260310T000000Z"/></c:free-busy-query>`)
	expect(t, st, 403, body)

	// Bob asks for Alice's availability through his outbox.
	req := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//t//EN\r\nMETHOD:REQUEST\r\nBEGIN:VFREEBUSY\r\nUID:q1\r\nDTSTAMP:20260101T000000Z\r\n" +
		"DTSTART:20260310T000000Z\r\nDTEND:20260311T000000Z\r\nORGANIZER:mailto:bob@example.com\r\n" +
		"ATTENDEE:mailto:alice@example.com\r\nATTENDEE:mailto:carol@external.org\r\nATTENDEE:mailto:nobody@@\r\nEND:VFREEBUSY\r\nEND:VCALENDAR\r\n"
	st, body, _ = b.req("POST", "/dav/calendars/bob/outbox/", req, "Content-Type", "text/calendar")
	expect(t, st, 200, body)
	body = unfold(body)
	has(t, body, "schedule-response", "<D:href>mailto:alice@example.com</D:href>", "2.0;Success", "METHOD:REPLY",
		"FREEBUSY;FBTYPE=BUSY:20260310T100000Z/20260310T120000Z", "5.3;No scheduling support", "ATTENDEE:mailto:alice@example.com")

	// Bob cannot ask on someone else's behalf.
	forged := strings.Replace(req, "ORGANIZER:mailto:bob@example.com", "ORGANIZER:mailto:alice@example.com", 1)
	st, body, _ = b.req("POST", "/dav/calendars/bob/outbox/", forged, "Content-Type", "text/calendar")
	expect(t, st, 403, body)
	has(t, body, "valid-organizer")
	st, body, _ = b.req("POST", "/dav/calendars/bob/outbox/", "garbage", "Content-Type", "text/calendar")
	expect(t, st, 403, body)
	// A transparent calendar drops out of availability.
	st, body, _ = a.req("PROPPATCH", "/dav/calendars/alice/main/", `<propertyupdate xmlns="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav"><set><prop><c:schedule-calendar-transp><c:transparent/></c:schedule-calendar-transp></prop></set></propertyupdate>`)
	expect(t, st, 207, body)
	_, body, _ = b.req("POST", "/dav/calendars/bob/outbox/", req, "Content-Type", "text/calendar")
	lacks(t, unfold(body), "FBTYPE=BUSY:")

	// A user who hides their availability answers 3.8 to others only.
	au, _ := e.st.UserByName("alice")
	if err := e.st.SetHideFreeBusy(au.ID, true); err != nil {
		t.Fatal(err)
	}
	_, body, _ = b.req("POST", "/dav/calendars/bob/outbox/", req, "Content-Type", "text/calendar")
	body = unfold(body)
	has(t, body, "3.8;No authority")
	lacks(t, body, "FREEBUSY")
	own := strings.Replace(req, "ORGANIZER:mailto:bob@example.com", "ORGANIZER:mailto:alice@example.com", 1)
	_, body, _ = a.req("POST", "/dav/calendars/alice/outbox/", own, "Content-Type", "text/calendar", "Recipient", "mailto:alice@example.com")
	has(t, unfold(body), "2.0;Success")
}

func TestExplicitOutboxDelivery(t *testing.T) {
	e := newSchedEnv(t, nil)
	a, b := e.alice, e.bob
	msg := invite("x1", "Explicit", "0", "", "ATTENDEE;SCHEDULE-AGENT=CLIENT:mailto:bob@example.com")
	msg = strings.Replace(msg, "VERSION:2.0\r\n", "VERSION:2.0\r\nMETHOD:REQUEST\r\n", 1)
	st, body, _ := a.req("POST", "/dav/calendars/alice/outbox/", msg, "Content-Type", "text/calendar")
	expect(t, st, 200, body)
	has(t, body, "mailto:bob@example.com", "2.0;Success")
	st, mine, _ := b.req("GET", "/dav/calendars/bob/bobcal/x1.ics", "")
	expect(t, st, 200, mine)
}

func TestSchedulingDiscovery(t *testing.T) {
	e := newSchedEnv(t, nil)
	b := e.bob
	st, body, h := b.req("OPTIONS", "/dav/", "")
	expect(t, st, 204, body)
	has(t, strings.Join(h["Dav"], ","), "calendar-auto-schedule")

	st, body, _ = b.req("PROPFIND", "/dav/principals/bob/", `<propfind xmlns="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav"><prop>
		<c:calendar-user-address-set/><c:schedule-inbox-URL/><c:schedule-outbox-URL/><c:calendar-user-type/></prop></propfind>`, "Depth", "0")
	expect(t, st, 207, body)
	has(t, body, "<D:href>mailto:bob@example.com</D:href>", "<D:href>/dav/principals/bob/</D:href>",
		"/dav/calendars/bob/inbox/", "/dav/calendars/bob/outbox/", "INDIVIDUAL")

	st, body, _ = b.req("PROPFIND", "/dav/calendars/bob/", `<propfind xmlns="DAV:"><prop><resourcetype/></prop></propfind>`, "Depth", "1")
	expect(t, st, 207, body)
	has(t, body, "/dav/calendars/bob/inbox/", "/dav/calendars/bob/outbox/", "schedule-inbox", "schedule-outbox")

	// The inbox advertises its default calendar and lets it be changed.
	pf := `<propfind xmlns="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav"><prop><c:schedule-default-calendar-URL/></prop></propfind>`
	_, body, _ = b.req("PROPFIND", "/dav/calendars/bob/inbox/", pf, "Depth", "0")
	has(t, body, "/dav/calendars/bob/bobcal/")
	st, body, _ = b.req("MKCALENDAR", "/dav/calendars/bob/second/", "")
	expect(t, st, 201, body)
	set := func(href string) (int, string) {
		st, body, _ := b.req("PROPPATCH", "/dav/calendars/bob/inbox/", `<propertyupdate xmlns="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav"><set><prop>
			<c:schedule-default-calendar-URL><href>`+href+`</href></c:schedule-default-calendar-URL></prop></set></propertyupdate>`)
		return st, body
	}
	st, body = set("/dav/calendars/bob/second/")
	expect(t, st, 207, body)
	has(t, body, "200 OK")
	_, body, _ = b.req("PROPFIND", "/dav/calendars/bob/inbox/", pf, "Depth", "0")
	has(t, body, "/dav/calendars/bob/second/")
	_, body = set("/dav/calendars/bob/nonexistent/")
	has(t, body, "409")
	// New invitations now land in the chosen calendar.
	e.alice.req("PUT", "/dav/calendars/alice/main/n.ics", invite("n", "New", "0", "", bobAtt))
	st, body, _ = b.req("GET", "/dav/calendars/bob/second/n.ics", "")
	expect(t, st, 200, body)

	// Reserved names and read-only inbox.
	st, body, _ = b.req("MKCALENDAR", "/dav/calendars/bob/inbox2/", "")
	expect(t, st, 201, body)
	st, body, _ = b.req("MKCALENDAR", "/dav/calendars/bob/inbox/", "")
	expect(t, st, 403, body)
	st, body, _ = b.req("MKCALENDAR", "/dav/calendars/bob/outbox/", "") // exists as a virtual collection
	expect(t, st, 405, body)
	st, body, _ = b.req("PUT", "/dav/calendars/bob/inbox/x.ics", invite("x", "forged", "0", ""))
	expect(t, st, 403, body)
	st, body, _ = b.req("DELETE", "/dav/calendars/bob/inbox/", "")
	expect(t, st, 403, body)
	st, body, _ = b.req("PROPPATCH", "/dav/calendars/bob/inbox/", `<propertyupdate xmlns="DAV:"><set><prop><displayname>x</displayname></prop></set></propertyupdate>`)
	expect(t, st, 207, body)
	has(t, body, "403")
	// Calendars report their transparency; free-busy-query is advertised.
	_, body, _ = b.req("PROPFIND", "/dav/calendars/bob/bobcal/", `<propfind xmlns="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav"><prop><c:schedule-calendar-transp/><supported-report-set/></prop></propfind>`, "Depth", "0")
	has(t, body, "<C:opaque/>", "<C:free-busy-query/>")
}

// fakeSMTP is a minimal SMTP server collecting the messages it receives.
type fakeSMTP struct {
	ln   net.Listener
	mu   sync.Mutex
	msgs []string
}

func newFakeSMTP(t *testing.T) *fakeSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSMTP{ln: ln}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
	return f
}

func (f *fakeSMTP) serve(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	say := func(s string) { c.Write([]byte(s + "\r\n")) }
	say("220 fake ESMTP")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		switch cmd := strings.ToUpper(strings.TrimSpace(line)); {
		case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
			say("250 fake")
		case strings.HasPrefix(cmd, "DATA"):
			say("354 go ahead")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil || l == ".\r\n" {
					break
				}
				b.WriteString(l)
			}
			f.mu.Lock()
			f.msgs = append(f.msgs, b.String())
			f.mu.Unlock()
			say("250 queued")
		case strings.HasPrefix(cmd, "QUIT"):
			say("221 bye")
			return
		default:
			say("250 ok")
		}
	}
}

func (f *fakeSMTP) messages() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.msgs...)
}

func TestIMIPForExternalAttendees(t *testing.T) {
	smtpSrv := newFakeSMTP(t)
	addr := smtpSrv.ln.Addr().(*net.TCPAddr)
	e := newSchedEnv(t, &sched.SMTP{Host: "127.0.0.1", Port: addr.Port, From: "whitegopher <noreply@example.com>"})
	a := e.alice
	st, body, _ := a.req("PUT", "/dav/calendars/alice/main/ext.ics", invite("ext", "Lunch, with é", "0", "", carolAtt))
	expect(t, st, 201, body)
	_, got, _ := a.req("GET", "/dav/calendars/alice/main/ext.ics", "")
	has(t, unfold(got), "SCHEDULE-STATUS=1.1:mailto:carol@external.org")

	msgs := smtpSrv.messages()
	if len(msgs) != 1 {
		t.Fatalf("%d messages", len(msgs))
	}
	m := msgs[0]
	has(t, m, "To: carol@external.org", "Reply-To: alice@example.com", "From: \"whitegopher\" <noreply@example.com>",
		"multipart/alternative", "text/calendar; charset=UTF-8; method=REQUEST", "Subject: =?UTF-8?q?Invitation:_Lunch,_with_=C3=A9?=")
	has(t, m, "BEGIN:VEVENT", "ORGANIZER")

	// Cancelling emails the attendee again.
	a.req("DELETE", "/dav/calendars/alice/main/ext.ics", "")
	msgs = smtpSrv.messages()
	if len(msgs) != 2 || !strings.Contains(msgs[1], "method=CANCEL") {
		t.Fatalf("cancel mail: %d messages", len(msgs))
	}
}

func TestReplyToSingleInstanceCreatesOverride(t *testing.T) {
	e := newSchedEnv(t, nil)
	a, b := e.alice, e.bob
	series := "/dav/calendars/alice/main/series.ics"
	st, body, _ := a.req("PUT", series, invite("series", "Weekly", "0", "RRULE:FREQ=WEEKLY;COUNT=4\r\n", bobAtt))
	expect(t, st, 201, body)

	// Bob declines only the second occurrence, by adding an override to his copy.
	_, mine, _ := b.req("GET", "/dav/calendars/bob/bobcal/series.ics", "")
	override := "BEGIN:VEVENT\r\nUID:series\r\nDTSTAMP:20260101T000000Z\r\nRECURRENCE-ID:20260317T100000Z\r\n" +
		"DTSTART:20260317T100000Z\r\nDTEND:20260317T110000Z\r\nSUMMARY:Weekly\r\nSEQUENCE:0\r\n" +
		"ORGANIZER;CN=Alice:mailto:alice@example.com\r\nATTENDEE;CN=Bob;PARTSTAT=DECLINED:mailto:bob@example.com\r\nEND:VEVENT\r\n"
	edited := strings.Replace(mine, "END:VCALENDAR", override+"END:VCALENDAR", 1)
	st, body, _ = b.req("PUT", "/dav/calendars/bob/bobcal/series.ics", edited)
	expect(t, st/100, 2, body)

	_, got, _ := a.req("GET", series, "")
	got = unfold(got)
	has(t, got, "RECURRENCE-ID:20260317T100000Z", "PARTSTAT=DECLINED")
	if strings.Count(got, "BEGIN:VEVENT") != 2 {
		t.Fatalf("want master plus one override:\n%s", got)
	}
	// The override carries no recurrence rule of its own, and the series keeps Bob's open answer.
	i := strings.Index(got, "RECURRENCE-ID")
	over := got[strings.LastIndex(got[:i], "BEGIN:VEVENT"):]
	lacks(t, over[:strings.Index(over, "END:VEVENT")], "RRULE")
	master := got[:strings.Index(got, "END:VEVENT")]
	has(t, master, "RRULE:FREQ=WEEKLY;COUNT=4", "PARTSTAT=NEEDS-ACTION")

	// The override sits at the second occurrence and keeps its length.
	has(t, over, "DTSTART:20260317T100000Z", "DTEND:20260317T110000Z")
}
