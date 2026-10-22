package netx

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"testing"
	"time"
)

func req(remote string, hdr ...string) *http.Request {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = remote
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Add(hdr[i], hdr[i+1])
	}
	return r
}

func TestClientIP(t *testing.T) {
	proxies := Proxies{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("::1/128")}
	for _, c := range []struct {
		name string
		r    *http.Request
		want string
	}{
		{"direct peer", req("203.0.113.9:5000"), "203.0.113.9"},
		{"untrusted peer cannot set the header", req("203.0.113.9:5000", "X-Forwarded-For", "1.2.3.4"), "203.0.113.9"},
		{"trusted proxy forwards", req("10.1.2.3:5000", "X-Forwarded-For", "198.51.100.7"), "198.51.100.7"},
		{"trusted hops are skipped", req("10.1.2.3:5000", "X-Forwarded-For", "198.51.100.7, 10.9.9.9"), "198.51.100.7"},
		{"client-supplied left entry is ignored", req("10.1.2.3:5000", "X-Forwarded-For", "1.2.3.4, 198.51.100.7"), "198.51.100.7"},
		{"several header lines", req("10.1.2.3:5000", "X-Forwarded-For", "1.2.3.4", "X-Forwarded-For", "198.51.100.7"), "198.51.100.7"},
		{"malformed header falls back to the peer", req("10.1.2.3:5000", "X-Forwarded-For", "not-an-ip"), "10.1.2.3"},
		{"ipv4-mapped peer", req("[::ffff:10.1.2.3]:5000", "X-Forwarded-For", "198.51.100.7"), "198.51.100.7"},
		{"no header", req("10.1.2.3:5000"), "10.1.2.3"},
		{"ipv6 proxy", req("[::1]:5000", "X-Forwarded-For", "2001:db8::5"), "2001:db8::5"},
	} {
		if got := proxies.ClientIP(c.r); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
	if (Proxies)(nil).ClientIP(req("10.1.2.3:5000", "X-Forwarded-For", "1.2.3.4")) != "10.1.2.3" {
		t.Error("no trusted proxies: headers must be ignored")
	}
}

func TestIsHTTPS(t *testing.T) {
	p := Proxies{netip.MustParsePrefix("127.0.0.0/8")}
	if !p.IsHTTPS(req("127.0.0.1:1", "X-Forwarded-Proto", "HTTPS")) {
		t.Error("trusted proxy saying https")
	}
	if p.IsHTTPS(req("203.0.113.9:1", "X-Forwarded-Proto", "https")) {
		t.Error("untrusted peer must not be believed")
	}
	if p.IsHTTPS(req("127.0.0.1:1")) {
		t.Error("no header means http")
	}
}

func TestLimiter(t *testing.T) {
	l := NewLimiter(3, 50*time.Millisecond)
	for i := 0; i < 3; i++ {
		if !l.Allowed("a") {
			t.Fatalf("blocked after %d failures", i)
		}
		l.Fail("a")
	}
	if l.Allowed("a") {
		t.Fatal("not blocked after 3 failures")
	}
	if !l.Allowed("b") {
		t.Fatal("other keys are independent")
	}
	l.Reset("a")
	if !l.Allowed("a") {
		t.Fatal("reset did not clear")
	}
	for i := 0; i < 3; i++ {
		l.Fail("a")
	}
	time.Sleep(70 * time.Millisecond)
	if !l.Allowed("a") {
		t.Fatal("failures did not expire")
	}

	// Disabled and nil limiters never block.
	for _, d := range []*Limiter{NewLimiter(0, time.Minute), nil} {
		for i := 0; i < 100; i++ {
			d.Fail("x")
		}
		if !d.Allowed("x") {
			t.Fatal("disabled limiter blocked")
		}
		d.Reset("x")
	}
}

func TestLimiterGroupsIPv6ByPrefix(t *testing.T) {
	l := NewLimiter(2, time.Minute)
	l.Fail("2001:db8:1:2::1")
	l.Fail("2001:db8:1:2:ffff::9") // same /64, another address
	if l.Allowed("2001:db8:1:2::77") {
		t.Fatal("a fresh address in the same /64 dodged the limit")
	}
	if !l.Allowed("2001:db8:1:3::1") {
		t.Fatal("a different /64 was blocked")
	}
	l.Fail("::ffff:192.0.2.1")
	l.Fail("192.0.2.1")
	if l.Allowed("192.0.2.1") {
		t.Fatal("IPv4 addresses are kept as they are")
	}
	if !l.Allowed("192.0.2.2") {
		t.Fatal("IPv4 neighbours are independent")
	}
}

func TestLimiterBoundsMemory(t *testing.T) {
	// Expired keys are swept once the table is large...
	l := NewLimiter(1, 10*time.Millisecond)
	for i := 0; i < 3*sweepAbove; i++ {
		if i%500 == 0 {
			time.Sleep(11 * time.Millisecond) // let earlier keys expire
			l.lastSweep = time.Time{}         // and let the sweep run despite the test's speed
		}
		l.Fail("k" + strconv.Itoa(i))
	}
	l.mu.Lock()
	n := len(l.fails)
	l.mu.Unlock()
	if n > sweepAbove+500 {
		t.Fatalf("%d keys kept, expired ones should have been swept", n)
	}

	// ...and a spray of live keys cannot grow the table without bound.
	l = NewLimiter(1, time.Hour)
	for i := 0; i < maxKeys+2*sweepAbove; i++ {
		if i%1000 == 0 {
			l.lastSweep = time.Time{} // let the sweep run despite the test's speed
		}
		l.Fail("k" + strconv.Itoa(i))
	}
	l.mu.Lock()
	n = len(l.fails)
	l.mu.Unlock()
	if n > maxKeys+1000 {
		t.Fatalf("table grew to %d keys", n)
	}
}
