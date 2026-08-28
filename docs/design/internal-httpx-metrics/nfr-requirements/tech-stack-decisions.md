# Tech Stack Decisions — internal/httpx/metrics

## New dependencies

None. Every required package is already in `go.mod` via transitive use in `pkg/metrics` and `pkg/httpmetrics`:

| Dependency | Source (existing) | Usage |
|---|---|---|
| `github.com/prometheus/client_golang/prometheus` | existing | `Gatherer` interface type. |
| `github.com/prometheus/client_golang/prometheus/promhttp` | existing | `HandlerFor(...)` constructor. |
| `net/http` | stdlib | return type. |

## Test stack

| Decision | Choice | Rationale |
|---|---|---|
| Registry per test | Fresh `prometheus.NewRegistry()` | Prevents cross-test leakage; no reliance on `DefaultRegisterer` which other tests in the suite may mutate. |
| HTTP driver | `net/http/httptest.NewRecorder()` + `http.NewRequest(...)` | Drive `http.Handler` directly — no Gin engine needed for a `net/http` level wrapper. |
| Fixture metrics | Ordinary `prometheus.NewCounter` / `NewGauge` | Avoids depending on `pkg/metrics` — keeps this package's tests isolated to the promhttp surface. |

## Versioning guard

`promhttp.HandlerOpts` has grown fields across minor versions (e.g., `EnableOpenMetrics` was added in v1.11, `Timeout` older). The option struct in `Handler` uses named fields only (no positional/struct literal shortcuts on fields we don't control), so minor-version upgrades add fields without breaking our call site.

## Regeneration guard

`.oapi-codegen.yaml` carries a comment at `output-options.exclude-operation-ids` explaining why `GetMetrics` is excluded. A future developer who re-adds `GetMetrics` by removing the exclusion will see the comment first, and a fresh `make generate` + run will fail loudly (duplicate Gin route panic at startup) rather than silently breaking scrapes.
