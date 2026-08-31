# NFR Requirements — internal/idempotency

## Performance
- `Acquire` is on the hot path of every `POST /segments` request. It must complete in < 5ms p99 under normal load (single round-trip: DELETE + SELECT FOR UPDATE + optional INSERT).
- `Prune` and `ReapStale` are background operations. No latency target; must not block request-serving goroutines.

## Scalability
- Key volume is bounded by `IDEMPOTENCY_KEY_TTL` × request rate. At 1h TTL and 100 req/s, that is ~360K live rows. The `idempotency_keys_expires` index keeps `Prune` and TTL-based `DELETE` efficient at this scale.
- `ReapStale` scans only the in-flight subset via the partial index `idempotency_keys_inflight_acquired (acquired_at) WHERE in_flight`. The in-flight set is bounded by request concurrency (single-digit thousand rows in steady state), so the reaper runs in milliseconds even at peak load.
- `SELECT FOR UPDATE` on primary key is O(1) — no full-table scans.

## Availability
- Store failures surface as internal errors to the caller. The HTTP handler decides whether to fail-open (allow request through) or fail-closed (return 503). The store itself has no availability policy — it surfaces errors and lets the caller decide.

## Security
- The store stores caller-supplied `bodyHash` values. No validation of hash content beyond being a non-empty string — the SHA-256 hex standard is a project convention, not enforced by the store.
- `response_body` is stored as JSONB. No PII scrubbing is performed at the store layer — callers must not store sensitive data in response bodies that are persisted.

## Reliability
- `Acquire` wraps its mutations in a transaction with `SELECT FOR UPDATE` to prevent lost-update races under concurrent retries.
- Abandoned in-flight records (server crash before `Complete`/`Release`, bypassing the handler's defer safety net) are recovered by `ReapStale`, which deletes any `in_flight = true` row whose `acquired_at` is older than `IDEMPOTENCY_STALE_THRESHOLD`. The threshold is clamped at startup to be `>= http.Server.WriteTimeout` so a genuinely-running request can never be reaped from under its handler.
- `Complete`, `Release`, and `ReapStale` are race-safe across HA replicas: each operation is a single atomic UPDATE/DELETE on a primary-key-indexed row; concurrent calls converge on the same end state.

## Maintainability
- Same `dbConn` interface + `newTestStore(tx)` pattern as M4 metastore. Tests use real Postgres via testcontainers; `pgx.Tx` rollback per test for isolation.
- Separate-connection tests used only when transaction-rollback isolation is insufficient (e.g., testing concurrent Acquire behaviour).

## Configuration
| Variable | Type | Default | Description |
|---|---|---|---|
| `IDEMPOTENCY_KEY_TTL` | duration | `1h` | How long keys are retained after creation; bounds the longest replay window |
| `IDEMPOTENCY_STALE_THRESHOLD` | duration | `60s` | How long an in-flight row may exist before the reaper considers it orphaned. Clamped at startup to `>= http.Server.WriteTimeout` (60s) |
| `IDEMPOTENCY_REAPER_INTERVAL` | duration | `1m` | Period of the background goroutine that runs Prune (TTL) + ReapStale (orphans). Each tick adds ±25% jitter |
