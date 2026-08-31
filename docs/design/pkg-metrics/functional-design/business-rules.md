# Business Rules — P2 `pkg/metrics`

## Core Rules

| ID | Rule |
|---|---|
| BR-MET-01 | Never use the global `prometheus.DefaultRegisterer`. Always create a custom `prometheus.NewRegistry()`. |
| BR-MET-02 | Process and Go collectors are registered by default. Each has an opt-out Option. |
| BR-MET-03 | Process and Go collectors are registered on the raw registry directly — they bypass the namespace prefix and keep standard `go_*` / `process_*` names. |
| BR-MET-04 | `WrapRegistererWithPrefix` applies the namespace to app metrics that register through `NamespacedRegisterer()`. Callers of `NamespacedRegisterer()` set only `Subsystem` and `Name` in `prometheus.Opts` — never `Namespace`, to avoid double-prefixing. |
| BR-MET-05 | `WithNamespace` is optional. Empty string (the default) means no prefix. Prometheus `job`/`instance` labels prevent cross-service collision; namespace is a clarity convention, not a correctness requirement. |
| BR-MET-06 | No panics anywhere in production code. `New` returns an error if any collector registration fails. `MustRegister` is never called in production paths. |
| BR-MET-07 | `Registry` is safe for concurrent use — delegated to the prometheus library. |
| BR-MET-08 | `RawRegisterer()` exposes the un-prefixed registerer for community-portable metric families (e.g. `http_*`, `db_*`) that should keep their well-known names regardless of the OpenTAMS namespace — same treatment as the standard `go_*` / `process_*` collectors. App-owned metrics MUST continue to use `NamespacedRegisterer()` so they pick up the namespace prefix. The split prevents the double-prefix bug where a caller setting `Namespace: "opentams"` on a metric registered through a `WrapRegistererWithPrefix("opentams_", ...)`-wrapped registerer would emit `opentams_opentams_http_*` on the wire. |

## Scope Boundary

| In scope | Out of scope |
|---|---|
| Registry creation and configuration | TAMS-specific metric names |
| Process + Go collectors | HTTP request counters/histograms |
| Namespace-prefixed registerer | Log level counter (zapcore coupling) |
| `/metrics` HTTP handler | DB pool, object store, rate limiter metrics |

## Caller Convention

### App-owned metrics — use `NamespacedRegisterer()`

Internal modules receive `reg.NamespacedRegisterer()` and use it to register their
own metrics at construction time:

```go
c := prometheus.NewCounterVec(prometheus.CounterOpts{
    Subsystem: "service",       // set by caller
    Name:      "operation_duration_seconds",
    Help:      "...",
}, []string{"app_service", "operation", "result"})

if err := reg.NamespacedRegisterer().Register(c); err != nil {
    return nil, fmt.Errorf("register service counter: %w", err)
}
```

Result: `opentams_service_operation_duration_seconds` (with namespace
`"opentams"`). This is the path taken by `internal/service/metrics.go`,
`internal/idempotency`, etc.

### Community-portable metrics — use `RawRegisterer()`

Metric families with well-known industry names (`http_request_*`,
`db_query_*`, …) should keep those names regardless of the OpenTAMS
namespace, so dashboards and alerting rules from upstream Prometheus
exporter ecosystems work unmodified. Use `RawRegisterer()` for these:

```go
mw, err := httpmetrics.New(reg.RawRegisterer())
```

Result: `http_request_duration_seconds`, `http_requests_total` — same
treatment as the standard `go_*` / `process_*` collectors. See BR-MET-08.
