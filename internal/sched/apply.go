package sched

import (
	"errors"
	"strings"

	"github.com/emersion/go-ical"

	"github.com/bitflipp/galdav/internal/store"
)

// withoutMethod returns a deep copy of an iTIP message minus its METHOD,
// i.e. a plain calendar object.
func withoutMethod(itip *ical.Calendar) *ical.Calendar {
	cal := &ical.Calendar{Component: cloneComp(itip.Component)}
	cal.Props.Del(ical.PropMethod)
	return cal
}

func setPartstat(c *ical.Component, isMe func(string) bool, ps string) {
	for i := range c.Props[ical.PropAttendee] {
		if p := &c.Props[ical.PropAttendee][i]; isMe(p.Value) {
			p.Params.Set(ical.ParamParticipationStatus, ps)
			return
		}
	}
}

// applyRequest puts an invitation into the recipient's calendar: it updates
// the existing copy (keeping the recipient's own answer unless the sequence
// advanced) or creates one in their default calendar.
func (s *Service) applyRequest(to store.User, itip *ical.Calendar) error {
	comps := schedulable(itip)
	first := comps[0]
	if first.Name != ical.CompEvent && first.Name != ical.CompToDo {
		return nil
	}
	uid := getValue(first, ical.PropUID)
	nc := withoutMethod(itip)
	isMe := func(a string) bool { return s.isUser(a, to) }

	coll, obj, err := s.Store.FindByUID(to.ID, uid)
	if err == nil {
		if old, derr := decode(obj.Data); derr == nil {
			carryOver(old, nc, isMe)
		}
		return s.putRaw(coll, obj.URI, uid, first.Name, encode(nc))
	}
	if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	coll, err = s.Store.DefaultCalendar(to, first.Name)
	if errors.Is(err, store.ErrNotFound) {
		return nil // no calendar to hold it: the inbox copy is all there is
	}
	if err != nil {
		return err
	}
	uri := uidURI(uid)
	if existing, gerr := s.Store.GetObject(coll.ID, uri); gerr == nil && existing.UID != uid {
		uri = "itip-" + uri // another object already owns the plain name
	}
	return s.putRaw(coll, uri, uid, first.Name, encode(nc))
}

// carryOver keeps the recipient's local state when an update arrives:
// their PARTSTAT (unless the organizer bumped SEQUENCE), alarms and TRANSP.
func carryOver(old, nc *ical.Calendar, isMe func(string) bool) {
	byRID := map[string]*ical.Component{}
	for _, c := range schedulable(old) {
		byRID[ridKey(c)] = c
	}
	for _, c := range schedulable(nc) {
		o := byRID[ridKey(c)]
		if o == nil {
			continue
		}
		if sequenceOf(c) <= sequenceOf(o) {
			if ps, ok := partstat(o, isMe); ok {
				setPartstat(c, isMe, ps)
			}
		}
		hasAlarm := false
		for _, ch := range c.Children {
			hasAlarm = hasAlarm || ch.Name == ical.CompAlarm
		}
		if !hasAlarm {
			for _, ch := range o.Children {
				if ch.Name == ical.CompAlarm {
					c.Children = append(c.Children, cloneComp(ch))
				}
			}
		}
		if p := o.Props.Get(ical.PropTransparency); p != nil && c.Props.Get(ical.PropTransparency) == nil {
			c.Props.Set(p)
		}
	}
}

// applyCancel marks the recipient's copy cancelled (or excludes cancelled
// instances of a series).
func (s *Service) applyCancel(to store.User, itip *ical.Calendar) error {
	comps := schedulable(itip)
	uid := getValue(comps[0], ical.PropUID)
	coll, obj, err := s.Store.FindByUID(to.ID, uid)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	cal, err := decode(obj.Data)
	if err != nil {
		return nil
	}
	mine := schedulable(cal)
	for _, cc := range comps {
		seq := getValue(cc, ical.PropSequence)
		rid := ridKey(cc)
		if rid == "" { // whole series
			for _, c := range mine {
				setProp(c, newProp(ical.PropStatus, "CANCELLED"))
				if seq != "" {
					setProp(c, newProp(ical.PropSequence, seq))
				}
			}
			continue
		}
		matched := false
		for _, c := range mine {
			if ridKey(c) == rid {
				setProp(c, newProp(ical.PropStatus, "CANCELLED"))
				matched = true
			}
		}
		if !matched { // a plain instance of the series: exclude it
			ridProp := cc.Props.Get(ical.PropRecurrenceID)
			for _, c := range mine {
				if ridKey(c) == "" {
					ex := newProp(ical.PropExceptionDates, rid)
					for k, v := range ridProp.Params {
						ex.Params[k] = append([]string(nil), v...)
					}
					c.Props.Add(&ex)
				}
			}
		}
	}
	return s.putRaw(coll, obj.URI, uid, mine[0].Name, encode(cal))
}

// applyReply records an attendee's answer in the organizer's calendar copy.
func (s *Service) applyReply(organizer store.User, itip *ical.Calendar) error {
	comps := schedulable(itip)
	uid := getValue(comps[0], ical.PropUID)
	coll, obj, err := s.Store.FindByUID(organizer.ID, uid)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	cal, err := decode(obj.Data)
	if err != nil {
		return nil
	}
	mine := schedulable(cal)
	changed := false
	for _, rc := range comps {
		if sequenceOf(rc) < maxSequence(mine) {
			continue // stale reply
		}
		rid := ridKey(rc)
		for _, ra := range rc.Props.Values(ical.PropAttendee) {
			want := normAddr(ra.Value)
			ps := ra.Params.Get(ical.ParamParticipationStatus)
			if ps == "" {
				continue
			}
			for _, c := range mine {
				if rid != ridKey(c) && !(rid == "" && ridKey(c) != "") {
					continue
				}
				for i := range c.Props[ical.PropAttendee] {
					p := &c.Props[ical.PropAttendee][i]
					if normAddr(p.Value) != want {
						continue
					}
					p.Params.Set(ical.ParamParticipationStatus, strings.ToUpper(ps))
					p.Params.Del(ical.ParamRSVP)
					p.Params.Set("SCHEDULE-STATUS", StatusSuccess)
					changed = true
				}
			}
		}
	}
	if !changed {
		return nil
	}
	return s.putRaw(coll, obj.URI, uid, mine[0].Name, encode(cal))
}
