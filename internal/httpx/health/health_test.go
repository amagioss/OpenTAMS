package health_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/amagioss/opentams/internal/httpx/health"
)

// ---------- fakes (hand-rolled function-field style, per NFR-HLTH-T2) ----------

type fakeDBProbe struct {
	ping func(ctx context.Context) error
}

func (f *fakeDBProbe) Ping(ctx context.Context) error { return f.ping(ctx) }

type fakeObjectProbe struct {
	healthCheck func(ctx context.Context) error
}

func (f *fakeObjectProbe) HealthCheck(ctx context.Context) error { return f.healthCheck(ctx) }

type fakeJWKSProbe struct {
	check func(ctx context.Context) error
}

func (f *fakeJWKSProbe) Check(ctx context.Context) error { return f.check(ctx) }

// ---------- probe-function helpers ----------

func okProbe() func(context.Context) error {
	return func(context.Context) error { return nil }
}

func errProbe(msg string) func(context.Context) error {
	return func(context.Context) error { return errors.New(msg) }
}

// slowProbe blocks for d, but is cancellable via ctx.
func slowProbe(d time.Duration) func(context.Context) error {
	return func(ctx context.Context) error {
		select {
		case <-time.After(d):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func panicProbe(msg string) func(context.Context) error {
	return func(context.Context) error { panic(msg) }
}

// baseOpts returns a fully-populated, all-healthy Options suitable for defaulting tests.
func baseOpts() health.Options {
	return health.Options{
		Version:        "test",
		ReadyTimeout:   200 * time.Millisecond,
		DetailsTimeout: 500 * time.Millisecond,
		DB:             &fakeDBProbe{ping: okProbe()},
		Object:         &fakeObjectProbe{healthCheck: okProbe()},
		JWKS:           &fakeJWKSProbe{check: okProbe()},
	}
}

// ==========================================================================
// Constructor — BR-HLTH-13
// ==========================================================================

// TC-HLTH-01
func TestNew_NilDBReturnsSentinelError(t *testing.T) {
	opts := baseOpts()
	opts.DB = nil
	c, err := health.New(opts)
	if err == nil {
		t.Fatal("expected error for nil DB probe")
	}
	if !errors.Is(err, health.ErrMissingDBProbe) {
		t.Fatalf("expected errors.Is(err, ErrMissingDBProbe); got %v", err)
	}
	if c != nil {
		t.Fatal("expected nil Checker on construction error")
	}
}

// TC-HLTH-02
func TestNew_NilObjectStoreReturnsSentinelError(t *testing.T) {
	opts := baseOpts()
	opts.Object = nil
	c, err := health.New(opts)
	if err == nil {
		t.Fatal("expected error for nil Object probe")
	}
	if !errors.Is(err, health.ErrMissingObjectStoreProbe) {
		t.Fatalf("expected errors.Is(err, ErrMissingObjectStoreProbe); got %v", err)
	}
	if c != nil {
		t.Fatal("expected nil Checker on construction error")
	}
}

// TC-HLTH-03 — JWKS is optional (BR-HLTH-09).
func TestNew_NilJWKSIsAllowed(t *testing.T) {
	opts := baseOpts()
	opts.JWKS = nil
	c, err := health.New(opts)
	if err != nil {
		t.Fatalf("unexpected error with nil JWKS: %v", err)
	}
	if c == nil {
		t.Fatal("expected non-nil Checker when only JWKS is nil")
	}
	if !c.Ready(context.Background()) {
		t.Error("expected Ready=true when both required probes are healthy")
	}
}

// TC-HLTH-04 — zero-valued timeouts get sensible defaults (500ms / 2s).
func TestNew_AppliesDefaultTimeouts(t *testing.T) {
	// A blocking DB probe that only unblocks on ctx cancel. If ReadyTimeout defaults
	// to 500ms, Ready should return false in ~500ms. If it fails to default
	// (e.g. uses 0 literally), Ready would either return instantly or never.
	opts := health.Options{
		Version:        "test",
		ReadyTimeout:   0,
		DetailsTimeout: 0,
		DB: &fakeDBProbe{ping: func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		}},
		Object: &fakeObjectProbe{healthCheck: okProbe()},
	}
	c, err := health.New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	start := time.Now()
	if c.Ready(context.Background()) {
		t.Error("expected Ready=false when DB is blocked past default budget")
	}
	elapsed := time.Since(start)
	if elapsed < 400*time.Millisecond {
		t.Errorf("Ready returned in %v; default ReadyTimeout should be ~500ms", elapsed)
	}
	if elapsed > 900*time.Millisecond {
		t.Errorf("Ready returned in %v; default ReadyTimeout should be ~500ms (not 2s or unbounded)", elapsed)
	}
}

// ==========================================================================
// Ready — BR-HLTH-01, BR-HLTH-02, BR-HLTH-03
// ==========================================================================

// TC-HLTH-05
func TestReady_AllProbesSucceed(t *testing.T) {
	c, err := health.New(baseOpts())
	if err != nil {
		t.Fatal(err)
	}
	if !c.Ready(context.Background()) {
		t.Error("expected Ready=true when all required probes succeed")
	}
}

// TC-HLTH-06
func TestReady_DBFailureReturnsFalse(t *testing.T) {
	opts := baseOpts()
	opts.DB = &fakeDBProbe{ping: errProbe("db down")}
	c, _ := health.New(opts)
	if c.Ready(context.Background()) {
		t.Error("expected Ready=false when DB probe fails")
	}
}

// TC-HLTH-07
func TestReady_ObjectStoreFailureReturnsFalse(t *testing.T) {
	opts := baseOpts()
	opts.Object = &fakeObjectProbe{healthCheck: errProbe("s3 down")}
	c, _ := health.New(opts)
	if c.Ready(context.Background()) {
		t.Error("expected Ready=false when Object probe fails")
	}
}

// TC-HLTH-08 — JWKS not consulted by Ready (BR-HLTH-02).
func TestReady_DoesNotInvokeJWKSProbe(t *testing.T) {
	var jwksCalled atomic.Bool
	opts := baseOpts()
	opts.JWKS = &fakeJWKSProbe{check: func(context.Context) error {
		jwksCalled.Store(true)
		return nil
	}}
	c, _ := health.New(opts)
	if !c.Ready(context.Background()) {
		t.Error("expected Ready=true")
	}
	if jwksCalled.Load() {
		t.Error("Ready must not invoke JWKS probe (BR-HLTH-02)")
	}
}

// TC-HLTH-09 — Ready respects ReadyTimeout on a slow probe.
func TestReady_TimeoutWhenProbeHangs(t *testing.T) {
	opts := baseOpts()
	opts.ReadyTimeout = 100 * time.Millisecond
	opts.DB = &fakeDBProbe{ping: slowProbe(500 * time.Millisecond)}
	c, _ := health.New(opts)

	start := time.Now()
	ready := c.Ready(context.Background())
	elapsed := time.Since(start)

	if ready {
		t.Error("expected Ready=false when DB probe exceeds timeout")
	}
	if elapsed >= 300*time.Millisecond {
		t.Errorf("Ready took %v; expected ~100ms (ReadyTimeout)", elapsed)
	}
}

// TC-HLTH-10 — probes run concurrently, not serially.
func TestReady_RunsProbesConcurrently(t *testing.T) {
	opts := baseOpts()
	opts.ReadyTimeout = 500 * time.Millisecond
	opts.DB = &fakeDBProbe{ping: slowProbe(100 * time.Millisecond)}
	opts.Object = &fakeObjectProbe{healthCheck: slowProbe(100 * time.Millisecond)}
	c, _ := health.New(opts)

	start := time.Now()
	ready := c.Ready(context.Background())
	elapsed := time.Since(start)

	if !ready {
		t.Fatal("expected Ready=true")
	}
	if elapsed >= 180*time.Millisecond {
		t.Errorf("Ready took %v; expected ~100ms — probes appear to be running serially", elapsed)
	}
}

// TC-HLTH-11 — a probe that panics must not crash Ready (NFR-HLTH-R1).
func TestReady_PanicInProbeReturnsFalseNoEscape(t *testing.T) {
	opts := baseOpts()
	opts.DB = &fakeDBProbe{ping: panicProbe("simulated db panic")}
	c, _ := health.New(opts)

	var (
		ready     bool
		recovered any
	)
	func() {
		defer func() { recovered = recover() }()
		ready = c.Ready(context.Background())
	}()

	if recovered != nil {
		t.Fatalf("Ready must not propagate panic (NFR-HLTH-R1); got: %v", recovered)
	}
	if ready {
		t.Error("expected Ready=false when a probe panics")
	}
}

// TC-HLTH-11b — Ready is safe under concurrent calls (regression guard against
// accidental shared mutable state in the Checker).
func TestReady_ConcurrentCallsAreSafe(t *testing.T) {
	c, err := health.New(baseOpts())
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = c.Ready(context.Background())
		}()
	}
	wg.Wait()
}

// ==========================================================================
// Details — BR-HLTH-04, BR-HLTH-05, BR-HLTH-06
// ==========================================================================

// TC-HLTH-12
func TestDetails_AllHealthyComponents(t *testing.T) {
	c, _ := health.New(baseOpts())
	d := c.Details(context.Background())

	if d.Status != health.StatusHealthy {
		t.Errorf("want overall=%q, got %q", health.StatusHealthy, d.Status)
	}
	for _, k := range []string{"metastore", "objectstore", "jwtauth"} {
		cs, ok := d.Components[k]
		if !ok {
			t.Errorf("missing component %q", k)
			continue
		}
		if cs.Status != health.StatusHealthy {
			t.Errorf("component %q: want healthy, got %q", k, cs.Status)
		}
	}
	if len(d.Components) != 3 {
		t.Errorf("want 3 components, got %d: %v", len(d.Components), d.Components)
	}
}

// TC-HLTH-13 — JWKS absent when opts.JWKS == nil (BR-HLTH-09).
func TestDetails_OmitsJWKSWhenNil(t *testing.T) {
	opts := baseOpts()
	opts.JWKS = nil
	c, _ := health.New(opts)
	d := c.Details(context.Background())

	if _, ok := d.Components["jwtauth"]; ok {
		t.Error("expected jwtauth component to be omitted when JWKS is nil")
	}
	if len(d.Components) != 2 {
		t.Errorf("want 2 components (metastore, objectstore), got %d", len(d.Components))
	}
	if d.Status != health.StatusHealthy {
		t.Errorf("want overall=healthy, got %q", d.Status)
	}
}

// TC-HLTH-14 — worst-of aggregation.
func TestDetails_WorstOfAggregation(t *testing.T) {
	opts := baseOpts()
	opts.Object = &fakeObjectProbe{healthCheck: errProbe("s3 down")}
	c, _ := health.New(opts)
	d := c.Details(context.Background())

	if d.Status != health.StatusUnhealthy {
		t.Errorf("want overall=%q when one component is unhealthy, got %q",
			health.StatusUnhealthy, d.Status)
	}
	if d.Components["metastore"].Status != health.StatusHealthy {
		t.Errorf("metastore should be healthy, got %q", d.Components["metastore"].Status)
	}
	if d.Components["objectstore"].Status != health.StatusUnhealthy {
		t.Errorf("objectstore should be unhealthy, got %q", d.Components["objectstore"].Status)
	}
}

// TC-HLTH-15
func TestDetails_VersionFieldSetFromOptions(t *testing.T) {
	opts := baseOpts()
	opts.Version = "v1.2.3-test"
	c, _ := health.New(opts)
	d := c.Details(context.Background())

	if d.Version != "v1.2.3-test" {
		t.Errorf("want Version=v1.2.3-test, got %q", d.Version)
	}
}

// TC-HLTH-16 — LatencyMs populated and non-trivially measured.
func TestDetails_LatencyMsPopulated(t *testing.T) {
	opts := baseOpts()
	opts.DB = &fakeDBProbe{ping: slowProbe(20 * time.Millisecond)}
	c, _ := health.New(opts)
	d := c.Details(context.Background())

	cs, ok := d.Components["metastore"]
	if !ok {
		t.Fatal("metastore component missing")
	}
	if cs.LatencyMs == nil {
		t.Fatal("expected LatencyMs to be non-nil")
	}
	if *cs.LatencyMs < 15 {
		t.Errorf("metastore latency looks suspiciously low: %d ms (expected ≥ 15)", *cs.LatencyMs)
	}
	if *cs.LatencyMs > 500 {
		t.Errorf("metastore latency looks suspiciously high: %d ms", *cs.LatencyMs)
	}
}

// TC-HLTH-17 — panic in probe → that component unhealthy, no escape (NFR-HLTH-R1).
func TestDetails_PanicInProbeYieldsUnhealthyComponent(t *testing.T) {
	opts := baseOpts()
	opts.DB = &fakeDBProbe{ping: panicProbe("db exploded")}
	c, _ := health.New(opts)

	var (
		d         health.Details
		recovered any
	)
	func() {
		defer func() { recovered = recover() }()
		d = c.Details(context.Background())
	}()

	if recovered != nil {
		t.Fatalf("Details must not propagate panic (NFR-HLTH-R1); got: %v", recovered)
	}
	if d.Components["metastore"].Status != health.StatusUnhealthy {
		t.Errorf("metastore should be unhealthy after panic, got %q", d.Components["metastore"].Status)
	}
	if d.Status != health.StatusUnhealthy {
		t.Errorf("overall should be unhealthy, got %q", d.Status)
	}
	if d.Components["objectstore"].Status != health.StatusHealthy {
		t.Errorf("objectstore (independent of panic) should still be healthy, got %q",
			d.Components["objectstore"].Status)
	}
}

// TC-HLTH-18 — Details respects DetailsTimeout per-probe.
func TestDetails_RespectsDetailsTimeout(t *testing.T) {
	opts := baseOpts()
	opts.DetailsTimeout = 100 * time.Millisecond
	opts.Object = &fakeObjectProbe{healthCheck: slowProbe(500 * time.Millisecond)}
	c, _ := health.New(opts)

	start := time.Now()
	d := c.Details(context.Background())
	elapsed := time.Since(start)

	if elapsed >= 300*time.Millisecond {
		t.Errorf("Details took %v; expected ~100ms (DetailsTimeout)", elapsed)
	}
	if d.Components["objectstore"].Status != health.StatusUnhealthy {
		t.Errorf("objectstore should be unhealthy after timeout, got %q",
			d.Components["objectstore"].Status)
	}
	if d.Status != health.StatusUnhealthy {
		t.Errorf("overall should be unhealthy, got %q", d.Status)
	}
}
