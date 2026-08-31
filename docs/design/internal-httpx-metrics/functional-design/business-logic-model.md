# Business Logic Model — internal/httpx/metrics

## Package surface

```go
package metrics

import (
    "net/http"

    "github.com/prometheus/client_golang/prometheus"
    "github.com/prometheus/client_golang/prometheus/promhttp"
)

// Handler returns the HTTP handler for the /metrics endpoint.
// The returned handler serves Prometheus exposition format (and OpenMetrics
// when requested via Accept) against the provided gatherer.
//
// The gatherer is typically the same *prometheus.Registry used by pkg/metrics
// to register application metrics. Passing prometheus.DefaultGatherer is
// supported but discouraged — prefer an explicit, injected registry.
func Handler(gatherer prometheus.Gatherer) http.Handler
```

Nothing else is exported.

---

## Handler(gatherer) — implementation

```
1. opts := promhttp.HandlerOpts{
       ErrorHandling:     promhttp.ContinueOnError,
       Timeout:           10 * time.Second,
       EnableOpenMetrics: true,
       Registry:          nil,   // no recursion
   }
2. return promhttp.HandlerFor(gatherer, opts)
```

That's the whole implementation. ~8 lines of code.

---

## M14 wiring (informational — not M13 scope)

The M14 server package is responsible for mounting this handler. Sketch:

```go
import (
    "github.com/gin-gonic/gin"
    "github.com/amagioss/opentams/internal/httpx/metrics"
    ...
)

router := gin.New()
// ... apply middleware chain ...

// Mount the generated handlers for the TAMS API.
api.RegisterHandlersWithOptions(router, strictHandler, ginOpts)

// Mount /metrics directly — not part of the strict interface.
router.GET("/metrics", gin.WrapH(metrics.Handler(reg)))
```

Order: `router.GET("/metrics", ...)` MUST come after `RegisterHandlersWithOptions` only if the generated registration doesn't claim `/metrics` (it won't, because `GetMetrics` is excluded from codegen). If a future regeneration accidentally re-includes `GetMetrics`, Gin will panic on duplicate route registration — a loud failure mode that catches the bug immediately.

---

## Test plan (unit — `metrics_test.go`)

Tests run in-process against the public `Handler` function:

| # | Test | Setup | Assertion |
|---|---|---|---|
| TC-MET-01 | `Handler returns Prometheus format by default` | Register one counter in a fresh `prometheus.NewRegistry()`. Call `Handler(reg).ServeHTTP(rec, req)` with no `Accept` header. | Response `Content-Type` starts with `text/plain`; body contains the counter's `HELP`, `TYPE`, and value lines. |
| TC-MET-02 | `Handler returns OpenMetrics when Accept requests it` | Same registry. Set `Accept: application/openmetrics-text`. | `Content-Type` is `application/openmetrics-text; version=1.0.0; charset=utf-8` (or later). Body ends with `# EOF`. |
| TC-MET-03 | `Handler uses injected gatherer, not default` | Register metric only on custom registry. Register a different metric on `prometheus.DefaultGatherer`. | Response body contains the custom-registry metric and does NOT contain the default-registry metric. |
| TC-MET-04 | `Handler does not require auth` | Registry with one metric. Send request with no `Authorization` header. | 200 OK with body. No 401. |
| TC-MET-05 | `Handler serves correctly with an empty gatherer` | Fresh empty `prometheus.NewRegistry()`. | 200 OK. Body is valid exposition format (possibly just the process-collector metrics if registered, or empty). No panic. |

Coverage target: 100% statement coverage on this file — trivial given the function is 2 statements.

---

## Out of scope

- Exposition format filtering by metric name / label — promhttp doesn't support this without custom middleware; no evidence we need it.
- Gzip compression — promhttp handles this when the client sends `Accept-Encoding: gzip`; no additional wiring needed.
- Rate limiting on `/metrics` — scrape rate is controlled by the scraper's config, not the server. If abuse becomes a concern, use the existing M11 rate-limit middleware at the M14 route level.
- Metrics namespacing (`opentams_*` prefix) — owned by `pkg/metrics` at registration time, not by this handler.
