package metrics_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/amagioss/opentams/internal/httpx/metrics"
)

// newRegistryWithCounter returns a fresh registry populated with one counter
// named `name` incremented to 1. It never touches DefaultRegisterer (NFR-MET-T2).
func newRegistryWithCounter(t *testing.T, name string) *prometheus.Registry {
	t.Helper()
	reg := prometheus.NewRegistry()
	c := prometheus.NewCounter(prometheus.CounterOpts{
		Name: name,
		Help: "test counter for " + name,
	})
	if err := reg.Register(c); err != nil {
		t.Fatalf("register counter %s: %v", name, err)
	}
	c.Inc()
	return reg
}

// serve issues an HTTP GET against the handler and returns the response + body.
func serve(t *testing.T, h http.Handler, accept string) (resp *http.Response, body string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/metrics", http.NoBody)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	resp = rec.Result()
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	body = string(raw)
	return resp, body
}

// TC-MET-01 — default Prometheus text format.
func TestHandler_DefaultPrometheusFormat(t *testing.T) {
	reg := newRegistryWithCounter(t, "opentams_test_requests_total")

	resp, body := serve(t, metrics.Handler(reg), "")

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want 200; body=%q", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type=%q, want text/plain prefix", ct)
	}
	for _, want := range []string{
		"# HELP opentams_test_requests_total",
		"# TYPE opentams_test_requests_total counter",
		"opentams_test_requests_total 1",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q\n---\n%s", want, body)
		}
	}
}

// TC-MET-02 — OpenMetrics when Accept requests it.
func TestHandler_OpenMetricsWhenAcceptRequested(t *testing.T) {
	reg := newRegistryWithCounter(t, "opentams_om_total")

	resp, body := serve(t, metrics.Handler(reg), "application/openmetrics-text")

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/openmetrics-text") {
		t.Errorf("Content-Type=%q, want application/openmetrics-text prefix", ct)
	}
	if !strings.HasSuffix(strings.TrimRight(body, "\n"), "# EOF") {
		t.Errorf("OpenMetrics body must end with # EOF; got tail=%q",
			body[maxInt(0, len(body)-80):])
	}
}

// TC-MET-03 — Handler uses injected gatherer, not DefaultGatherer.
func TestHandler_UsesInjectedGathererNotDefault(t *testing.T) {
	customReg := newRegistryWithCounter(t, "opentams_custom_total")

	// Intentionally NOT touching prometheus.DefaultRegisterer (NFR-MET-T2).
	// Instead, verify that a metric registered on a *separate* registry that
	// we do NOT pass in is absent from the response.
	unusedReg := newRegistryWithCounter(t, "opentams_unused_total")
	_ = unusedReg

	_, body := serve(t, metrics.Handler(customReg), "")

	if !strings.Contains(body, "opentams_custom_total") {
		t.Errorf("body should contain opentams_custom_total (injected registry); body=%q", body)
	}
	if strings.Contains(body, "opentams_unused_total") {
		t.Errorf("body must NOT contain opentams_unused_total (different registry); body=%q", body)
	}
}

// TC-MET-04 — no auth required (handler accepts requests with no Authorization).
func TestHandler_NoAuthRequired(t *testing.T) {
	reg := newRegistryWithCounter(t, "opentams_noauth_total")

	req := httptest.NewRequest(http.MethodGet, "/metrics", http.NoBody)
	// Explicitly no Authorization header.
	rec := httptest.NewRecorder()
	metrics.Handler(reg).ServeHTTP(rec, req)
	resp := rec.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status=%d, want 200 (handler must not gate on auth)", resp.StatusCode)
	}
	if resp.StatusCode == http.StatusUnauthorized {
		t.Error("handler returned 401; /metrics must not enforce auth at this layer")
	}
}

// TC-MET-05 — empty gatherer serves 200 with valid exposition format.
func TestHandler_EmptyGathererServes200(t *testing.T) {
	emptyReg := prometheus.NewRegistry()

	resp, body := serve(t, metrics.Handler(emptyReg), "")

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want 200", resp.StatusCode)
	}
	// Body may legitimately be empty or contain only whitespace; both are
	// valid Prometheus exposition format. The contract is: no panic, 200 OK.
	if strings.Contains(body, "# TYPE go_") || strings.Contains(body, "# TYPE process_") {
		t.Errorf("empty registry must not expose default process/go collectors; body=%q", body)
	}
}

// TC-MET-06 — NFR-MET-R1: nil gatherer must not panic at construction; instead
// the returned handler serves 500 on every request so the misconfiguration is
// observable in operational logs.
func TestHandler_NilGathererReturns500Handler(t *testing.T) {
	var h http.Handler
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("Handler(nil) must not panic at construction (NFR-MET-R1); got: %v", r)
			}
		}()
		h = metrics.Handler(nil)
	}()

	if h == nil {
		t.Fatal("Handler(nil) returned nil http.Handler — must return a 500-serving handler")
	}

	resp, _ := serve(t, h, "")
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("nil-gatherer handler status=%d, want 500 (NFR-MET-R1)", resp.StatusCode)
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
