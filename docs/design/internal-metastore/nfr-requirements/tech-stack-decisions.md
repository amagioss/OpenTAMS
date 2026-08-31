---
unit: M4 internal/metastore
stage: NFR Requirements
status: Complete
---

# Tech Stack Decisions — internal/metastore

## DB Driver: pgx v5 + pgxpool (decided)

| Package | Purpose |
|---|---|
| `github.com/jackc/pgx/v5` | PostgreSQL driver — native types, rich error inspection |
| `github.com/jackc/pgx/v5/pgxpool` | Connection pool — health checks, min/max conns, lifetime |
| `github.com/jackc/pgx/v5/pgconn` | Low-level error types (`PgError`) for pgcode inspection |

`database/sql` rejected — forces pgx into compatibility mode, loses native type benefits.
`sqlx` rejected — thin wrapper over `database/sql`, same problem, maintenance-mode library.

## Migrations: golang-migrate (decided)

`github.com/golang-migrate/migrate/v4` with `pgx/v5` driver.

- Sequential numbered migrations (`000001_init.up.sql` / `000001_init.down.sql`)
- Library API used in server startup for schema version validation (read-only check, no auto-apply)
- CLI used for explicit migration apply/rollback
- Migrations live in `migrations/` at repo root
- Rejected: `pressly/goose` — weaker programmatic API, no pgx v5 native driver

## PostgreSQL Extension

`CREATE EXTENSION IF NOT EXISTS btree_gist` — required for the `EXCLUSION` constraint on segments.
Applied in migration `000001`.

## Connection Pool Configuration

Sourced from `internal/config.Config` at construction time:

| Parameter | Default | Source |
|---|---|---|
| `MaxConns` | 10 | `DB_MAX_CONNS` env var |
| `MinConns` | 2 | `DB_MIN_CONNS` env var |
| `MaxConnLifetime` | 1h | `DB_MAX_CONN_LIFETIME` |
| `HealthCheckPeriod` | 1m | fixed |
| `ConnectTimeout` | 5s | `DB_CONNECT_TIMEOUT` |

## Error Mapping

pgx error codes map to `apperror` types at the store boundary — callers never see `pgconn.PgError`.

| pgcode | Condition | apperror |
|---|---|---|
| `23P01` | exclusion_violation | `ErrSegmentOverlap` |
| `23503` | foreign_key_violation | `ErrNotFound` |
| `23505` | unique_violation | `ErrObjectIDExists` (storage allocation) |
| `pgx.ErrNoRows` | no row returned | `ErrNotFound` |

Unmapped DB errors are wrapped as `apperror.Generic(500, ...)` with the original error logged.

## Testing: testcontainers-go with real Postgres

`github.com/testcontainers/testcontainers-go` — spins up a real Postgres container per test suite.

**Rationale**: The store contains non-trivial SQL — overlap queries, EXCLUSION constraint behaviour,
cascade deletes, savepoint handling, lateral joins. Mocking the store interface tests nothing about
the implementation. Tests must exercise real SQL against a real database.

**Rejected**: mock store — tests only the service layer, not the store itself.
**Rejected**: shared test DB — non-hermetic, order-dependent, breaks in CI.

### dbConn interface — enables transaction-rollback test isolation

`PostgresStore` holds an unexported `dbConn` interface, not `*pgxpool.Pool` directly:

```go
type dbConn interface {
    Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
    Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
    QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
    Begin(ctx context.Context) (pgx.Tx, error)
    SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults
}
```

Both `*pgxpool.Pool` and `pgx.Tx` satisfy this interface.
In pgx v5, `pgx.Tx.Begin(ctx)` creates a **savepoint** (nested transaction), not a new DB connection.

- **Production**: `NewPostgresStore(pool)` — `Begin()` opens a real transaction on a pool connection
- **Tests**: `newTestStore(tx)` — `Begin()` creates a savepoint within the test-owned transaction

This means all store writes go through the test's outer `pgx.Tx`. Rolling it back undoes everything. No truncate, no schema isolation needed.

### Test structure

```
internal/metastore/
├── store_test.go       — TestMain: start container, run migrations, create pool; shared tx helpers
├── sources_test.go     — SourceStore integration tests
├── flows_test.go       — FlowStore integration tests
├── segments_test.go    — SegmentStore integration tests (overlap, savepoints, ref counts)
```

`TestMain` in `store_test.go`:
1. Start Postgres container via testcontainers-go
2. Run golang-migrate up against the container
3. Create `*pgxpool.Pool` with the container DSN
4. Run all tests (pool shared across suite)
5. Terminate container

Each test:
1. Calls `pool.Begin(ctx)` to get an outer `pgx.Tx`
2. Constructs `newTestStore(tx)` — store uses the tx as its `dbConn`
3. Runs assertions
4. Calls `tx.Rollback()` in `t.Cleanup` — all writes undone, next test starts clean

### Test coverage target

100% statement coverage. All branches of overlap detection, error mapping, and ref-count logic must be exercised with real SQL.
