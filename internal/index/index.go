// Package index validates calendar and contact objects and extracts the
// metadata the store indexes (UID, component, occurrence bounds).
package index

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-vcard"
)

// Parsed is the indexed metadata of a validated object.
type Parsed struct {
	UID       string
	Component string
	First     sql.NullInt64
	Last      sql.NullInt64
}

// Calendar validates an iCalendar object resource per RFC 4791 5.3.2.1
// and returns its index data. A non-empty errInner is the precondition
// element to report with a 403.
func Calendar(data []byte, allowed []string) (p Parsed, errInner string) {
	cal, err := ical.NewDecoder(bytes.NewReader(data)).Decode()
	if err != nil {
		return p, "<C:valid-calendar-data/>"
	}
	if v, _ := cal.Props.Text(ical.PropVersion); v != "2.0" {
		return p, "<C:valid-calendar-data/>"
	}
	if cal.Props.Get(ical.PropMethod) != nil {
		return p, "<C:valid-calendar-object-resource/>"
	}
	var main []*ical.Component
	for _, c := range cal.Children {
		if c.Name != ical.CompTimezone {
			main = append(main, c)
		}
	}
	if len(main) == 0 {
		return p, "<C:valid-calendar-object-resource/>"
	}
	p.Component = main[0].Name
	p.UID, _ = main[0].Props.Text(ical.PropUID)
	if p.UID == "" {
		return p, "<C:valid-calendar-object-resource/>"
	}
	for _, c := range main {
		uid, _ := c.Props.Text(ical.PropUID)
		if c.Name != p.Component || uid != p.UID {
			return p, "<C:valid-calendar-object-resource/>"
		}
	}
	ok := false
	for _, a := range allowed {
		ok = ok || a == p.Component
	}
	if !ok {
		return p, "<C:supported-calendar-component/>"
	}
	p.First, p.Last = occurrenceBounds(main)
	return p, ""
}

// occurrenceBounds computes unix-second bounds over all components of one
// object. Anything it cannot determine yields NULL (unknown/unbounded),
// which keeps the object a candidate for every time-range query.
func occurrenceBounds(comps []*ical.Component) (first, last sql.NullInt64) {
	var lo, hi time.Time
	bounded := true
	for _, c := range comps {
		start, end, ok := Span(c)
		if !ok {
			return first, last
		}
		if lo.IsZero() || start.Before(lo) {
			lo = start
		}
		if end.After(hi) {
			hi = end
		}
		if c.Props.Get(ical.PropRecurrenceRule) != nil {
			ro, err := c.Props.RecurrenceRule()
			if err != nil {
				return first, last
			}
			switch {
			case ro.Count > 0 && ro.Count <= 5000:
				set, err := c.RecurrenceSet(time.UTC)
				if err != nil || set == nil {
					return first, last
				}
				all := set.All()
				if len(all) > 0 {
					if e := all[len(all)-1].Add(end.Sub(start)); e.After(hi) {
						hi = e
					}
				}
			case !ro.Until.IsZero():
				if e := ro.Until.Add(end.Sub(start)); e.After(hi) {
					hi = e
				}
			default:
				bounded = false
			}
		}
		if c.Props.Get(ical.PropRecurrenceDates) != nil {
			bounded = false
		}
	}
	first = sql.NullInt64{Int64: lo.Unix(), Valid: true}
	if bounded {
		last = sql.NullInt64{Int64: hi.Unix(), Valid: true}
	}
	return first, last
}

// Span returns DTSTART and the end (DTEND/DUE/DURATION/start).
func Span(c *ical.Component) (start, end time.Time, ok bool) {
	sp := c.Props.Get(ical.PropDateTimeStart)
	if sp == nil {
		if c.Name != ical.CompToDo {
			return start, end, false
		}
		if d := c.Props.Get(ical.PropDue); d != nil {
			t, err := d.DateTime(time.UTC)
			return t, t, err == nil
		}
		return start, end, false
	}
	start, err := sp.DateTime(time.UTC)
	if err != nil {
		return start, end, false
	}
	end = start
	switch {
	case c.Props.Get(ical.PropDateTimeEnd) != nil:
		end, err = c.Props.Get(ical.PropDateTimeEnd).DateTime(time.UTC)
	case c.Props.Get(ical.PropDue) != nil:
		end, err = c.Props.Get(ical.PropDue).DateTime(time.UTC)
	case c.Props.Get(ical.PropDuration) != nil:
		var d time.Duration
		d, err = c.Props.Get(ical.PropDuration).Duration()
		end = start.Add(d)
	case sp.ValueType() == ical.ValueDate:
		end = start.Add(24 * time.Hour)
	}
	if err != nil || end.Before(start) {
		return start, end, false
	}
	return start, end, true
}

// Card validates a single vCard. Cards without UID get uri as UID.
func Card(data []byte, uri string) (p Parsed, errInner string) {
	dec := vcard.NewDecoder(bytes.NewReader(data))
	card, err := dec.Decode()
	if err != nil {
		return p, "<A:valid-address-data/>"
	}
	if _, err := dec.Decode(); !errors.Is(err, io.EOF) {
		return p, "<A:valid-address-data/>" // more than one card
	}
	if v := card.Value(vcard.FieldVersion); v != "3.0" && v != "4.0" && v != "2.1" {
		return p, "<A:valid-address-data/>"
	}
	p.Component = "VCARD"
	p.UID = strings.TrimSpace(card.Value(vcard.FieldUID))
	if p.UID == "" {
		p.UID = uri
	}
	return p, ""
}

// ETag returns the (unquoted) entity tag for object data.
func ETag(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:16])
}
