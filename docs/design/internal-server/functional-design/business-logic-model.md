# Business Logic Model — internal/server

## Package surface

```go
package server

import (
    "context"
    "errors"
    "net"
    "net/http"
    "time"

    "github.com/amagioss/opentams/gen/api"
    "github.com/amagioss/opentams/internal/auth"
    "github.com/amagioss/opentams/internal/config"
    "github.com/amagioss/opentams/internal/httpx/health"
    "github.com/gin-gonic/gin"
    "github.com/prometheus/client_golang/prometheus"
    "go.uber.org/zap"
)

// Deps is the fully-constructed dependency bundle passed to New. Every
// pointer/interface field is REQUIRED unless explicitly marked optional.
type Deps struct {
    Config                *config.Config
    Logger                *zap.Logger
    Handlers              api.StrictServerInterface // *handlers.Handler in practice
    Checker               health.Checker
    AuthProvider          auth.Provider
    Gatherer              prometheus.Gatherer
    HTTPMetricsRegisterer prometheus.Registerer
}

// Server is the HTTP application. One Server per process.
type Server struct {
    cfg    *config.Config
    log    *zap.Logger
    engine *gin.Engine
    srv    *http.Server
    lis    net.Listener // captured in Start so Addr() reports the real bound address
}

// Sentinel errors returned by New on misconfiguration. Callers should treat
// any non-nil error from New as fatal (log.Fatal).
var (
    ErrMissingConfig       = errors.New("server: Config is required")
    ErrMissingLogger       = errors.New("server: Logger is required")
    ErrMissingHandlers     = errors.New("server: Handlers is required")
    ErrMissingChecker      = errors.New("server: Checker is required")
    ErrMissingAuth         = errors.New("server: AuthProvider is required")
    ErrMissingGatherer     = errors.New("server: Gatherer is required")
    ErrMissingRegisterer   = errors.New("server: HTTPMetricsRegisterer is required")
    ErrInvalidTLSConfig    = errors.New("server: both ServerTLSCertFile and ServerTLSKeyFile must be set together, or neither")
)

func New(deps Deps) (*Server, error)
func (s *Server) Start(ctx context.Context) error
func (s *Server) Shutdown(ctx context.Context) error
func (s *Server) Handler() http.Handler
func (s *Server) Addr() string
```

---

## New(deps) — construction

```
1. Validate deps (return sentinel on first nil field, per BR-SRV-11).
2. Validate TLS config: (cert != "") == (key != "") → otherwise ErrInvalidTLSConfig.
3. Apply Gin release mode iff cfg.AppEnv == "production"; test/dev use TestMode/DebugMode.
4. engine := gin.New()   // NOT gin.Default — we wire middleware explicitly.
5. Build the HTTP metrics middleware via
     httpmetrics.New(deps.HTTPMetricsRegisterer,
                     httpmetrics.WithSkipPaths("/healthz", "/readyz", "/metrics"),
                     httpmetrics.WithBuckets(httpMetricsBuckets))
   deps.HTTPMetricsRegisterer is the **un-prefixed** registerer
   (`pkg/metrics.Registry.RawRegisterer()` — see pkg-metrics BR-MET-08).
   `httpMetricsBuckets` is a 13-boundary 1ms..30s profile (TAMS-tuned —
   see BR-SRV-13). Propagate any error as "server: build http metrics: %w".
6. Apply the shared chain to the engine root (BR-SRV-04 steps 1–5):
     engine.Use(
       requestid.Middleware(),
       httprecoveryginadapter.Middleware(logger, middleware.PanicWriter()),
       httplog.Middleware(logger),
       httpMetricsMiddleware,
       middleware.ErrorHandler(),
     )
7. Build two route groups (BR-SRV-03):
     public := engine.Group("")                                           // no auth
     authed := engine.Group("")                                           //
     authed.Use(middleware.Auth(deps.AuthProvider))                       // auth on authed only
8. Mount public endpoints on `public` (BR-SRV-06):
     public.GET("/healthz",  health.HealthzHandler())                     // M13a — pure 200
     public.GET("/readyz",   health.ReadyzHandler(deps.Checker))          // M13a — 200/503
     public.GET("/metrics",  gin.WrapH(metrics.Handler(deps.Gatherer)))   // M13b — promhttp
8.5. Wire spec-driven request validation on `authed` (BR-SRV-14):
     swagger, _ := api.GetSwagger()
     swagger.Servers = nil                                                  // proxy-friendly
     authed.Use(ginmiddleware.OapiRequestValidatorWithOptions(swagger, &ginmiddleware.Options{
         ErrorHandler: validatorErrorHandler,                               // RFC 9457 PD
         Options: openapi3filter.Options{
             AuthenticationFunc: openapi3filter.NoopAuthenticationFunc,     // auth handled above
         },
         SilenceServersWarning: true,
     }))
9. Mount the authenticated TAMS API surface — including /health/details —
   on `authed` via the generated helper:
     api.RegisterHandlersWithOptions(authed, deps.Handlers, api.GinServerOptions{
         ErrorHandler: paramParseErrorHandler,   // RFC 9457 ProblemDetails
     })
   Safe: /healthz, /readyz, and /metrics are excluded from codegen via
   .oapi-codegen.yaml → output-options.exclude-operation-ids, so
   RegisterHandlersWithOptions will NOT try to re-register them. No collision.
10. Build *http.Server with engine as Handler, timeouts per BR-SRV-10.
11. Return &Server{engine: engine, srv: srv, cfg: deps.Config, log: deps.Logger}, nil.
```

### Why no route table / no manual dispatch

Because `GetHealthz`, `GetReadyz`, and `GetMetrics` are excluded from oapi-codegen, the generated `StrictServerInterface` and its `RegisterHandlersWithOptions` **do not know about these operations**. Consequently:

- Calling `RegisterHandlersWithOptions(authed, ...)` registers only the authenticated routes the server cares about — no collision with the public mounts in step 8.
- The server package does NOT need to hand-roll a 70-row route table or copy routes between engines. All generation-driven ergonomics (oapi-codegen regen adds a route → it shows up automatically under `authed`) are preserved.
- If a future developer reverses the exclusion without knowing, Gin will panic at startup with "handlers already registered for path" — loud failure, exactly what we want.

The OpenAPI spec continues to describe all three public endpoints; only their Go receiver methods are suppressed at codegen time. The implementations live in `internal/httpx/health/endpoints.go` and `internal/httpx/metrics/metrics.go` respectively.

---

## Start(ctx) — lifecycle

```
1. addr := fmt.Sprintf(":%d", s.cfg.ServerPort)
2. lis, err := net.Listen("tcp", addr)
   if err: return fmt.Errorf("server: listen: %w", err)
3. s.lis = lis
4. Register context-done watcher:
     go func() {
         <-ctx.Done()
         // DO NOT call Shutdown here — caller owns shutdown semantics.
         // Just close the listener to unblock Serve.
         _ = s.srv.Close()  // abrupt; caller should call Shutdown for graceful.
     }()
5. Serve:
   if tls configured:
     err = s.srv.ServeTLS(lis, cert, key)
   else:
     err = s.srv.Serve(lis)
6. if errors.Is(err, http.ErrServerClosed): return nil
   return err
```

Note: the Close() in the ctx-done watcher is a safety net — callers that rely on Start's context plumbing without explicit Shutdown still get an unblock. The correct pattern is documented:

```go
ctx, cancel := signal.NotifyContext(...)
defer cancel()
go func() {
    <-ctx.Done()
    shutdownCtx, c := context.WithTimeout(context.Background(), cfg.ServerGracefulShutdownPeriod)
    defer c()
    _ = srv.Shutdown(shutdownCtx)
}()
_ = srv.Start(ctx)
```

---

## Shutdown(ctx) — graceful drain

```
return s.srv.Shutdown(ctx)
```

Delegates wholesale to `http.Server.Shutdown`. The caller's `ctx` carries the graceful deadline. `http.Server.Shutdown` returns when all in-flight connections complete OR the context deadline fires.

---

## Handler() / Addr()

```
Handler() http.Handler   { return s.engine }
Addr() string             { if s.lis != nil { return s.lis.Addr().String() }; return fmt.Sprintf(":%d", s.cfg.ServerPort) }
```

`Handler()` is used by in-process integration tests (`httptest.NewServer(srv.Handler())`) without binding a port. `Addr()` is useful when `ServerPort == 0` (test mode) to discover the OS-assigned port.

---

## paramParseErrorHandler (for siw.ErrorHandler)

```go
func paramParseErrorHandler(c *gin.Context, err error, status int) {
    pd := apperror.ProblemDetails{
        Type:      "about:blank",
        Title:     http.StatusText(status),
        Status:    status,
        Detail:    err.Error(),
        Instance:  c.Request.RequestURI,
        RequestID: requestidginadapter.FromContext(c),
    }
    c.Header("Content-Type", "application/problem+json")
    c.JSON(status, pd)
    c.Abort()
}
```

Mirrors `middleware.ErrorHandler` output shape so clients see one consistent error schema whether the failure came from param parsing or from a handler error return.

---

## Test plan (unit — `server_test.go`)

| # | Test | Setup | Assertion |
|---|---|---|---|
| TC-SRV-01 | `New returns ErrMissingHandlers when Handlers is nil` | Deps with all fields set except Handlers | `errors.Is(err, ErrMissingHandlers)` |
| TC-SRV-02 | `New returns ErrMissing* for each required field` | Loop over 7 required fields, nil one at a time | Each returns the matching sentinel |
| TC-SRV-03 | `New returns ErrInvalidTLSConfig when exactly one of cert/key is set` | cfg with only ServerTLSCertFile set | `errors.Is(err, ErrInvalidTLSConfig)` |
| TC-SRV-04 | `GET /healthz is public (no auth)` | Build Server; issue request with NO Authorization header | 200 OK |
| TC-SRV-05 | `GET /readyz is public (no auth)` | Same, with a mock Checker returning Ready=true | 200 OK |
| TC-SRV-06 | `GET /metrics is public (no auth)` | Registry with one counter | 200 OK, body contains counter |
| TC-SRV-07 | `GET /health/details requires auth` | No Authorization header | 401 ProblemDetails (application/problem+json) |
| TC-SRV-08 | `GET /flows requires auth` | No Authorization header | 401 ProblemDetails |
| TC-SRV-09 | `GET /flows with valid bearer token succeeds` | Stub auth provider that accepts any bearer; stub Handlers that returns empty list | 200 OK |
| TC-SRV-10 | `Param parse error returns ProblemDetails, not {"msg":"..."}` | GET /flows/not-a-uuid | 400 `application/problem+json` body |
| TC-SRV-11 | `Handler error (apperror.ErrNotFound) becomes 404 ProblemDetails` | Stub Handlers returning `apperror.New(ErrNotFound,...)` on GET /flows/{id} | 404 `application/problem+json`, Title "Not Found" |
| TC-SRV-12 | `Panic in handler becomes 500 ProblemDetails` | Stub Handlers that panics | 500 `application/problem+json` |
| TC-SRV-13 | `X-Request-ID is set on every response` | Any public endpoint | Response header `X-Request-ID` non-empty |
| TC-SRV-14 | `Shutdown returns when Start's ctx is cancelled` | Start in goroutine; cancel ctx; Shutdown with 1s timeout | `Shutdown` returns nil within 1s |
| TC-SRV-15 | `Start with already-bound port returns bind error` | Bind a listener first; Start with same port | error not nil, not http.ErrServerClosed |
| TC-SRV-16 | `Handler() returns a usable http.Handler` | httptest.NewServer(srv.Handler()); GET /healthz | 200 OK |
| TC-SRV-17 | `Wired HTTP histogram uses TAMS-tuned buckets` | Build server with shared registry; drive one observation through a non-skipped path; gather and inspect `http_request_duration_seconds` bucket upper-bounds | 13 buckets exactly, matching `httpMetricsBuckets` (1ms..30s). Guards BR-SRV-13. |
| TC-SRV-18 | `OapiRequestValidator rejects missing required body field` | POST /tams/v1/flows/{id}/segments with `{}` body and valid auth + idempotency header | 400 ProblemDetails. Counterfactually proves validator runs: with validator disabled, the fakeSSI panic on PostFlowSegments surfaces as 500. Guards BR-SRV-14. |
| TC-SRV-19 | `Missing required X-Idempotency-Key returns 400 ProblemDetails` | POST /tams/v1/flows/{id}/segments with valid body but no `X-Idempotency-Key` header | 400 ProblemDetails. Defence-in-depth: caught by both strict-server's `siw.ErrorHandler` and the validator middleware. |

All tests use real `gin.Engine` + `httptest.NewRecorder` (or `httptest.NewServer` for lifecycle-involving cases), NO network beyond loopback. Stub handlers via hand-rolled type implementing `api.StrictServerInterface`. Stub `auth.Provider`, `health.Checker`, `prometheus.Gatherer` similarly.

Coverage target: 100% on server.go + problem.go. Lifecycle paths (TLS branch) may be <100%; the TLS branch is exercised via a narrow unit that swaps out `srv.ServeTLS` with a recording fake — see NFR-SRV-T3.

---

## File layout

```
internal/server/
├── server.go         # Server type, New, Start, Shutdown, Handler, Addr
├── errors.go         # sentinel errors
├── problem.go        # paramParseErrorHandler (RFC 9457 ProblemDetails for siw.ErrorHandler)
└── server_test.go
```

No route table and no per-endpoint dispatch glue — `api.RegisterHandlersWithOptions` handles every route on the authenticated group, and the three public endpoints are three direct `router.GET(...)` lines. Kept small on purpose — the "hard" logic lives in handlers/middleware; this package is wiring.
