package index

import (
	"strings"
	"testing"
	"time"
)

func ev(extra string) []byte {
	return []byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//t//EN\r\nBEGIN:VEVENT\r\nUID:u\r\nDTSTAMP:20260101T000000Z\r\n" +
		"DTSTART:20260301T100000Z\r\nDTEND:20260301T110000Z\r\n" + extra + "END:VEVENT\r\nEND:VCALENDAR\r\n")
}

func TestOccurrenceBounds(t *testing.T) {
	start := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	cases := []struct {
		name, extra string
		last        time.Time // zero = unbounded
	}{
		{"single", "", start.Add(time.Hour)},
		{"count", "RRULE:FREQ=DAILY;COUNT=3\r\n", start.AddDate(0, 0, 2).Add(time.Hour)},
		{"until", "RRULE:FREQ=DAILY;UNTIL=20260310T100000Z\r\n", time.Date(2026, 3, 10, 11, 0, 0, 0, time.UTC)},
		{"forever", "RRULE:FREQ=WEEKLY\r\n", time.Time{}},
	}
	for _, c := range cases {
		p, bad := Calendar(ev(c.extra), []string{"VEVENT"})
		if bad != "" {
			t.Fatalf("%s: %s", c.name, bad)
		}
		if p.First.Int64 != start.Unix() {
			t.Errorf("%s: first %d", c.name, p.First.Int64)
		}
		if c.last.IsZero() == p.Last.Valid || (p.Last.Valid && p.Last.Int64 != c.last.Unix()) {
			t.Errorf("%s: last %+v, want %v", c.name, p.Last, c.last)
		}
	}
	if _, bad := Calendar([]byte(strings.Replace(string(ev("")), "VERSION:2.0", "VERSION:1.0", 1)), []string{"VEVENT"}); bad == "" {
		t.Error("bad version accepted")
	}
}
