# Tech Stack Decisions — internal/idempotency

## Database driver
**Decision**: pgx v5 + pgxpool (same as M4 metastore).
**Rationale**: already in go.mod; consistent with project-wide decision. `pgx.Tx.Begin` creates savepoints, enabling the `newTestStore(tx)` isolation pattern.
**Rejected**: database/sql — rejected project-wide in M4 NFR.

## Test infrastructure
**Decision**: testcontainers-go with `postgres:16-alpine`; `pgx.Tx` rollback-per-test isolation via `newTestStore(tx)`; separate-connection tests when concurrent behaviour must be tested.
**Rationale**: same pattern as M4. Real Postgres gives accurate lock and transaction semantics that cannot be reproduced with mocks.
**Rejected**: mocking the store — masks real transaction and locking behaviour.

## Body hash algorithm (project standard)
**Decision**: SHA-256 hex string (64 chars), computed by the HTTP handler before calling `Store.Acquire`.
**Rationale**: SHA-256 is collision-resistant for this use case (detecting body changes). Hex encoding is human-readable and easy to log.
**Rejected**: MD5 (collision risk), CRC32 (too weak for security-adjacent use).

## Key column type
**Decision**: `VARCHAR(255)` in PostgreSQL schema.
**Rationale**: enforces the 255-char limit at the DB level as a safety net in addition to the app-level check in `Acquire`. Fits within a single B-tree index page entry.
**Rejected**: `TEXT` — no DB-level enforcement; relies solely on application check.

## Prune scheduling
**Decision**: `IDEMPOTENCY_PRUNE_INTERVAL` config var (duration, default `1h`). Background goroutine in the server calls `Store.Prune` on this interval.
**Rationale**: TTL-based lazy expiry in `Acquire` handles correctness; Prune is purely for storage hygiene and does not need sub-minute precision.
**Rejected**: per-request lazy delete only — rows accumulate indefinitely without periodic bulk cleanup.

## Concurrent access pattern
**Decision**: `SELECT FOR UPDATE` inside a transaction in `Acquire`.
**Rationale**: prevents two concurrent callers from both seeing ErrNoRows and both inserting, which would cause a primary-key conflict on the second insert. Row-level locking is O(1) on the primary key.
**Rejected**: optimistic concurrency (INSERT then handle conflict) — requires retry logic and produces noisier error paths.
