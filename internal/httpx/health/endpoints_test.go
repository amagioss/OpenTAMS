package health_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/amagioss/opentams/internal/httpx/health"
)

func init() { gin.SetMode(gin.TestMode) }

// mockChecker is local to endpoints_test.go. The Checker tests already use
// hand-rolled probe fakes; the handler tests need a whole Checker stub.
type mockChecker struct {
	ready func(context.Context) bool
}

func (m *mockChecker) Ready(ctx context.Context) bool {
	if m.ready == nil {
		return true
	}
	return m.ready(ctx)
}

func (m *mockChecker) Details(context.Context) health.Details { return health.Details{} }

func serveHealth(t *testing.T, h gin.HandlerFunc, path string) *http.Response {
	t.Helper()
	r := gin.New()
	r.GET(path, h)
	req := httptest.NewRequest(http.MethodGet, path, http.NoBody)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec.Result()
}

// TC-EP-HLTH-01 — HealthzHandler is a pure 200 with no body and no deps.
func TestHealthzHandler_Returns200(t *testing.T) {
	resp := serveHealth(t, health.HealthzHandler(), "/healthz")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status=%d, want 200", resp.StatusCode)
	}
	if resp.ContentLength > 0 {
		t.Errorf("expected empty body, got Content-Length=%d", resp.ContentLength)
	}
}

// TC-EP-HLTH-02 — HealthzHandler must not consult any Checker; verify by
// never having one (signature takes no Checker at all).
func TestHealthzHandler_NoChecker(t *testing.T) {
	// If the signature accepted a Checker, this line wouldn't compile.
	var h = health.HealthzHandler()
	if h == nil {
		t.Fatal("HealthzHandler returned nil")
	}
}

// TC-EP-HLTH-03 — ReadyzHandler returns 200 when Checker.Ready is true.
func TestReadyzHandler_Returns200WhenReady(t *testing.T) {
	c := &mockChecker{ready: func(context.Context) bool { return true }}

	resp := serveHealth(t, health.ReadyzHandler(c), "/readyz")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status=%d, want 200", resp.StatusCode)
	}
}

// TC-EP-HLTH-04 — ReadyzHandler returns 503 when Checker.Ready is false.
func TestReadyzHandler_Returns503WhenNotReady(t *testing.T) {
	c := &mockChecker{ready: func(context.Context) bool { return false }}

	resp := serveHealth(t, health.ReadyzHandler(c), "/readyz")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status=%d, want 503", resp.StatusCode)
	}
}

// TC-EP-HLTH-05 — ReadyzHandler propagates the request context to the Checker
// (so client cancellation / server shutdown cuts the probe short).
func TestReadyzHandler_PropagatesContextToChecker(t *testing.T) {
	type ctxKey struct{}
	sentinel := "ready-sentinel"

	var gotCtx context.Context
	c := &mockChecker{ready: func(ctx context.Context) bool {
		gotCtx = ctx
		return true
	}}

	r := gin.New()
	r.GET("/readyz", health.ReadyzHandler(c))

	req := httptest.NewRequest(http.MethodGet, "/readyz", http.NoBody)
	req = req.WithContext(context.WithValue(req.Context(), ctxKey{}, sentinel))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if gotCtx == nil {
		t.Fatal("Checker.Ready was not invoked")
	}
	if v, _ := gotCtx.Value(ctxKey{}).(string); v != sentinel {
		t.Error("ReadyzHandler did not propagate the request context to Checker.Ready")
	}
}

// TC-EP-HLTH-06 — ReadyzHandler returns 503 when Checker is nil (belt-and-
// braces: the package must not panic if a caller forgets to wire a checker).
// Rationale: nil-safety at the handler boundary costs one line and turns a
// silent crash into a loud, observable misconfig on the readyz probe.
func TestReadyzHandler_NilCheckerReturns503(t *testing.T) {
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		resp := serveHealth(t, health.ReadyzHandler(nil), "/readyz")
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("status=%d, want 503 when Checker is nil", resp.StatusCode)
		}
	}()

	if recovered != nil {
		t.Fatalf("ReadyzHandler(nil) must not panic; got: %v", recovered)
	}
}
