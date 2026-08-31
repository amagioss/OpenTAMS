# NFR Requirements — internal/httpx/metrics

Covers non-functional concerns not in [`business-rules.md`](../functional-design/business-rules.md) or `business-logic-model.md`. Behavioural rules (direct router mount, opinionated promhttp options, gatherer injection, no auth) are normative there.

## Performance

- **NFR-MET-P1**: `Handler` is a pure constructor — one `promhttp.HandlerFor` call, one options struct allocation, no goroutines, no I/O. Invocation cost at startup is negligible.
- **NFR-MET-P2**: Per-request cost is promhttp's own — a full registry walk. This package adds zero overhead on the scrape path.

## Reliability

- **NFR-MET-R1**: If the injected `prometheus.Gatherer` is nil, `Handler` must not panic at construction. Rationale: nil-safety is cheap and catching the misconfiguration early at wire-up time beats a runtime crash on the first scrape. Implementation: add a guard that returns a handler which serves 500 on every request, so the misconfiguration is observable in operational logs rather than silent.
- **NFR-MET-R2**: `promhttp.ContinueOnError` means a single misbehaving collector does not blank the response. Operators see partial metrics plus a `# TYPE` gap, not an empty body.

## Testability

- **NFR-MET-T1**: 100% statement coverage.
- **NFR-MET-T2**: Each test instantiates a fresh `prometheus.NewRegistry()` — no shared state between tests, no use of `prometheus.DefaultRegisterer`.
- **NFR-MET-T3**: `Content-Type` assertions accept both `text/plain` (default exposition) and `application/openmetrics-text` (when `Accept` requests it). Tests do not assert on exact version strings embedded in the media type — only on the prefix.
- **NFR-MET-T4**: No network, no goroutines, no sleeps. Every test runs in <10ms.

## Security

- **NFR-MET-S1**: This package does not authenticate requests and does not redact metric names or values. Callers (M14) are responsible for scope restrictions via deployment topology. If `/metrics` exposes sensitive labels, the fix is at the registration site (`pkg/metrics` caller) or in the scrape network policy — not here.

## Maintainability

- **NFR-MET-M1**: No config knobs exposed to callers. All `promhttp.HandlerOpts` tuning lives in this file. Rationale: one-place tuning beats per-caller overrides for an endpoint that has no legitimate per-caller variation.
- **NFR-MET-M2**: Regression guard — if `GetMetrics` ever reappears in `gen/api/opentams.gen.go` (e.g., someone reverts the codegen exclusion), Gin will panic on duplicate `/metrics` route registration at startup. Loud failure mode; no silent drift.
