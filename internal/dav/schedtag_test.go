package dav_test

import (
	"strings"
	"sync"
	"testing"

	"github.com/Top-Ranger/whitegopher/internal/sched"
)

type captureMail struct {
	mu   sync.Mutex
	sent []sched.Message
}

func (c *captureMail) Send(m sched.Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent = append(c.sent, m)
	return nil
}

func (c *captureMail) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.sent)
}

func tagOf(h map[string][]string) string {
	if v := h["Schedule-Tag"]; len(v) > 0 {
		return v[0]
	}
	return ""
}

func TestScheduleTagAndSignificantChanges(t *testing.T) {
	mail := &captureMail{}
	e := newSchedEnv(t, mail)
	a, b := e.alice, e.bob
	path := "/dav/calendars/alice/main/meet.ics"
	bobPath := "/dav/calendars/bob/bobcal/meet.ics"

	// A scheduling object carries a Schedule-Tag; a plain event does not.
	st, body, h := a.req("PUT", path, invite("meet", "Planning", "0", "", bobAtt, carolAtt))
	expect(t, st, 201, body)
	tag1 := tagOf(h)
	if tag1 == "" {
		t.Fatal("no Schedule-Tag on a new scheduling object")
	}
	if mail.count() != 1 {
		t.Fatalf("carol should have been invited by email: %d", mail.count())
	}
	st, _, h = a.req("GET", path, "")
	expect(t, st, 200, "")
	if tagOf(h) != tag1 {
		t.Fatalf("GET tag %q, PUT tag %q", tagOf(h), tag1)
	}
	st, body, h = a.req("PUT", "/dav/calendars/alice/main/plain.ics", event("plain", "Solo", "20260401T100000Z", "20260401T110000Z", ""))
	expect(t, st, 201, body)
	if tagOf(h) != "" {
		t.Fatalf("plain event got Schedule-Tag %q", tagOf(h))
	}

	// The tag is a property too.
	st, body, _ = a.req("PROPFIND", path, `<propfind xmlns="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav"><prop><c:schedule-tag/></prop></propfind>`, "Depth", "0")
	expect(t, st, 207, body)
	has(t, body, "schedule-tag>"+tag1)

	// A title change is not significant: same tag, Bob's copy still updated,
	// but nobody is emailed again.
	retitled := invite("meet", "Planning v2", "0", "", bobAtt, carolAtt)
	st, body, h = a.req("PUT", path, retitled)
	expect(t, st, 204, body)
	if tagOf(h) != tag1 {
		t.Fatalf("tag changed on an insignificant edit: %q -> %q", tag1, tagOf(h))
	}
	if mail.count() != 1 {
		t.Fatalf("insignificant edit was emailed: %d messages", mail.count())
	}
	_, bobCopy, _ := b.req("GET", bobPath, "")
	has(t, bobCopy, "SUMMARY:Planning v2")

	// Moving the event is significant: new tag, email goes out again.
	moved := strings.Replace(retitled, "DTSTART:20260310T100000Z", "DTSTART:20260311T100000Z", 1)
	moved = strings.Replace(moved, "DTEND:20260310T110000Z", "DTEND:20260311T110000Z", 1)
	st, body, h = a.req("PUT", path, moved)
	expect(t, st, 204, body)
	tag2 := tagOf(h)
	if tag2 == "" || tag2 == tag1 {
		t.Fatalf("tag not renewed on a significant edit: %q -> %q", tag1, tag2)
	}
	if mail.count() != 2 {
		t.Fatalf("significant edit not emailed: %d messages", mail.count())
	}

	// A new attendee is invited even if nothing else changed.
	dave := "ATTENDEE;PARTSTAT=NEEDS-ACTION:mailto:dave@external.org"
	st, body, _ = a.req("PUT", path, strings.Replace(moved, carolAtt+"\r\n", carolAtt+"\r\n"+dave+"\r\n", 1))
	expect(t, st, 204, body)
	if mail.count() != 3 {
		t.Fatalf("new attendee not invited: %d messages", mail.count())
	}
	_, got, h := a.req("GET", path, "")
	tag3 := tagOf(h)

	// If-Schedule-Tag-Match guards PUT and DELETE.
	st, body, _ = a.req("PUT", path, moved, "If-Schedule-Tag-Match", tag1)
	expect(t, st, 412, body)
	st, body, _ = a.req("DELETE", path, "", "If-Schedule-Tag-Match", tag1)
	expect(t, st, 412, body)
	st, body, _ = a.req("PUT", path, got, "If-Schedule-Tag-Match", tag3)
	expect(t, st/100, 2, body)

	// An attendee's answer is applied to the organizer's copy by the server,
	// which counts as a change: the organizer's tag moves on.
	_, _, h3 := a.req("GET", path, "")
	before := tagOf(h3)
	_, mine, _ := b.req("GET", bobPath, "")
	accepted := strings.Replace(mine, "PARTSTAT=NEEDS-ACTION;RSVP=TRUE:mailto:bob@example.com", "PARTSTAT=ACCEPTED:mailto:bob@example.com", 1)
	st, body, _ = b.req("PUT", bobPath, accepted)
	expect(t, st/100, 2, body)
	_, g, h4 := a.req("GET", path, "")
	has(t, g, "PARTSTAT=ACCEPTED")
	if tagOf(h4) == before || tagOf(h4) == "" {
		t.Fatalf("organizer tag unchanged after a reply: %q", tagOf(h4))
	}

	// Schedule-Reply: F lets an attendee change their copy without telling the organizer.
	declined := strings.Replace(accepted, "PARTSTAT=ACCEPTED", "PARTSTAT=TENTATIVE", 1)
	st, body, _ = b.req("PUT", bobPath, declined, "Schedule-Reply", "F")
	expect(t, st/100, 2, body)
	_, g, _ = a.req("GET", path, "")
	has(t, g, "PARTSTAT=ACCEPTED")
	lacks(t, g, "PARTSTAT=TENTATIVE")
}

func TestSharedCalendarEditsScheduleAsOwner(t *testing.T) {
	mail := &captureMail{}
	e := newSchedEnv(t, mail)
	a, b := e.alice, e.bob
	st, body, _ := a.req("POST", "/dav/calendars/alice/main/", shareBody("set", "mailto:bob@example.com", "<CS:read-write/>"), "Content-Type", "application/xml")
	expect(t, st, 200, body)

	// Bob creates an event in Alice's calendar organized by Alice: the
	// invitation goes out from Alice, and the stored copy records the result.
	path := "/dav/calendars/bob/main/team.ics"
	st, body, h := b.req("PUT", path, invite("team", "Team", "0", "", carolAtt))
	expect(t, st, 201, body)
	if tagOf(h) == "" {
		t.Fatal("no Schedule-Tag for a scheduling object written through a share")
	}
	if mail.count() != 1 || mail.sent[0].From != "alice@example.com" || mail.sent[0].Method != "REQUEST" {
		t.Fatalf("mail: %+v", mail.sent)
	}
	_, got, _ := a.req("GET", "/dav/calendars/alice/main/team.ics", "")
	has(t, unfold(got), "SCHEDULE-STATUS=1.1:mailto:carol@external.org")

	// Deleting through the share cancels on the owner's behalf.
	st, body, _ = b.req("DELETE", path, "")
	expect(t, st, 204, body)
	if mail.count() != 2 || mail.sent[1].Method != "CANCEL" || mail.sent[1].From != "alice@example.com" {
		t.Fatalf("mail: %+v", mail.sent)
	}
}
