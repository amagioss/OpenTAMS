package handlers_test

import (
	"context"
	"testing"

	"github.com/amagioss/opentams/gen/api"
	"github.com/amagioss/opentams/internal/httpx/handlers"
	"github.com/amagioss/opentams/internal/httpx/health"
)

// newHealthHandler wires up a Handler whose only dependency is the health
// checker — every other service is nil because the health endpoints do not
// consult them.
func newHealthHandler(hc health.Checker) *handlers.Handler {
	return handlers.New(nil, testConfig(), nil, nil, nil, nil, nil, hc, nil)
}

// Note: /healthz and /readyz handler tests live in
// internal/httpx/health/endpoints_test.go — those endpoints are no longer
// part of the strict server interface (excluded from oapi-codegen so M14 can
// mount them on an unauthenticated route group).

// TC-HDL-HLTH-04: GetHealthDetails returns 200 JSON with the Checker's
// per-component report mapped into the api schema shape.
func TestGetHealthDetails_MapsCheckerOutputToAPIBody(t *testing.T) {
	latency := int64(42)
	hc := &mockChecker{
		details: func(context.Context) health.Details {
			return health.Details{
				Status:  health.StatusHealthy,
				Version: "v1.2.3",
				Components: map[string]health.ComponentStatus{
					"metastore":   {Status: health.StatusHealthy, LatencyMs: &latency},
					"objectstore": {Status: health.StatusHealthy, LatencyMs: &latency},
				},
			}
		},
	}
	h := newHealthHandler(hc)

	resp, err := h.GetHealthDetails(context.Background(), api.GetHealthDetailsRequestObject{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	body, ok := resp.(api.GetHealthDetails200JSONResponse)
	if !ok {
		t.Fatalf("want GetHealthDetails200JSONResponse, got %T", resp)
	}
	if string(body.Status) != string(health.StatusHealthy) {
		t.Errorf("body.Status=%q, want %q", body.Status, health.StatusHealthy)
	}
	if body.Version != "v1.2.3" {
		t.Errorf("body.Version=%q, want v1.2.3", body.Version)
	}
	if len(body.Components) != 2 {
		t.Errorf("body.Components len=%d, want 2", len(body.Components))
	}
	ms, ok := body.Components["metastore"]
	if !ok {
		t.Fatal("missing metastore component in body")
	}
	if ms.LatencyMs == nil {
		t.Fatal("metastore.LatencyMs should be non-nil")
	}
	if *ms.LatencyMs != 42 {
		t.Errorf("metastore.LatencyMs=%d, want 42", *ms.LatencyMs)
	}
	if ms.Status != api.HealthComponentStatusStatusHealthy {
		t.Errorf("metastore.Status=%q, want healthy", ms.Status)
	}
}

// TC-HDL-HLTH-05: GetHealthDetails propagates an "unhealthy" overall status
// and the individual unhealthy component into the response.
func TestGetHealthDetails_UnhealthyOverallStatus(t *testing.T) {
	hc := &mockChecker{
		details: func(context.Context) health.Details {
			return health.Details{
				Status:  health.StatusUnhealthy,
				Version: "v1",
				Components: map[string]health.ComponentStatus{
					"metastore":   {Status: health.StatusHealthy},
					"objectstore": {Status: health.StatusUnhealthy},
				},
			}
		},
	}
	h := newHealthHandler(hc)

	resp, err := h.GetHealthDetails(context.Background(), api.GetHealthDetailsRequestObject{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	body := resp.(api.GetHealthDetails200JSONResponse)

	if body.Status != api.HealthDetailsStatus(health.StatusUnhealthy) {
		t.Errorf("overall body.Status=%q, want unhealthy", body.Status)
	}
	if body.Components["objectstore"].Status != api.HealthComponentStatusStatusUnhealthy {
		t.Errorf("objectstore.Status=%q, want unhealthy", body.Components["objectstore"].Status)
	}
	if body.Components["metastore"].Status != api.HealthComponentStatusStatusHealthy {
		t.Errorf("metastore.Status=%q, want healthy", body.Components["metastore"].Status)
	}
	if body.Components["metastore"].LatencyMs != nil {
		t.Errorf("metastore.LatencyMs=%v, want nil (not populated by mock)", body.Components["metastore"].LatencyMs)
	}
}
