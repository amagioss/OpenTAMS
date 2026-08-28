# Business Rules — internal/server

Scope: M14 router/lifecycle. Wires the generated OpenAPI handlers (M12), the `health.Checker` (M13a), and the `metrics.Handler` (M13b) into a running `*http.Server` with the M11 middleware chain applied per Q4-decided route groups.

## BR-SRV-01: Package location is `internal/server/`
Sibling of `internal/httpx/` (not under it). Rationale: `server` owns lifecycle (TLS, graceful shutdown, port binding) in addition to HTTP — it's an application-level concern, not a handler detail.

## BR-SRV-02: Pre-wired dependency injection via a `Deps` struct
`server.New(deps Deps) (*Server, error)` takes a fully-constructed dependency bundle. The caller (M15 `cmd/opentams`) builds `metastore.PostgresStore`, `objectstore.S3Store`, the three services, `handlers.Handler`, `health.Checker`, `auth.Provider`, and the `prometheus.Registry`, then hands them to `server.New`.

Rationale: keeps the `server` package pure-HTTP. Tests can stub `Deps` fields directly without spinning up real Postgres/S3. The `server` package does NOT import `metastore`, `objectstore`, or the service packages — it only depends on the handler + checker + auth provider interfaces.

## BR-SRV-03: Two Gin route groups split by auth requirement
One public group (no auth middleware) carries `/healthz`, `/readyz`, `/metrics`. One authenticated group (with `middleware.Auth(deps.AuthProvider)`) carries everything else, including the TAMS API surface **and** `/health/details` (the richer probe is spec'd to require auth — per M13a BR-HLTH-12).

Both groups share the full infrastructure chain: request-ID → panic recovery → access log → HTTP metrics → error handler. Only the auth step differs.

### Route-group composition
- **Public group handlers** are mounted directly (not via `RegisterHandlersWithOptions`):
  - `GET /healthz` → `health.HealthzHandler()` (M13a)
  - `GET /readyz`  → `health.ReadyzHandler(deps.Checker)` (M13a)
  - `GET /metrics` → `gin.WrapH(metrics.Handler(deps.Gatherer))` (M13b)
- **Authenticated group** is wired via `api.RegisterHandlersWithOptions(authGroup, deps.Handlers, GinServerOptions{ErrorHandler: paramParseErrorHandler})`. `GET /health/details` is part of this generated set (kept on the strict-server interface — authenticated, non-trivial body).

This split is only possible because `GetHealthz`, `GetReadyz`, and `GetMetrics` are excluded from oapi-codegen (`.oapi-codegen.yaml → output-options.exclude-operation-ids`). Otherwise `RegisterHandlersWithOptions` would register them on the authenticated group, colliding with the public mounts. The OpenAPI spec still documents all three — only the generated Go receiver methods are suppressed.

## BR-SRV-04: Middleware order is fixed
From outermost to innermost, in order of `router.Use`:
1. `requestid.Middleware()` — MUST be first so every downstream log/metric/error carries X-Request-ID
2. `httprecovery.Middleware(logger, middleware.PanicWriter())` — catches panics above the log/metric layer so crashes are surfaced as 500 ProblemDetails
3. `httplog.Middleware(logger)` — records the access log once panic-safe
4. `httpmetrics.New(reg.RawRegisterer(), httpmetrics.WithSkipPaths("/healthz", "/readyz", "/metrics"))` — labels histograms with recovered status codes (so 500-from-panic is counted correctly). Passes the **un-prefixed** registerer (pkg-metrics BR-MET-08) so HTTP histograms ride the canonical `http_request_duration_seconds` / `http_requests_total` names — same treatment as `go_*` / `process_*`. Health and scrape endpoints are skipped so liveness / scrape traffic does not dominate the histograms.
5. `middleware.ErrorHandler()` — final shaping of `c.Errors` → ProblemDetails
6. `middleware.Auth(provider)` — **authenticated group only** — runs after ErrorHandler is registered so auth failures flow through it

Rationale: each layer must only observe the request after layers it depends on have set up their state. Request-ID before log, recovery before log+metrics, error-handler before auth (so auth's 401 is also shaped into ProblemDetails).

## BR-SRV-05: Generated handlers wired via `RegisterHandlersWithOptions` with a custom `ErrorHandler`
`GinServerOptions.ErrorHandler` is set to a function that emits `apperror.ProblemDetails` for param-parse failures — the generated default writes `{"msg":"..."}` which violates RFC 9457 (per M12 FD). The server package owns this ErrorHandler closure; it does NOT live in `handlers/` because it has no access to `c.Errors`, just the raw `err`.

## BR-SRV-06: `/metrics` mounted outside the strict server interface
`/metrics` is excluded from oapi-codegen per M13b. The server package mounts it directly via `publicGroup.GET("/metrics", gin.WrapH(metrics.Handler(deps.Gatherer)))`. If a future regeneration accidentally re-registers `/metrics` under the generated routes, Gin panics on duplicate route at startup — loud failure mode is intentional.

## BR-SRV-07: `Server` type is the primary abstraction; inner handler is exposed for tests
Public surface:
```go
func New(deps Deps) (*Server, error)          // validates deps, builds router, wires http.Server
func (s *Server) Start(ctx context.Context) error    // blocks; returns when ctx done or ListenAndServe errors
func (s *Server) Shutdown(ctx context.Context) error // delegates to http.Server.Shutdown; bounded by caller's ctx
func (s *Server) Handler() http.Handler               // returns the Gin engine for in-process tests
func (s *Server) Addr() string                        // bound listener address (useful when Port=0 picks free port)
```
No free-standing `Run(...)` convenience function — `cmd/opentams` instantiates the `Server` and calls `Start`/`Shutdown` directly.

## BR-SRV-08: Graceful shutdown deadline from config
`Start(ctx)` returns when `ctx` is cancelled by the caller. After that, the caller calls `Shutdown(ctxWithTimeout)`, where the timeout is `cfg.ServerGracefulShutdownPeriod`. The server package does **not** handle OS signals itself — that is `cmd/opentams`'s job (M15). Rationale: signal semantics differ across deployments (signal-only vs orchestrator-managed), and isolating them in `main` keeps the library reusable.

## BR-SRV-09: TLS is opt-in via config
If BOTH `cfg.ServerTLSCertFile` AND `cfg.ServerTLSKeyFile` are non-empty, `Start` calls `http.Server.ListenAndServeTLS(certFile, keyFile)`. If neither is set, it calls `ListenAndServe`. If exactly one is set, `New` returns a validation error — this is a misconfiguration that MUST fail loudly at construction, not silently fall back to plaintext.

## BR-SRV-10: HTTP timeouts have package-level defaults
`http.Server.ReadHeaderTimeout = 10s`, `ReadTimeout = 30s`, `WriteTimeout = 60s`, `IdleTimeout = 120s`. These are not configurable in MVP — segment POST/GET bodies are small (JSON, not the media itself which lives in object store), and the defaults cover the realistic upper bounds with margin. If future load testing demands tuning, promote to config.

## BR-SRV-11: Required dependencies validated at construction
`New(deps)` returns an error when any of these are nil:
- `deps.Config`
- `deps.Logger`
- `deps.Handlers` (the generated `StrictServerInterface` impl)
- `deps.Checker`
- `deps.AuthProvider`
- `deps.Gatherer`
- `deps.HTTPMetricsRegisterer`

Sentinel errors (e.g. `ErrMissingHandlers`, `ErrMissingChecker`, ...) allow callers and tests to assert via `errors.Is`. This mirrors BR-HLTH-13's pattern — fail loudly at boot, never silently at the first request.

## BR-SRV-12: Start returns `http.ErrServerClosed` as a non-error
When `Shutdown` is called concurrently with `Start`, the embedded `http.Server` returns `http.ErrServerClosed`. The `Server.Start` wrapper MUST translate this into `nil` before returning. Any other error is a genuine failure (bind error, TLS cert problem) and propagates.

## BR-SRV-14: Spec-driven request validation via embedded OpenAPI document
The `internal/server` wiring MUST attach `ginmiddleware.OapiRequestValidatorWithOptions(swagger, ...)` to the **authenticated** route group, AFTER `middleware.Auth` and BEFORE the strict server's `RegisterHandlersWithOptions`. The OpenAPI document is loaded at boot via `api.GetSwagger()` (emitted by oapi-codegen with `embedded-spec: true` in `.oapi-codegen.yaml`) so request validation is fed by the same spec that drives codegen — the wire and the validator can never disagree.

What this enforces beyond strict-server's built-in checks:
- `required` body fields (strict-server enforces required headers/query params via `siw.ErrorHandler`, but does NOT enforce required JSON-body fields — those would otherwise unmarshal as zero values and reach handlers).
- `pattern` regex (e.g. `timerange` syntax) on body fields.
- `enum` membership.
- `minLength` / `maxLength` / `minimum` / `maximum`.
- Nested object validation across union branches.

Wiring constraints:
1. Validator attaches to the `authed` group only — `/healthz`, `/readyz`, `/metrics` are excluded from oapi-codegen and have no spec entries; running the validator on the engine root would 404 them with a spec-routing error.
2. `swagger.Servers = nil` before passing to the middleware — defence against a future `servers:` block addition to the spec; kin-openapi's host-matching would otherwise reject otherwise-valid requests behind reverse proxies based on the `Host` header. The current spec has no `servers:` block so this is a no-op today; kept because the line is zero-cost and the failure mode it prevents (operator adds `servers:` for SDK generation, traffic mysteriously starts 400ing) would be opaque without it.
3. `Options.AuthenticationFunc = openapi3filter.NoopAuthenticationFunc` — required because the spec carries `security: [bearer_auth: []]` at the top level (inherited from BBC TAMS v8.0). Auth is enforced by `middleware.Auth` higher in the chain; without the noop, kin-openapi would reject every authenticated request with `ErrAuthenticationServiceMissing`. Not optional given the spec carries security.
4. `Options.SilenceServersWarning = true` — the empty `Servers` slice is intentional (see #2); the warning would otherwise print at boot on every restart.
5. Validation errors are translated to RFC 9457 ProblemDetails by `validatorErrorHandler` (in `internal/server/validator.go`), which delegates to `paramParseErrorHandler` so the response shape is byte-equivalent to the strict-server param-parse error path. Clients see one error schema across rejection layers; the validator handler also performs a `*routers.RouteError` type-switch to upgrade the status to 404 (defence-in-depth — structurally unreachable in the current group-middleware wiring, but fires correctly if the validator is ever moved to engine root or attached via gin's `NoRoute`).

Verified by TC-SRV-18 (validator catches missing required body field — counterfactually confirmed: status flips from 500 panic to 400 when the validator is disabled, proving it is the validator doing the rejection, not a downstream layer).

Future polish: the kin-openapi error message currently lands in `pd.Detail` as a verbose unstructured string (~3 KB for a missing-field rejection because kin-openapi dumps the full schema and every `oneOf` branch). Surfacing per-field errors as a typed `pd.Errors []FieldError` slice is a known follow-up, not yet scheduled. Out of scope for this rule.

## BR-SRV-13: HTTP request-duration histogram uses a TAMS-tuned bucket profile
The `internal/server` wiring MUST pass `httpmetrics.WithBuckets(httpMetricsBuckets)` to `httpmetrics.New`. `httpMetricsBuckets` is a 13-boundary `1ms, 5ms, 10ms, 25ms, 50ms, 100ms, 250ms, 500ms, 1s, 2.5s, 5s, 10s, 30s` profile (see `internal/server/server.go`).

Rationale — `prometheus.DefBuckets` is wrong for this service in three directions, listed in order of decreasing severity for the actual TAMS workload:

1. **Sync bulk-POST tail collapses into `+Inf`**. `POST /flow-segments` accepts up to 1000 segments per request (`maxItems: 1000` in `api/schemas/flow-segment-post-body.json`) and is fully synchronous — the connection holds open until every segment is registered (no 202/async path exists in TAMS v8.0; verified by `rg '"202"' api/opentams-api-bundled.yaml` returning no matches). Healthy bulk POST runs 200ms–5s; under DB row-lock contention, GIN-index degradation, or pgxpool starvation the tail extends into 10–20s territory. DefBuckets caps at 10s, so this entire legitimate-stress range becomes indistinguishable from a 55s-near-timeout request — `histogram_quantile` cannot estimate p99 / p99.9 in the region where operators most need to see degradation. This argument is workload-driven and stands independently of `WriteTimeout`.
2. **Fast-path coarseness erases cached-replay observability**. DefBuckets starts at 5ms. The idempotency cached-replay path returns in 1–3ms (single-row PK lookup + cached body bytes); segment GETs commonly finish in 1–5ms. Without the 1ms boundary every fast path collapses into bucket #1 and p50 / p99 in that region are uninterpretable.
3. **Silent-failure detection at the timeout boundary**. `WriteTimeout` (BR-SRV-10) is 60s and Go severs the connection mid-response without surfacing as a `5xx` in `http_requests_total` — silent connection drops are otherwise invisible. The `30s` boundary creates a meaningful "approaching the timeout" alert signal: `rate(http_request_duration_seconds_bucket{le="+Inf"}[5m]) - rate(http_request_duration_seconds_bucket{le="30"}[5m]) > 0` reads "we are silently dropping connections." DefBuckets has no signal at all in this region.

**Invariants to preserve under future change**:
- The ceiling (`30s` today) MUST sit below `WriteTimeout` (`60s` today). If `WriteTimeout` moves, this profile MUST be reviewed in the same change — the *relationship* between the ceiling and `WriteTimeout` is the invariant, not the specific 30s value.
- The floor (`1ms` today) MUST sit at-or-below the realistic best-case latency of the cached-replay path. If a future change pushes cached-replay below 1ms (e.g. an in-process LRU bypassing the DB lookup), add a sub-1ms boundary in the same change.
- Justification #1 is **conditional on `POST /flow-segments` remaining synchronous**. If a future change introduces async bulk processing (202 + Location, separate worker pool, status endpoint), the legitimate handler-side tail collapses to <1s and a tighter ceiling becomes appropriate; this profile MUST be reviewed in the same change.

Verified by TC-SRV-17 (regresses the wiring and the bucket count exactly).

## Out of scope for M14
- Rate limiting — `cfg.ServerRateLimitRPS`/`Burst` exist but no middleware implementation yet. Deferred to a future module; the chain in BR-SRV-04 leaves the slot open between `httpmetrics` and `ErrorHandler`.
- CORS — no cross-origin browser clients in MVP; add when needed.
- HTTP/2 / H2C — Go stdlib handles HTTP/2 over TLS automatically when present; explicit cleartext H2 is deferred.
- Trusted proxy configuration — Gin's default is permissive; revisit when behind a specific ingress.
- Profiling endpoints (`/debug/pprof`) — not in spec.
