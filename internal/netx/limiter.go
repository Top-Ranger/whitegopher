package netx

import (
	"net/netip"
	"sync"
	"time"
)

const (
	// sweepAbove is the number of tracked keys beyond which expired ones are
	// swept (at most once per sweepEvery), and maxKeys the hard cap: past it
	// the table is cleared, which is the lesser evil than unbounded memory
	// under a spray of addresses.
	sweepAbove = 4096
	sweepEvery = time.Second
	maxKeys    = 100000
)

// Limiter allows at most max failures per key within a window. Keys that are
// IPv6 addresses are grouped by their /64, since one subscriber normally owns
// a whole /64 and could otherwise dodge the limit with a fresh address each
// time.
type Limiter struct {
	mu        sync.Mutex
	max       int
	window    time.Duration
	fails     map[string][]time.Time
	lastSweep time.Time
}

// NewLimiter returns a Limiter. A max below 1 disables it, as does a nil
// *Limiter.
func NewLimiter(max int, window time.Duration) *Limiter {
	return &Limiter{max: max, window: window, fails: map[string][]time.Time{}}
}

// key groups IPv6 addresses by /64; anything else is used as is.
func key(k string) string {
	a, err := netip.ParseAddr(k)
	if err != nil {
		return k
	}
	if a.Is4In6() {
		return a.Unmap().String()
	}
	if !a.Is6() {
		return k
	}
	p, err := a.Prefix(64)
	if err != nil {
		return k
	}
	return p.String()
}

func (l *Limiter) prune(k string, now time.Time) []time.Time {
	recent := l.fails[k][:0]
	for _, t := range l.fails[k] {
		if now.Sub(t) < l.window {
			recent = append(recent, t)
		}
	}
	if len(recent) == 0 {
		delete(l.fails, k)
	} else {
		l.fails[k] = recent
	}
	return recent
}

// Allowed reports whether key may try again.
func (l *Limiter) Allowed(k string) bool {
	if l == nil {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.max < 1 {
		return true
	}
	return len(l.prune(key(k), time.Now())) < l.max
}

// Fail records a failure for key.
func (l *Limiter) Fail(k string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.max < 1 {
		return
	}
	now := time.Now()
	if len(l.fails) > sweepAbove && now.Sub(l.lastSweep) >= sweepEvery {
		l.lastSweep = now
		for old := range l.fails {
			l.prune(old, now)
		}
		if len(l.fails) > maxKeys {
			l.fails = map[string][]time.Time{}
		}
	}
	k = key(k)
	l.prune(k, now)
	l.fails[k] = append(l.fails[k], now)
}

// Reset forgets key's failures.
func (l *Limiter) Reset(k string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.fails, key(k))
}
