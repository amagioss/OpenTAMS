# Tech Stack Decisions — internal/httpx/health

## New dependencies

| Package | Dependency | Version | Rationale |
|---|---|---|---|
| `internal/httpx/health` | `golang.org/x/sync/errgroup` | new (add to `go.mod`) | Standard Go concurrency primitive for "run N tasks, cancel siblings on first error, wait for all." The readyz and details algorithms both need this exact semantics. Alternatives (manual goroutine + channel bookkeeping) re-implement the same logic with more surface area for bugs. `x/sync` is the Go team's staging area for stdlib extensions — battle-tested, zero-maintenance. |

No other dependencies. `context` is stdlib. `time` is stdlib. Probe interfaces are package-local.

## Existing dependencies reused

| Package | Dependency | Usage |
|---|---|---|
| `internal/httpx/health` | `internal/config` | Read `Version` string only. Passed via `Options.Version` at construction; `health` does not import `config`. |
| `internal/httpx/handlers` (new `health.go`) | `internal/httpx/health` | Checker injected into `Handler`. |
| `internal/httpx/handlers` (new `health.go`) | `gen/api` | Response types only. |

The `health` package does NOT import `metastore`, `objectstore`, or `jwtauth` — those packages' health-check methods satisfy the narrow probe interfaces defined locally (structural typing). Inversion of dependencies preserves the M11 rule that `pkg/` and leaf `internal/` packages stay decoupled.

## Test stack

| Decision | Choice | Rationale |
|---|---|---|
| Fakes | Hand-rolled `fakeDBProbe`, `fakeObjectStoreProbe`, `fakeJWKSProbe` with function-field shape | Consistent with M8/M9/M10/M12 pattern; no mock framework adopted. |
| Timing assertions | Real `time.Sleep` + 100ms tolerance | `Checker` uses real `context.WithTimeout`; faking `time` would require a clock abstraction not present elsewhere in the codebase. 100ms tolerance is consistent with other timeout tests in `internal/idempotency`. |
| Concurrency probe | Pair of fake probes that record `time.Now()` on entry; assert windows overlap | Proves parallelism without race-condition theater. |
| Coverage | `go test -race -cover ./internal/httpx/health/...` target 100% | REQ-TEST-02 + race-clean is project default. |

## Handler wiring test stack (`internal/httpx/handlers/health.go` and its test)

| Decision | Choice | Rationale |
|---|---|---|
| Mock Checker in handler tests | Hand-rolled `mockChecker` with function fields for `Ready` and `Details` | Matches the `mockSegmentService` / `mockStorageService` pattern in `mock_test.go`. |
| Constructor signature | Append `hc health.Checker` as the last arg to `handlers.New` | Breaks existing tests — they need a trailing `nil`. Acceptable; single mechanical update across `_test.go` files. |

## Error wrapping

- Probe errors captured inside `health.Checker` are NOT wrapped with `fmt.Errorf("health: ...: %w", err)`. They are dropped after mapping to `ComponentStatus{Status: unhealthy}` per NFR-HLTH-S1.
- `handlers.Handler` methods for health never return a non-nil error — probe failures become 503 or body status, not errors. Consistent with BR-HLTH-11.

## Constructor error surface

- `New(opts) (Checker, error)` returns **exported sentinel errors** `ErrMissingDBProbe` and `ErrMissingObjectStoreProbe` (not `fmt.Errorf`-wrapped strings). Rationale: callers — and our own tests — can assert with `errors.Is(err, health.ErrMissingDBProbe)`, which is more robust than string matching. There is no information to add via wrapping (the opts struct field is the whole story), so the sentinel value IS the message.
- Callers (M14 `cmd/opentams` / `internal/server`) are expected to treat any non-nil error from `New` as fatal and abort process startup (`log.Fatalf`). This is documented in BR-HLTH-13; the `health` package does not itself call `os.Exit` or `log.Fatal`.
