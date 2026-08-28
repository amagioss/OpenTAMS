# Business Logic Model — P2 `pkg/metrics`

## Purpose

Zero-TAMS-domain Prometheus registry helper. Provides the shared registry
infrastructure that all `internal/` modules register their metrics into.
No TAMS metric names or domain knowledge lives here.

## Responsibilities

| Responsibility | Owner |
|---|---|
| Custom isolated Prometheus registry | `pkg/metrics` |
| Go runtime and process collectors | `pkg/metrics` |
| Namespace-prefixed registerer for app-owned metrics | `pkg/metrics` |
| Un-prefixed registerer for community-portable metric families (`http_*`, `db_*`, …) | `pkg/metrics` |
| Gatherer for the `/metrics` endpoint (handler is wired by the consumer) | `pkg/metrics` |
| HTTP request counters/histograms | `pkg/httpmetrics` (Gin middleware) |
| DB pool metrics | `internal/metastore` |
| Object store latency | `internal/objectstore` |
| Log level counter (zapcore.Core) | Wiring layer (`internal/server`) |

## Data Flow

```
main.go / internal/server
  └─ metrics.New(metrics.WithNamespace("opentams"))
       ├─ registers: process collector → raw registry (standard names)
       ├─ registers: go collector     → raw registry (standard names)
       └─ wraps raw with namespace prefix → wrapped registerer
  └─ reg.NamespacedRegisterer() → passed to app-owned metric constructors (service, idempotency, …)
  └─ reg.RawRegisterer()        → passed to community-portable middleware (pkg/httpmetrics)
  └─ reg.Gatherer()             → wired into promhttp.HandlerFor at GET /metrics by the consumer
```

## Public API

```go
func New(opts ...Option) (*Registry, error)

func WithNamespace(ns string) Option
func WithoutProcessCollector() Option
func WithoutGoCollector() Option

func (r *Registry) NamespacedRegisterer() prometheus.Registerer  // applies WithNamespace prefix
func (r *Registry) RawRegisterer() prometheus.Registerer         // un-prefixed; for community-portable names
func (r *Registry) Gatherer() prometheus.Gatherer                // pass to promhttp.HandlerFor
```

There is intentionally no `Handler()` method. Wiring the `/metrics` HTTP
endpoint is the consumer's responsibility — typically a one-liner
`promhttp.HandlerFor(reg.Gatherer(), promhttp.HandlerOpts{})` or
equivalent — which keeps `pkg/metrics` framework-neutral (no implicit
dependency on `net/http` handler conventions) and lets each consumer pick
its own framework adapter (Gin, chi, Echo, plain `net/http`).

## Internal Structure

```go
type Registry struct {
    promRegistry *prometheus.Registry  // holds all metrics; used for gathering and as RawRegisterer
    registerer   prometheus.Registerer // namespace-prefixed wrapper around promRegistry
}
```

`promRegistry` is the single underlying registry. Process and Go collectors
are registered directly on it so their names (`go_*`, `process_*`) are
unaffected by the namespace prefix; `RawRegisterer()` returns it directly.
App metrics that should pick up the prefix are registered through
`registerer` (the `WrapRegistererWithPrefix` wrapper), exposed as
`NamespacedRegisterer()`.
