package sched

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/emersion/go-ical"

	"github.com/Top-Ranger/whitegopher/internal/store"
)

// TextMailer sends a plain-text email. *SMTP implements it.
type TextMailer interface {
	SendText(to, subject, text string) error
}

// notifyLocal emails a local user about a new invitation or a cancellation,
// for calendar apps that do not surface the scheduling inbox. had says the
// user already had a copy of the event before the message was applied.
// Updates to events they already know are not mailed.
func (s *Service) notifyLocal(to store.User, method string, itip *ical.Calendar, had bool) {
	if !to.NotifyInvites || to.Email == "" {
		return
	}
	tm, ok := s.Mail.(TextMailer)
	if !ok {
		return
	}
	var verb string
	switch {
	case method == "REQUEST" && !had:
		verb = "Invitation"
	case method == "CANCEL" && had:
		verb = "Cancelled"
	default:
		return
	}
	comps := schedulable(itip)
	if len(comps) == 0 {
		return
	}
	subject, body := notifyText(verb, comps[0], to.Email)
	// Sent in the background: a slow relay must not stall the organizer's request.
	go func() {
		if err := tm.SendText(to.Email, subject, body); err != nil {
			slog.Error("notification mail", "to", to.Email, "err", err)
		}
	}()
}

func notifyText(verb string, c *ical.Component, self string) (subject, body string) {
	title := getValue(c, ical.PropSummary)
	if title == "" {
		title = "(no title)"
	}
	subject = verb + ": " + title
	var b strings.Builder
	if verb == "Cancelled" {
		fmt.Fprintf(&b, "An event was cancelled.\n\n")
	} else {
		fmt.Fprintf(&b, "You have been invited to a new event.\n\n")
	}
	fmt.Fprintf(&b, "Title:     %s\n", title)
	if when := whenText(c); when != "" {
		fmt.Fprintf(&b, "When:      %s\n", when)
	}
	if org := organizerText(c); org != "" {
		fmt.Fprintf(&b, "Organizer: %s\n", org)
	}
	if loc := getValue(c, ical.PropLocation); loc != "" {
		fmt.Fprintf(&b, "Where:     %s\n", loc)
	}
	if verb == "Cancelled" {
		b.WriteString("\nYour calendar has been updated.\n")
	} else {
		b.WriteString("\nThe event is already in your calendar.\n")
	}
	return subject, b.String()
}

func organizerText(c *ical.Component) string {
	p := c.Props.Get(ical.PropOrganizer)
	if p == nil {
		return ""
	}
	addr := strings.TrimPrefix(normAddr(p.Value), "mailto:")
	if cn := p.Params.Get(ical.ParamCommonName); cn != "" {
		if strings.HasPrefix(addr, "/") || strings.HasPrefix(addr, "http") {
			return cn
		}
		return cn + " <" + addr + ">"
	}
	return addr
}

// whenText renders the start (and end) of c, in the zone the event names.
func whenText(c *ical.Component) string {
	sp := c.Props.Get(ical.PropDateTimeStart)
	if sp == nil {
		return ""
	}
	start, err := sp.DateTime(time.UTC)
	if err != nil {
		return ""
	}
	const dayFmt = "Monday, 2 January 2006"
	var end time.Time
	hasEnd := false
	if ep := c.Props.Get(ical.PropDateTimeEnd); ep != nil {
		if e, err := ep.DateTime(time.UTC); err == nil {
			end, hasEnd = e, true
		}
	}
	if strings.EqualFold(sp.Params.Get(ical.ParamValue), "DATE") {
		out := start.Format(dayFmt) + " (all day)"
		// DTEND of a date event is exclusive.
		if hasEnd && end.Sub(start) > 24*time.Hour {
			out = start.Format(dayFmt) + " – " + end.AddDate(0, 0, -1).Format(dayFmt) + " (all day)"
		}
		return out
	}
	out := start.Format(dayFmt + ", 15:04")
	if hasEnd && end.After(start) {
		end = end.In(start.Location())
		if end.Format("2006-01-02") == start.Format("2006-01-02") {
			out += "–" + end.Format("15:04")
		} else {
			out += " – " + end.Format(dayFmt+", 15:04")
		}
	}
	if sp.Params.Get(ical.ParamTimezoneID) != "" || strings.HasSuffix(sp.Value, "Z") {
		out += " " + start.Format("MST")
	}
	if c.Props.Get(ical.PropRecurrenceRule) != nil {
		out += " (repeats)"
	}
	return out
}
