package store_test

import (
	"bytes"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/bitflipp/galdav/internal/store"
	"github.com/bitflipp/galdav/internal/storetest"
)

func newCollection(t *testing.T, st *store.Store, owner string) store.Collection {
	t.Helper()
	uid, err := st.CreateUser(store.User{Username: owner})
	if err != nil {
		t.Fatal(err)
	}
	c := store.Collection{OwnerID: uid, Kind: store.KindCalendar, Slug: "c"}
	if c.ID, err = st.CreateCollection(c); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestConcurrentUIDConflict(t *testing.T) {
	st := storetest.New(t)
	c := newCollection(t, st, "alice")
	var ok, conflicts atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := st.PutObject(store.Object{
				CollectionID: c.ID, URI: string(rune('a'+i)) + ".ics", UID: "same", ETag: "e", Data: []byte("x"), Component: "VEVENT",
			}, store.PutCond{})
			var uc *store.UIDConflictError
			switch {
			case err == nil:
				ok.Add(1)
			case errors.As(err, &uc):
				conflicts.Add(1)
			default:
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	if ok.Load() != 1 || conflicts.Load() != 15 {
		t.Fatalf("ok=%d conflicts=%d, want 1 and 15", ok.Load(), conflicts.Load())
	}
}

func TestFirstUserRace(t *testing.T) {
	st := storetest.New(t)
	var ok atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := st.CreateUserIfNone(store.User{Username: string(rune('a' + i)), IsAdmin: true})
			if err == nil {
				ok.Add(1)
			} else if !errors.Is(err, store.ErrExists) {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	if n, _ := st.CountUsers(); ok.Load() != 1 || n != 1 {
		t.Fatalf("created %d, users %d", ok.Load(), n)
	}
}

func TestCaseSensitivityAndDuplicates(t *testing.T) {
	st := storetest.New(t)
	for _, n := range []string{"Alice", "alice"} {
		if _, err := st.CreateUser(store.User{Username: n}); err != nil {
			t.Fatalf("%s: %v", n, err)
		}
	}
	if _, err := st.CreateUser(store.User{Username: "alice"}); !errors.Is(err, store.ErrExists) {
		t.Fatalf("duplicate user: %v", err)
	}
	if u, err := st.UserByName("ALICE"); err == nil {
		t.Fatalf("case-insensitive lookup matched %+v", u)
	}
	u, _ := st.UserByName("alice")
	for _, slug := range []string{"Cal", "cal"} {
		if _, err := st.CreateCollection(store.Collection{OwnerID: u.ID, Kind: store.KindCalendar, Slug: slug}); err != nil {
			t.Fatalf("%s: %v", slug, err)
		}
	}
	if _, err := st.CreateCollection(store.Collection{OwnerID: u.ID, Kind: store.KindCalendar, Slug: "cal"}); !errors.Is(err, store.ErrExists) {
		t.Fatalf("duplicate collection: %v", err)
	}
}

func TestObjectRoundTrip(t *testing.T) {
	st := storetest.New(t)
	c := newCollection(t, st, "alice")
	big := bytes.Repeat([]byte("0123456789abcdef"), 1<<18) // 4 MiB
	long := string(bytes.Repeat([]byte("u"), 400))         // UIDs may exceed typical index limits
	for _, o := range []store.Object{
		{CollectionID: c.ID, URI: "ünï.ics", UID: "ünï-☃", ETag: "1", Data: []byte("héllo ☃"), Component: "VEVENT"},
		{CollectionID: c.ID, URI: "big.ics", UID: long, ETag: "2", Data: big, Component: "VEVENT"},
		{CollectionID: c.ID, URI: "bin.ics", UID: "bin", ETag: "3", Data: []byte{0, 1, 2, 0xff, 0xfe}, Component: "VEVENT"},
	} {
		if _, err := st.PutObject(o, store.PutCond{}); err != nil {
			t.Fatalf("%s: %v", o.URI, err)
		}
		got, err := st.GetObject(c.ID, o.URI)
		if err != nil || !bytes.Equal(got.Data, o.Data) || got.UID != o.UID || got.Size != int64(len(o.Data)) {
			t.Fatalf("%s: %v size %d uid ok=%v", o.URI, err, got.Size, got.UID == o.UID)
		}
	}
	// A different object with the same long UID is a conflict.
	_, err := st.PutObject(store.Object{CollectionID: c.ID, URI: "other.ics", UID: long, ETag: "x", Data: []byte("x")}, store.PutCond{})
	var uc *store.UIDConflictError
	if !errors.As(err, &uc) || uc.URI != "big.ics" {
		t.Fatalf("long uid conflict: %v", err)
	}
}

func TestSyncSequenceAndCascade(t *testing.T) {
	st := storetest.New(t)
	c := newCollection(t, st, "alice")
	put := func(uri string) {
		if _, err := st.PutObject(store.Object{CollectionID: c.ID, URI: uri, UID: uri, ETag: "e", Data: []byte("x")}, store.PutCond{}); err != nil {
			t.Fatal(err)
		}
	}
	put("a")
	put("b")
	seq1, _ := st.SyncSeq(c.ID)
	put("a")
	if err := st.DeleteObject(c.ID, "b", nil); err != nil {
		t.Fatal(err)
	}
	seq2, _ := st.SyncSeq(c.ID)
	if seq2 <= seq1 {
		t.Fatalf("seq did not advance: %d -> %d", seq1, seq2)
	}
	uris, err := st.ChangedURIs(c.ID, seq1, seq2)
	if err != nil || len(uris) != 2 {
		t.Fatalf("changed: %v %v", uris, err)
	}
	if err := st.DeleteUser(c.OwnerID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetCollectionByID(c.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("collection survived user deletion: %v", err)
	}
	if objs, _ := st.ListObjects(c.ID); len(objs) != 0 {
		t.Fatal("objects survived cascade")
	}
}

func TestReopenKeepsData(t *testing.T) {
	dsn := storetest.DSN(t)
	st, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	st.CreateUser(store.User{Username: "alice"})
	st.Close()
	st, err = store.Open(dsn) // migrations must be idempotent
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.UserByName("alice"); err != nil {
		t.Fatal(err)
	}
}

func TestBadDSN(t *testing.T) {
	if _, err := store.Open("not a dsn"); err == nil {
		t.Fatal("expected error")
	}
}

func TestShares(t *testing.T) {
	st := storetest.New(t)
	aid, _ := st.CreateUser(store.User{Username: "alice"})
	bid, _ := st.CreateUser(store.User{Username: "bob"})
	cal := store.Collection{OwnerID: aid, Kind: store.KindCalendar, Slug: "work", DisplayName: "Work", Color: "#111111"}
	cal.ID, _ = st.CreateCollection(cal)
	book := store.Collection{OwnerID: aid, Kind: store.KindAddressBook, Slug: "people"}
	book.ID, _ = st.CreateCollection(book)
	// Bob already owns a calendar called "work": the mount must get another name.
	st.CreateCollection(store.Collection{OwnerID: bid, Kind: store.KindCalendar, Slug: "work"})

	sh, err := st.SetShare(cal, bid, store.AccessRead)
	if err != nil || sh.Slug != "alice-work" || sh.Access != store.AccessRead {
		t.Fatalf("share: %+v %v", sh, err)
	}
	if sh2, err := st.SetShare(cal, bid, store.AccessReadWrite); err != nil || sh2.ID != sh.ID || sh2.Access != store.AccessReadWrite {
		t.Fatalf("upgrade: %+v %v", sh2, err)
	}
	if _, err := st.SetShare(cal, bid, store.AccessReadWrite); err != nil { // unchanged: still fine
		t.Fatal(err)
	}
	if _, err := st.SetShare(cal, aid, store.AccessRead); err == nil {
		t.Fatal("shared with owner")
	}
	if _, err := st.SetShare(cal, bid, "admin"); err == nil {
		t.Fatal("bad access accepted")
	}
	inbox, _ := st.Inbox(aid)
	if _, err := st.SetShare(inbox, bid, store.AccessRead); err == nil {
		t.Fatal("inbox was shareable")
	}

	// Resolution: bob's own "work" wins by name; the mount is reachable by its slug.
	own, err := st.ResolveCollection(bid, store.KindCalendar, "work")
	if err != nil || own.Access != "" || own.OwnerID != bid {
		t.Fatalf("own: %+v %v", own, err)
	}
	m, err := st.ResolveCollection(bid, store.KindCalendar, "alice-work")
	if err != nil || m.ID != cal.ID || m.Access != store.AccessReadWrite || m.OwnerName != "alice" || m.DisplayName != "Work" || m.Color != "#111111" {
		t.Fatalf("mount: %+v %v", m, err)
	}
	if _, err := st.ResolveCollection(bid, store.KindAddressBook, "alice-work"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("wrong kind resolved: %v", err)
	}
	// The sharee's presentation overrides apply to the view only.
	if err := st.UpdateShareProps(sh.ID, "Alice's work", "#222222", 5, false); err != nil {
		t.Fatal(err)
	}
	m, _ = st.ResolveCollection(bid, store.KindCalendar, "alice-work")
	if m.DisplayName != "Alice's work" || m.Color != "#222222" || m.SortOrder != 5 {
		t.Fatalf("overrides: %+v", m)
	}
	if orig, _ := st.GetCollectionByID(cal.ID); orig.DisplayName != "Work" || orig.Color != "#111111" {
		t.Fatalf("owner's collection changed: %+v", orig)
	}
	// Names taken by mounts block new collections, reserved ones too.
	for _, slug := range []string{"alice-work", "work", "inbox", "outbox"} {
		if taken, _ := st.SlugTaken(bid, store.KindCalendar, slug); !taken {
			t.Errorf("%q should be taken", slug)
		}
	}
	if taken, _ := st.SlugTaken(bid, store.KindAddressBook, "work"); taken {
		t.Error("kinds have separate namespaces")
	}

	st.SetShare(book, bid, store.AccessRead)
	if mounts, _ := st.ListMounts(bid, store.KindCalendar); len(mounts) != 1 {
		t.Fatalf("calendar mounts: %+v", mounts)
	}
	if sharees, _ := st.Sharees(cal.ID); len(sharees) != 1 || sharees[0].Username != "bob" {
		t.Fatalf("sharees: %+v", sharees)
	}

	// Leaving, owner deletion and user deletion all clean up.
	if err := st.DeleteShare(cal.ID, bid); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteShare(cal.ID, bid); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second leave: %v", err)
	}
	st.SetShare(cal, bid, store.AccessRead)
	st.DeleteCollection(cal.ID)
	if mounts, _ := st.ListMounts(bid, store.KindCalendar); len(mounts) != 0 {
		t.Fatal("mount survived the owner deleting the collection")
	}
	st.DeleteUser(bid)
	if sharees, _ := st.Sharees(book.ID); len(sharees) != 0 {
		t.Fatal("share survived the sharee's deletion")
	}
}
