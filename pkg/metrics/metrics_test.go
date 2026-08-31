package metrics_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/amagioss/opentams/pkg/metrics"
)

// --- TC-MET-01: New with no options succeeds ---

func TestNew_NoOptions_Succeeds(t *testing.T) {
	reg, err := metrics.New()
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	if reg == nil {
		t.Fatal("New() returned nil registry")
	}
}

// --- TC-MET-02: Go and process collectors registered by default ---

func TestNew_DefaultCollectors_Present(t *testing.T) {
	reg, _ := metrics.New()
	names := gatherNames(t, reg)

	if !hasPrefix(names, "go_") {
		t.Error("go_* metrics not present with default options")
	}
	if !hasPrefix(names, "process_") {
		t.Error("process_* metrics not present with default options")
	}
}

// --- TC-MET-03: WithoutProcessCollector suppresses process metrics ---

func TestNew_WithoutProcessCollector_NoProcessMetrics(t *testing.T) {
	reg, _ := metrics.New(metrics.WithoutProcessCollector())
	names := gatherNames(t, reg)

	if hasPrefix(names, "process_") {
		t.Error("process_* metrics present despite WithoutProcessCollector")
	}
}

// --- TC-MET-04: WithoutGoCollector suppresses Go metrics ---

func TestNew_WithoutGoCollector_NoGoMetrics(t *testing.T) {
	reg, _ := metrics.New(metrics.WithoutGoCollector())
	names := gatherNames(t, reg)

	if hasPrefix(names, "go_") {
		t.Error("go_* metrics present despite WithoutGoCollector")
	}
}

// --- TC-MET-05: WithNamespace prefixes app metrics ---

func TestWithNamespace_PrefixesAppMetrics(t *testing.T) {
	reg, _ := metrics.New(metrics.WithNamespace("opentams"))

	counter := prometheus.NewCounter(prometheus.CounterOpts{
		Subsystem: "http",
		Name:      "requests_total",
		Help:      "test counter",
	})
	if err := reg.NamespacedRegisterer().Register(counter); err != nil {
		t.Fatalf("Register: %v", err)
	}

	names := gatherNames(t, reg)
	if !contains(names, "opentams_http_requests_total") {
		t.Errorf("expected opentams_http_requests_total, got: %v", names)
	}
}

// --- TC-MET-06: WithNamespace does not prefix go/process collectors ---

func TestWithNamespace_DoesNotPrefixStandardCollectors(t *testing.T) {
	reg, _ := metrics.New(metrics.WithNamespace("opentams"))
	names := gatherNames(t, reg)

	for _, name := range names {
		if strings.HasPrefix(name, "opentams_go_") {
			t.Errorf("go collector metric incorrectly prefixed: %s", name)
		}
		if strings.HasPrefix(name, "opentams_process_") {
			t.Errorf("process collector metric incorrectly prefixed: %s", name)
		}
	}
}

// --- TC-MET-07: No namespace means no prefix on app metrics ---

func TestNoNamespace_AppMetricsUnprefixed(t *testing.T) {
	reg, _ := metrics.New()

	counter := prometheus.NewCounter(prometheus.CounterOpts{
		Subsystem: "http",
		Name:      "requests_total",
		Help:      "test counter",
	})
	if err := reg.NamespacedRegisterer().Register(counter); err != nil {
		t.Fatalf("Register: %v", err)
	}

	names := gatherNames(t, reg)
	if !contains(names, "http_requests_total") {
		t.Errorf("expected http_requests_total without prefix, got: %v", names)
	}
}

// --- TC-MET-08: Duplicate registration returns error, does not panic ---

func TestRegisterer_DuplicateRegistration_ReturnsError(t *testing.T) {
	reg, _ := metrics.New()

	counter := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "test_dup_counter",
		Help: "test",
	})

	if err := reg.NamespacedRegisterer().Register(counter); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	if err := reg.NamespacedRegisterer().Register(counter); err == nil {
		t.Error("second Register: expected error for duplicate, got nil")
	}
}

// --- TC-MET-09: Handler returns 200 with prometheus content type ---

func TestHandler_Returns200WithPrometheusContentType(t *testing.T) {
	reg, _ := metrics.New()
	h := promhttp.HandlerFor(reg.Gatherer(), promhttp.HandlerOpts{})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/metrics", http.NoBody)
	h.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("Handler status = %d, want 200", w.Code)
	}
	ct := w.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain", ct)
	}
}

// --- TC-MET-10: Gatherer output includes registered app metrics ---

func TestGatherer_OutputIncludesRegisteredMetric(t *testing.T) {
	reg, _ := metrics.New(metrics.WithNamespace("test"))

	counter := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "my_total",
		Help: "test metric",
	})
	if err := reg.NamespacedRegisterer().Register(counter); err != nil {
		t.Fatalf("Register: %v", err)
	}
	counter.Inc()

	h := promhttp.HandlerFor(reg.Gatherer(), promhttp.HandlerOpts{})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/metrics", http.NoBody)
	h.ServeHTTP(w, r)

	if !strings.Contains(w.Body.String(), "test_my_total 1") {
		t.Errorf("metric test_my_total not found in gatherer output:\n%s", w.Body.String())
	}
}

// --- TC-MET-11: Gatherer returns usable gatherer ---

func TestGatherer_CanGatherMetrics(t *testing.T) {
	reg, _ := metrics.New()

	mfs, err := reg.Gatherer().Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if len(mfs) == 0 {
		t.Error("Gather returned no metric families")
	}
}

// --- TC-MET-12: Both collectors suppressed — registry still usable ---

func TestNew_BothCollectorsSuppressed_RegistryStillWorks(t *testing.T) {
	reg, err := metrics.New(
		metrics.WithoutProcessCollector(),
		metrics.WithoutGoCollector(),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	counter := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "still_works_total",
		Help: "test",
	})
	if err := reg.NamespacedRegisterer().Register(counter); err != nil {
		t.Errorf("Register after suppressing collectors: %v", err)
	}
}

// --- TC-MET-13: New returns error when process collector already registered ---

func TestNew_ProcessCollectorAlreadyRegistered_ReturnsError(t *testing.T) {
	raw := prometheus.NewRegistry()
	if err := raw.Register(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{})); err != nil {
		t.Fatalf("test setup: Register process collector: %v", err)
	}

	_, err := metrics.New(metrics.WithRawRegistry(raw))
	if err == nil {
		t.Error("expected error when process collector already registered, got nil")
	}
}

// --- TC-MET-14: New returns error when go collector already registered ---

func TestNew_GoCollectorAlreadyRegistered_ReturnsError(t *testing.T) {
	raw := prometheus.NewRegistry()
	if err := raw.Register(collectors.NewGoCollector()); err != nil {
		t.Fatalf("test setup: Register go collector: %v", err)
	}

	_, err := metrics.New(metrics.WithRawRegistry(raw), metrics.WithoutProcessCollector())
	if err == nil {
		t.Error("expected error when go collector already registered, got nil")
	}
}

// --- TC-MET-16: RawRegisterer registers metrics under their literal names ---
//
// Used by community-portable metric families (e.g. http_*, db_*) that should
// keep their well-known names regardless of the namespace configured for app
// metrics — same rationale as go_* / process_* collectors. See BR-MET-08.

func TestRawRegisterer_RegistersWithoutNamespacePrefix(t *testing.T) {
	reg, _ := metrics.New(metrics.WithNamespace("opentams"))

	counter := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "http_requests_total",
		Help: "test counter",
	})
	if err := reg.RawRegisterer().Register(counter); err != nil {
		t.Fatalf("RawRegisterer().Register: %v", err)
	}
	counter.Inc()

	names := gatherNames(t, reg)
	if !contains(names, "http_requests_total") {
		t.Errorf("expected http_requests_total without prefix, got: %v", names)
	}
	for _, n := range names {
		if n == "opentams_http_requests_total" {
			t.Errorf("RawRegisterer must not apply namespace; got: %s", n)
		}
	}
}

// --- TC-MET-17: RawRegisterer points at the same underlying Gatherer ---

func TestRawRegisterer_SharesGathererWithRegistry(t *testing.T) {
	reg, _ := metrics.New(metrics.WithNamespace("opentams"))

	counter := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "raw_shared_total",
		Help: "test",
	})
	if err := reg.RawRegisterer().Register(counter); err != nil {
		t.Fatalf("Register: %v", err)
	}
	counter.Inc()

	mfs, err := reg.Gatherer().Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	found := false
	for _, mf := range mfs {
		if mf.GetName() == "raw_shared_total" {
			found = true
		}
	}
	if !found {
		t.Error("metric registered via RawRegisterer not visible through Gatherer")
	}
}

// --- TC-MET-15: Duplicate registration with namespace returns error ---

func TestRegisterer_DuplicateRegistration_WithNamespace_ReturnsError(t *testing.T) {
	reg, _ := metrics.New(metrics.WithNamespace("opentams"))

	counter := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "dup_ns_total",
		Help: "test",
	})
	if err := reg.NamespacedRegisterer().Register(counter); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	if err := reg.NamespacedRegisterer().Register(counter); err == nil {
		t.Error("expected error for duplicate with namespace, got nil")
	}
}

// --- helpers ---

func gatherNames(t *testing.T, reg *metrics.Registry) []string {
	t.Helper()
	mfs, err := reg.Gatherer().Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	names := make([]string, 0, len(mfs))
	for _, mf := range mfs {
		names = append(names, mf.GetName())
	}
	return names
}

func hasPrefix(names []string, prefix string) bool {
	for _, n := range names {
		if strings.HasPrefix(n, prefix) {
			return true
		}
	}
	return false
}

func contains(names []string, name string) bool {
	for _, n := range names {
		if n == name {
			return true
		}
	}
	return false
}
