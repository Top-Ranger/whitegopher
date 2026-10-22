package sched

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"strings"

	"github.com/emersion/go-ical"

	"github.com/bitflipp/galdav/internal/store"
)

// ErrNotITIP means a message carried no calendar part galdav acts on.
var ErrNotITIP = errors.New("sched: message has no iTIP calendar part")

// InboundResult describes what became of one received iMIP message.
type InboundResult struct {
	Method    string
	UID       string
	Sender    string
	Delivered []string // usernames whose calendars were updated
	Ignored   string   // why nothing was delivered, if so
}

// ReceiveMail processes an iMIP message (RFC 6047) arriving by email: a REPLY
// from an external attendee to a local organizer, or a REQUEST/CANCEL from an
// external organizer to local attendees. Email is trivially forged, so the
// calendar's ORGANIZER/ATTENDEE must match the message's From (or Sender); with
// requireAuth the receiving MTA must also have vouched for the sender in an
// Authentication-Results header (dkim, spf or dmarc pass).
func (s *Service) ReceiveMail(raw io.Reader, requireAuth bool) (InboundResult, error) {
	var res InboundResult
	msg, err := mail.ReadMessage(raw)
	if err != nil {
		return res, fmt.Errorf("read message: %w", err)
	}
	sender, err := senderAddress(msg.Header)
	if err != nil {
		res.Ignored = "no usable From address"
		return res, nil
	}
	res.Sender = sender
	if requireAuth && !authenticated(msg.Header) {
		res.Ignored = "sender not authenticated by the mail server (Authentication-Results)"
		return res, nil
	}
	body, err := calendarPart(msg.Header.Get("Content-Type"), msg.Header.Get("Content-Transfer-Encoding"), msg.Body, 0)
	if err != nil {
		return res, err
	}
	cal, err := decode(body)
	if err != nil {
		res.Ignored = "calendar part does not parse"
		return res, nil
	}
	comps := schedulable(cal)
	res.Method = strings.ToUpper(getValue(cal.Component, ical.PropMethod))
	if len(comps) == 0 {
		res.Ignored = "no event or task in the message"
		return res, nil
	}
	if c := comps[0]; c.Name != ical.CompEvent && c.Name != ical.CompToDo {
		res.Ignored = "not an event or task"
		return res, nil
	}
	res.UID = getValue(comps[0], ical.PropUID)
	from := "mailto:" + sender

	switch res.Method {
	case "REPLY":
		// Every ATTENDEE in a reply must be the sender; drop anything else.
		for _, c := range comps {
			var mine []ical.Prop
			for _, a := range c.Props.Values(ical.PropAttendee) {
				if normAddr(a.Value) == from {
					mine = append(mine, a)
				}
			}
			c.Props.Del(ical.PropAttendee)
			for i := range mine {
				c.Props.Add(&mine[i])
			}
			if len(mine) == 0 {
				res.Ignored = "the reply is not from the attendee it names"
				return res, nil
			}
		}
		org := comps[0].Props.Get(ical.PropOrganizer)
		if org == nil {
			res.Ignored = "reply without ORGANIZER"
			return res, nil
		}
		to, ok := s.lookup(org.Value)
		if !ok {
			res.Ignored = "organizer is not a local user"
			return res, nil
		}
		if err := s.deliverLocal(to, from, "REPLY", cal); err != nil {
			return res, err
		}
		res.Delivered = append(res.Delivered, to.Username)
	case "REQUEST", "CANCEL":
		org := comps[0].Props.Get(ical.PropOrganizer)
		if org == nil || normAddr(org.Value) != from {
			res.Ignored = "the organizer is not the sender"
			return res, nil
		}
		if _, local := s.lookup(org.Value); local {
			res.Ignored = "organizer is a local user; invitations for them never arrive by email"
			return res, nil
		}
		seen := map[int64]bool{}
		for _, c := range comps {
			for _, a := range c.Props.Values(ical.PropAttendee) {
				to, ok := s.lookup(a.Value)
				if !ok || seen[to.ID] {
					continue
				}
				seen[to.ID] = true
				// Never let a message overwrite an event the user organizes
				// or one that came from somebody else.
				if !s.mayUpdateFrom(to, res.UID, from) {
					continue
				}
				if err := s.deliverLocal(to, from, res.Method, cal); err != nil {
					return res, err
				}
				res.Delivered = append(res.Delivered, to.Username)
			}
		}
		if len(res.Delivered) == 0 {
			res.Ignored = "no local attendee could be updated"
		}
	default:
		res.Ignored = "method " + res.Method + " is not handled"
	}
	return res, nil
}

// mayUpdateFrom reports whether an iTIP message from organizer from may
// create or update user's copy of uid: yes if there is none yet, or if the
// existing copy names the same organizer.
func (s *Service) mayUpdateFrom(user store.User, uid, from string) bool {
	_, obj, err := s.Store.FindByUID(user.ID, uid)
	if errors.Is(err, store.ErrNotFound) {
		return true
	}
	if err != nil {
		return false
	}
	existing := parseSched(obj.Data)
	return existing != nil && normAddr(existing.organizer.Value) == from
}

func senderAddress(h mail.Header) (string, error) {
	a, err := mail.ParseAddress(h.Get("From"))
	if err != nil {
		if a, err = mail.ParseAddress(h.Get("Sender")); err != nil {
			return "", err
		}
	}
	return strings.ToLower(a.Address), nil
}

// authenticated checks the topmost Authentication-Results header, which is
// the one added by our own mail server (earlier ones can be forged).
func authenticated(h mail.Header) bool {
	v := h.Get("Authentication-Results")
	if v == "" {
		return false
	}
	v = strings.ToLower(v)
	for _, ok := range []string{"dmarc=pass", "dkim=pass", "spf=pass"} {
		if strings.Contains(v, ok) {
			return true
		}
	}
	return false
}

// calendarPart finds the text/calendar part of a (possibly multipart) body.
func calendarPart(contentType, encoding string, body io.Reader, depth int) ([]byte, error) {
	if depth > 5 {
		return nil, ErrNotITIP
	}
	if contentType == "" {
		contentType = "text/plain"
	}
	mt, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return nil, ErrNotITIP
	}
	switch {
	case mt == "text/calendar" || mt == "application/ics":
		raw, err := io.ReadAll(io.LimitReader(decodeTransfer(body, encoding), 1<<20))
		return raw, err
	case strings.HasPrefix(mt, "multipart/"):
		mr := multipart.NewReader(body, params["boundary"])
		for {
			p, err := mr.NextRawPart()
			if err == io.EOF {
				return nil, ErrNotITIP
			}
			if err != nil {
				return nil, ErrNotITIP
			}
			got, err := calendarPart(p.Header.Get("Content-Type"), p.Header.Get("Content-Transfer-Encoding"), p, depth+1)
			if err == nil {
				return got, nil
			}
			if !errors.Is(err, ErrNotITIP) {
				return nil, err
			}
		}
	}
	return nil, ErrNotITIP
}

func decodeTransfer(r io.Reader, encoding string) io.Reader {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "base64":
		return base64.NewDecoder(base64.StdEncoding, newlineStripper{r})
	case "quoted-printable":
		return quotedprintable.NewReader(r)
	}
	return r
}

// newlineStripper drops CR/LF so base64 bodies with line breaks decode.
type newlineStripper struct{ r io.Reader }

func (n newlineStripper) Read(p []byte) (int, error) {
	buf := make([]byte, len(p))
	for {
		m, err := n.r.Read(buf)
		out := bytes.ReplaceAll(bytes.ReplaceAll(buf[:m], []byte("\r"), nil), []byte("\n"), nil)
		if len(out) > 0 || err != nil {
			return copy(p, out), err
		}
	}
}
