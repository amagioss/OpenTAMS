package handlers

import (
	"context"

	"github.com/amagioss/opentams/gen/api"
	"github.com/amagioss/opentams/internal/httpx/health"
)

// GetHealthz and GetReadyz are intentionally NOT implemented here. They are
// excluded from oapi-codegen (`.oapi-codegen.yaml → exclude-operation-ids`)
// and served by gin.HandlerFuncs in `internal/httpx/health/endpoints.go`,
// mounted on M14's *public* (unauthenticated) route group. Keeping them on
// the strict server interface would force them onto the authenticated group
// and cause a route-registration conflict with the public mount.
//
// GetHealthDetails stays here — it's authenticated (BR-HLTH-12) and benefits
// from the generated strict response types.

// GetHealthDetails returns the per-component health report.
// BR-HLTH-04 / BR-HLTH-05 / BR-HLTH-09.
func (h *Handler) GetHealthDetails(
	ctx context.Context,
	_ api.GetHealthDetailsRequestObject,
) (api.GetHealthDetailsResponseObject, error) {
	d := h.health.Details(ctx)

	components := make(map[string]api.HealthComponentStatus, len(d.Components))
	for name, cs := range d.Components {
		components[name] = toAPIComponent(cs)
	}

	return api.GetHealthDetails200JSONResponse{
		Status:     api.HealthDetailsStatus(d.Status),
		Version:    d.Version,
		Components: components,
	}, nil
}

// toAPIComponent maps the health package's ComponentStatus to the generated
// api.HealthComponentStatus. The spec declares latency_ms as `integer` which
// codegen renders as *int; the health package measures in int64 for
// overflow-safety. Cast at the boundary — measured latencies always fit.
func toAPIComponent(cs health.ComponentStatus) api.HealthComponentStatus {
	var latency *int
	if cs.LatencyMs != nil {
		v := int(*cs.LatencyMs)
		latency = &v
	}
	return api.HealthComponentStatus{
		Status:    api.HealthComponentStatusStatus(cs.Status),
		LatencyMs: latency,
	}
}
