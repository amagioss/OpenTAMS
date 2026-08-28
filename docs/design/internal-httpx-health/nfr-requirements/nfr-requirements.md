# NFR Requirements — internal/httpx/health

Covers non-functional concerns not already addressed in [`business-rules.md`](../functional-design/business-rules.md) or [`business-logic-model.md`](../functional-design/business-logic-model.md). Behavioural rules (probe interfaces, readyz gating, timeouts, status enum) are normative there and are not restated here.

## Performance

- **NFR-HLTH-P1**: `GetHealthz` allocates no heap memory per call. The response object is an empty struct literal.
- **NFR-HLTH-P2**: `Ready` and `Details` upper-bound latency is `opts.ReadyTimeout` / `opts.DetailsTimeout` plus errgroup overhead (~tens of microseconds). A slow probe cannot extend the budget because each probe runs in its own derived context.
- **NFR-HLTH-P3**: Probes run in parallel, not serial. For N probes with per-probe timeout T, worst-case wall time is T, not N×T.

## Reliability

- **NFR-HLTH-R1**: A panicking probe must not take down the HTTP request. The `health` package recovers panics inside each goroutine spawned by its errgroup and converts them to `unhealthy` for that component. The outer middleware panic recovery (`pkg/httprecovery`) is the secondary safety net, not the primary.
- **NFR-HLTH-R2**: Probe goroutines inherit the request context. When the client disconnects, the parent context cancels and all in-flight probes abort promptly (bounded by their per-probe timeout).
- **NFR-HLTH-R3**: `Checker` methods are safe for concurrent invocation. Construction via `New(opts)` produces an immutable value — no shared mutable state across calls.

## Observability

- **NFR-HLTH-O1**: The `health` package does not accept a `*zap.Logger` and does not emit log lines. Probe outcomes are surfaced in HTTP responses; request-level log lines come from `pkg/httplog` at the middleware layer. Rationale: adding an internal logger duplicates information already captured on the request log and forces every caller to plumb a logger argument.
- **NFR-HLTH-O2**: The `health` package does not emit Prometheus metrics. Probe-failure signals already flow through `pkg/httpmetrics` as the 503/200 status split on `/readyz` and the response body on `/health/details`. Rationale: metrics on `/metrics` about `/metrics`-adjacent health produce recursive noise and little operational value.

## Testability

- **NFR-HLTH-T1**: 100% statement coverage on the `health` package. Handler methods in `internal/httpx/handlers/health.go` additionally covered by direct `StrictServerInterface` calls, consistent with M12 conventions — no Gin engine in handler-level tests.
- **NFR-HLTH-T2**: Probe interfaces are satisfied by hand-rolled fakes with function-field shape, consistent with M8–M12: `type fakeDBProbe struct{ ping func(context.Context) error }`. No gomock, no testify/mock.
- **NFR-HLTH-T3**: Timeout semantics are tested by a fake probe that blocks on `<-ctx.Done()` and returns `ctx.Err()`. The test asserts that `Ready(ctx)` returns `false` within `opts.ReadyTimeout + tolerance` (tolerance = 100ms).
- **NFR-HLTH-T4**: Concurrency is tested by a probe that records its start timestamp; two such probes started from a single `Ready` call must have overlapping start-time windows (proves parallel, not serial, execution).
- **NFR-HLTH-T5**: Panic-in-probe recovery (NFR-HLTH-R1) is directly tested with a fake that calls `panic("boom")`.

## Security

- **NFR-HLTH-S1**: `Details` response body does not include probe error messages. Component `status` is the only public signal. Rationale: probe errors may contain connection strings, hostnames, or authentication artifacts — all inappropriate for an authenticated-but-broadly-readable endpoint.
- **NFR-HLTH-S2**: `/health/details` auth enforcement is the responsibility of M14 route wiring + M11 Auth middleware. This package assumes the request reached it only if auth passed.

## Maintainability

- **NFR-HLTH-M1**: Adding a new probe type (e.g., a future cache layer) requires: (a) a new narrow interface in `health/`, (b) a new `Options` field, (c) a new map entry in `Details`. No changes to existing probe types.
- **NFR-HLTH-M2**: Probe interfaces live in the `health` package, not in the dependency packages. Rationale: dependency-injection-friendly — `metastore`, `objectstore`, `jwtauth` remain unaware of the probe consumer. Classic inversion of dependencies.
