package sched

import (
	"crypto/rand"
	"encoding/hex"
	"sort"
	"strings"

	"github.com/emersion/go-ical"

	"github.com/bitflipp/galdav/internal/store"
)

// significantProps are the properties whose change counts as a
// scheduling-significant change (RFC 6638 3.2.8): the time, recurrence and
// status of the event, its location, and the SEQUENCE the organizer bumps to
// ask attendees to answer again.
var significantProps = []string{
	ical.PropDateTimeStart, ical.PropDateTimeEnd, ical.PropDuration, ical.PropDue,
	ical.PropRecurrenceRule, ical.PropRecurrenceDates, ical.PropExceptionDates, "EXRULE",
	ical.PropStatus, ical.PropLocation, ical.PropSequence,
}

// significantChange reports whether nw differs from old in a way attendees
// must be told about: an instance added or removed, or a significant
// property changed in any instance.
func significantChange(old, nw *schedObject) bool {
	oldBy, newBy := byRID(old.comps), byRID(nw.comps)
	if len(oldBy) != len(newBy) {
		return true
	}
	for rid, nc := range newBy {
		oc, ok := oldBy[rid]
		if !ok {
			return true
		}
		for _, name := range significantProps {
			if propSignature(oc, name) != propSignature(nc, name) {
				return true
			}
		}
	}
	return false
}

func byRID(comps []*ical.Component) map[string]*ical.Component {
	m := map[string]*ical.Component{}
	for _, c := range comps {
		m[ridKey(c)] = c
	}
	return m
}

// propSignature canonicalises all values of a property, with their
// parameters, so equal sets compare equal whatever their order.
func propSignature(c *ical.Component, name string) string {
	var parts []string
	for _, p := range c.Props.Values(name) {
		keys := make([]string, 0, len(p.Params))
		for k := range p.Params {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var b strings.Builder
		for _, k := range keys {
			b.WriteString(";" + k + "=" + strings.Join(p.Params[k], ","))
		}
		parts = append(parts, b.String()+":"+p.Value)
	}
	sort.Strings(parts)
	return strings.Join(parts, "\n")
}

// Classify says how a PUT by u of newData over oldData (nil for a new
// object) bears on the Schedule-Tag. scheduling is false for objects that
// take no part in scheduling. For the organizer's copy, significant says the
// tag must change; an attendee's copy changes tag only when the organizer's
// messages update it.
func (s *Service) Classify(u store.User, oldData, newData []byte) (scheduling, significant bool) {
	nw := parseSched(newData)
	if nw == nil {
		return false, false
	}
	if !s.isUser(nw.organizer.Value, u) {
		return true, false
	}
	if oldData == nil {
		return true, true
	}
	old := parseSched(oldData)
	return true, old == nil || significantChange(old, nw)
}

// NewScheduleTag returns a fresh random Schedule-Tag.
func NewScheduleTag() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
