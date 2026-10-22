// Package obs provides request metrics (Prometheus text format) and a
// structured access log, without external dependencies.
package obs

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// Counter is a monotonically increasing count.
type Counter struct{ v atomic.Int64 }

func (c *Counter) Inc() {
	if c != nil {
		c.v.Add(1)
	}
}

func (c *Counter) value() int64 { return c.v.Load() }

// Login counters, incremented by the authentication code.
var (
	LoginFailures Counter
	LoginsBlocked Counter
)

var buckets = []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10}

var methods = map[string]bool{
	"GET": true, "HEAD": true, "PUT": true, "DELETE": true, "POST": true, "OPTIONS": true,
	"PROPFIND": true, "PROPPATCH": true, "REPORT": true, "MKCALENDAR": true, "MKCOL": true,
}

type reqKey struct{ method, class string }

// Metrics aggregates HTTP request statistics.
type Metrics struct {
	started time.Time

	mu       sync.Mutex
	requests map[reqKey]int64
	bucket   []int64 // cumulative-ready: count per upper bound, last is +Inf
	sum      float64
	count    int64
}

// New returns an empty Metrics.
func New() *Metrics {
	return &Metrics{started: time.Now(), requests: map[reqKey]int64{}, bucket: make([]int64, len(buckets)+1)}
}

func (m *Metrics) observe(method string, status int, d time.Duration) {
	if !methods[method] {
		method = "OTHER"
	}
	k := reqKey{method, strconv.Itoa(status/100) + "xx"}
	secs := d.Seconds()
	i := sort.SearchFloat64s(buckets, secs)
	m.mu.Lock()
	m.requests[k]++
	m.bucket[i]++
	m.sum += secs
	m.count++
	m.mu.Unlock()
}

// Write renders the metrics in the Prometheus text exposition format. db, if
// non-nil, contributes connection-pool gauges; version labels the build info.
func (m *Metrics) Write(w io.Writer, db *sql.DB, version string) {
	fmt.Fprintf(w, "# TYPE whitegopher_build_info gauge\nwhitegopher_build_info{version=%q} 1\n", version)
	fmt.Fprintf(w, "# TYPE whitegopher_uptime_seconds gauge\nwhitegopher_uptime_seconds %d\n", int64(time.Since(m.started).Seconds()))
	fmt.Fprintf(w, "# TYPE whitegopher_login_failures_total counter\nwhitegopher_login_failures_total %d\n", LoginFailures.value())
	fmt.Fprintf(w, "# TYPE whitegopher_logins_blocked_total counter\nwhitegopher_logins_blocked_total %d\n", LoginsBlocked.value())
	if db != nil {
		st := db.Stats()
		fmt.Fprintf(w, "# TYPE whitegopher_db_connections_open gauge\nwhitegopher_db_connections_open %d\n", st.OpenConnections)
		fmt.Fprintf(w, "# TYPE whitegopher_db_connections_in_use gauge\nwhitegopher_db_connections_in_use %d\n", st.InUse)
	}

	m.mu.Lock()
	keys := make([]reqKey, 0, len(m.requests))
	for k := range m.requests {
		keys = append(keys, k)
	}
	counts := make([]int64, len(keys))
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].method != keys[j].method {
			return keys[i].method < keys[j].method
		}
		return keys[i].class < keys[j].class
	})
	for i, k := range keys {
		counts[i] = m.requests[k]
	}
	bucket := append([]int64(nil), m.bucket...)
	sum, count := m.sum, m.count
	m.mu.Unlock()

	fmt.Fprint(w, "# TYPE whitegopher_http_requests_total counter\n")
	for i, k := range keys {
		fmt.Fprintf(w, "whitegopher_http_requests_total{method=%q,code=%q} %d\n", k.method, k.class, counts[i])
	}
	fmt.Fprint(w, "# TYPE whitegopher_http_request_duration_seconds histogram\n")
	var cum int64
	for i, le := range buckets {
		cum += bucket[i]
		fmt.Fprintf(w, "whitegopher_http_request_duration_seconds_bucket{le=%q} %d\n", strconv.FormatFloat(le, 'g', -1, 64), cum)
	}
	cum += bucket[len(buckets)]
	fmt.Fprintf(w, "whitegopher_http_request_duration_seconds_bucket{le=\"+Inf\"} %d\n", cum)
	fmt.Fprintf(w, "whitegopher_http_request_duration_seconds_sum %g\nwhitegopher_http_request_duration_seconds_count %d\n", sum, count)
}

// Handler serves the metrics.
func (m *Metrics) Handler(db *sql.DB, version string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		m.Write(w, db, version)
	})
}

type recordKey struct{}

// Record carries per-request facts that inner handlers fill in for the
// access log.
type Record struct{ User string }

// SetUser notes the authenticated user for the access log.
func SetUser(ctx context.Context, name string) {
	if rec, ok := ctx.Value(recordKey{}).(*Record); ok {
		rec.User = name
	}
}

type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	return n, err
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Middleware records every request in m (if non-nil) and, when log is non-nil,
// writes one structured access-log line per request. clientIP attributes the
// request to an address (proxy-aware). /healthz is not logged.
func Middleware(m *Metrics, log *slog.Logger, clientIP func(*http.Request) string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &Record{}
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r.WithContext(context.WithValue(r.Context(), recordKey{}, rec)))
		if sw.status == 0 {
			sw.status = http.StatusOK
		}
		d := time.Since(start)
		if m != nil {
			m.observe(r.Method, sw.status, d)
		}
		if log != nil && r.URL.Path != "/healthz" {
			log.Info("request", "method", r.Method, "path", r.URL.Path, "status", sw.status,
				"bytes", sw.bytes, "ms", d.Milliseconds(), "ip", clientIP(r), "user", rec.User)
		}
	})
}
