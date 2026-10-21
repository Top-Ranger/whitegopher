package sched

import (
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-ical"
)

func mustCal(t *testing.T, lines ...string) *ical.Calendar {
	t.Helper()
	src := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//t//EN\r\n" + strings.Join(lines, "\r\n") + "\r\nEND:VCALENDAR\r\n"
	cal, err := decode([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	return cal
}

func at(day, hour int) time.Time { return time.Date(2026, 3, day, hour, 0, 0, 0, time.UTC) }

func never(string) bool { return false }

func TestBusyInstances(t *testing.T) {
	ev := func(uid, extra string) []string {
		return []string{"BEGIN:VEVENT", "UID:" + uid, "DTSTAMP:20260101T000000Z", "DTSTART:20260302T100000Z", "DTEND:20260302T110000Z", extra, "END:VEVENT"}
	}
	win0, win1 := at(1, 0), at(31, 0)
	cases := []struct {
		name  string
		lines []string
		isMe  func(string) bool
		want  []Period
	}{
		{"single", ev("a", "SUMMARY:x"), never, []Period{{at(2, 10), at(2, 11), false}}},
		{"transparent", ev("a", "TRANSP:TRANSPARENT"), never, nil},
		{"cancelled", ev("a", "STATUS:CANCELLED"), never, nil},
		{"tentative", ev("a", "STATUS:TENTATIVE"), never, []Period{{at(2, 10), at(2, 11), true}}},
		{"declined by me", ev("a", "ATTENDEE;PARTSTAT=DECLINED:mailto:me@x"), func(a string) bool { return a == "mailto:me@x" }, nil},
		{"declined by other", ev("a", "ATTENDEE;PARTSTAT=DECLINED:mailto:other@x"), func(a string) bool { return a == "mailto:me@x" }, []Period{{at(2, 10), at(2, 11), false}}},
		{"tentative by me", ev("a", "ATTENDEE;PARTSTAT=TENTATIVE:mailto:me@x"), func(a string) bool { return a == "mailto:me@x" }, []Period{{at(2, 10), at(2, 11), true}}},
		{"count", ev("a", "RRULE:FREQ=DAILY;COUNT=3"), never,
			[]Period{{at(2, 10), at(2, 11), false}, {at(3, 10), at(3, 11), false}, {at(4, 10), at(4, 11), false}}},
		{"exdate", ev("a", "RRULE:FREQ=DAILY;COUNT=3\r\nEXDATE:20260303T100000Z"), never,
			[]Period{{at(2, 10), at(2, 11), false}, {at(4, 10), at(4, 11), false}}},
		{"override moves one instance", append(ev("a", "RRULE:FREQ=DAILY;COUNT=2"),
			"BEGIN:VEVENT", "UID:a", "DTSTAMP:20260101T000000Z", "RECURRENCE-ID:20260303T100000Z", "DTSTART:20260303T150000Z", "DTEND:20260303T160000Z", "END:VEVENT"), never,
			[]Period{{at(2, 10), at(2, 11), false}, {at(3, 15), at(3, 16), false}}},
		{"override cancels one instance", append(ev("a", "RRULE:FREQ=DAILY;COUNT=2"),
			"BEGIN:VEVENT", "UID:a", "DTSTAMP:20260101T000000Z", "RECURRENCE-ID:20260303T100000Z", "DTSTART:20260303T100000Z", "DTEND:20260303T110000Z", "STATUS:CANCELLED", "END:VEVENT"), never,
			[]Period{{at(2, 10), at(2, 11), false}}},
	}
	for _, c := range cases {
		cal := mustCal(t, c.lines...)
		got := busyInstances(schedulable(cal), win0, win1, c.isMe)
		if len(got) != len(c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
			continue
		}
		used := map[int]bool{}
		for _, w := range c.want {
			found := false
			for i, g := range got {
				if !used[i] && g.Start.Equal(w.Start) && g.End.Equal(w.End) && g.Tentative == w.Tentative {
					used[i], found = true, true
					break
				}
			}
			if !found {
				t.Errorf("%s: missing %v in %v", c.name, w, got)
			}
		}
	}
}

func TestBusyInstancesWindow(t *testing.T) {
	cal := mustCal(t, "BEGIN:VEVENT", "UID:w", "DTSTAMP:20260101T000000Z", "DTSTART:20260302T100000Z", "DTEND:20260302T110000Z", "RRULE:FREQ=DAILY", "END:VEVENT")
	got := busyInstances(schedulable(cal), at(10, 0), at(12, 0), never)
	if len(got) != 2 || !got[0].Start.Equal(at(10, 10)) {
		t.Fatalf("unbounded recurrence in window: %v", got)
	}
	// An event that started before the window but runs into it.
	cal = mustCal(t, "BEGIN:VEVENT", "UID:w", "DTSTAMP:20260101T000000Z", "DTSTART:20260301T230000Z", "DTEND:20260302T010000Z", "END:VEVENT")
	if got := busyInstances(schedulable(cal), at(2, 0), at(3, 0), never); len(got) != 1 {
		t.Fatalf("straddling event: %v", got)
	}
}

func TestMergePeriods(t *testing.T) {
	ps := []Period{
		{at(2, 10), at(2, 12), false},
		{at(2, 11), at(2, 13), false}, // overlaps the first
		{at(2, 13), at(2, 14), false}, // touches
		{at(2, 15), at(2, 18), true},  // tentative, partly covered below
		{at(2, 16), at(2, 17), false}, // busy inside the tentative period
		{at(1, 22), at(2, 1), false},  // starts before the window
		{at(5, 0), at(6, 0), false},   // after the window
	}
	busy, tent := mergePeriods(ps, at(2, 0), at(3, 0))
	wantBusy := []Period{{at(2, 0), at(2, 1), false}, {at(2, 10), at(2, 14), false}, {at(2, 16), at(2, 17), false}}
	wantTent := []Period{{at(2, 15), at(2, 16), true}, {at(2, 17), at(2, 18), true}}
	eq := func(a, b []Period) bool {
		if len(a) != len(b) {
			return false
		}
		for i := range a {
			if !a[i].Start.Equal(b[i].Start) || !a[i].End.Equal(b[i].End) {
				return false
			}
		}
		return true
	}
	if !eq(busy, wantBusy) || !eq(tent, wantTent) {
		t.Fatalf("busy %v\ntent %v", busy, tent)
	}
}

func TestRenderFreeBusy(t *testing.T) {
	raw := string(RenderFreeBusy("REPLY", "u1", "mailto:a@x", "mailto:b@x", at(1, 0), at(8, 0), at(1, 0),
		[]Period{{at(2, 10), at(2, 11), false}, {at(3, 10), at(3, 11), false}}, []Period{{at(4, 10), at(4, 11), true}}))
	out := strings.ReplaceAll(raw, "\r\n ", "") // unfold
	for _, want := range []string{
		"METHOD:REPLY", "BEGIN:VFREEBUSY", "UID:u1", "DTSTART:20260301T000000Z", "DTEND:20260308T000000Z",
		"FREEBUSY;FBTYPE=BUSY:20260302T100000Z/20260302T110000Z,20260303T100000Z/20260303T110000Z",
		"FREEBUSY;FBTYPE=BUSY-TENTATIVE:20260304T100000Z/20260304T110000Z", "ATTENDEE:mailto:b@x", "ORGANIZER:mailto:a@x",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	if _, err := decode([]byte(raw)); err != nil {
		t.Fatalf("output does not parse: %v", err)
	}
}

func TestEncodeRoundTripAndFolding(t *testing.T) {
	long := strings.Repeat("é", 60) + " long summary, with; punctuation " + strings.Repeat("x", 100)
	cal := mustCal(t, "BEGIN:VEVENT", "UID:r", "DTSTAMP:20260101T000000Z", "DTSTART:20260302T100000Z",
		"ATTENDEE;CN=\"Doe, Jane\";PARTSTAT=ACCEPTED:mailto:j@x", "SUMMARY:short", "END:VEVENT")
	cal.Children[0].Props.Set(&ical.Prop{Name: "SUMMARY", Value: long, Params: ical.Params{}})
	out := encode(cal)
	for _, line := range strings.Split(string(out), "\r\n") {
		if len(line) > 75 {
			t.Fatalf("line of %d octets: %q", len(line), line)
		}
	}
	back, err := decode(out)
	if err != nil {
		t.Fatal(err)
	}
	ev := schedulable(back)[0]
	if getValue(ev, "SUMMARY") != long {
		t.Fatalf("summary changed: %q", getValue(ev, "SUMMARY"))
	}
	att := ev.Props.Get("ATTENDEE")
	if att.Params.Get("CN") != "Doe, Jane" || att.Params.Get("PARTSTAT") != "ACCEPTED" || att.Value != "mailto:j@x" {
		t.Fatalf("attendee: %+v", att)
	}
}

func TestNormAddr(t *testing.T) {
	if normAddr(" MAILTO:Bob@Example.COM ") != "mailto:bob@example.com" {
		t.Fatal(normAddr(" MAILTO:Bob@Example.COM "))
	}
	if normAddr("/dav/principals/Bob/") != "/dav/principals/Bob/" {
		t.Fatal("paths must keep their case")
	}
}
