// Package metrics provides a single-function wrapper around
// promhttp.HandlerFor with opinionated options for serving the OpenTAMS
// /metrics endpoint.
//
// The /metrics endpoint is intentionally NOT routed through the oapi-codegen
// strict server interface (see .oapi-codegen.yaml → exclude-operation-ids).
// It is mounted directly on the Gin router at M14 wire-up time via
// gin.WrapH(metrics.Handler(reg)).
//
// Design: see docs/design/internal-httpx-metrics/.
package metrics

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// scrapeTimeout bounds the time promhttp spends walking the registry on a
// single scrape. 10s comfortably covers the worst real scrape while keeping
// a stuck collector from blocking the handler indefinitely.
const scrapeTimeout = 10 * time.Second

// Handler returns the HTTP handler for /metrics. The returned handler serves
// the Prometheus text exposition format by default, and OpenMetrics when the
// client sends Accept: application/openmetrics-text.
//
// The gatherer is normally the same *prometheus.Registry used to register
// application metrics. Passing prometheus.DefaultGatherer is supported but
// discouraged — prefer an explicit, injected registry.
//
// Nil-gatherer behaviour (NFR-MET-R1): Handler does not panic when gatherer
// is nil. Instead it returns a handler that serves HTTP 500 on every request,
// so the misconfiguration surfaces in operational logs rather than crashing
// the process on the first scrape.
func Handler(gatherer prometheus.Gatherer) http.Handler {
	if gatherer == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w,
				"metrics: nil gatherer — server misconfigured",
				http.StatusInternalServerError)
		})
	}

	opts := promhttp.HandlerOpts{
		ErrorHandling:     promhttp.ContinueOnError,
		Timeout:           scrapeTimeout,
		EnableOpenMetrics: true,
		Registry:          nil,
	}
	return promhttp.HandlerFor(gatherer, opts)
}
