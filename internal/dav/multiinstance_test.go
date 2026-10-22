package dav_test

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/bitflipp/galdav/internal/auth"
	"github.com/bitflipp/galdav/internal/dav"
	"github.com/bitflipp/galdav/internal/store"
	"github.com/bitflipp/galdav/internal/storetest"
)

// Several galdav processes may share one database. Everything they rely on
// lives in the database, so concurrent writes through different instances
// must stay consistent and be visible to all.
func TestTwoInstancesShareOneDatabase(t *testing.T) {
	dsn := storetest.DSN(t)
	var urls []string
	for i := 0; i < 2; i++ {
		// Separate stores over the same database, like separate processes.
		other, err := store.Open(dsn)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { other.Close() })
		if i == 0 {
			hash, _ := auth.HashPassword("pw")
			if _, err := other.CreateUser(store.User{Username: "alice", PasswordHash: hash}); err != nil {
				t.Fatal(err)
			}
		}
		s := &dav.Server{Store: other, Auth: &auth.Authenticator{Store: other, Realm: "R"}, BasePath: "/dav"}
		ts := httptest.NewServer(s.Handler())
		t.Cleanup(ts.Close)
		urls = append(urls, ts.URL)
	}
	w := func(i int) who { return who{t, urls[i%2], "alice", "pw"} }

	code, b, _ := w(0).req("MKCALENDAR", "/dav/calendars/alice/c/", "")
	expect(t, code, 201, b)

	// Concurrent creates through both instances, plus racing writers for one UID.
	const n = 24
	var wg sync.WaitGroup
	results := make([]int, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			uid := fmt.Sprintf("u%d", i)
			if i >= n/2 {
				uid = "same" // half the writers fight over one UID with different URIs
			}
			ev := event(uid, "x", "20260101T100000Z", "20260101T110000Z", "")
			results[i], _, _ = w(i).req("PUT", fmt.Sprintf("/dav/calendars/alice/c/o%d.ics", i), ev)
		}()
	}
	wg.Wait()
	created, conflicts := 0, 0
	for i, c := range results {
		switch {
		case c == 201:
			created++
		case c == 409 && i >= n/2:
			conflicts++
		default:
			t.Fatalf("writer %d: status %d", i, c)
		}
	}
	if created != n/2+1 || conflicts != n/2-1 {
		t.Fatalf("created %d, conflicts %d: exactly one writer of the shared UID should win", created, conflicts)
	}

	// Both instances see every object, and a sync from scratch lists them.
	for i := 0; i < 2; i++ {
		code, b, _ := w(i).req("REPORT", "/dav/calendars/alice/c/", `<d:sync-collection xmlns:d="DAV:"><d:sync-token/><d:sync-level>1</d:sync-level><d:prop><d:getetag/></d:prop></d:sync-collection>`)
		expect(t, code, 207, b)
		if got := strings.Count(b, ".ics</D:href>"); got != created {
			t.Fatalf("instance %d lists %d objects, want %d", i, got, created)
		}
	}
}
