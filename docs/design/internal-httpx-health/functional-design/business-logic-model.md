# Business Logic Model — internal/httpx/health

## Package surface

```go
package health

// ComponentStatus is the public status shape for a single component.
type ComponentStatus struct {
    Status    Status         // healthy | degraded | unhealthy
    LatencyMs *int64         // nil if not measured
}

type Status string
const (
    StatusHealthy   Status = "healthy"
    StatusDegraded  Status = "degraded"  // reserved; not produced by initial impl
    StatusUnhealthy Status = "unhealthy"
)

// Narrow, per-dependency probe interfaces. Each is satisfied by the
// corresponding infrastructure type (metastore.PostgresStore,
// objectstore.S3Store, jwtauth.Validator, etc.)
type DBProbe          interface { Ping(ctx context.Context) error }
type ObjectStoreProbe interface { HealthCheck(ctx context.Context) error }
type JWKSProbe        interface { Check(ctx context.Context) error }

type Options struct {
    Version        string        // build-time version injected via ldflags
    ReadyTimeout   time.Duration // default 500ms
    DetailsTimeout time.Duration // default 2s
    // JWKS is optional: nil if auth uses DevProvider.
    DB       DBProbe
    Object   ObjectStoreProbe
    JWKS     JWKSProbe
}

type Details struct {
    Status     Status
    Version    string
    Components map[string]ComponentStatus
}

type Checker interface {
    Ready(ctx context.Context) bool           // true = healthy; false = any critical probe failed
    Details(ctx context.Context) Details      // per-component + overall
}

// Sentinel errors returned by New on misconfiguration.
var (
    ErrMissingDBProbe          = errors.New("health: DBProbe is required")
    ErrMissingObjectStoreProbe = errors.New("health: ObjectStoreProbe is required")
)

func New(opts Options) (Checker, error)
```

Implementation note: `Checker` is an interface (not a struct) so handlers can inject a fake for tests.

---

## Ready(ctx) bool

Purpose: fast readiness gate for K8s.

```
1. Derive per-probe context with timeout = opts.ReadyTimeout (500ms)
2. Run DB.Ping and Object.HealthCheck concurrently via errgroup
   (JWKS.Check NOT invoked — not a critical dependency per BR-HLTH-02)
3. Wait for both to complete (or any to error)
4. Return true iff both returned nil error
```

Concurrency: use `golang.org/x/sync/errgroup`. On first error, cancel sibling probes.

Latency: bounded by `opts.ReadyTimeout` because each probe has its own derived context.

---

## Details(ctx) Details

Purpose: per-component health report for dashboards.

```
1. Derive per-probe context with timeout = opts.DetailsTimeout (2s)
2. Build components map:
   - "metastore"   → probeDB()
   - "objectstore" → probeObject()
   - "jwtauth"     → probeJWKS()  // omitted entirely if opts.JWKS == nil
   All three probe calls run concurrently via errgroup (errors captured, not returned).
3. Compute overall status = worstOf(components)
4. Return Details{Status: overall, Version: opts.Version, Components: components}
```

Per-component probe helper (pseudocode):

```
probe(name, fn):
    start := time.Now()
    err := fn(ctx)    // ctx has per-probe timeout
    latency := time.Since(start).Milliseconds()
    status := StatusHealthy if err == nil else StatusUnhealthy
    return ComponentStatus{Status: status, LatencyMs: &latency}
```

Worst-of rule (BR-HLTH-04):
```
rank: healthy = 0, degraded = 1, unhealthy = 2
overall = component with max rank
if components is empty: overall = healthy
```

---

## Handler methods (in `internal/httpx/handlers/health.go`)

### GetHealthz(ctx, req)
```
1. return api.GetHealthz200Response{}, nil
```
No probe. No allocation. Fastest possible path.

### GetReadyz(ctx, req)
```
1. if h.health.Ready(ctx):
     return api.GetReadyz200Response{}, nil
   else:
     return api.GetReadyz503Response{}, nil
```

### GetHealthDetails(ctx, req)
```
1. d := h.health.Details(ctx)
2. body := api.HealthDetails{
       Status:     string(d.Status),
       Version:    d.Version,
       Components: mapToAPI(d.Components),   // map[string]ComponentStatus → api schema
   }
3. return api.GetHealthDetails200JSONResponse(body), nil
```

Mapping `health.ComponentStatus` → `api.HealthComponentStatus`:
```
for name, cs := range d.Components:
    out[name] = api.HealthComponentStatus{
        Status:    string(cs.Status),
        LatencyMs: cs.LatencyMs,  // *int64 → *int (spec type)
    }
```
Note: spec defines `latency_ms` as `integer` (no format). Generated type is `*int`. Cast from `*int64` to `*int` at the handler boundary — safe because measured latencies fit in int on all supported platforms.

---

## Handler struct wiring (patch to `internal/httpx/handlers/handler.go`)

```go
type Handler struct {
    // existing fields: cfg, sources, flows, segments, storage, idempotency, logger
    health health.Checker    // NEW — required
}

func New(
    logger *zap.Logger,
    cfg config.Config,
    src sourceStore,
    fl flow.FlowService,
    seg segment.SegmentService,
    stor storage.StorageService,
    idem idempotencyStore,
    hc health.Checker,       // NEW — appended as last arg
) *Handler
```

Test impact: all existing `handlers.New(...)` callers in `internal/httpx/handlers/*_test.go` must pass a seventh arg. Most tests can pass `nil` since they don't exercise health endpoints. Only `health_test.go` (new in M13) will provide a real fake.

---

## Error handling

- Probe errors are captured per-component, never returned from `Ready` or `Details`. `Checker` methods never error.
- Timeouts → `context.DeadlineExceeded` → surfaces as `unhealthy` on that component.
- Panics in a probe → recovered **inside** the `health` package (per NFR-HLTH-R1). Each probe goroutine wraps its call in `defer recover()`; a recovered panic is treated as probe failure (`Ready` → false; `Details` → that component `unhealthy`). Panics never escape the checker.
- Misconfiguration at construction (nil required probe) → surfaces as an error from `New`, per BR-HLTH-13. Not a runtime concern once `New` succeeds.

---

## Out of scope

- Background health polling (e.g. for metric emission). The `health` package is poll-on-demand only; if future work wants Prometheus gauges for component health, that's a separate module.
- Historical health tracking / windowing.
- Transitive dependencies (e.g. probing whether Postgres can reach its WAL disk). Each probe is a shallow reachability check.
