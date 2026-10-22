package netx

import (
	"sync"
	"time"
)

// Limiter allows at most max failures per key within a window.
type Limiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	fails  map[string][]time.Time
}

// NewLimiter returns a Limiter. A max below 1 disables it, as does a nil
// *Limiter.
func NewLimiter(max int, window time.Duration) *Limiter {
	return &Limiter{max: max, window: window, fails: map[string][]time.Time{}}
}

func (l *Limiter) prune(key string, now time.Time) []time.Time {
	recent := l.fails[key][:0]
	for _, t := range l.fails[key] {
		if now.Sub(t) < l.window {
			recent = append(recent, t)
		}
	}
	if len(recent) == 0 {
		delete(l.fails, key)
	} else {
		l.fails[key] = recent
	}
	return recent
}

// Allowed reports whether key may try again.
func (l *Limiter) Allowed(key string) bool {
	if l == nil {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.max < 1 {
		return true
	}
	return len(l.prune(key, time.Now())) < l.max
}

// Fail records a failure for key.
func (l *Limiter) Fail(key string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if l.max < 1 {
		return
	}
	if len(l.fails) > 4096 { // bound memory under a spray of addresses
		for k := range l.fails {
			l.prune(k, now)
		}
	}
	l.prune(key, now)
	l.fails[key] = append(l.fails[key], now)
}

// Reset forgets key's failures.
func (l *Limiter) Reset(key string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.fails, key)
}
