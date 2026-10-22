package sched

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-ical"

	"github.com/bitflipp/galdav/internal/index"
	"github.com/bitflipp/galdav/internal/store"
)

// Service performs scheduling for the DAV server.
type Service struct {
	Store *store.Store
	// BasePath is the DAV URL prefix, e.g. "/dav".
	BasePath string
	// Mail delivers iMIP messages to addresses without a local account; nil
	// disables email delivery.
	Mail Mailer
	// Now is overridable in tests.
	Now func() time.Time
}

func New(st *store.Store, basePath string, mail Mailer) *Service {
	return &Service{Store: st, BasePath: basePath, Mail: mail}
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// --- addresses --------------------------------------------------------------

func (s *Service) principalPath(username string) string {
	return s.BasePath + "/principals/" + url.PathEscape(username) + "/"
}

// Addresses returns u's calendar user addresses, preferred first.
func (s *Service) Addresses(u store.User) []string {
	var out []string
	if u.Email != "" {
		out = append(out, "mailto:"+u.Email)
	}
	return append(out, s.principalPath(u.Username))
}

// pathOf extracts the path of an http(s) calendar address, or "".
func pathOf(addr string) string {
	if !strings.HasPrefix(addr, "/") && !strings.HasPrefix(strings.ToLower(addr), "http") {
		return ""
	}
	u, err := url.Parse(addr)
	if err != nil {
		return ""
	}
	return u.Path
}

// isUser reports whether addr is one of u's addresses.
func (s *Service) isUser(addr string, u store.User) bool {
	a := normAddr(addr)
	if strings.HasPrefix(a, "mailto:") {
		return u.Email != "" && a == "mailto:"+u.Email
	}
	return pathOf(a) == s.principalPath(u.Username)
}

// lookup finds the local user an address belongs to.
func (s *Service) lookup(addr string) (store.User, bool) {
	a := normAddr(addr)
	if rest, ok := strings.CutPrefix(a, "mailto:"); ok {
		u, err := s.Store.UserByEmail(rest)
		return u, err == nil
	}
	if p := pathOf(a); strings.HasPrefix(p, s.BasePath+"/principals/") {
		name, _ := url.PathUnescape(strings.Trim(strings.TrimPrefix(p, s.BasePath+"/principals/"), "/"))
		if name != "" && !strings.Contains(name, "/") {
			u, err := s.Store.UserByName(name)
			return u, err == nil
		}
	}
	return store.User{}, false
}

// Lookup finds the local user a calendar address (mailto: or principal URL)
// belongs to.
func (s *Service) Lookup(addr string) (store.User, bool) { return s.lookup(addr) }

// --- scheduling objects -----------------------------------------------------

// schedObject is a calendar object taking part in scheduling.
type schedObject struct {
	cal       *ical.Calendar
	comps     []*ical.Component // VEVENT/VTODO components
	uid       string
	organizer *ical.Prop
}

// parseSched returns nil unless data holds a VEVENT/VTODO with an ORGANIZER
// and at least one ATTENDEE.
func parseSched(data []byte) *schedObject {
	cal, err := decode(data)
	if err != nil {
		return nil
	}
	o := &schedObject{cal: cal}
	hasAttendee := false
	for _, c := range schedulable(cal) {
		if c.Name != ical.CompEvent && c.Name != ical.CompToDo {
			return nil
		}
		o.comps = append(o.comps, c)
		if o.uid == "" {
			o.uid = getValue(c, ical.PropUID)
		}
		if o.organizer == nil {
			o.organizer = c.Props.Get(ical.PropOrganizer)
		}
		hasAttendee = hasAttendee || len(c.Props.Values(ical.PropAttendee)) > 0
	}
	if o.uid == "" || o.organizer == nil || !hasAttendee {
		return nil
	}
	return o
}

// attendees returns the distinct attendee addresses in first-seen order,
// with the property that names each.
func (o *schedObject) attendees() (addrs []string, props map[string]*ical.Prop) {
	props = map[string]*ical.Prop{}
	for _, c := range o.comps {
		vals := c.Props.Values(ical.PropAttendee)
		for i := range vals {
			a := normAddr(vals[i].Value)
			if _, seen := props[a]; !seen {
				props[a] = &vals[i]
				addrs = append(addrs, a)
			}
		}
	}
	return addrs, props
}

func ridKey(c *ical.Component) string { return getValue(c, ical.PropRecurrenceID) }

func partstat(c *ical.Component, isMe func(string) bool) (string, bool) {
	for _, a := range c.Props.Values(ical.PropAttendee) {
		if isMe(a.Value) {
			if ps := strings.ToUpper(a.Params.Get(ical.ParamParticipationStatus)); ps != "" {
				return ps, true
			}
			return "NEEDS-ACTION", true
		}
	}
	return "", false
}

// --- implicit scheduling on PUT/DELETE --------------------------------------

// ProcessPut runs implicit scheduling after u stored newData, replacing
// oldData (nil for a new object). It returns replacement data when the
// stored copy must be rewritten (to record SCHEDULE-STATUS), else nil.
// noReply (Schedule-Reply: F) stops an attendee's change from being sent to
// the organizer.
func (s *Service) ProcessPut(u store.User, oldData, newData []byte, noReply bool) []byte {
	nw := parseSched(newData)
	if nw == nil {
		return nil
	}
	var old *schedObject
	if oldData != nil {
		if bytes.Equal(oldData, newData) {
			return nil
		}
		old = parseSched(oldData)
	}
	if s.isUser(nw.organizer.Value, u) {
		return s.organizerPut(u, old, nw)
	}
	if !noReply {
		s.attendeePut(u, old, nw)
	}
	return nil
}

func (s *Service) organizerPut(u store.User, old, nw *schedObject) []byte {
	orgAddr := normAddr(nw.organizer.Value)
	sendable := func(o *schedObject) (addrs []string, props map[string]*ical.Prop) {
		all, ps := o.attendees()
		for _, a := range all {
			if a == orgAddr || s.isUser(a, u) || !scheduleAgentServer(ps[a]) {
				continue
			}
			addrs = append(addrs, a)
		}
		return addrs, ps
	}
	newAddrs, _ := sendable(nw)
	status := map[string]string{}

	// Attendees who are already invited hear about a change only if it is a
	// significant one (RFC 6638 3.2.8), so a typo fix in the title does not
	// send a new email. Local attendees are always updated: that is silent
	// for them and keeps their copy current. New attendees are always invited.
	significant := old == nil || significantChange(old, nw)
	var oldAddrs []string
	if old != nil {
		oldAddrs, _ = sendable(old)
	}
	invited := map[string]bool{}
	for _, a := range oldAddrs {
		invited[a] = true
	}
	var notify []string
	for _, a := range newAddrs {
		if _, local := s.lookup(a); significant || !invited[a] || local {
			notify = append(notify, a)
		}
	}

	if len(notify) > 0 {
		req := newCalendar("REQUEST")
		for _, c := range nw.cal.Children {
			cc := cloneComp(c)
			for i := range cc.Props[ical.PropAttendee] {
				cc.Props[ical.PropAttendee][i].Params.Del("SCHEDULE-STATUS")
			}
			req.Children = append(req.Children, cc)
		}
		for _, a := range notify {
			status[a] = s.deliver(orgAddr, a, "REQUEST", req)
		}
	}
	if old != nil {
		keep := map[string]bool{}
		for _, a := range newAddrs {
			keep[a] = true
		}
		var removed []string
		for _, a := range oldAddrs {
			if !keep[a] {
				removed = append(removed, a)
			}
		}
		if len(removed) > 0 {
			cancel := s.cancelCalendar(old)
			for _, a := range removed {
				s.deliver(orgAddr, a, "CANCEL", cancel)
			}
		}
	}

	changed := false
	for _, c := range nw.comps {
		for i := range c.Props[ical.PropAttendee] {
			p := &c.Props[ical.PropAttendee][i]
			if st, ok := status[normAddr(p.Value)]; ok && p.Params.Get("SCHEDULE-STATUS") != st {
				p.Params.Set("SCHEDULE-STATUS", st)
				changed = true
			}
		}
	}
	if !changed {
		return nil
	}
	return encode(nw.cal)
}

// cancelCalendar builds a METHOD:CANCEL message for o.
func (s *Service) cancelCalendar(o *schedObject) *ical.Calendar {
	cancel := newCalendar("CANCEL")
	seq := maxSequence(o.comps) + 1
	for _, c := range o.cal.Children {
		cc := cloneComp(c)
		if cc.Name != ical.CompTimezone {
			setProp(cc, newProp(ical.PropStatus, "CANCELLED"))
			setProp(cc, newProp(ical.PropSequence, strconv.Itoa(seq)))
			setProp(cc, newProp(ical.PropDateTimeStamp, utc(s.now())))
		}
		cancel.Children = append(cancel.Children, cc)
	}
	return cancel
}

func (s *Service) attendeePut(u store.User, old, nw *schedObject) {
	isMe := func(a string) bool { return s.isUser(a, u) }
	if !scheduleAgentServer(nw.organizer) {
		return
	}
	oldState := map[string]string{}
	if old != nil {
		for _, c := range old.comps {
			if ps, ok := partstat(c, isMe); ok {
				oldState[ridKey(c)] = ps
			}
		}
	}
	var changed []*ical.Component
	for _, c := range nw.comps {
		ps, ok := partstat(c, isMe)
		if !ok || ps == "NEEDS-ACTION" {
			continue
		}
		if prev, had := oldState[ridKey(c)]; !had || prev != ps {
			changed = append(changed, c)
		}
	}
	if len(changed) == 0 {
		return
	}
	s.sendReply(u, nw, changed, "")
}

// sendReply sends a METHOD:REPLY for comps to the organizer. A non-empty
// forced overrides the PARTSTAT.
func (s *Service) sendReply(u store.User, o *schedObject, comps []*ical.Component, forced string) {
	reply := newCalendar("REPLY")
	var from string
	for _, c := range comps {
		rc := ical.NewComponent(c.Name)
		for _, name := range []string{ical.PropUID, ical.PropSequence, ical.PropRecurrenceID, ical.PropOrganizer,
			ical.PropSummary, ical.PropDateTimeStart, ical.PropDateTimeEnd, ical.PropDue} {
			for _, p := range c.Props.Values(name) {
				cp := p
				cp.Params = ical.Params{}
				for k, v := range p.Params {
					cp.Params[k] = append([]string(nil), v...)
				}
				rc.Props.Add(&cp)
			}
		}
		setProp(rc, newProp(ical.PropDateTimeStamp, utc(s.now())))
		for _, a := range c.Props.Values(ical.PropAttendee) {
			if !s.isUser(a.Value, u) {
				continue
			}
			ap := a
			ap.Params = ical.Params{}
			for k, v := range a.Params {
				ap.Params[k] = append([]string(nil), v...)
			}
			ap.Params.Del(ical.ParamRSVP)
			ap.Params.Del("SCHEDULE-STATUS")
			if forced != "" {
				ap.Params.Set(ical.ParamParticipationStatus, forced)
			}
			rc.Props.Set(&ap)
			from = normAddr(a.Value)
			break
		}
		reply.Children = append(reply.Children, rc)
	}
	if from == "" {
		return
	}
	s.deliver(from, normAddr(o.organizer.Value), "REPLY", reply)
}

// ProcessDelete runs implicit scheduling after u deleted a calendar object.
func (s *Service) ProcessDelete(u store.User, oldData []byte, noReply bool) {
	old := parseSched(oldData)
	if old == nil {
		return
	}
	if s.isUser(old.organizer.Value, u) {
		orgAddr := normAddr(old.organizer.Value)
		addrs, props := old.attendees()
		cancel := s.cancelCalendar(old)
		for _, a := range addrs {
			if a == orgAddr || s.isUser(a, u) || !scheduleAgentServer(props[a]) {
				continue
			}
			s.deliver(orgAddr, a, "CANCEL", cancel)
		}
		return
	}
	if noReply || !scheduleAgentServer(old.organizer) {
		return
	}
	isMe := func(a string) bool { return s.isUser(a, u) }
	var comps []*ical.Component
	for _, c := range old.comps {
		if ps, ok := partstat(c, isMe); ok && ps != "DECLINED" {
			comps = append(comps, c)
		}
	}
	if len(comps) > 0 {
		s.sendReply(u, old, comps, "DECLINED")
	}
}

// --- delivery ---------------------------------------------------------------

// Request-status codes (RFC 5546 3.6 / RFC 6638 3.2.9).
const (
	StatusSent        = "1.1"
	StatusDelivered   = "1.2"
	StatusSuccess     = "2.0"
	StatusInvalidUser = "3.7"
	StatusNoAuthority = "3.8"
	StatusUnavailable = "5.1"
	StatusNoScheduler = "5.3"
)

// StatusText gives the description that accompanies a status code in
// schedule-response elements.
var StatusText = map[string]string{
	StatusSent: "Sent", StatusDelivered: "Delivered", StatusSuccess: "Success",
	StatusInvalidUser: "Invalid Calendar User", StatusNoAuthority: "No authority", StatusUnavailable: "Service unavailable",
	StatusNoScheduler: "No scheduling support for user",
}

// Deliver sends an iTIP message and returns the request-status code.
func (s *Service) Deliver(fromAddr, toAddr, method string, itip *ical.Calendar) string {
	return s.deliver(normAddr(fromAddr), normAddr(toAddr), method, itip)
}

func (s *Service) deliver(fromAddr, toAddr, method string, itip *ical.Calendar) string {
	if to, ok := s.lookup(toAddr); ok {
		if err := s.deliverLocal(to, fromAddr, method, itip); err != nil {
			slog.Error("scheduling delivery", "to", toAddr, "method", method, "err", err)
			return StatusUnavailable
		}
		return StatusDelivered
	}
	if strings.HasPrefix(toAddr, "mailto:") {
		if s.Mail == nil {
			return StatusNoScheduler
		}
		if err := s.Mail.Send(Message{To: strings.TrimPrefix(toAddr, "mailto:"), From: strings.TrimPrefix(fromAddr, "mailto:"),
			Method: method, ICS: encode(itip), Summary: summaryOf(itip)}); err != nil {
			slog.Error("iMIP delivery", "to", toAddr, "err", err)
			return StatusUnavailable
		}
		return StatusSent
	}
	return StatusInvalidUser
}

func summaryOf(cal *ical.Calendar) string {
	for _, c := range schedulable(cal) {
		if v := getValue(c, ical.PropSummary); v != "" {
			return v
		}
	}
	return ""
}

func (s *Service) deliverLocal(to store.User, fromAddr, method string, itip *ical.Calendar) error {
	comps := schedulable(itip)
	if len(comps) == 0 {
		return errors.New("empty iTIP message")
	}
	uid := getValue(comps[0], ical.PropUID)
	inbox, err := s.Store.Inbox(to.ID)
	if err != nil {
		return err
	}
	sum := sha1.Sum([]byte(uid + "|" + fromAddr))
	data := encode(itip)
	if err := s.putRaw(inbox, "itip-"+hex.EncodeToString(sum[:10])+".ics", uid, comps[0].Name, data); err != nil {
		return err
	}
	switch method {
	case "REQUEST":
		return s.applyRequest(to, itip)
	case "CANCEL":
		return s.applyCancel(to, itip)
	case "REPLY":
		return s.applyReply(to, itip)
	}
	return nil
}

// putRaw stores data without validation, indexing it when it parses.
func (s *Service) putRaw(c store.Collection, uri, uid, component string, data []byte) error {
	o := store.Object{CollectionID: c.ID, URI: uri, UID: uid, ETag: index.ETag(data), Data: data, Component: component}
	if p, bad := index.Calendar(data, strings.Split(c.Components, ",")); bad == "" {
		o.FirstOcc, o.LastOcc = p.First, p.Last
	}
	_, err := s.Store.PutObject(o, store.PutCond{AllowDupUID: true})
	return err
}

// putScheduled stores a scheduling object changed by the server (an
// invitation, cancellation or reply was applied), which gives it a new
// Schedule-Tag.
func (s *Service) putScheduled(c store.Collection, uri, uid, component string, data []byte) error {
	o := store.Object{CollectionID: c.ID, URI: uri, UID: uid, ETag: index.ETag(data), Data: data, Component: component, ScheduleTag: NewScheduleTag()}
	if p, bad := index.Calendar(data, strings.Split(c.Components, ",")); bad == "" {
		o.FirstOcc, o.LastOcc = p.First, p.Last
	}
	_, err := s.Store.PutObject(o, store.PutCond{AllowDupUID: true})
	return err
}

var plainUID = regexp.MustCompile(`^[A-Za-z0-9._@-]{1,100}$`)

func uidURI(uid string) string {
	if plainUID.MatchString(uid) {
		return uid + ".ics"
	}
	sum := sha1.Sum([]byte(uid))
	return "itip-" + hex.EncodeToString(sum[:10]) + ".ics"
}
