package dav_test

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Top-Ranger/whitegopher/internal/auth"
	"github.com/Top-Ranger/whitegopher/internal/dav"
	"github.com/Top-Ranger/whitegopher/internal/store"
	"github.com/Top-Ranger/whitegopher/internal/storetest"
)

func TestQuotaAndRequestLimit(t *testing.T) {
	small := func(uid string) string { return event(uid, "x", "20260101T100000Z", "20260101T110000Z", "") }
	size := len(small("a"))
	st := storetest.New(t)
	var ids []int64
	for _, n := range []string{"alice", "bob"} {
		hash, _ := auth.HashPassword(n + "pw")
		id, err := st.CreateUser(store.User{Username: n, PasswordHash: hash})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	s := &dav.Server{Store: st, Auth: &auth.Authenticator{Store: st, Realm: "R"}, BasePath: "/dav",
		MaxBody: 4096, DefaultQuota: int64(size * 5 / 2)}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	alice, bob := who{t, ts.URL, "alice", "alicepw"}, who{t, ts.URL, "bob", "bobpw"}
	for _, w := range []who{alice, bob} {
		code, b, _ := w.req("MKCALENDAR", "/dav/calendars/"+w.user+"/c/", "")
		expect(t, code, 201, b)
	}
	// Default quota of two and a half objects: two fit, the third does not.
	for _, uid := range []string{"a", "b"} {
		code, b, _ := alice.req("PUT", "/dav/calendars/alice/c/"+uid+".ics", small(uid))
		expect(t, code, 201, b)
	}
	code, b, _ := alice.req("PUT", "/dav/calendars/alice/c/c.ics", small("c"))
	expect(t, code, 507, b)
	has(t, b, "quota-not-exceeded")

	// Replacing an object counts only the difference.
	code, b, _ = alice.req("PUT", "/dav/calendars/alice/c/a.ics", small("a"))
	expect(t, code, 204, b)

	// The home set reports usage and what is left.
	code, b, _ = alice.req("PROPFIND", "/dav/calendars/alice/", `<propfind xmlns="DAV:"><prop><quota-used-bytes/><quota-available-bytes/></prop></propfind>`, "Depth", "0")
	expect(t, code, 207, b)
	has(t, b, "<D:quota-used-bytes>", "<D:quota-available-bytes>")

	// Bob is unaffected by Alice's usage, and an unlimited override lifts the cap.
	code, b, _ = bob.req("PUT", "/dav/calendars/bob/c/a.ics", small("a"))
	expect(t, code, 201, b)
	zero := int64(0)
	if err := st.UpdateUser(store.User{ID: ids[0], Username: "alice", QuotaBytes: &zero}); err != nil {
		t.Fatal(err)
	}
	code, b, _ = alice.req("PUT", "/dav/calendars/alice/c/c.ics", small("c"))
	expect(t, code, 201, b)

	// Request size limit.
	code, b, _ = alice.req("PUT", "/dav/calendars/alice/c/big.ics", small("big")+strings.Repeat("X", 5000))
	expect(t, code, 413, b)
}
