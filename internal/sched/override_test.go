package sched

import (
	"strings"
	"testing"
	"time"
)

func TestFormatDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		time.Hour:                       "PT1H",
		90 * time.Minute:                "PT1H30M",
		24 * time.Hour:                  "P1D",
		25*time.Hour + 5*time.Second:    "P1DT1H5S",
		0:                               "PT0S",
		45 * time.Second:                "PT45S",
		2*24*time.Hour + 15*time.Minute: "P2DT15M",
	} {
		if got := formatDuration(d); got != want {
			t.Errorf("%v: got %s, want %s", d, got, want)
		}
	}
}

func TestNewOverrideWithoutTimesInReply(t *testing.T) {
	master, err := decode([]byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:x\r\nBEGIN:VEVENT\r\nUID:s\r\nDTSTAMP:20260101T000000Z\r\n" +
		"DTSTART;TZID=Europe/Berlin:20260310T100000\r\nDTEND;TZID=Europe/Berlin:20260310T113000\r\nRRULE:FREQ=WEEKLY\r\nEXDATE;TZID=Europe/Berlin:20260324T100000\r\n" +
		"ORGANIZER:mailto:o@example.com\r\nATTENDEE;PARTSTAT=NEEDS-ACTION:mailto:bob@example.com\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	reply, err := decode([]byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:x\r\nMETHOD:REPLY\r\nBEGIN:VEVENT\r\nUID:s\r\nDTSTAMP:20260101T000000Z\r\n" +
		"RECURRENCE-ID;TZID=Europe/Berlin:20260317T100000\r\nORGANIZER:mailto:o@example.com\r\nATTENDEE;PARTSTAT=DECLINED:mailto:BOB@example.com\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	o := newOverride(schedulable(master), schedulable(reply)[0])
	if o == nil {
		t.Fatal("no override")
	}
	wrap := newCalendar("")
	wrap.Children = append(wrap.Children, o)
	out := string(encode(wrap))
	for _, want := range []string{"RECURRENCE-ID;TZID=Europe/Berlin:20260317T100000", "DTSTART;TZID=Europe/Berlin:20260317T100000", "DURATION:PT1H30M"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	for _, bad := range []string{"RRULE", "EXDATE", "DTEND"} {
		if strings.Contains(out, bad) {
			t.Errorf("unexpected %q in\n%s", bad, out)
		}
	}

	// A stranger's answer, or one for a non-recurring object, creates nothing.
	stranger := strings.ReplaceAll(string(encode(reply)), "BOB@example.com", "eve@example.com")
	rs, _ := decode([]byte(stranger))
	if newOverride(schedulable(master), schedulable(rs)[0]) != nil {
		t.Fatal("override created for an unknown attendee")
	}
}
