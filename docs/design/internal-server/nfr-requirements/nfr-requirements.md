# NFR Requirements — internal/server

Covers non-functional concerns not already stated in [`business-rules.md`](../functional-design/business-rules.md) or `business-logic-model.md`. Behavioural rules (route groups, middleware chain, dep validation, lifecycle contract, TLS opt-in, timeouts) are normative there and are NOT restated here.

## Performance

- **NFR-SRV-P1**: `server.New(deps)` is O(1) in route count. All per-route setup is delegated to `api.RegisterHandlersWithOptions`, which builds Gin's radix tree in a single pass; `server` itself only adds three static routes to the public group.
- **NFR-SRV-P2**: Per-request overhead of the middleware chain is dominated by the histogram observation in `httpmetrics` (sub-microsecond on warm CPU). `requestid` and `httprecovery` are allocation-free on the happy path; `httplog` writes one zap log line. No additional allocation introduced by `server` — it is wiring, not compute.
- **NFR-SRV-P3**: `Shutdown` is bounded by the caller's context. `server` does not introduce its own deadline on top; the only time budget is what the caller passes in. Rationale: avoids surprise double-bounding when a caller already computed `cfg.ServerGracefulShutdownPeriod`.

## Reliability

- **NFR-SRV-R1**: `New(deps)` is the single point where misconfiguration fails loudly. No `panic` during construction (even TLS validation errors are returned, not panicked). Rationale: the caller is `cmd/opentams`, which logs and exits cleanly on `New` error — a panic would lose the error detail in the stack trace noise.
- **NFR-SRV-R2**: `Start` does not leak goroutines on shutdown. The ctx-done watcher goroutine exits when either (a) ctx is cancelled and `s.srv.Close()` returns, or (b) `Serve` returns first and the watcher blocks on ctx.Done forever — the process is exiting anyway, so this is acceptable. If a future `Restart` primitive is added, a leak-free variant will be needed.
- **NFR-SRV-R3**: `http.ErrServerClosed` is not logged as a failure. `Start`'s caller sees `nil` after a clean shutdown, never `http.ErrServerClosed`. Rationale: rotating "server closed" lines through operational logs at every redeploy is noise.
- **NFR-SRV-R4**: Partial TLS misconfiguration is caught at construction, not at first TLS handshake. Exactly-one of `ServerTLSCertFile` / `ServerTLSKeyFile` → `ErrInvalidTLSConfig` from `New`. A running-but-plaintext-because-one-cert-was-missing server is a security incident waiting to happen.

## Observability

- **NFR-SRV-O1**: `server` does not emit its own Prometheus metrics. HTTP-level latencies and status codes come from `pkg/httpmetrics` via the middleware chain. Adding `server`-level metrics (e.g. "server_starts_total") would report once per process and carry no diagnostic signal.
- **NFR-SRV-O2**: `server` logs exactly two operational events at `INFO` level: "server starting" (with `addr`, `tls_enabled`) at the top of `Start`, and "server stopped" on exit. Health of the request stream is the responsibility of `pkg/httplog`. Rationale: low log volume keeps the boot sequence scannable.
- **NFR-SRV-O3**: `paramParseErrorHandler` MUST propagate `X-Request-ID` into the ProblemDetails body (`RequestID` field) so clients can correlate a malformed request with server logs. The request-ID is already set in context by `requestid.Middleware`, which runs first in the chain.

## Testability

- **NFR-SRV-T1**: All 16 test cases (TC-SRV-01..16) run against a real `gin.Engine` built by `server.New` — no mocking of Gin's routing. Stubs are limited to the `Deps` interface surface: hand-rolled `api.StrictServerInterface` impl for handler behaviour, hand-rolled `auth.Provider`, hand-rolled `health.Checker`, `prometheus.NewRegistry()` as the registry/gatherer. Rationale: `server` is wiring; testing the wiring against a fake router proves nothing.
- **NFR-SRV-T2**: No real TCP listener in unit tests (except TC-SRV-15). Every behavioural test drives the engine via `server.Handler()` + `httptest.NewRecorder` + synthetic `*http.Request`. Lifecycle tests (TC-SRV-14, TC-SRV-15) are the only ones that call `net.Listen` — and only on `:0` so the OS picks a free port.
- **NFR-SRV-T3**: The TLS branch of `Start` is exercised by injecting a recording fake for `srv.ServeTLS` via a package-private `serveTLSFn func(...) error` seam (default = `s.srv.ServeTLS`). The test swaps the seam, drives `Start`, asserts the cert/key path reached the fake. Rationale: a real TLS listener needs generated certs, flaky on CI; the injection seam is 4 lines and gives us the assertion we care about ("TLS branch selected when both cert and key are set").
- **NFR-SRV-T4**: Hand-rolled mocks use the function-field shape established in M8–M13 (e.g. `mockAuthProvider{ validate func(ctx, token) (*Claims, error) }`). No gomock, no testify/mock.
- **NFR-SRV-T5**: Test file sits in `internal/server/server_test.go` with `package server_test` (external test package). Rationale: it should exercise only the exported surface; internal-package helpers are a red flag that the test is coupling to implementation details.

## Security

- **NFR-SRV-S1**: The Auth middleware attaches ONLY to the authenticated route group, never to the root engine. A future addition of another public endpoint must go through `public.Group("")`, not `engine.Group("")`, to stay unauthenticated. The two groups are both rooted at `""` for symmetry; the auth middleware is the only distinguishing runtime property.
- **NFR-SRV-S2**: `paramParseErrorHandler` MUST NOT echo the raw request body or query string verbatim. It writes `err.Error()` (from the oapi-codegen parser, which describes the failed parameter without quoting the input) into `ProblemDetails.Detail` and `c.Request.RequestURI` into `Instance`. RequestURI is already a URL — no XSS surface.
- **NFR-SRV-S3**: `/health/details` is authenticated (BR-HLTH-12), mounted on the authenticated group per BR-SRV-03. A probe failure body that might contain sensitive strings (NFR-HLTH-S1 mitigates most of this) is only visible to authenticated operators. `server` carries no additional redaction logic.
- **NFR-SRV-S4**: The server does not set `Access-Control-Allow-*` or any CORS-permissive headers by default. MVP has no browser clients; adding permissive CORS later MUST be an explicit decision, not a drift.

## Maintainability

- **NFR-SRV-M1**: Adding a new route requires ONLY a spec change + `oapi-codegen` regen. `server` does not hold a route table, so there is no drift surface between the spec and the server wiring for authenticated endpoints.
- **NFR-SRV-M2**: Adding a new public (unauthenticated) endpoint requires a new line in the public group in `server.New`. These are explicit by design — public endpoints are a smaller set that needs careful review.
- **NFR-SRV-M3**: The `Deps` struct is the single boundary between M14 and M15 (`cmd/opentams`). Adding a new required dependency is a breaking change to `server.New`'s signature — intentional friction so callers update config/boot sequencing deliberately.
- **NFR-SRV-M4**: No interface is defined for `*Server` itself. `server.New` returns `*Server` (concrete), not an interface. Rationale: there is one `Server` implementation, and exposing an interface invites accidental second implementations that diverge in subtle lifecycle behaviour.

## Configuration

- **NFR-SRV-C1**: `server` reads config exclusively via `deps.Config`. It does not call `os.Getenv` directly. `cmd/opentams` owns env/flag parsing and passes a populated `*config.Config`.
- **NFR-SRV-C2**: HTTP timeouts (BR-SRV-10) are package-level constants, not config fields. Promoting them requires both a code change and a config migration — deliberately a two-step to resist premature tuning.
