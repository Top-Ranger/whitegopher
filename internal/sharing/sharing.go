// Package sharing manages invitations to share calendars and address books:
// sharing a collection, answering an invitation, and the CalendarServer-style
// notification resources clients read invitations from.
package sharing

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/bitflipp/galdav/internal/index"
	"github.com/bitflipp/galdav/internal/store"
)

// Service shares collections between users.
type Service struct {
	Store *store.Store
	// BasePath is the DAV URL prefix, e.g. "/dav".
	BasePath string
	// RequireAccept makes new shares wait until the sharee accepts them.
	RequireAccept bool

	// Now is overridable in tests.
	Now func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Share shares c (owned by owner) with another user at the access level.
// With RequireAccept a new invitation lands in the sharee's notifications.
func (s *Service) Share(owner store.User, c store.Collection, with store.User, access string) (store.Share, error) {
	sh, err := s.Store.SetShare(c, with.ID, access, s.RequireAccept)
	if err != nil {
		return sh, err
	}
	if sh.State == store.SharePending {
		return sh, s.putInvite(owner, c, with, sh, statusNoResponse)
	}
	// A pending invitation that no longer needs an answer (the owner re-shared
	// with acceptance off): bring its notification up to date if it exists.
	return sh, s.updateInvite(owner, c, with, sh, statusFor(sh.State))
}

// Unshare ends a share. A sharee who had not answered is told the invitation
// was withdrawn.
func (s *Service) Unshare(owner store.User, c store.Collection, with store.User) error {
	sh, err := s.Store.ShareFor(c.ID, with.ID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return err
	}
	if err := s.Store.DeleteShare(c.ID, with.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if sh.State == store.SharePending {
		return s.putInvite(owner, c, with, sh, statusDeleted)
	}
	return s.updateInvite(owner, c, with, sh, statusDeleted)
}

// Answer records the sharee's answer to an invitation. The owner is told by a
// reply notification when the invitation was still open.
func (s *Service) Answer(sharee store.User, sh store.Share, accept bool) error {
	state := store.ShareDeclined
	if accept {
		state = store.ShareAccepted
	}
	if sh.State == state {
		return nil
	}
	if err := s.Store.SetShareState(sh.ID, sharee.ID, state); err != nil {
		return err
	}
	c, err := s.Store.GetCollectionByID(sh.CollectionID)
	if err != nil {
		return err
	}
	owner, err := s.Store.UserByID(c.OwnerID)
	if err != nil {
		return err
	}
	if err := s.updateInvite(owner, c, sharee, sh, statusFor(state)); err != nil {
		return err
	}
	if sh.State == store.SharePending {
		return s.putReply(owner, c, sharee, sh, state)
	}
	return nil
}

// --- notifications ----------------------------------------------------------

type inviteStatus string

const (
	statusNoResponse inviteStatus = "invite-noresponse"
	statusAccepted   inviteStatus = "invite-accepted"
	statusDeclined   inviteStatus = "invite-declined"
	statusDeleted    inviteStatus = "invite-deleted"
)

func statusFor(state string) inviteStatus {
	switch state {
	case store.ShareAccepted:
		return statusAccepted
	case store.ShareDeclined:
		return statusDeclined
	}
	return statusNoResponse
}

// Notification kinds, stored in the object's component column and reported
// as CS:notificationtype.
const (
	KindInvite = "invite-notification"
	KindReply  = "invite-reply"
)

func (s *Service) collHref(owner store.User, c store.Collection) string {
	area := "calendars"
	if c.Kind == store.KindAddressBook {
		area = "addressbooks"
	}
	return s.BasePath + "/" + area + "/" + url.PathEscape(owner.Username) + "/" + url.PathEscape(c.Slug) + "/"
}

func addrOf(u store.User, base string) string {
	if u.Email != "" {
		return "mailto:" + u.Email
	}
	return base + "/principals/" + url.PathEscape(u.Username) + "/"
}

func nameOf(u store.User) string {
	if u.DisplayName != "" {
		return u.DisplayName
	}
	return u.Username
}

func esc(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}

const nsDecl = ` xmlns:D="DAV:" xmlns:CS="http://calendarserver.org/ns/" xmlns:C="urn:ietf:params:xml:ns:caldav"`

func (s *Service) inviteXML(owner store.User, c store.Collection, with store.User, sh store.Share, status inviteStatus) []byte {
	shared := "calendar"
	if c.Kind == store.KindAddressBook {
		shared = "addressbook"
	}
	access := "<CS:read/>"
	if sh.Access == store.AccessReadWrite {
		access = "<CS:read-write/>"
	}
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n<CS:notification" + nsDecl + ">")
	b.WriteString("<CS:dtstamp>" + s.now().UTC().Format("20060102T150405Z") + "</CS:dtstamp>")
	b.WriteString(`<CS:invite-notification shared-type="` + shared + `">`)
	b.WriteString("<CS:uid>" + esc(sh.InviteUID) + "</CS:uid>")
	b.WriteString("<D:href>" + esc(addrOf(with, s.BasePath)) + "</D:href>")
	b.WriteString("<CS:" + string(status) + "/>")
	b.WriteString("<CS:access>" + access + "</CS:access>")
	b.WriteString("<CS:hosturl><D:href>" + esc(s.collHref(owner, c)) + "</D:href></CS:hosturl>")
	b.WriteString("<CS:organizer><D:href>" + esc(addrOf(owner, s.BasePath)) + "</D:href><CS:common-name>" + esc(nameOf(owner)) + "</CS:common-name></CS:organizer>")
	name := c.DisplayName
	if name == "" {
		name = c.Slug
	}
	b.WriteString("<CS:summary>" + esc(name) + "</CS:summary>")
	if c.Kind == store.KindCalendar {
		b.WriteString("<C:supported-calendar-component-set>")
		for _, n := range strings.Split(c.Components, ",") {
			b.WriteString(`<C:comp name="` + esc(n) + `"/>`)
		}
		b.WriteString("</C:supported-calendar-component-set>")
	}
	b.WriteString("</CS:invite-notification></CS:notification>")
	return []byte(b.String())
}

func (s *Service) replyXML(owner store.User, c store.Collection, sharee store.User, sh store.Share, state string) []byte {
	status := statusAccepted
	if state == store.ShareDeclined {
		status = statusDeclined
	}
	name := c.DisplayName
	if name == "" {
		name = c.Slug
	}
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n<CS:notification" + nsDecl + ">")
	b.WriteString("<CS:dtstamp>" + s.now().UTC().Format("20060102T150405Z") + "</CS:dtstamp>")
	b.WriteString("<CS:invite-reply>")
	b.WriteString("<D:href>" + esc(addrOf(sharee, s.BasePath)) + "</D:href>")
	b.WriteString("<CS:" + string(status) + "/>")
	b.WriteString("<CS:hosturl><D:href>" + esc(s.collHref(owner, c)) + "</D:href></CS:hosturl>")
	b.WriteString("<CS:in-reply-to>" + esc(sh.InviteUID) + "</CS:in-reply-to>")
	b.WriteString("<CS:summary>" + esc(name) + "</CS:summary>")
	b.WriteString("<CS:common-name>" + esc(nameOf(sharee)) + "</CS:common-name>")
	b.WriteString("</CS:invite-reply></CS:notification>")
	return []byte(b.String())
}

func uri(uid string) string { return uid + ".xml" }

func (s *Service) put(userID int64, uid, kind string, data []byte) error {
	nc, err := s.Store.Notifications(userID)
	if err != nil {
		return err
	}
	o := store.Object{CollectionID: nc.ID, URI: uri(uid), UID: uid, ETag: index.ETag(data), Data: data, Component: kind}
	_, err = s.Store.PutObject(o, store.PutCond{AllowDupUID: true})
	return err
}

// putInvite writes (or replaces) the invitation notification in the sharee's
// notification collection.
func (s *Service) putInvite(owner store.User, c store.Collection, with store.User, sh store.Share, status inviteStatus) error {
	return s.put(with.ID, sh.InviteUID, KindInvite+":"+sharedType(c), s.inviteXML(owner, c, with, sh, status))
}

// updateInvite changes the status of an invitation notification the sharee
// still has; if it was dismissed there is nothing to update.
func (s *Service) updateInvite(owner store.User, c store.Collection, with store.User, sh store.Share, status inviteStatus) error {
	nc, err := s.Store.Notifications(with.ID)
	if err != nil {
		return err
	}
	if _, err := s.Store.GetObject(nc.ID, uri(sh.InviteUID)); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return err
	}
	return s.putInvite(owner, c, with, sh, status)
}

func (s *Service) putReply(owner store.User, c store.Collection, sharee store.User, sh store.Share, state string) error {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return err
	}
	return s.put(owner.ID, "reply-"+hex.EncodeToString(b), KindReply+":"+sharedType(c), s.replyXML(owner, c, sharee, sh, state))
}

func sharedType(c store.Collection) string {
	if c.Kind == store.KindAddressBook {
		return "addressbook"
	}
	return "calendar"
}
