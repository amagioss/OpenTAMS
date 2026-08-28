package health

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// HealthzHandler returns the gin.HandlerFunc for GET /healthz. It is a pure
// liveness probe: always 200 with no body, no dependency checks, and no
// Checker dependency. BR-HLTH-01.
//
// /healthz is excluded from oapi-codegen and mounted directly on M14's
// public (unauthenticated) route group.
func HealthzHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Status(http.StatusOK)
	}
}

// ReadyzHandler returns the gin.HandlerFunc for GET /readyz. It calls
// c.Ready(ctx) and responds 200 when ready, 503 otherwise. BR-HLTH-02 /
// BR-HLTH-03.
//
// Nil-Checker behaviour: returns 503 on every request rather than panicking.
// Rationale: a readyz probe reporting "not ready" is a safe, loud failure
// mode — Kubernetes will stop routing traffic to this pod until the operator
// notices and re-deploys with a properly-wired Checker.
func ReadyzHandler(c Checker) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		if c == nil || !c.Ready(ctx.Request.Context()) {
			ctx.Status(http.StatusServiceUnavailable)
			return
		}
		ctx.Status(http.StatusOK)
	}
}
