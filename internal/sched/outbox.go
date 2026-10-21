package sched

import (
	"errors"
	"strings"
	"time"

	"github.com/emersion/go-ical"

	"github.com/bitflipp/galdav/internal/store"
)

// Errors from HandleOutbox, mapped to CalDAV preconditions by the caller.
var (
	ErrInvalidData      = errors.New("sched: invalid calendar data")
	ErrInvalidMessage   = errors.New("sched: invalid scheduling message")
	ErrInvalidOrganizer = errors.New("sched: originator is not the organizer/attendee")
)

// OutboxResult is the outcome for one recipient of an outbox POST.
type OutboxResult struct {
	Recipient string
	Status    string // request-status code, e.g. "2.0"
	Data      []byte // free-busy reply, when applicable
}

// HandleOutbox processes an iTIP message POSTed by u to their scheduling
// outbox (explicit scheduling, and free-busy requests). recipients, if
// non-empty, overrides the recipients derived from the message.
func (s *Service) HandleOutbox(u store.User, body []byte, recipients []string) ([]OutboxResult, error) {
	cal, err := decode(body)
	if err != nil {
		return nil, ErrInvalidData
	}
	method := strings.ToUpper(getValue(cal.Component, ical.PropMethod))
	comps := schedulable(cal)
	if method == "" || len(comps) == 0 {
		return nil, ErrInvalidMessage
	}
	first := comps[0]

	var sender string
	var defaults []string
	switch method {
	case "REPLY":
		atts := first.Props.Values(ical.PropAttendee)
		if len(atts) != 1 || !s.isUser(atts[0].Value, u) {
			return nil, ErrInvalidOrganizer
		}
		sender = normAddr(atts[0].Value)
		if org := first.Props.Get(ical.PropOrganizer); org != nil {
			defaults = []string{normAddr(org.Value)}
		}
	default:
		org := first.Props.Get(ical.PropOrganizer)
		if org == nil || !s.isUser(org.Value, u) {
			return nil, ErrInvalidOrganizer
		}
		sender = normAddr(org.Value)
		for _, c := range comps {
			for _, a := range c.Props.Values(ical.PropAttendee) {
				if n := normAddr(a.Value); n != sender && !s.isUser(n, u) && !contains(defaults, n) {
					defaults = append(defaults, n)
				}
			}
		}
	}
	if len(recipients) == 0 {
		recipients = defaults
	}
	for i := range recipients {
		recipients[i] = normAddr(recipients[i])
	}

	var out []OutboxResult
	if first.Name == ical.CompFreeBusy {
		if method != "REQUEST" {
			return nil, ErrInvalidMessage
		}
		start, e1 := dateTime(first, ical.PropDateTimeStart)
		end, e2 := dateTime(first, ical.PropDateTimeEnd)
		if e1 != nil || e2 != nil || !end.After(start) {
			return nil, ErrInvalidMessage
		}
		uid := getValue(first, ical.PropUID)
		for _, r := range recipients {
			res := OutboxResult{Recipient: r}
			if to, ok := s.lookup(r); ok {
				busy, tent, err := s.UserFreeBusy(to, start, end)
				if err != nil {
					res.Status = StatusUnavailable
				} else {
					res.Status = StatusSuccess
					res.Data = RenderFreeBusy("REPLY", uid, sender, r, start, end, s.now(), busy, tent)
				}
			} else if strings.HasPrefix(r, "mailto:") {
				res.Status = StatusNoScheduler
			} else {
				res.Status = StatusInvalidUser
			}
			out = append(out, res)
		}
		return out, nil
	}

	for _, r := range recipients {
		code := s.deliver(sender, r, method, cal)
		if code == StatusDelivered || code == StatusSent {
			code = StatusSuccess
		}
		out = append(out, OutboxResult{Recipient: r, Status: code})
	}
	return out, nil
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func dateTime(c *ical.Component, name string) (time.Time, error) {
	p := c.Props.Get(name)
	if p == nil {
		return time.Time{}, errors.New("missing " + name)
	}
	return p.DateTime(time.UTC)
}
