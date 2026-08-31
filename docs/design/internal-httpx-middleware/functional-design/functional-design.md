# Functional Design — M11 Middleware

## Scope

Six packages across `pkg/` and `internal/httpx/middleware/`:

| Package | Location | Type |
|---|---|---|
| `pkg/requestid` | pkg | Agnostic core |
| `pkg/requestid/ginadapter` | pkg | Gin adapter |
| `pkg/httplog` | pkg | Gin-specific |
| `pkg/httpmetrics` | pkg | Gin-specific |
| `pkg/httprecovery` | pkg | Agnostic core |
| `pkg/httprecovery/ginadapter` | pkg | Gin adapter |
| `internal/httpx/middleware` | internal | TAMS-specific |

---

## pkg/requestid

### API
```go
const Header = "X-Request-ID"

func Generate() string
func WithContext(ctx context.Context, id string) context.Context
func FromContext(ctx context.Context) string
```

### Business Rules
- **BR-RID-01**: `Generate` returns a new UUID v4 string.
- **BR-RID-02**: `WithContext` stores the ID under an unexported typed key — no collisions with other packages.
- **BR-RID-03**: `FromContext` returns empty string if no ID is stored.

---

## pkg/requestid/ginadapter

### API
```go
func Middleware() gin.HandlerFunc
func FromContext(c *gin.Context) string
```

### Business Rules
- **BR-RID-GIN-01**: Read `X-Request-ID` request header. If non-empty, use as-is (trust and forward). If empty, call `requestid.Generate()`.
- **BR-RID-GIN-02**: Set `X-Request-ID` on the response header unconditionally.
- **BR-RID-GIN-03**: Store the ID in `gin.Context` under a typed key for fast retrieval.
- **BR-RID-GIN-04**: Also store in standard `context.Context` via `requestid.WithContext` so non-Gin code (background goroutines, service calls) can access it.

---

## pkg/httplog

### API
```go
func Middleware(logger *zap.Logger, opts ...Option) gin.HandlerFunc
func Logger(c *gin.Context) *zap.Logger

type Option func(*config)
func WarnStatuses(codes ...int) Option
```

### Business Rules
- **BR-LOG-01**: Before `c.Next()`: retrieve request ID via `requestid/ginadapter.FromContext`; build child logger with fields `request_id`, `method`, `path`; store in `gin.Context`.
- **BR-LOG-02**: After `c.Next()`: log request completion with fields `status`, `latency_ms`.
- **BR-LOG-03**: Log level selection — ERROR for 5xx; WARN for status codes in `WarnStatuses` (default: 401, 403, 429); INFO for all others.
- **BR-LOG-04**: `Logger(c)` returns the child logger stored by this middleware. If not set (middleware not in chain), returns the root logger passed at construction — never returns nil.
- **BR-LOG-05**: "Repeated 401/403" and "spike" pattern detection is out of scope for per-request middleware — handled at the monitoring/alerting layer.

---

## pkg/httpmetrics

### API
```go
type Option func(*config)

func New(reg prometheus.Registerer, opts ...Option) (gin.HandlerFunc, error)

func WithSubsystem(subsystem string) Option        // adds Subsystem segment to both metrics
func WithBuckets(buckets []float64) Option         // overrides histogram bucket boundaries
func WithConstLabels(labels prometheus.Labels) Option  // stamps every observation
func WithSkipPaths(paths ...string) Option         // suppresses observations for matched routes
```

### Metrics registered
| Metric | Type | Labels |
|---|---|---|
| `http_request_duration_seconds` | Histogram | method, route, status |
| `http_requests_total`           | Counter   | method, route, status |

The middleware applies **no Namespace and no Subsystem of its own** by
default. Final naming on the wire is determined entirely by (a) the
`prometheus.Registerer` the caller passes (which can apply a namespace
prefix via `WrapRegistererWithPrefix`) and (b) the optional
`WithSubsystem(...)` option.

The package is intentionally framework-thin: a caller that wants the
canonical, portable names (`http_request_duration_seconds`,
`http_requests_total` — same treatment as `go_*` / `process_*`) just
passes an un-prefixed registerer. A caller that wants prefixed names
(`myapi_http_*`) passes `httpmetrics.WithSubsystem("myapi")` or wraps
the registerer.

#### OpenTAMS-specific wiring (informational)

`internal/server` constructs the middleware with
`httpmetrics.New(reg.RawRegisterer(), httpmetrics.WithSkipPaths("/healthz", "/readyz", "/metrics"))`,
so HTTP histograms emit canonical names alongside `go_*` / `process_*`,
while `opentams_service_*` continues to be prefixed via
`reg.NamespacedRegisterer()`. See pkg-metrics BR-MET-08.

### Business Rules
- **BR-MET-01**: After `c.Next()`: record duration and increment counter.
- **BR-MET-02**: `route` label = `c.FullPath()` (Gin route template, e.g. `/flows/:flowId`). Empty string if route not matched — label value `"unmatched"`.
- **BR-MET-03**: `status` label = string representation of HTTP status code (e.g. `"200"`, `"404"`).
- **BR-MET-04**: The middleware does not set `Namespace` on its `prometheus.Opts`. `Subsystem` is empty by default and only set via the explicit `WithSubsystem(...)` option. Any namespace prefix on the wire comes from the registerer the caller passes — never from inside the package. This makes the double-prefix bug (caller's wrapped registerer + middleware-injected `Namespace`) unreachable from the API surface (regression-tested in TC-HTM-05/06).
- **BR-MET-05**: Returns error if metric registration fails (duplicate registration). Never panics.
- **BR-MET-06**: `WithSkipPaths(paths...)` suppresses observations for matched routes (typically `/healthz`, `/readyz`, `/metrics`) so liveness / readiness / scrape traffic does not dominate the histograms. Skipped requests still flow through the middleware — only the observe / increment is skipped.
- **BR-MET-07**: `WithSubsystem`, `WithBuckets`, `WithConstLabels` are pure functional options — no validation in the option setters; the underlying prometheus library validates on `Register`. Default buckets are `prometheus.DefBuckets`; default constLabels are nil; default subsystem is empty.

---

## pkg/httprecovery

### API
```go
type ResponseWriter func(w http.ResponseWriter, recovered any, stack []byte)
```

Defines the contract for writing the panic error response. No Gin dependency.

---

## pkg/httprecovery/ginadapter

### API
```go
func Middleware(logger *zap.Logger, writer httprecovery.ResponseWriter) gin.HandlerFunc
```

### Business Rules
- **BR-REC-01**: Defers `recover()`. If panic is nil — do nothing.
- **BR-REC-02**: On panic: log at ERROR level with fields `recovered` (panic value) and `stack` (full stack trace).
- **BR-REC-03**: Call `writer` to write the HTTP response. Writer is caller-supplied — the package imposes no response format.
- **BR-REC-04**: Call `c.Abort()` after writing to prevent downstream handlers from running.

---

## internal/httpx/middleware

### Auth
```go
func Auth(provider auth.Provider) gin.HandlerFunc
func AuthSubject(c *gin.Context) string
```

#### Business Rules
- **BR-AUTH-01**: Extract Bearer token from `Authorization` header. If header absent or scheme is not `Bearer` → 401 via ErrorHandler, `c.Abort()`.
- **BR-AUTH-02**: Call `provider.Validate(ctx, token)`. On any error (`ErrTokenExpired`, `ErrUnauthorized`, other) → 401, `c.Abort()`.
- **BR-AUTH-03**: On success: store subject string in `gin.Context` under a typed key.
- **BR-AUTH-04**: Auth errors are passed to `c.Error(err)` and handled by `ErrorHandler` middleware — Auth itself does not write the response.

### ErrorHandler
```go
func ErrorHandler() gin.HandlerFunc
```

#### Business Rules
- **BR-ERR-01**: After `c.Next()`: if `c.Writer.Written()` is true → skip (handler already wrote response).
- **BR-ERR-02**: If `len(c.Errors) == 0` → skip.
- **BR-ERR-03**: Take the last error from `c.Errors`. Unwrap to `*apperror.AppError`.
- **BR-ERR-04**: AppError → ProblemDetails: `status` from apperror code table; `type` URI from 19 stable URIs (omit field if not catalogued); `title` from code; `detail` from `AppError.Message`.
- **BR-ERR-05**: Non-AppError → 500, no `type`, `title: "Internal Server Error"`, `detail` omitted.
- **BR-ERR-06**: Write `Content-Type: application/problem+json`.

### PanicWriter
```go
func PanicWriter() httprecovery.ResponseWriter
```
Default `ResponseWriter` for `httprecovery/ginadapter`. Writes RFC 9457 ProblemDetails 500 with no `type` field. Keeps RFC 9457 format out of `pkg/`.

---

## Middleware chain order

```
Recovery(PanicWriter) → RequestID → Logger → Metrics → ErrorHandler → [Auth*] → Handler
```

`Auth` applied per route-group. Health endpoints bypass it.

---

## Context keys

Each package defines its own unexported typed key to prevent collisions:
```go
type contextKey int
const key contextKey = iota
```

Exported retrieval helpers are the only public API for accessing context values.
