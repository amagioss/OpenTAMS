# Tech Stack Decisions — P2 `pkg/metrics`

## Metrics Library

**Decision:** `github.com/prometheus/client_golang v1.23.2`

Confirmed in plan. Prometheus is the standard for k8s-ecosystem observability.
No evaluation needed — already decided before construction.

## Key Design Decisions

### No `promauto.Factory`

`promauto` is the idiomatic prometheus helper for auto-registration. Rejected because
`promauto.NewCounterVec` etc. call `MustRegister` internally, which panics on duplicate
registration. The no-panic requirement makes `promauto` incompatible.

**Alternative:** Callers use `prometheus.NewCounterVec(opts, labels)` directly and
register via `reg.NamespacedRegisterer().Register(c)` (or `reg.RawRegisterer().Register(c)`
for community-portable names) which returns an error.

### No factory methods on `Registry`

Considered wrapping prometheus constructors (`Registry.NewCounterVec`, `Registry.NewHistogramVec`
etc.) to auto-apply the namespace. Rejected because:
- Large API surface to maintain
- Duplicates what prometheus already provides
- Limits callers to only the metric types we chose to wrap

**Alternative:** `prometheus.WrapRegistererWithPrefix` applies the namespace at the
registerer level, so callers use standard prometheus constructors and register through
`reg.NamespacedRegisterer()`. Namespace is applied automatically without a custom factory.

### Log level counter excluded from `pkg/metrics`

A `zapcore.Core` wrapper that counts log lines by level was considered for `pkg/metrics`.
Rejected because it would import `go.uber.org/zap/zapcore`, coupling two independently
importable packages. The log level counter belongs in the wiring layer (`internal/server`)
where both `pkg/logger` and `pkg/metrics` are already imported.

### No `promauto`-style string-based metric lookup

Evaluated the pattern used in internal Amagi services (`IncreaseCounter(name string, labels []string)`).
Rejected because:
- String-based lookup at call time — typos fail at runtime, not compile time
- Map lookup on every metric update (allocation + lock on hot path)
- Hides prometheus types — callers can't use testutil, exemplars, or advanced features
- Appropriate for internal services with training-wheel APIs; wrong for an open-source library

**Decision:** Callers hold references to real prometheus metric objects and call
`.Inc()`, `.Observe()` etc. directly. No indirection layer.
