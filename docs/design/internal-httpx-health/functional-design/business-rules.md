# Business Rules — internal/httpx/health

Scope: liveness, readiness, and detailed health status for the OpenTAMS server.
Covers three operations on the strict server interface: `GetHealthz`, `GetReadyz`, `GetHealthDetails`.
The fourth Health-tag endpoint, `/metrics`, is served by a separate package (`internal/httpx/metrics`)
and is intentionally excluded from the oapi-codegen strict interface — see that package's design doc.

---

## BR-HLTH-01: Liveness is process-liveness only
`GetHealthz` performs **no external dependency checks**. It returns `200` as long as the Go process is alive and the HTTP server can dispatch the request. Intended for Kubernetes liveness probes. A 404/503 from `/healthz` would cause K8s to kill the pod, so this endpoint must never depend on downstream systems.

Rationale: separation-of-concerns between liveness and readiness is standard K8s practice — mixing them causes cascading restarts when a dependency blips.

## BR-HLTH-02: Readiness gates on metastore + objectstore only
`GetReadyz` returns `200` when both the metastore (Postgres) and the objectstore (S3-compatible) respond successfully within the probe timeout. It returns `503` when either probe fails. JWKS reachability is **not** gated by readiness — JWKS failures surface per-request as `401` and are retry-able; DB/S3 failures are service-wide and warrant taking the pod out of rotation.

Rationale: Q3 spar locked "DB + ObjectStore" as the critical dependency set. The service cannot honor writes without either.

## BR-HLTH-03: Readiness response body is empty
`GetReadyz` writes an empty body on both `200` and `503`. Kubernetes readiness probes ignore the body. For per-component detail, callers use `/health/details`.

Rationale: matches spec description (`Response body is intentionally empty`).

## BR-HLTH-04: Details returns per-component status + overall worst-of
`GetHealthDetails` returns a JSON `HealthDetails` object with:
- `status`: overall health — the **worst** status across all components (`unhealthy` > `degraded` > `healthy`).
- `version`: the build-time version string (from `config.Version`; empty string if unset).
- `components`: a map keyed by component name (`metastore`, `objectstore`, `jwtauth`) with each value carrying `status` and optional `latency_ms`.

Rationale: matches the `HealthDetails` schema in `api/schemas/`. Worst-of aggregation is the spec's definition (`Overall service health — worst status across all components`).

## BR-HLTH-05: Health status enum is three-valued
Status values per component and overall: `healthy` | `degraded` | `unhealthy`.
- `healthy`: probe returned `nil` error within the timeout.
- `unhealthy`: probe returned non-nil error or timed out.
- `degraded`: **reserved** — not produced by the initial M13 implementation. Probes today return `error` (binary). `degraded` activates later when probes can signal partial functionality (e.g. DB reachable but slow, S3 reads working but writes failing). The three-valued enum ships now so the schema is stable when richer probes land.

## BR-HLTH-06: Probe contract is narrow and per-dependency
Each dependency exposes a purpose-built interface owned by the `health` package — not a generic `Probe` abstraction:
```go
type DBProbe        interface { Ping(ctx context.Context) error }
type ObjectStoreProbe interface { HealthCheck(ctx context.Context) error }
type JWKSProbe      interface { Check(ctx context.Context) error }
```
Rationale: Q2 spar locked narrow interfaces. Each dependency has its own idiom (Postgres `Ping`, S3 `HealthCheck`, JWKS refresh), and a shared `Check(ctx) error` would force artificial naming. The `health.Checker` aggregator maps each probe result to `ComponentStatus`.

## BR-HLTH-07: Probe timeouts differ by endpoint
- `GetReadyz`: per-probe timeout **500ms**. Probes run concurrently (errgroup). Any probe timeout counts as `unhealthy` → overall `503`.
- `GetHealthDetails`: per-probe timeout **2s**. Probes run concurrently. Per-component status reflects each probe's outcome; overall is worst-of.

Rationale: Q5 spar. Readyz is on the K8s critical path (probed every few seconds) so it must fail fast. Details is human-facing (dashboards) and can afford a longer budget.

## BR-HLTH-08: No caching
Each call to `GetReadyz` and `GetHealthDetails` runs fresh probes. No in-memory cache, no coalescing of concurrent calls.

Rationale: Q5. K8s probes at a fixed cadence; details is low-QPS. Caching adds staleness risk without meaningful QPS savings. If probe load becomes a problem, a single-flight pattern can be added without changing the HTTP contract.

## BR-HLTH-09: JWKS probe is optional
When auth is configured with `DevProvider` (env `APP_ENV=development`), no JWKS probe is registered. The `components` map in `GetHealthDetails` omits the `jwtauth` entry entirely. Worst-of aggregation still works (absent components do not contribute).

Rationale: dev mode has no real JWKS endpoint. Emitting a synthetic "healthy" entry would be misleading; emitting "unhealthy" would falsely fail overall status.

## BR-HLTH-10: Handler methods live in the M12 handlers package
The three handlers (`GetHealthz`, `GetReadyz`, `GetHealthDetails`) are implemented as methods on `handlers.Handler` in a new file `internal/httpx/handlers/health.go`. The `health.Checker` is wired into `handlers.Handler` via constructor dependency injection. This preserves the M12 convention: all `StrictServerInterface` methods live on `handlers.Handler`; business logic lives in a dedicated package.

Rationale: consistent with how sources → `service/source`, flows → `service/flow`, segments → `service/segment` are wired.

## BR-HLTH-11: Errors never leak ProblemDetails for probe failures
`GetReadyz` on failure returns `503` with an empty body, not a `ProblemDetails` object. `GetHealthDetails` always returns `200` with a body whose `status` field conveys health; it never returns a 4xx/5xx for probe failures. Only `401` (missing/invalid auth) and `429` (rate limit) can surface as errors on `GetHealthDetails`, and those originate in middleware, not the handler.

Rationale: the spec explicitly models unhealthy as part of the response body, not an error code. Mixing probe failures into `ProblemDetails` would require callers to parse two shapes for the same signal.

## BR-HLTH-13: Required probes validated at construction; nil returns error
`New(opts)` returns `(Checker, error)`. If `opts.DB == nil` → returns `ErrMissingDBProbe`. If `opts.Object == nil` → returns `ErrMissingObjectStoreProbe`. `opts.JWKS == nil` is **not** an error (JWKS is optional per BR-HLTH-09). The caller (M14 server boot) is responsible for treating a non-nil error as a fatal misconfiguration (`log.Fatal`) and aborting process startup.

Rationale: nil DB or Object probe in production would cause `/readyz` to report healthy with no actual dependency gating — a silent correctness bug that only surfaces during an outage. An explicit error at construction makes the misconfig loud at boot. Panic was rejected because errors are more composable (the caller can log structured context, emit a metric, etc.) and match the rest of the codebase's preference for error returns over panic.

## BR-HLTH-12: No auth on /healthz and /readyz; auth required on /health/details
Enforced at the OpenAPI spec level (`security: []` on the unauthenticated endpoints; default security on `/health/details`). The handler package does not re-check auth. Route wiring in M14 is responsible for ensuring the auth middleware is not applied to `/healthz` and `/readyz`.

Rationale: spec-level security assertions are the source of truth; handler-level re-checks would drift.

---

## Design Decisions (inline rationale — per PDG-v2)

| Decision | Choice | Alternatives considered | Why |
|---|---|---|---|
| Probe interface shape | Narrow per-dependency interfaces | Single generic `Probe { Check(ctx) (ComponentStatus, error) }` | Q2=b spar. Each dependency has its own natural method name; a single shared interface forces artificial consistency. |
| Readyz critical set | DB + ObjectStore | DB only; DB + ObjectStore + JWKS | Q3=b spar. JWKS failures are per-request and retry-able; DB/S3 failures are service-wide. |
| Probe aggregation strategy | Concurrent via `errgroup`, worst-of overall status | Sequential probes | Readyz 500ms budget is too tight to run three sequential probes. Concurrent also reflects real dependency topology. |
| Caching | None | Short TTL cache (1s) on probe results | Q5 spar. Low QPS makes caching not worth the staleness risk. |
| `degraded` status production | Reserved, not produced | Produce `degraded` on slow-but-working probes (latency > threshold) | Defer. Needs threshold calibration; binary error/no-error is sufficient for K8s readiness. Revisit when richer probes land. |
| Metrics handler location | Separate `internal/httpx/metrics` package, excluded from oapi-codegen | Implement `GetMetrics` via strict response object that wraps promhttp | promhttp needs `*http.Request` for Accept negotiation; strict response objects only get `http.ResponseWriter` at Visit time. Mounting promhttp directly at the router in M14 is idiomatic Go and removes a workaround. See [`internal/httpx/metrics` business rules](../../internal-httpx-metrics/functional-design/business-rules.md). |
| Version source | `config.Version` field (ldflags-injected at build time) | Hard-coded constant; git-describe runtime shell | ldflags is the standard Go build-time injection. Empty string default is safe — the spec does not require a populated version. |
