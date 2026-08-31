# Business Rules — internal/httpx/metrics

Scope: Prometheus metrics exposition endpoint (`GET /metrics`).
A thin wrapper over `github.com/prometheus/client_golang/prometheus/promhttp`
with opinionated defaults. Mounted directly on the Gin router in M14 —
**not** routed through the oapi-codegen strict server interface.

---

## BR-MET-01: `/metrics` is served by promhttp, not via the strict interface
The OpenAPI spec defines `operationId: GetMetrics` on `GET /metrics`. We intentionally **exclude** this operation from oapi-codegen via `output-options.exclude-operation-ids: [GetMetrics]` in `.oapi-codegen.yaml`. The `StrictServerInterface` in `gen/api/opentams.gen.go` does **not** list `GetMetrics`, and the `handlers.Handler` struct does **not** implement it.

Instead, `internal/httpx/metrics` exports a `Handler(gatherer)` constructor that returns an `http.Handler` wrapping `promhttp.HandlerFor(gatherer, opts)`. M14 mounts this on the Gin engine via `router.GET("/metrics", gin.WrapH(metrics.Handler(reg)))` after the generated `RegisterHandlers(...)` call.

Rationale: the strict wrapper invokes `responseObject.VisitGetMetricsResponse(w http.ResponseWriter) error` with no `*http.Request`. `promhttp.Handler` needs request headers (`Accept`, `Accept-Encoding`) for content negotiation and gzip. Routing `/metrics` through the strict interface requires either synthesizing a fake request (loses content negotiation) or re-implementing the Prometheus exposition format ourselves. Mounting promhttp directly at the router gives full content negotiation, matches every other Go project's wiring pattern, and removes a workaround. The OpenAPI spec remains accurate — we only bypass codegen, not the contract.

## BR-MET-02: Spec intact; only codegen excluded
`api/opentams-api-v1.yaml` (source spec) retains `operationId: GetMetrics` on `GET /metrics` with `tags: [Metrics]`. External API documentation, OpenAPI linters, and client generators still see the endpoint. The bundled spec (`api/opentams-api-bundled.yaml`) is regenerated from the source via `redocly bundle` and also retains the operation. Only the Go code generator skips it.

Rationale: the spec is the contract. Suppressing `GetMetrics` from the spec would be a contract change; suppressing it only from Go codegen is an implementation detail.

## BR-MET-03: Gatherer is injected, never the default global
`metrics.Handler` takes a `prometheus.Gatherer` as argument. Callers (M14) inject the same `*prometheus.Registry` that the rest of the service uses for metric registration (`pkg/metrics`). The metrics package does not touch `prometheus.DefaultGatherer`.

Rationale: per `pkg/metrics` NFR decisions, the service avoids the global default registry so tests are isolated and multi-process scenarios (unlikely but possible) stay clean.

## BR-MET-04: promhttp options are opinionated and centralized here
The `metrics.Handler` constructor sets `promhttp.HandlerOpts` once:
- `ErrorHandling: promhttp.ContinueOnError` — if one metric fails to encode, serve the rest. Alternative (`HTTPErrorOnError`) would hide all metrics from a single bad collector, which is worse for operators.
- `Timeout: 10 * time.Second` — upper bound on exposition. Protects against pathological collectors.
- `EnableOpenMetrics: true` — respond in OpenMetrics format when the client asks for it (`Accept: application/openmetrics-text`).
- `Registry: nil` — `/metrics` handler failures are not themselves exposed as metrics. Recursion hazard; not worth the value.

Callers do not pass options. If an environment needs different options, the tuning happens here, in one place.

Rationale: opinionated defaults keep M14 wiring trivial (`router.GET("/metrics", gin.WrapH(metrics.Handler(reg)))`) and put operational knobs in one file.

## BR-MET-05: No authentication enforced
The `metrics.Handler` performs no auth. Deployment topology (Kubernetes NetworkPolicy, private VPC, scrape-only sidecar) restricts who can reach `/metrics`. Matches Prometheus industry practice and the OpenAPI spec's `security: []` on the operation.

Rationale: Q6 spar. If a future deployment needs authenticated `/metrics`, that's an M14 route-wiring change (wrap with an auth middleware), not a change to this package.

## BR-MET-06: Package surface is minimal and stable
Public API:
```go
func Handler(gatherer prometheus.Gatherer) http.Handler
```
Nothing else is exported. No Options struct, no configuration injection, no logger. The function is deterministic and has no side effects.

Rationale: this is a 1-function package. Keeping it minimal signals "you only need to call this once at startup and wire the result to the router."

## BR-MET-07: Testing is against the public http.Handler
The unit test for this package drives `metrics.Handler(reg)` with a `httptest.NewRecorder` and verifies:
- `Content-Type` is Prometheus exposition format (or OpenMetrics when `Accept` requests it).
- A known registered metric appears in the body.
- No auth header is required.

Rationale: testing the thin wrapper directly covers both the promhttp config and the gatherer wiring in one integration-style unit test.

---

## Design Decisions (inline rationale — per PDG-v2)

| Decision | Choice | Alternatives considered | Why |
|---|---|---|---|
| How `/metrics` is mounted | Directly on the Gin router via `gin.WrapH` in M14, bypassing the strict server interface | (a) Implement `GetMetrics` on `handlers.Handler` with a custom response type that synthesizes a minimal `*http.Request` inside `Visit`; (b) Re-implement Prometheus exposition format manually | User directive: "don't force /metrics through strict response objects; mount promhttp directly at the router level instead." Option (a) works but loses content negotiation and adds code we'd throw away the moment oapi-codegen adds a non-strict escape for streaming handlers. Option (b) is re-inventing a wheel. |
| OpenAPI spec handling | Keep `GetMetrics` in the spec; exclude only from codegen via `exclude-operation-ids` | Remove `GetMetrics` from the spec entirely | D2 decision. Spec is contract; removing the operation would lie to clients about what the server exposes. |
| Spec tag for `/metrics` | New `Metrics` tag (split from `Health`) | Keep `Health` tag for all four operations | Q1=b. Separates the "process health" endpoints (liveness/readiness/details) from the "telemetry exposition" endpoint. Makes the intent of each group clearer in generated API docs. |
| Package size | Keep `internal/httpx/metrics` as a thin wrapper package (~15 LOC) | Inline the 2-line wiring directly in M14 | D1=a. Gives us a unit test target in M13 without depending on M14; centralizes promhttp option tuning in one place; keeps M14 wiring trivial. |
| promhttp options | `ContinueOnError`, 10s timeout, OpenMetrics enabled, no recursive registry | promhttp defaults | Continuing on error is the operator-friendly choice (single broken collector doesn't hide all metrics). 10s timeout bounds pathological collectors. OpenMetrics support costs nothing and is expected by modern scrapers. |
| Auth on `/metrics` | None | Env-flag-controlled auth | Q6. Spec mandates no auth. Adding a flag "just in case" adds API surface without a clear user; easier to add later if needed. |
