package dav_test

import (
	"strings"
	"sync"
	"testing"
	"time"
)

type textMail struct {
	captureMail
	tmu  sync.Mutex
	text []string // "to|subject|body"
}

func (m *textMail) SendText(to, subject, body string) error {
	m.tmu.Lock()
	defer m.tmu.Unlock()
	m.text = append(m.text, to+"|"+subject+"|"+body)
	return nil
}

func (m *textMail) texts() []string {
	// Notifications are sent in the background.
	time.Sleep(200 * time.Millisecond)
	m.tmu.Lock()
	defer m.tmu.Unlock()
	return append([]string(nil), m.text...)
}

func TestLocalNotificationMail(t *testing.T) {
	mail := &textMail{}
	e := newSchedEnv(t, mail)
	a := e.alice
	path := "/dav/calendars/alice/main/n.ics"

	st, body, _ := a.req("PUT", path, invite("n", "Planning", "0", "", bobAtt))
	expect(t, st, 201, body)
	got := mail.texts()
	if len(got) != 1 {
		t.Fatalf("want 1 mail for the new invitation, got %q", got)
	}
	has(t, got[0], "bob@example.com|Invitation: Planning|", "Alice <alice@example.com>", "Tuesday, 10 March 2026, 10:00–11:00 UTC")

	// An update to a known event is not mailed.
	st, body, _ = a.req("PUT", path, strings.Replace(invite("n", "Planning", "1", "", bobAtt), "20260310T100000Z", "20260311T100000Z", 1))
	expect(t, st, 204, body)
	if got := mail.texts(); len(got) != 1 {
		t.Fatalf("update must not be mailed: %q", got)
	}

	// Deleting the event cancels it.
	st, body, _ = a.req("DELETE", path, "")
	expect(t, st, 204, body)
	got = mail.texts()
	if len(got) != 2 {
		t.Fatalf("want a cancellation mail, got %q", got)
	}
	has(t, got[1], "bob@example.com|Cancelled: Planning|")

	// Removing an attendee cancels for them, and opting out silences mail.
	p2 := "/dav/calendars/alice/main/m.ics"
	st, body, _ = a.req("PUT", p2, invite("m", "Lunch", "0", "", bobAtt))
	expect(t, st, 201, body)
	st, body, _ = a.req("PUT", p2, invite("m", "Lunch", "1", "", "ATTENDEE:mailto:dave@external.org"))
	expect(t, st, 204, body)
	got = mail.texts()
	if len(got) != 4 || !strings.Contains(got[3], "Cancelled: Lunch") {
		t.Fatalf("removal must be mailed: %q", got)
	}
	u, _ := e.st.UserByName("bob")
	if err := e.st.SetNotifyInvites(u.ID, false); err != nil {
		t.Fatal(err)
	}
	st, body, _ = a.req("PUT", "/dav/calendars/alice/main/o.ics", invite("o", "Quiet", "0", "", bobAtt))
	expect(t, st, 201, body)
	if got := mail.texts(); len(got) != 4 {
		t.Fatalf("opted-out user must not be mailed: %q", got)
	}
}
