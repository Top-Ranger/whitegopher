package admin_test

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/Top-Ranger/whitegopher/internal/auth"
	"github.com/Top-Ranger/whitegopher/internal/store"
)

func TestShareInvitationInAdminUI(t *testing.T) {
	e := newEnvOpts(t, true)
	hash, _ := auth.HashPassword("hunter2hunter2")
	aliceID, _ := e.st.CreateUser(store.User{Username: "alice", PasswordHash: hash})
	bobID, _ := e.st.CreateUser(store.User{Username: "bob", PasswordHash: hash})
	cid, _ := e.st.CreateCollection(store.Collection{OwnerID: aliceID, Kind: store.KindCalendar, Slug: "work", DisplayName: "Work"})
	login := func(name string) *http.Client {
		c := newClient()
		resp, b := e.do(c, "POST", "/admin/login", url.Values{"username": {name}, "password": {"hunter2hunter2"}})
		status(t, resp, 303, b)
		return c
	}
	ac, bc := login("alice"), login("bob")
	collURL := "/admin/collections/" + strconv.FormatInt(cid, 10)

	resp, b := e.post(ac, collURL+"/shares", "username", "bob", "access", "readwrite")
	status(t, resp, 303, b)
	resp, b = e.do(ac, "GET", collURL, nil)
	contains(t, b, "waiting for them to accept")

	// Bob sees an invitation, not a shared calendar.
	resp, b = e.do(bc, "GET", "/admin/", nil)
	status(t, resp, 200, b)
	contains(t, b, "Invitations")
	contains(t, b, "Work")
	if strings.Contains(b, "Shared with you") {
		t.Fatal("pending share is already listed as shared")
	}
	sh, err := e.st.ShareFor(cid, bobID)
	if err != nil || sh.State != store.SharePending {
		t.Fatalf("%+v %v", sh, err)
	}
	id := strconv.FormatInt(sh.ID, 10)

	// Nobody else can answer for bob.
	resp, b = e.post(ac, "/admin/invites/"+id+"/accept")
	status(t, resp, 404, b)

	resp, b = e.post(bc, "/admin/invites/"+id+"/accept")
	status(t, resp, 303, b)
	resp, b = e.do(bc, "GET", "/admin/", nil)
	contains(t, b, "Shared with you")
	if strings.Contains(b, "Invitations") {
		t.Fatal("answered invitation still listed")
	}
	resp, b = e.do(ac, "GET", collURL, nil)
	if strings.Contains(b, "waiting for them to accept") {
		t.Fatal("owner still sees the share as waiting")
	}

	// Declining a fresh invitation leaves it out of bob's home.
	resp, b = e.post(ac, collURL+"/shares/"+strconv.FormatInt(bobID, 10)+"/remove")
	status(t, resp, 303, b)
	resp, b = e.post(ac, collURL+"/shares", "username", "bob", "access", "read")
	status(t, resp, 303, b)
	sh, _ = e.st.ShareFor(cid, bobID)
	resp, b = e.post(bc, "/admin/invites/"+strconv.FormatInt(sh.ID, 10)+"/decline")
	status(t, resp, 303, b)
	resp, b = e.do(bc, "GET", "/admin/", nil)
	if strings.Contains(b, "Shared with you") || strings.Contains(b, "Invitations") {
		t.Fatalf("declined share still shown:\n%s", b)
	}
	resp, b = e.do(ac, "GET", collURL, nil)
	contains(t, b, "declined")
}

func TestPublicLinkInAdminUI(t *testing.T) {
	e := newEnv(t)
	hash, _ := auth.HashPassword("hunter2hunter2")
	aliceID, _ := e.st.CreateUser(store.User{Username: "alice", PasswordHash: hash})
	bobID, _ := e.st.CreateUser(store.User{Username: "bob", PasswordHash: hash})
	cid, _ := e.st.CreateCollection(store.Collection{OwnerID: aliceID, Kind: store.KindCalendar, Slug: "work"})
	login := func(name string) *http.Client {
		c := newClient()
		resp, b := e.do(c, "POST", "/admin/login", url.Values{"username": {name}, "password": {"hunter2hunter2"}})
		status(t, resp, 303, b)
		return c
	}
	ac, bc := login("alice"), login("bob")
	_ = bobID
	collURL := "/admin/collections/" + strconv.FormatInt(cid, 10)

	resp, b := e.do(ac, "GET", collURL, nil)
	contains(t, b, "Create public link")
	resp, b = e.post(bc, collURL+"/public")
	status(t, resp, 404, b) // not bob's calendar
	if _, err := e.st.PublicLink(cid); err == nil {
		t.Fatal("bob created a link for alice's calendar")
	}

	resp, b = e.post(ac, collURL+"/public")
	status(t, resp, 303, b)
	tok, err := e.st.PublicLink(cid)
	if err != nil {
		t.Fatal(err)
	}
	resp, b = e.do(ac, "GET", collURL, nil)
	contains(t, b, "/public/"+tok+".ics")

	// The link works without a login, through the same server.
	resp, b = e.do(newClient(), "GET", "/public/"+tok+".ics", nil)
	status(t, resp, 200, b)
	contains(t, b, "BEGIN:VCALENDAR")

	resp, b = e.post(ac, collURL+"/public")
	status(t, resp, 303, b)
	if tok2, _ := e.st.PublicLink(cid); tok2 == tok {
		t.Fatal("link not renewed")
	}
	resp, b = e.do(newClient(), "GET", "/public/"+tok+".ics", nil)
	status(t, resp, 404, b)

	resp, b = e.post(ac, collURL+"/public/delete")
	status(t, resp, 303, b)
	if _, err := e.st.PublicLink(cid); err == nil {
		t.Fatal("link not removed")
	}
	resp, b = e.do(ac, "GET", collURL, nil)
	contains(t, b, "Create public link")
}
