package dav_test

import (
	"strings"
	"testing"
)

func reportData(t *testing.T, w who, path, calData string) string {
	t.Helper()
	body := `<c:calendar-multiget xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav"><d:prop><c:calendar-data>` + calData +
		`</c:calendar-data></d:prop><d:href>` + path + `</d:href></c:calendar-multiget>`
	st, out, _ := w.req("REPORT", "/dav/calendars/alice/main/", body)
	expect(t, st, 207, out)
	// The data is XML-escaped inside the response.
	r := strings.NewReplacer("&lt;", "<", "&gt;", ">", "&amp;", "&", "&#xD;", "\r", "&#xA;", "\n", "&#34;", `"`)
	return unfold(r.Replace(out))
}

func TestPartialCalendarDataAndExpand(t *testing.T) {
	e := newSchedEnv(t, nil)
	a := e.alice
	const path = "/dav/calendars/alice/main/series.ics"
	ics := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//t//EN\r\n" +
		"BEGIN:VEVENT\r\nUID:series\r\nDTSTAMP:20260101T000000Z\r\nDTSTART:20260310T100000Z\r\nDTEND:20260310T110000Z\r\n" +
		"RRULE:FREQ=WEEKLY;COUNT=5\r\nSUMMARY:Weekly\r\nDESCRIPTION:Secret agenda\r\nLOCATION:Room 1\r\nEND:VEVENT\r\n" +
		"BEGIN:VEVENT\r\nUID:series\r\nDTSTAMP:20260101T000000Z\r\nRECURRENCE-ID:20260317T100000Z\r\nDTSTART:20260317T120000Z\r\nDTEND:20260317T130000Z\r\n" +
		"SUMMARY:Weekly (moved)\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	st, body, _ := a.req("PUT", path, ics)
	expect(t, st, 201, body)

	// Full data is unchanged without a spec.
	full := reportData(t, a, path, "")
	has(t, full, "DESCRIPTION:Secret agenda", "PRODID:-//t//EN", "RRULE:FREQ=WEEKLY;COUNT=5")

	// Partial retrieval: only the requested components and properties.
	partial := reportData(t, a, path, `<c:comp name="VCALENDAR"><c:prop name="VERSION"/><c:comp name="VEVENT"><c:prop name="UID"/><c:prop name="SUMMARY"/><c:prop name="RECURRENCE-ID"/></c:comp></c:comp>`)
	has(t, partial, "BEGIN:VCALENDAR", "VERSION:2.0", "SUMMARY:Weekly", "SUMMARY:Weekly (moved)", "UID:series", "RECURRENCE-ID:20260317T100000Z")
	lacks(t, partial, "DESCRIPTION", "PRODID", "DTSTART", "RRULE", "LOCATION")
	if strings.Count(partial, "BEGIN:VEVENT") != 2 {
		t.Fatalf("components:\n%s", partial)
	}
	noEvents := reportData(t, a, path, `<c:comp name="VCALENDAR"><c:allprop/></c:comp>`)
	has(t, noEvents, "PRODID:-//t//EN")
	lacks(t, noEvents, "BEGIN:VEVENT")
	withAll := reportData(t, a, path, `<c:comp name="VCALENDAR"><c:prop name="VERSION"/><c:allcomp/></c:comp>`)
	has(t, withAll, "DESCRIPTION:Secret agenda")

	// expand: instances overlapping the window, the moved one at its new time.
	exp := reportData(t, a, path, `<c:expand start="20260316T000000Z" end="20260331T000000Z"/>`)
	if strings.Count(exp, "BEGIN:VEVENT") != 2 {
		t.Fatalf("want 2 instances:\n%s", exp)
	}
	has(t, exp, "RECURRENCE-ID:20260317T100000Z", "DTSTART:20260317T120000Z", "DTEND:20260317T130000Z",
		"RECURRENCE-ID:20260324T100000Z", "DTSTART:20260324T100000Z", "DTEND:20260324T110000Z", "SUMMARY:Weekly (moved)", "SUMMARY:Weekly\r\n")
	lacks(t, exp, "RRULE", "20260310", "20260331")

	// expand together with partial retrieval.
	both := reportData(t, a, path, `<c:comp name="VCALENDAR"><c:comp name="VEVENT"><c:prop name="DTSTART"/><c:prop name="RECURRENCE-ID"/></c:comp></c:comp><c:expand start="20260301T000000Z" end="20260312T000000Z"/>`)
	has(t, both, "RECURRENCE-ID:20260310T100000Z", "DTSTART:20260310T100000Z")
	lacks(t, both, "SUMMARY", "VERSION")

	// limit-recurrence-set keeps the master and only overlapping overrides.
	lim := reportData(t, a, path, `<c:limit-recurrence-set start="20260323T000000Z" end="20260331T000000Z"/>`)
	has(t, lim, "RRULE:FREQ=WEEKLY;COUNT=5")
	lacks(t, lim, "RECURRENCE-ID")
	lim = reportData(t, a, path, `<c:limit-recurrence-set start="20260316T000000Z" end="20260318T000000Z"/>`)
	has(t, lim, "RECURRENCE-ID:20260317T100000Z")

	// Time zones: instances come out in UTC, following the DST change.
	tz := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//t//EN\r\nBEGIN:VEVENT\r\nUID:berlin\r\nDTSTAMP:20260101T000000Z\r\n" +
		"DTSTART;TZID=Europe/Berlin:20260325T100000\r\nDTEND;TZID=Europe/Berlin:20260325T110000\r\nRRULE:FREQ=WEEKLY;COUNT=3\r\nSUMMARY:Berlin\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	st, body, _ = a.req("PUT", "/dav/calendars/alice/main/berlin.ics", tz)
	expect(t, st, 201, body)
	bexp := reportData(t, a, "/dav/calendars/alice/main/berlin.ics", `<c:expand start="20260301T000000Z" end="20260501T000000Z"/>`)
	has(t, bexp, "DTSTART:20260325T090000Z", "DTSTART:20260401T080000Z", "DTSTART:20260408T080000Z", "RECURRENCE-ID:20260401T080000Z")
	lacks(t, bexp, "TZID", "VTIMEZONE")

	// All-day events keep dates.
	day := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//t//EN\r\nBEGIN:VEVENT\r\nUID:day\r\nDTSTAMP:20260101T000000Z\r\n" +
		"DTSTART;VALUE=DATE:20260310\r\nDTEND;VALUE=DATE:20260311\r\nRRULE:FREQ=DAILY;COUNT=3\r\nSUMMARY:Day\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	st, body, _ = a.req("PUT", "/dav/calendars/alice/main/day.ics", day)
	expect(t, st, 201, body)
	dexp := reportData(t, a, "/dav/calendars/alice/main/day.ics", `<c:expand start="20260301T000000Z" end="20260501T000000Z"/>`)
	has(t, dexp, "DTSTART;VALUE=DATE:20260311", "DTEND;VALUE=DATE:20260312", "RECURRENCE-ID;VALUE=DATE:20260312")
}
