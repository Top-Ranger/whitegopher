package store_test

import (
	"bytes"
	"errors"
	"path/filepath"
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

func TestReopenKeepsDataSQLite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "g.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	st.CreateUser(store.User{Username: "alice"})
	st.Close()
	st, err = store.Open(path) // migrations must be idempotent
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.UserByName("alice"); err != nil {
		t.Fatal(err)
	}
}

func TestReopenKeepsDataMySQL(t *testing.T) {
	dsn := storetest.MySQLDSN(t)
	st, err := store.OpenDriver(store.DriverMySQL, dsn)
	if err != nil {
		t.Fatal(err)
	}
	st.CreateUser(store.User{Username: "alice"})
	st.Close()
	st, err = store.OpenDriver(store.DriverMySQL, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.UserByName("alice"); err != nil {
		t.Fatal(err)
	}
}

func TestUnknownDriver(t *testing.T) {
	if _, err := store.OpenDriver("postgres", ""); err == nil {
		t.Fatal("expected error")
	}
}
