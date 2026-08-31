package httpmetrics_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/amagioss/opentams/pkg/httpmetrics"
	"github.com/amagioss/opentams/pkg/metrics"
)

func init() { gin.SetMode(gin.TestMode) }

// newRouter wires the middleware against a raw prometheus.Registry — i.e. an
// un-prefixed Registerer, the contract pkg/httpmetrics promises (no namespace,
// no subsystem, no caller-prefix injection of any kind).
func newRouter(t *testing.T, opts ...httpmetrics.Option) (*gin.Engine, *prometheus.Registry) {
	t.Helper()
	reg := prometheus.NewRegistry()
	mw, err := httpmetrics.New(reg, opts...)
	if err != nil {
		t.Fatalf("httpmetrics.New: %v", err)
	}
	r := gin.New()
	r.Use(mw)
	return r, reg
}

// TC-HTM-01: successful request increments counter and records histogram.
func TestNew_RecordsMetrics(t *testing.T) {
	r, reg := newRouter(t)
	r.GET("/flows/:flowId", func(c *gin.Context) { c.Status(http.StatusOK) })
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/flows/abc", http.NoBody))

	if testutil.CollectAndCount(reg) == 0 {
		t.Error("expected metrics to be registered and collected")
	}
}

// TC-HTM-02: unmatched route uses "unmatched" label.
func TestNew_UnmatchedRoute(t *testing.T) {
	r, reg := newRouter(t)
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/no-such-route", http.NoBody))
	if testutil.CollectAndCount(reg) == 0 {
		t.Error("expected metrics even for unmatched routes")
	}
}

// TC-HTM-03: duplicate registration on the same registry returns error
// (no panics; mirrors BR-MET-06 for app metrics).
func TestNew_DuplicateRegistration(t *testing.T) {
	reg := prometheus.NewRegistry()
	if _, err := httpmetrics.New(reg); err != nil {
		t.Fatalf("first registration failed: %v", err)
	}
	if _, err := httpmetrics.New(reg); err == nil {
		t.Error("expected error on duplicate histogram registration")
	}
}

// TC-HTM-03b: passing a nil registerer returns an error rather than
// panicking on the first Register call. The package-level godoc commits
// to "never panics; returns the registration error verbatim", and a nil
// reg is the most likely accidental violation. Regression for review
// finding R5.
func TestNew_NilRegisterer_ReturnsErrorNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("expected error, got panic: %v", r)
		}
	}()
	if _, err := httpmetrics.New(nil); err == nil {
		t.Error("expected error for nil registerer, got nil")
	}
}

// TC-HTM-04: counter registration error is returned (duration ok, counter
// collides with a pre-existing collector under the same name).
func TestNew_CounterRegistrationConflict(t *testing.T) {
	reg := prometheus.NewRegistry()
	clash := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "http_requests_total",
		Help: "pre-existing",
	}, []string{"method", "route", "status"})
	if err := reg.Register(clash); err != nil {
		t.Fatalf("pre-register counter: %v", err)
	}
	if _, err := httpmetrics.New(reg); err == nil {
		t.Error("expected error when counter registration fails")
	}
}

// TC-HTM-04b: when New fails on the counter registration, the histogram
// it had already registered must be unregistered before returning. Without
// the rollback, a caller that fixes the upstream conflict and retries New
// hits AlreadyRegisteredError on the histogram — there is no recovery path
// because the caller never received a handle to the orphaned collector.
//
// Regression for review finding R3 ("partial-registration leak").
//
// Setup nuance: the pre-registered counter uses the *same* Help / labels
// that `New` itself emits. That makes the first New fail with a pure
// "already registered" collision (not a dimHash mismatch). After the
// caller unregisters their collector, the dimHashesByName entry for
// `http_requests_total` still exists — but it matches the dimHash New
// will compute on retry, so retry registration of the counter is fine.
// The retry's only remaining failure mode is the leaked histogram, which
// is exactly what this test isolates.
func TestNew_CounterRegistrationConflict_AllowsRetryAfterCleanup(t *testing.T) {
	reg := prometheus.NewRegistry()
	clash := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "http_requests_total",
		Help: "Total number of HTTP requests.",
	}, []string{"method", "route", "status"})
	if err := reg.Register(clash); err != nil {
		t.Fatalf("pre-register counter: %v", err)
	}

	if _, err := httpmetrics.New(reg); err == nil {
		t.Fatal("expected error from first New (counter already registered)")
	}

	// Caller resolves the upstream conflict and retries.
	reg.Unregister(clash)

	if _, err := httpmetrics.New(reg); err != nil {
		t.Errorf("retry after upstream cleanup failed — histogram leaked from first New: %v", err)
	}
}

// TC-HTM-05: regression for the historic double-prefix bug.
//
// Old API (`New(namespace string, reg prometheus.Registerer)`) set
// `Namespace: "opentams"` on the metric *and* received a registerer that the
// caller had already wrapped with `WrapRegistererWithPrefix("opentams_", ...)`,
// so collected names were `opentams_opentams_http_request_duration_seconds`.
// The new API takes no namespace and the caller is required to pass an
// un-prefixed registerer (typically `metrics.Registry.RawRegisterer()`); the
// metric names on the wire MUST be the canonical, portable
// `http_request_duration_seconds` / `http_requests_total`.
func TestNew_NamesAreUnprefixed_OnWire(t *testing.T) {
	r, reg := newRouter(t)
	r.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", http.NoBody))

	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	have := map[string]bool{}
	for _, mf := range mfs {
		have[mf.GetName()] = true
	}
	for _, want := range []string{"http_request_duration_seconds", "http_requests_total"} {
		if !have[want] {
			t.Errorf("missing canonical metric %q in %v", want, keysOf(have))
		}
	}
	// Anti-assertion: never apply opentams_ prefix from inside this package.
	for name := range have {
		if name == "opentams_http_request_duration_seconds" ||
			name == "opentams_http_requests_total" {
			t.Errorf("middleware applied a namespace prefix; got %q", name)
		}
	}
}

// TC-HTM-06: end-to-end through pkg/metrics.RawRegisterer — the production
// wiring shape. Confirms that going through the namespace-aware Registry but
// targeting RawRegisterer produces unprefixed names. This is the regression
// guard against the double-prefix bug recurring in production wiring.
func TestNew_ViaMetricsRawRegisterer_ProducesUnprefixedNames(t *testing.T) {
	mr, err := metrics.New(metrics.WithNamespace("opentams"))
	if err != nil {
		t.Fatalf("metrics.New: %v", err)
	}
	mw, err := httpmetrics.New(mr.RawRegisterer())
	if err != nil {
		t.Fatalf("httpmetrics.New: %v", err)
	}
	r := gin.New()
	r.Use(mw)
	r.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", http.NoBody))

	mfs, err := mr.Gatherer().Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, mf := range mfs {
		name := mf.GetName()
		if name == "opentams_opentams_http_request_duration_seconds" ||
			name == "opentams_opentams_http_requests_total" ||
			name == "opentams_http_request_duration_seconds" ||
			name == "opentams_http_requests_total" {
			t.Errorf("expected portable name http_*; got prefixed %q", name)
		}
	}
	want := map[string]bool{
		"http_request_duration_seconds": false,
		"http_requests_total":           false,
	}
	for _, mf := range mfs {
		if _, ok := want[mf.GetName()]; ok {
			want[mf.GetName()] = true
		}
	}
	for k, ok := range want {
		if !ok {
			t.Errorf("missing portable metric %q", k)
		}
	}
}

// TC-HTM-07: WithSkipPaths suppresses observations for matched routes.
// /healthz, /readyz, /metrics traffic dominates a typical Prometheus scrape
// loop and otherwise drowns out interesting handler latencies. Skipped
// requests still flow through the middleware (so other middleware behaves
// identically); they just produce no histogram/counter samples.
func TestWithSkipPaths_OmitsObservationsForSkippedRoutes(t *testing.T) {
	r, reg := newRouter(t, httpmetrics.WithSkipPaths("/healthz", "/metrics"))
	r.GET("/healthz", func(c *gin.Context) { c.Status(http.StatusOK) })
	r.GET("/metrics", func(c *gin.Context) { c.Status(http.StatusOK) })
	r.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })

	for _, p := range []string{"/healthz", "/metrics", "/x"} {
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, p, http.NoBody))
	}

	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() != "http_requests_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			route := ""
			for _, l := range m.GetLabel() {
				if l.GetName() == "route" {
					route = l.GetValue()
				}
			}
			if route == "/healthz" || route == "/metrics" {
				t.Errorf("skipped route %q produced a counter sample", route)
			}
		}
	}
}

// TC-HTM-08: WithSkipPaths still records observations for non-skipped routes.
// Guard against an over-eager implementation that drops everything.
func TestWithSkipPaths_KeepsObservationsForOtherRoutes(t *testing.T) {
	r, reg := newRouter(t, httpmetrics.WithSkipPaths("/healthz"))
	r.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", http.NoBody))

	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	found := false
	for _, mf := range mfs {
		if mf.GetName() != "http_requests_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "route" && l.GetValue() == "/x" {
					found = true
				}
			}
		}
	}
	if !found {
		t.Error("non-skipped route /x did not produce a counter sample")
	}
}

// TC-HTM-09: WithSubsystem prefixes the metric names with the supplied
// subsystem segment (e.g. "myapi" → "myapi_http_request_duration_seconds").
// This is the recommended way to disambiguate when one process exposes
// multiple HTTP-shaped surfaces (admin API, public API, internal RPC) on
// the same registry.
func TestWithSubsystem_AddsSubsystemSegment(t *testing.T) {
	r, reg := newRouter(t, httpmetrics.WithSubsystem("myapi"))
	r.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", http.NoBody))

	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	have := map[string]bool{}
	for _, mf := range mfs {
		have[mf.GetName()] = true
	}
	for _, want := range []string{"myapi_http_request_duration_seconds", "myapi_http_requests_total"} {
		if !have[want] {
			t.Errorf("missing %q in %v", want, keysOf(have))
		}
	}
	for n := range have {
		if n == "http_request_duration_seconds" || n == "http_requests_total" {
			t.Errorf("subsystem not applied; got bare %q", n)
		}
	}
}

// TC-HTM-10: WithBuckets overrides the default histogram buckets.
// Verifies by examining the histogram's bucket boundaries — if the option
// is wired correctly, the gathered metric carries exactly the supplied
// upper bounds (plus the implicit +Inf bucket added by Prometheus).
func TestWithBuckets_OverridesDefaults(t *testing.T) {
	want := []float64{0.01, 0.1, 1, 10}
	r, reg := newRouter(t, httpmetrics.WithBuckets(want))
	r.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", http.NoBody))

	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	var got []float64
	for _, mf := range mfs {
		if mf.GetName() != "http_request_duration_seconds" {
			continue
		}
		for _, m := range mf.GetMetric() {
			h := m.GetHistogram()
			if h == nil {
				continue
			}
			for _, b := range h.GetBucket() {
				got = append(got, b.GetUpperBound())
			}
		}
	}
	if len(got) != len(want) {
		t.Fatalf("bucket count: got %d (%v), want %d (%v)", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("bucket[%d]: got %v, want %v", i, got[i], want[i])
		}
	}
}

// TC-HTM-10b: an empty (but non-nil) buckets slice falls back to the
// prometheus defaults rather than registering a degenerate zero-bucket
// histogram. Regression for review finding R4: distinguishing
// `WithBuckets(nil)` from `WithBuckets([]float64{})` would surprise
// callers — both expressions read as "I have no opinion, use defaults".
func TestWithBuckets_EmptyFallsBackToDefaults(t *testing.T) {
	r, reg := newRouter(t, httpmetrics.WithBuckets([]float64{}))
	r.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", http.NoBody))

	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	var got []float64
	for _, mf := range mfs {
		if mf.GetName() != "http_request_duration_seconds" {
			continue
		}
		for _, m := range mf.GetMetric() {
			h := m.GetHistogram()
			if h == nil {
				continue
			}
			for _, b := range h.GetBucket() {
				got = append(got, b.GetUpperBound())
			}
		}
	}
	if len(got) != len(prometheus.DefBuckets) {
		t.Fatalf("empty buckets did not fall back to defaults: got %d buckets (%v), want %d (DefBuckets)",
			len(got), got, len(prometheus.DefBuckets))
	}
	for i := range prometheus.DefBuckets {
		if got[i] != prometheus.DefBuckets[i] {
			t.Errorf("bucket[%d]: got %v, want %v (DefBuckets)", i, got[i], prometheus.DefBuckets[i])
		}
	}
}

// TC-HTM-11: WithConstLabels stamps every observation with the supplied
// constant labels. Useful for distinguishing instances when several
// processes scrape into the same Prometheus (e.g. tenant=, region=).
func TestWithConstLabels_AppliesToBothMetrics(t *testing.T) {
	r, reg := newRouter(t, httpmetrics.WithConstLabels(prometheus.Labels{
		"service": "api",
		"region":  "us-east-1",
	}))
	r.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", http.NoBody))

	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	checked := 0
	for _, mf := range mfs {
		name := mf.GetName()
		if name != "http_request_duration_seconds" && name != "http_requests_total" {
			continue
		}
		checked++
		for _, m := range mf.GetMetric() {
			seen := map[string]string{}
			for _, l := range m.GetLabel() {
				seen[l.GetName()] = l.GetValue()
			}
			if seen["service"] != "api" {
				t.Errorf("%s: const label service=%q, want %q", name, seen["service"], "api")
			}
			if seen["region"] != "us-east-1" {
				t.Errorf("%s: const label region=%q, want %q", name, seen["region"], "us-east-1")
			}
		}
	}
	if checked != 2 {
		t.Errorf("expected to inspect both http_* metric families, inspected %d", checked)
	}
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
