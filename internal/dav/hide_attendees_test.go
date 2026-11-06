package dav_test

import (
	"strings"
	"testing"

	"github.com/bitflipp/whitegopher/internal/sched"
)

const daveAtt = "ATTENDEE;CN=Dave;PARTSTAT=NEEDS-ACTION:mailto:dave@external.org"

func TestHideAttendees(t *testing.T) {
	mail := &captureMail{}
	e := newSchedEnv(t, mail)
	alice, _ := e.st.UserByName("alice")
	path := "/dav/calendars/alice/main/h1.ics"

	// Default: invitees see everyone.
	st, body, _ := e.alice.req("PUT", "/dav/calendars/alice/main/h0.ics", invite("h0", "Open", "0", "", bobAtt, carolAtt))
	expect(t, st, 201, body)
	_, mine, _ := e.bob.req("GET", "/dav/calendars/bob/bobcal/h0.ics", "")
	has(t, unfold(mine), "carol@external.org", "bob@example.com")

	// The organizer opts in: Bob sees only Alice and himself, Carol's mail too,
	// and the inbox copy is filtered as well. Alice keeps the full list.
	if err := e.st.SetHideAttendees(alice.ID, true); err != nil {
		t.Fatal(err)
	}
	st, body, _ = e.alice.req("PUT", path, invite("h1", "Private", "0", "", bobAtt, carolAtt, daveAtt))
	expect(t, st, 201, body)
	_, mine, _ = e.bob.req("GET", "/dav/calendars/bob/bobcal/h1.ics", "")
	mine = unfold(mine)
	has(t, mine, "mailto:bob@example.com", "mailto:alice@example.com")
	lacks(t, mine, "carol@external.org", "dave@external.org")
	_, theirs, _ := e.alice.req("GET", path, "")
	has(t, unfold(theirs), "bob@example.com", "carol@external.org", "dave@external.org")
	for _, m := range mail.sent {
		if m.To == "" {
			continue
		}
		for _, other := range []string{"carol@external.org", "dave@external.org"} {
			if m.To != other && strings.Contains(unfold(string(m.ICS)), other) {
				t.Fatalf("mail to %s leaks %s:\n%s", m.To, other, string(m.ICS))
			}
		}
	}

	// Bob's answer still reaches Alice.
	acc := strings.Replace(mine, "PARTSTAT=NEEDS-ACTION;RSVP=TRUE:mailto:bob@example.com", "PARTSTAT=ACCEPTED:mailto:bob@example.com", 1)
	st, body, _ = e.bob.req("PUT", "/dav/calendars/bob/bobcal/h1.ics", acc)
	expect(t, st, 204, body)
	_, theirs, _ = e.alice.req("GET", path, "")
	has(t, unfold(theirs), "PARTSTAT=ACCEPTED;SCHEDULE-STATUS=2.0:mailto:bob@example.com")

	// An explicit "no" is overridden by the server-wide force, and a server
	// default applies to organizers who never chose.
	no := false
	alice.HideAttendees = &no
	if sched.HidesAttendees(alice, true, false) {
		t.Fatal("own choice must beat the server default")
	}
	if !sched.HidesAttendees(alice, false, true) {
		t.Fatal("force must beat the own choice")
	}
	alice.HideAttendees = nil
	if !sched.HidesAttendees(alice, true, false) || sched.HidesAttendees(alice, false, false) {
		t.Fatal("unset follows the server default")
	}

	// Force in the service hides even without a stored choice (cancel too).
	if err := e.st.SetHideAttendees(alice.ID, false); err != nil {
		t.Fatal(err)
	}
	e.svc.HideAttendeesForce = true
	st, body, _ = e.alice.req("PUT", path, invite("h1", "Private", "1", "", bobAtt, carolAtt))
	expect(t, st, 204, body)
	_, mine, _ = e.bob.req("GET", "/dav/calendars/bob/bobcal/h1.ics", "")
	lacks(t, unfold(mine), "carol@external.org")
}
