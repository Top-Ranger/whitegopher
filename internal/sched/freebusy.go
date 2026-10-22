package sched

import (
	"sort"
	"strings"
	"time"

	"github.com/emersion/go-ical"

	"github.com/bitflipp/whitegopher/internal/index"
	"github.com/bitflipp/whitegopher/internal/store"
)

// Period is a busy interval.
type Period struct {
	Start, End time.Time
	Tentative  bool
}

func overlapsWindow(s, e, rs, re time.Time) bool {
	if s.Equal(e) {
		return !s.Before(rs) && s.Before(re)
	}
	return s.Before(re) && e.After(rs)
}

// busyInstances expands the components of one object (all sharing a UID:
// the master plus recurrence overrides) into busy periods within [rs, re).
// isMe tells whether a calendar address belongs to the user whose
// availability is computed; events they declined do not count.
func busyInstances(comps []*ical.Component, rs, re time.Time, isMe func(string) bool) []Period {
	var master *ical.Component
	var overrides []*ical.Component
	for _, c := range comps {
		if c.Name != ical.CompEvent {
			continue
		}
		if c.Props.Get(ical.PropRecurrenceID) != nil {
			overrides = append(overrides, c)
		} else if master == nil {
			master = c
		}
	}
	busy := func(c *ical.Component) (counts, tentative bool) {
		if strings.EqualFold(getValue(c, ical.PropTransparency), "TRANSPARENT") {
			return false, false
		}
		status := strings.ToUpper(getValue(c, ical.PropStatus))
		if status == "CANCELLED" {
			return false, false
		}
		tentative = status == "TENTATIVE"
		for _, a := range c.Props.Values(ical.PropAttendee) {
			if !isMe(a.Value) {
				continue
			}
			switch strings.ToUpper(a.Params.Get(ical.ParamParticipationStatus)) {
			case "DECLINED":
				return false, false
			case "TENTATIVE":
				tentative = true
			}
		}
		return true, tentative
	}

	var out []Period
	var rids []time.Time
	for _, o := range overrides {
		if t, err := o.Props.Get(ical.PropRecurrenceID).DateTime(time.UTC); err == nil {
			rids = append(rids, t)
		}
		counts, tent := busy(o)
		if s, e, ok := index.Span(o); ok && counts && overlapsWindow(s, e, rs, re) {
			out = append(out, Period{s, e, tent})
		}
	}
	if master == nil {
		return out
	}
	counts, tent := busy(master)
	s, e, ok := index.Span(master)
	if !counts || !ok {
		return out
	}
	d := e.Sub(s)
	starts := []time.Time{s}
	if set, err := master.RecurrenceSet(time.UTC); err == nil && set != nil {
		starts = set.Between(rs.Add(-d), re, true)
	} else if master.Props.Get(ical.PropRecurrenceDates) != nil {
		for _, p := range master.Props.Values(ical.PropRecurrenceDates) {
			if t, err := p.DateTime(time.UTC); err == nil {
				starts = append(starts, t)
			}
		}
	}
	for _, t := range starts {
		replaced := false
		for _, r := range rids {
			replaced = replaced || r.Equal(t)
		}
		if !replaced && overlapsWindow(t, t.Add(d), rs, re) {
			out = append(out, Period{t, t.Add(d), tent})
		}
	}
	return out
}

// mergePeriods clamps to [rs, re], merges overlaps, and removes tentative
// time already covered by busy time.
func mergePeriods(ps []Period, rs, re time.Time) (busy, tentative []Period) {
	var b, t []Period
	for _, p := range ps {
		if p.Start.Before(rs) {
			p.Start = rs
		}
		if p.End.After(re) {
			p.End = re
		}
		if !p.End.After(p.Start) {
			continue
		}
		if p.Tentative {
			t = append(t, p)
		} else {
			b = append(b, p)
		}
	}
	b, t = union(b), union(t)
	// Subtract busy from tentative.
	for _, x := range b {
		var next []Period
		for _, y := range t {
			if !y.Start.Before(x.End) || !y.End.After(x.Start) {
				next = append(next, y)
				continue
			}
			if y.Start.Before(x.Start) {
				next = append(next, Period{y.Start, x.Start, true})
			}
			if y.End.After(x.End) {
				next = append(next, Period{x.End, y.End, true})
			}
		}
		t = next
	}
	return b, t
}

func union(ps []Period) []Period {
	sort.Slice(ps, func(i, j int) bool { return ps[i].Start.Before(ps[j].Start) })
	var out []Period
	for _, p := range ps {
		if n := len(out); n > 0 && !p.Start.After(out[n-1].End) {
			if p.End.After(out[n-1].End) {
				out[n-1].End = p.End
			}
			continue
		}
		out = append(out, p)
	}
	return out
}

// FreeBusy computes u's busy time in [start, end) over the given calendars.
// Transparent calendars are skipped.
func (s *Service) FreeBusy(u store.User, colls []store.Collection, start, end time.Time) (busy, tentative []Period, err error) {
	from, to := start.Unix(), end.Unix()
	isMe := func(addr string) bool { return s.isUser(addr, u) }
	var all []Period
	for _, c := range colls {
		if c.Kind != store.KindCalendar || c.Transparent {
			continue
		}
		objs, err := s.Store.QueryObjects(c.ID, "VEVENT", &from, &to)
		if err != nil {
			return nil, nil, err
		}
		for _, o := range objs {
			cal, err := decode(o.Data)
			if err != nil {
				continue
			}
			all = append(all, busyInstances(schedulable(cal), start, end, isMe)...)
		}
	}
	busy, tentative = mergePeriods(all, start, end)
	return busy, tentative, nil
}

// UserFreeBusy computes availability over all of u's calendars.
func (s *Service) UserFreeBusy(u store.User, start, end time.Time) (busy, tentative []Period, err error) {
	colls, err := s.Store.ListCollections(u.ID, store.KindCalendar)
	if err != nil {
		return nil, nil, err
	}
	return s.FreeBusy(u, colls, start, end)
}

// RenderFreeBusy builds a VCALENDAR holding one VFREEBUSY. For replies to a
// free-busy request pass the METHOD, the organizer and the attendee.
func RenderFreeBusy(method, uid, organizer, attendee string, start, end, now time.Time, busy, tentative []Period) []byte {
	cal := newCalendar(method)
	fb := ical.NewComponent(ical.CompFreeBusy)
	setProp(fb, newProp(ical.PropUID, uid))
	setProp(fb, newProp(ical.PropDateTimeStamp, utc(now)))
	setProp(fb, newProp(ical.PropDateTimeStart, utc(start)))
	setProp(fb, newProp(ical.PropDateTimeEnd, utc(end)))
	if organizer != "" {
		setProp(fb, newProp(ical.PropOrganizer, organizer))
	}
	if attendee != "" {
		setProp(fb, newProp(ical.PropAttendee, attendee))
	}
	add := func(kind string, ps []Period) {
		if len(ps) == 0 {
			return
		}
		vals := make([]string, len(ps))
		for i, p := range ps {
			vals[i] = utc(p.Start) + "/" + utc(p.End)
		}
		fb.Props.Add(&ical.Prop{Name: ical.PropFreeBusy, Value: strings.Join(vals, ","), Params: ical.Params{"FBTYPE": {kind}}})
	}
	add("BUSY", busy)
	add("BUSY-TENTATIVE", tentative)
	cal.Children = append(cal.Children, fb)
	return encode(cal)
}
