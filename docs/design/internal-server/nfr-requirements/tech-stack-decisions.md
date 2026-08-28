# Tech Stack Decisions — internal/server

## New dependencies

**None.** Everything `server` needs is already in `go.mod` from prior modules:

| Already-in-tree package | Used for |
|---|---|
| `github.com/gin-gonic/gin` | Route groups, engine, Handler interface (from M11/M12) |
| `github.com/prometheus/client_golang/prometheus` | `Gatherer`/`Registerer` types on `Deps` (from P2) |
| `github.com/prometheus/client_golang/prometheus/promhttp` | indirectly via `metrics.Handler` (M13b) |
| `go.uber.org/zap` | `Deps.Logger` (from P1) |
| `gen/api` | `StrictServerInterface`, `RegisterHandlersWithOptions`, `GinServerOptions` (from M12) |
| `internal/apperror` | `ProblemDetails` type for `paramParseErrorHandler` (from M2) |
| `internal/auth` | `Provider` interface on `Deps.AuthProvider` (from M6b) |
| `internal/config` | `*config.Config` on `Deps.Config` (from M3) |
| `internal/httpx/handlers` | indirectly — `Deps.Handlers` is an interface it satisfies (from M12) |
| `internal/httpx/health` | `Checker`, `HealthzHandler`, `ReadyzHandler` (from M13a) |
| `internal/httpx/metrics` | `Handler(gatherer)` (from M13b) |
| `internal/httpx/middleware` | `Auth`, `ErrorHandler`, `PanicWriter` (from M11) |
| `pkg/httplog` | `Middleware` (from M11) |
| `pkg/httpmetrics` | `New` (from M11) |
| `pkg/httprecovery/ginadapter` | `Middleware` (from M11) |
| `pkg/requestid` | `Middleware` (from M11) |
| `pkg/requestid/ginadapter` | `FromContext` for `paramParseErrorHandler` (from M11) |

The entire value of this module is the composition of existing building blocks. Adding a new direct dependency should be resisted — if something is missing, the right answer is almost always "add it to `pkg/httpx/*` or `internal/httpx/middleware`."

## Test stack

| Decision | Choice | Rationale |
|---|---|---|
| External test package | `package server_test` (not `package server`) | Forces tests through the exported surface. NFR-SRV-T5. |
| Fake `api.StrictServerInterface` | Hand-rolled struct with function fields per operation tested (NOT all 69 methods) | Embedding `api.UnimplementedStrictServerInterface` would force us to track every generated method; the tests only need GET /flows, GET /flows/{id}, GET /health/details, and a panicking handler. We define a minimal `fakeStrictServer` that implements ONLY the methods invoked and panics on others — which is the correct behaviour for unused methods anyway. |
| Fake `auth.Provider` | `type fakeAuth struct{ validate func(ctx, token) (*auth.Claims, error) }` | Consistent with M8/M9/M10/M12 pattern. |
| Fake `health.Checker` | Reuse `mockChecker` from `internal/httpx/handlers` if exportable; otherwise duplicate the tiny struct in the server test file | Acceptable duplication — the `mockChecker` shape is 3 fields. |
| Prometheus registry | `prometheus.NewRegistry()` per test function (NOT `DefaultRegisterer`) | Avoids cross-test state bleed; registers `opentams_http_*` histograms fresh each time. |
| TLS branch | `serveTLSFn` package-private seam replaced in test | NFR-SRV-T3 — explained there. |
| Lifecycle tests | `net.Listen("tcp", ":0")` for bind-error test; `httptest.NewServer(srv.Handler())` for request-path tests | `:0` lets the OS pick a port; `httptest.NewServer` avoids explicit `net.Listen` in most cases. |
| Race detector | `go test -race -cover ./internal/server/...` in default CI | REQ-TEST-02 consistent with the rest of the repo. |

## Wrapper/adapter choices

| Decision | Choice | Rationale |
|---|---|---|
| `/metrics` mount | `gin.WrapH(metrics.Handler(deps.Gatherer))` | `metrics.Handler` returns `http.Handler`; Gin's `WrapH` is the canonical adapter. `WrapF` is wrong (takes `http.HandlerFunc`). |
| `/healthz`, `/readyz` mount | Direct assignment of `gin.HandlerFunc` from `health` package | `HealthzHandler()` and `ReadyzHandler(c)` already return `gin.HandlerFunc` — no `WrapH` needed (M13a routing refinement). |
| `paramParseErrorHandler` signature | `func(*gin.Context, error, int)` matches `api.ErrorHandler` from oapi-codegen | The strict server's `GinServerOptions.ErrorHandler` field expects this exact shape. No adapter. |

## Error wrapping

- Errors from `net.Listen` in `Start` are wrapped with `"server: listen: %w"` so the operator sees both the `server` layer and the underlying syscall error.
- Errors from `httpmetrics.New` in `New` are wrapped with `"server: build http metrics: %w"`.
- Sentinel errors from `New` (`ErrMissing*`, `ErrInvalidTLSConfig`) are NOT wrapped — they ARE the message, consistent with M13a's `ErrMissingDBProbe` pattern. Callers assert via `errors.Is`.

## Gin mode

- `gin.SetMode(gin.ReleaseMode)` when `cfg.AppEnv == "production"`.
- `gin.SetMode(gin.TestMode)` when called from tests (tests explicitly set this before building the server).
- Otherwise `gin.DebugMode` — developer ergonomics in local runs.

Gin mode is package-global state in Gin itself; `server.New` sets it once per process. If a future multi-tenant scenario requires different modes per engine, we'd need to fork Gin — out of scope.

## File-level TDD ordering

1. `errors.go` — sentinel declarations (no test needed; covered transitively by TC-SRV-01/02/03).
2. `problem.go` — `paramParseErrorHandler`. Tested via TC-SRV-10 through the full engine.
3. `server.go` — `New` + `Start` + `Shutdown` + `Handler` + `Addr`.
4. `server_test.go` — all 16 test cases, RED first (compile-fail on missing types), then GREEN method-by-method.
