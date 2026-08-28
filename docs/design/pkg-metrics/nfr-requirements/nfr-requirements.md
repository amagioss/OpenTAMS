# NFR Requirements — P2 `pkg/metrics`

## Performance

| ID | Requirement |
|---|---|
| NFR-MET-P01 | Custom registry isolates metric gathering — no lock contention with other registries in the process. |
| NFR-MET-P02 | Metric registration occurs once at startup. No registration calls on the hot request path. |

## Reliability

| ID | Requirement |
|---|---|
| NFR-MET-R01 | No panics in production code. All registration errors are returned as `error` values. |
| NFR-MET-R02 | Duplicate metric registration returns an error at startup — fails fast rather than silently emitting incorrect data. |
| NFR-MET-R03 | Custom registry prevents accidental pollution of the global `prometheus.DefaultRegisterer`, which could cause conflicts when `pkg/metrics` is imported by other services. |

## Maintainability

| ID | Requirement |
|---|---|
| NFR-MET-M01 | `pkg/metrics` has no dependency on `pkg/logger` or any `internal/` package — independently importable. |
| NFR-MET-M02 | `pkg/metrics` has no dependency on `go.uber.org/zap` or `zapcore` — log-level counter wiring is the caller's responsibility. |
| NFR-MET-M03 | 100% statement coverage, race-clean (`go test -race`). |

## Reusability

| ID | Requirement |
|---|---|
| NFR-MET-RE01 | Zero TAMS domain knowledge. `pkg/metrics` is suitable for adoption by other Amagi services without modification. |
| NFR-MET-RE02 | Graduation path: move to `github.com/amagimedia/go-commons` when 3+ Amagi services adopt it. |
