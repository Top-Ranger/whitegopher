package sharing_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/bitflipp/whitegopher/internal/sharing"
	"github.com/bitflipp/whitegopher/internal/store"
	"github.com/bitflipp/whitegopher/internal/storetest"
)

func TestExpirePending(t *testing.T) {
	st := storetest.New(t)
	svc := &sharing.Service{Store: st, BasePath: "/dav", RequireAccept: true}
	alice := store.User{Username: "alice"}
	bob := store.User{Username: "bob"}
	alice.ID, _ = st.CreateUser(alice)
	bob.ID, _ = st.CreateUser(bob)

	mk := func(slug string) store.Collection {
		c := store.Collection{OwnerID: alice.ID, Kind: store.KindCalendar, Slug: slug, Components: "VEVENT"}
		c.ID, _ = st.CreateCollection(c)
		return c
	}
	oldInvite, answered, fresh, redeclined := mk("old"), mk("answered"), mk("fresh"), mk("redeclined")
	for _, c := range []store.Collection{oldInvite, answered, fresh, redeclined} {
		if _, err := svc.Share(alice, c, bob, store.AccessRead); err != nil {
			t.Fatal(err)
		}
	}
	age := func(c store.Collection) {
		if _, err := st.DB.Exec(`UPDATE collection_shares SET state_changed_at = NOW() - INTERVAL 100 DAY WHERE collection_id = ?`, c.ID); err != nil {
			t.Fatal(err)
		}
	}
	old, _ := st.ShareFor(oldInvite.ID, bob.ID)
	oldUID := old.InviteUID
	age(oldInvite)
	age(answered)
	age(redeclined)

	// Accepted shares are never expired.
	sh, _ := st.ShareFor(answered.ID, bob.ID)
	if err := svc.Answer(bob, sh, true); err != nil {
		t.Fatal(err)
	}
	age(answered)
	// Asking again after a decline starts the clock afresh.
	sh, _ = st.ShareFor(redeclined.ID, bob.ID)
	if err := svc.Answer(bob, sh, false); err != nil {
		t.Fatal(err)
	}
	age(redeclined)
	if _, err := svc.Share(alice, redeclined, bob, store.AccessRead); err != nil {
		t.Fatal(err)
	}

	n, err := svc.ExpirePending(90)
	if err != nil || n != 1 {
		t.Fatalf("expired %d: %v", n, err)
	}
	if sh, err := st.ShareFor(oldInvite.ID, bob.ID); err != nil || sh.State != store.ShareExpired {
		t.Fatalf("old invitation: %+v %v", sh, err)
	}
	for c, want := range map[store.Collection]string{answered: store.ShareAccepted, fresh: store.SharePending, redeclined: store.SharePending} {
		if sh, err := st.ShareFor(c.ID, bob.ID); err != nil || sh.State != want {
			t.Fatalf("%s: %+v %v", c.Slug, sh, err)
		}
	}

	// The sharee is told the invitation was withdrawn.
	nc, _ := st.Notifications(bob.ID)
	o, err := st.GetObject(nc.ID, oldUID+".xml")
	if err != nil || !strings.Contains(string(o.Data), "<CS:invite-deleted/>") {
		t.Fatalf("no withdrawal notice: %v\n%s", err, o.Data)
	}

	// Nobody can accept an invitation that was withdrawn ...
	exp, _ := st.ShareFor(oldInvite.ID, bob.ID)
	if err := svc.Answer(bob, exp, true); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("answered an expired invitation: %v", err)
	}
	// ... but the owner can ask again, which reuses the invitation.
	again, err := svc.Share(alice, oldInvite, bob, store.AccessRead)
	if err != nil || again.State != store.SharePending || again.InviteUID != oldUID {
		t.Fatalf("invite again: %+v %v", again, err)
	}
}

func TestRemoveFinishedAndPruneNotifications(t *testing.T) {
	st := storetest.New(t)
	svc := &sharing.Service{Store: st, BasePath: "/dav", RequireAccept: true}
	alice := store.User{Username: "alice"}
	bob := store.User{Username: "bob"}
	alice.ID, _ = st.CreateUser(alice)
	bob.ID, _ = st.CreateUser(bob)
	mk := func(slug string) store.Collection {
		c := store.Collection{OwnerID: alice.ID, Kind: store.KindCalendar, Slug: slug, Components: "VEVENT"}
		c.ID, _ = st.CreateCollection(c)
		return c
	}
	declined, expired, open, recent := mk("declined"), mk("expired"), mk("open"), mk("recent")
	uids := map[string]string{}
	for _, c := range []store.Collection{declined, expired, open, recent} {
		sh, err := svc.Share(alice, c, bob, store.AccessRead)
		if err != nil {
			t.Fatal(err)
		}
		uids[c.Slug] = sh.InviteUID
	}
	sh, _ := st.ShareFor(declined.ID, bob.ID)
	if err := svc.Answer(bob, sh, false); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.Exec(`UPDATE collection_shares SET state_changed_at = NOW() - INTERVAL 100 DAY WHERE collection_id IN (?, ?, ?)`, declined.ID, expired.ID, open.ID); err != nil {
		t.Fatal(err)
	}
	if n, err := svc.ExpirePending(90); err != nil || n != 2 { // expired and open
		t.Fatalf("expired %d: %v", n, err)
	}
	// "open" got an answer-less 100 days too, so make it a live invitation again.
	if _, err := svc.Share(alice, open, bob, store.AccessRead); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.Exec(`UPDATE collection_shares SET state_changed_at = NOW() - INTERVAL 100 DAY WHERE collection_id IN (?, ?)`, declined.ID, expired.ID); err != nil {
		t.Fatal(err)
	}

	n, err := svc.RemoveFinished(90)
	if err != nil || n != 2 {
		t.Fatalf("removed %d: %v", n, err)
	}
	for _, c := range []store.Collection{declined, expired} {
		if _, err := st.ShareFor(c.ID, bob.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("%s survived: %v", c.Slug, err)
		}
	}
	for _, c := range []store.Collection{open, recent} {
		if sh, err := st.ShareFor(c.ID, bob.ID); err != nil || sh.State != store.SharePending {
			t.Fatalf("%s: %+v %v", c.Slug, sh, err)
		}
	}

	// Old notifications go, except those of invitations still waiting.
	nc, _ := st.Notifications(bob.ID)
	if _, err := st.DB.Exec(`UPDATE objects SET updated_at = NOW() - INTERVAL 100 DAY WHERE collection_id = ? AND uri != ?`, nc.ID, uids["recent"]+".xml"); err != nil {
		t.Fatal(err)
	}
	seq, _ := st.SyncSeq(nc.ID)
	n, err = st.PruneNotifications(90)
	if err != nil || n != 2 { // declined and expired; open is pending, recent is young
		t.Fatalf("pruned %d: %v", n, err)
	}
	for slug, want := range map[string]bool{"declined": false, "expired": false, "open": true, "recent": true} {
		_, err := st.GetObject(nc.ID, uids[slug]+".xml")
		if (err == nil) != want {
			t.Fatalf("%s notification present=%v want %v (%v)", slug, err == nil, want, err)
		}
	}
	after, _ := st.SyncSeq(nc.ID)
	if changed, _ := st.ChangedURIs(nc.ID, seq, after); len(changed) != 2 {
		t.Fatalf("changes: %v", changed)
	}
}
