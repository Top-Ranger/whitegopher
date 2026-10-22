package obs_test

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bitflipp/whitegopher/internal/obs"
)

func TestMiddlewareLogsAndCounts(t *testing.T) {
	var logs strings.Builder
	log := slog.New(slog.NewJSONHandler(&logs, nil))
	m := obs.New()
	h := obs.Middleware(m, log, func(*http.Request) string { return "203.0.113.7" },
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			obs.SetUser(r.Context(), "alice")
			if r.URL.Path == "/missing" {
				http.NotFound(w, r)
				return
			}
			w.Write([]byte("hello"))
		}))
	for _, p := range []string{"/a", "/missing", "/healthz"} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("PROPFIND", p+"?secret=1", nil))
	}
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("BREW", "/a", nil))

	out := logs.String()
	for _, want := range []string{`"method":"PROPFIND"`, `"path":"/a"`, `"status":200`, `"bytes":5`, `"ip":"203.0.113.7"`, `"user":"alice"`, `"status":404`} {
		if !strings.Contains(out, want) {
			t.Errorf("access log lacks %s:\n%s", want, out)
		}
	}
	if strings.Contains(out, "secret") || strings.Contains(out, "/healthz") {
		t.Errorf("query string or healthz logged:\n%s", out)
	}

	var b strings.Builder
	m.Write(&b, nil, "test")
	for _, want := range []string{
		`whitegopher_build_info{version="test"} 1`,
		`whitegopher_http_requests_total{method="PROPFIND",code="2xx"} 2`,
		`whitegopher_http_requests_total{method="PROPFIND",code="4xx"} 1`,
		`whitegopher_http_requests_total{method="OTHER",code="2xx"} 1`,
		`whitegopher_http_request_duration_seconds_bucket{le="+Inf"} 4`,
		`whitegopher_http_request_duration_seconds_count 4`,
		`whitegopher_login_failures_total`,
	} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("metrics lack %s:\n%s", want, b.String())
		}
	}
}
