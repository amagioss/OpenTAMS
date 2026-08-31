# NFR Requirements — M11 Middleware

## Performance

- **NFR-MW-P1**: All middleware runs on the hot request path. No heap allocations beyond what Gin already makes. Zap child logger creation and UUID generation are O(1).
- **NFR-MW-P2**: Prometheus histogram observation is a single mutex-locked float64 write — acceptable on the request path.
- **NFR-MW-P3**: Recovery deferred function costs nothing on the happy path (no panic).

## Reliability

- **NFR-MW-R1**: Recovery runs outermost in the chain — must catch all panics including those in other middleware.
- **NFR-MW-R2**: Auth failure always calls `c.Abort()` — downstream handlers must not execute.
- **NFR-MW-R3**: ErrorHandler is idempotent — if `c.Writer.Written()` is true, do nothing.
- **NFR-MW-R4**: `httplog.Logger(c)` never returns nil — falls back to root logger if middleware not in chain.

## Security

- **NFR-MW-S1**: Auth rejects absent, malformed, and invalid tokens with 401. No token content in response body.
- **NFR-MW-S2**: ErrorHandler omits `detail` for non-AppError responses — no stack traces or internal messages leak to clients.
- **NFR-MW-S3**: Panic recovery logs full stack trace server-side (ERROR); writes only generic 500 ProblemDetails to client.
- **NFR-MW-S4**: Trusted X-Request-ID forwarding is intentional per BR-RID-GIN-01 — no validation applied.

## Testability

- **NFR-MW-T1**: 100% statement coverage for all seven packages.
- **NFR-MW-T2**: Tests use `gin.New()` + `httptest.NewRecorder()` — no real server needed.
- **NFR-MW-T3**: Auth tests use hand-rolled `mockProvider` with function field (consistent with M8–M10 pattern).
- **NFR-MW-T4**: Each package tested in isolation — no cross-package test dependencies within M11.

## Maintainability

- **NFR-MW-M1**: `pkg/` packages must not import any `internal/` package — required for reuse across Amagi services.
- **NFR-MW-M2**: Context key types are unexported per package — no cross-package key collisions.
- **NFR-MW-M3**: RFC 9457 ProblemDetails format stays in `internal/httpx/middleware` (PanicWriter) — not in `pkg/`.
