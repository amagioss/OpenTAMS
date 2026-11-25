---
unit: M16 internal/gc
stage: Functional Design
status: Draft (not implemented)
---

# Business Logic Model — object garbage collection

This document gives the schema, the SQL, the algorithms, the package layout, and each change outside `internal/gc`. The names of packages, files, types, functions, metrics, and settings in this document are binding. Use them as written, so that the tests in [`test-plan.md`](test-plan.md) and the other documents agree with the code.

## 1. Package layout

| Path | Contents |
|---|---|
| `internal/gc/gc.go` | `Config`, `Worker`, `New`, `Run`, `RunOnce`, `Report` |
| `internal/gc/sweep_released.go` | Sweep 1: one batch, the retry phase, the gauges |
| `internal/gc/sweep_allocation.go` | Sweep 2: one batch, the retry phase, the gauges |
| `internal/gc/metrics.go` | `Metrics`, `NewMetrics` |
| `internal/gc/*_test.go` | Unit tests with fakes of `Store` and `Deleter` |
| `internal/metastore/allocations.go` | `AllocateObjects` and the allocation errors |
| `internal/metastore/gc.go` | The methods of `gc.Store` on `*PostgresStore` |
| `internal/metastore/gc_test.go`, `allocations_test.go` | Integration tests against PostgreSQL (testcontainers) |
| `cmd/opentams/gc.go` | The `gc` command, the admin listener, the signal handling |
| `migrations/000006_allocations_and_deleting.{up,down}.sql` | Rename, new table, and segment index |
| `deployments/helm/opentams/templates/gc-*.yaml` | GC Deployment, Service, ServiceMonitor, NetworkPolicy |

`internal/gc` must not import `internal/httpx`. It depends on `internal/metastore` types only through the interface `gc.Store`, and on `internal/objectstore` through `gc.Deleter`.

## 2. Go types of `internal/gc`

```go
package gc

// Config holds the settings of the worker. cmd/opentams/gc.go fills it
// from config.Config.
type Config struct {
	PollInterval    time.Duration // GC_POLL_INTERVAL, default 15m
	BatchSize       int           // GC_BATCH_SIZE, default 1000, range 1..1000
	AllocationGrace time.Duration // GC_ALLOCATION_GRACE, default 5m
	StorageID       string        // OPENTAMS_OBJECT_STORE_ID, default "default"
}

const (
	dbTimeout = 30 * time.Second // BR-GC-68
	s3Timeout = 60 * time.Second // BR-GC-68
)

// Sweep names. They are also the values of the metric label "sweep".
const (
	SweepReleased   = "released"
	SweepAllocation = "allocation"
)

// Outcomes. They are the values of the metric label "outcome" and of
// the log field "outcome".
const (
	OutcomeDeleted            = "deleted"
	OutcomeBYOSRowOnly        = "byos_row_only"
	OutcomeRegisteredLeftover = "registered_leftover"
)

// Store is the database surface of the GC. *metastore.PostgresStore
// implements it.
type Store interface {
	ClaimReleasedObjects(ctx context.Context, storageID string, limit int) (ClaimResult, error)
	ListRetryObjects(ctx context.Context, storageID, afterID string, limit int) ([]string, error)
	DeleteClaimedObjects(ctx context.Context, ids []string) ([]string, error)
	ReleasedBacklog(ctx context.Context, storageID string) (Backlog, error)

	ClaimExpiredAllocations(ctx context.Context, storageID string, grace time.Duration, limit int) (ClaimResult, error)
	ListRetryAllocations(ctx context.Context, storageID, afterID string, limit int) ([]string, error)
	DeleteClaimedAllocations(ctx context.Context, ids []string) ([]string, error)
	AllocationBacklog(ctx context.Context, storageID string, grace time.Duration) (Backlog, error)
}

// ClaimResult is the result of one claim transaction.
type ClaimResult struct {
	Selected  int           // rows that the SELECT returned; drives the drain rule
	Claimed   []string      // IDs set to deleting = true, in ascending order
	RowOnly   []string      // sweep 1: BYOS rows deleted; sweep 2: leftover allocation rows deleted
	Drift     []DriftRecord // sweep 1 only
}

type DriftRecord struct {
	ObjectID string
	Actual   int64 // real segment count; the stored ref_count was 0
}

type Backlog struct {
	Pending        int64
	Retrying       int64
	UnknownStorage int64
	Rows           int64   // sweep 2 only: all rows of allocations
	OldestOverdue  float64 // sweep 2 only: seconds; 0 if Pending is 0
}

// Deleter is the object-store surface of the GC.
// *objectstore.S3Store implements it.
type Deleter interface {
	DeleteObjects(ctx context.Context, ids []string) ([]objectstore.DeleteResult, error)
}

// Report is the result of RunOnce.
type Report struct {
	Released   CycleResult
	Allocation CycleResult
}

type CycleResult struct {
	Deleted      int  // rows removed with OutcomeDeleted
	RowOnly      int  // rows removed with OutcomeBYOSRowOnly or OutcomeRegisteredLeftover
	Failed       int  // per-ID object-store errors
	Drift        int
	BatchError   bool // the cycle stopped because of a batch-level error
}

func New(store Store, del Deleter, cfg Config, m *Metrics, log *zap.Logger) (*Worker, error)

// Run starts the two loops and blocks. When stop is closed, the loops
// claim no new batch and return after the current batch. ctx is the hard
// limit: cmd/opentams cancels it SERVER_GRACEFUL_SHUTDOWN_PERIOD after stop.
// Run returns nil after a shutdown.
func (w *Worker) Run(ctx context.Context, stop <-chan struct{}) error

// RunOnce does one cycle of each sweep at the same time and returns.
func (w *Worker) RunOnce(ctx context.Context) (Report, error)
```

`New` returns an error if `BatchSize` is outside 1 to 1000, if `PollInterval` is 0 or less, if `AllocationGrace` is less than 0, or if `StorageID` is empty.

`Worker` has the unexported fields `dbTimeout` and `s3Timeout`. `New` sets them from `dbTimeout` and `s3Timeout` (the constants). Only tests in package `gc` change them.

## 3. Schema

### 3.1 Migration 000006 (`000006_allocations_and_deleting.up.sql`)

```sql
-- BR-GC-50. Rename the GC claim flag. PostgreSQL updates the predicates
-- of objects_gc_claim_idx and objects_gc_retry_idx automatically.
ALTER TABLE objects RENAME COLUMN reaping TO deleting;

-- BR-GC-01. One row for each object ID with an issued upload URL that no
-- segment has registered. No foreign key to flows (BR-GC-08).
CREATE TABLE IF NOT EXISTS allocations (
    id          TEXT        PRIMARY KEY,
    flow_id     UUID        NOT NULL,
    storage_id  TEXT        NOT NULL,
    expires_at  TIMESTAMPTZ NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleting    BOOLEAN     NOT NULL DEFAULT false
);

-- Sweep 2 claim (BR-GC-40).
CREATE INDEX IF NOT EXISTS allocations_gc_claim_idx
    ON allocations (expires_at, id) WHERE deleting = false;

-- Sweep 2 retry phase (BR-GC-64).
CREATE INDEX IF NOT EXISTS allocations_gc_retry_idx
    ON allocations (id) WHERE deleting = true;

-- BR-GC-51. Segment count of sweep 1 and the foreign-key check of an
-- objects delete. A plain index: it is part of this transaction.
CREATE INDEX IF NOT EXISTS segments_object_id_idx ON segments (object_id);
```

Do not add `BEGIN` or `COMMIT` to the file. The migration driver (`database/pgx/v5`, `cmd/opentams/migrate.go:10`) sends the file as one batch of statements. PostgreSQL operates a batch as one implicit transaction. Thus, if one statement fails, PostgreSQL rolls back all statements of the file (BR-GC-51).

`000006_allocations_and_deleting.down.sql`:

```sql
DROP INDEX IF EXISTS segments_object_id_idx;
DROP TABLE IF EXISTS allocations;
ALTER TABLE objects RENAME COLUMN deleting TO reaping;
```

### 3.2 Schema after the change

| Table | Columns that the GC uses |
|---|---|
| `objects` | `id TEXT PK`, `ref_count INT`, `storage_id TEXT NULL`, `deleting BOOLEAN` |
| `allocations` | `id TEXT PK`, `flow_id UUID`, `storage_id TEXT`, `expires_at TIMESTAMPTZ`, `created_at TIMESTAMPTZ`, `deleting BOOLEAN` |
| `segments` | `object_id TEXT NOT NULL REFERENCES objects(id)` (unchanged), new index `segments_object_id_idx` |

`metastore.ExpectedSchemaVersion` = 6 (`internal/metastore/schema.go:23`). Change the comment "Current head is migration 000005 (objects_storage_id_reaping)" to "Current head is migration 000006 (allocations_and_deleting)".

## 4. Allocation at `POST /flows/{flowId}/storage`

### 4.1 Service flow (`internal/service/storage/storage.go`)

1. `GetFlow`. Missing flow: 404. Read-only flow: 403 `read-only`. (No change.)
2. `validateAllocateSize`. (No change.)
3. Make the ID list: mode `limit` generates N UUIDv4 values. Mode `object_ids` uses the supplied list.
4. For each ID, in request sequence, call `GenerateUploadURL`. On an error, return it (BR-GC-05).
5. Call `store.AllocateObjects(ctx, metastore.AllocateParams{FlowID, StorageID, TTL: cfg.ObjectStorePresignExpiry, IDs, Requested: mode == object_ids})`.
6. Map the store errors (table in 4.3). Return the objects in request sequence.

The interface `storage.FlowReader` changes: remove `IsObjectRegistered`, add `AllocateObjects`. Remove `metastore.IsObjectRegistered` if no other code calls it.

The storage service needs the configured storage ID. Add a constructor parameter `storageID string`, and pass the same value that `serve.go` gives to the segment service (`controlledStorageID`, `cmd/opentams/serve.go:163-176`).

### 4.2 Store method (`internal/metastore/allocations.go`)

```go
type AllocateParams struct {
	FlowID    uuid.UUID
	StorageID string
	TTL       time.Duration
	IDs       []string
	Requested bool // true: mode object_ids (BR-GC-03); false: mode limit (BR-GC-02)
}

var (
	ErrObjectExists    = errors.New("metastore: object already exists")
	ErrObjectDeleting  = errors.New("metastore: object is being deleted")
	ErrObjectWrongFlow = errors.New("metastore: object is allocated to a different flow")
)

func (s *PostgresStore) AllocateObjects(ctx context.Context, p AllocateParams) error
```

Each returned error wraps a sentinel and names the ID: `fmt.Errorf("metastore: AllocateObjects: object %q: %w", id, ErrObjectExists)`.

Algorithm, in one transaction:

1. Sort a copy of `p.IDs` in ascending sequence (BR-GC-06).
2. If `p.Requested` is false, write all rows with one statement:

   ```sql
   INSERT INTO allocations (id, flow_id, storage_id, expires_at)
   SELECT unnest($1::text[]), $2, $3, now() + make_interval(secs => $4)
   ```

   `$4` is `p.TTL.Seconds()`.
3. If `p.Requested` is true, do these statements for each ID X in sorted sequence:

   ```sql
   -- (a) BR-GC-03 rows 1 and 2
   SELECT deleting FROM objects WHERE id = $1 FOR SHARE;
   ```

   A row with `deleting = true` gives `ErrObjectDeleting`. A row with `deleting = false` gives `ErrObjectExists`. No row: continue.

   ```sql
   -- (b) BR-GC-03 rows 5 and 6
   INSERT INTO allocations (id, flow_id, storage_id, expires_at)
   VALUES ($1, $2, $3, now() + make_interval(secs => $4))
   ON CONFLICT (id) DO UPDATE
       SET expires_at = EXCLUDED.expires_at
       WHERE allocations.flow_id = EXCLUDED.flow_id
         AND allocations.deleting = false
   RETURNING id;
   ```

   A returned row: continue. No returned row: the conflicting row is locked by `ON CONFLICT`. Read it:

   ```sql
   -- (c) BR-GC-03 rows 3 and 4
   SELECT deleting FROM allocations WHERE id = $1;
   ```

   `deleting = true` gives `ErrObjectDeleting`. In the other case, give `ErrObjectWrongFlow`.
4. On the first error, roll back and return the error (BR-GC-04). Else commit.

### 4.3 Error mapping in the storage service

| Store error | `apperror` code | HTTP |
|---|---|---|
| `metastore.ErrObjectExists` | `ErrObjectExists` (`object-exists`) | 400 |
| `metastore.ErrObjectDeleting` | `ErrObjectDeleting` (`object-deleting`) | 400 |
| `metastore.ErrObjectWrongFlow` | `ErrObjectWrongFlow` (`object-wrong-flow`) | 400 |
| other | unchanged (500) | 500 |

The handler (`internal/httpx/handlers/storage.go:40-46`) returns the `*apperror.AppError` as the error value. The error middleware renders it with its catalogued status (`internal/httpx/middleware/middleware.go:59-72`). Change the log level of this path from ERROR to WARN for an `*apperror.AppError` with status 400.

## 5. Segment registration (`InsertSegments`)

Steps 1 (flow lock), 2 (overlap checks), 3.5 (object timerange), 4 (segment insert), and 5 (flow refresh) of `internal/metastore/segments.go:190-330` do not change, with one exception: step 4 inserts only the accepted segments. Step 3 changes as follows.

### 5.1 Groups

1. Make groups of the segments by `object_id`. A group is controlled if one or more of its segments has no `get_urls`. Else it is BYOS. n is the number of segments in the group.
2. Sort the groups by `object_id` in ascending sequence.
3. Process each group with 5.2 (controlled) or 5.3 (BYOS).
4. If a group gets a rejection, add each segment index of the group to `InsertResult.RejectedIndices`, with a `RejectReason{Type: <problem type URI>, Detail: <text>}` at the same position. Do not write the segments of a rejected group. Add the other indices to `AcceptedIndices` after step 4.

`Type` is the full URI, for example `https://github.com/amagioss/opentams/problems/object-not-allocated`. The service copies it to `FailedSegment.Type` (`internal/service/segment/segment.go:215-240`). Set the title from the `apperror` catalogue and the status to 400.

### 5.2 Controlled group X with count n (BR-GC-12, BR-GC-13)

```sql
-- (1)
SELECT deleting FROM objects WHERE id = $1 FOR UPDATE;
```

- Row with `deleting = false`:

  ```sql
  UPDATE objects SET ref_count = ref_count + $2 WHERE id = $1;
  ```

  Accept.
- Row with `deleting = true`: reject, `object-deleting`.
- No row:

  ```sql
  -- (2)
  SELECT flow_id, storage_id, deleting FROM allocations WHERE id = $1 FOR UPDATE;
  ```

  - No row: do (1) one more time. If a row now exists, use the two cases above. Else reject, `object-not-allocated`.
  - `deleting = true`: reject, `object-deleting`.
  - `flow_id` not equal to the request flow: reject, `object-wrong-flow`.
  - Else:

    ```sql
    -- (3)
    DELETE FROM allocations WHERE id = $1;
    INSERT INTO objects (id, ref_count, storage_id, deleting)
    VALUES ($1, $2, $3, false)
    ON CONFLICT (id) DO NOTHING
    RETURNING id;
    ```

    `$3` is the `storage_id` of the allocation row. A returned row: accept. No returned row: do (1) one more time and use the first two cases.

### 5.3 BYOS group X with count n (BR-GC-14)

```sql
SELECT deleting FROM allocations WHERE id = $1 FOR SHARE;
```

A row with `deleting = true`: reject, `object-deleting`. Else:

```sql
INSERT INTO objects (id, ref_count, storage_id, deleting)
VALUES ($1, $2, NULL, false)
ON CONFLICT (id) DO UPDATE
    SET ref_count = objects.ref_count + EXCLUDED.ref_count
    WHERE objects.deleting = false
RETURNING id;
```

No returned row: reject, `object-deleting`. Else accept.

### 5.4 Removed code

Remove the per-segment upsert loop and the `ErrSegmentObjectReaping` path (`internal/metastore/segments.go:230-264`). Remove the mapping in `internal/service/segment/segment.go:111-112` and `:200`, and the 409 case in `internal/httpx/handlers/segments.go:302-305`.

## 6. Deletes

### 6.1 `DeleteFlow` (`internal/metastore/flows.go:433-468`, BR-GC-20)

```sql
-- (1)
SELECT 1 FROM flows WHERE id = $1 FOR UPDATE;
-- no row: return apperror ErrNotFound "flow not found" (no change to the error)

-- (2)
WITH deleted AS (
    DELETE FROM segments WHERE flow_id = $1 RETURNING object_id
)
SELECT object_id, count(*) FROM deleted GROUP BY object_id ORDER BY object_id;

-- (3) lock in ascending sequence, then decrease (BR-GC-23)
SELECT id FROM objects WHERE id = ANY($1::text[]) ORDER BY id FOR UPDATE;
UPDATE objects AS o
   SET ref_count = o.ref_count - d.n
  FROM unnest($1::text[], $2::int[]) AS d(id, n)
 WHERE o.id = d.id;

-- (4)
DELETE FROM flows WHERE id = $1;
```

Do steps (3) only if step (2) returned one or more rows. `$1` and `$2` of step (3) are parallel arrays in ascending ID sequence. The cascade of step (4) deletes `flow_tags` and other child rows as today. It finds no segment rows.

### 6.2 `DeleteSegmentsByTimerange` (`internal/metastore/segments.go:505-591`, BR-GC-21)

Keep all steps. Change the decrease loop (`:572-580`) to step (3) of 6.1, with the counts of the `GROUP BY`.

## 7. Sweep 1 (`internal/metastore/gc.go`, `internal/gc/sweep_released.go`)

### 7.1 `ClaimReleasedObjects(ctx, storageID, limit)`, one transaction

```sql
-- (1) candidates, BR-GC-30
SELECT id, storage_id FROM objects
WHERE ref_count = 0 AND deleting = false
  AND (storage_id IS NULL OR storage_id = $1)
ORDER BY id
LIMIT $2
FOR UPDATE SKIP LOCKED;

-- (2) segment-count check, BR-GC-33 ($1 = all candidate IDs)
SELECT object_id, count(*) FROM segments
WHERE object_id = ANY($1::text[])
GROUP BY object_id;

-- (2b) drift repair, only for IDs with a count
UPDATE objects AS o SET ref_count = d.n
  FROM unnest($1::text[], $2::int[]) AS d(id, n)
 WHERE o.id = d.id;

-- (3) BYOS row-only delete, BR-GC-34 ($1 = BYOS candidates without drift)
DELETE FROM objects WHERE id = ANY($1::text[]);

-- (4) claim, BR-GC-32 ($1 = controlled candidates without drift)
UPDATE objects SET deleting = true WHERE id = ANY($1::text[]);
```

Return `ClaimResult{Selected: rows of (1), Claimed: IDs of (4), RowOnly: IDs of (3), Drift: rows of (2)}`. Skip a statement if its ID list is empty.

### 7.2 `ListRetryObjects(ctx, storageID, afterID, limit)`

```sql
SELECT id FROM objects
WHERE deleting = true AND storage_id = $1 AND id > $2
ORDER BY id
LIMIT $3;
```

### 7.3 `DeleteClaimedObjects(ctx, ids)`

```sql
DELETE FROM objects WHERE id = ANY($1::text[]) AND deleting = true
RETURNING id;
```

Return the IDs of the deleted rows.

### 7.4 `ReleasedBacklog(ctx, storageID)`

```sql
SELECT
  (SELECT count(*) FROM objects WHERE ref_count = 0 AND deleting = false
      AND (storage_id IS NULL OR storage_id = $1))                     AS pending,
  (SELECT count(*) FROM objects WHERE deleting = true)                 AS retrying,
  (SELECT count(*) FROM objects WHERE ref_count = 0 AND deleting = false
      AND storage_id IS NOT NULL AND storage_id <> $1)                 AS unknown_storage;
```

## 8. Sweep 2 (`internal/metastore/gc.go`, `internal/gc/sweep_allocation.go`)

### 8.1 `ClaimExpiredAllocations(ctx, storageID, grace, limit)`, one transaction

```sql
-- (1) candidates, BR-GC-40
SELECT id FROM allocations
WHERE deleting = false
  AND storage_id = $1
  AND expires_at < now() - make_interval(secs => $2)
ORDER BY expires_at, id
LIMIT $3
FOR UPDATE SKIP LOCKED;

-- (2) object-row check, BR-GC-42
SELECT id FROM objects WHERE id = ANY($1::text[]);

-- (3) leftover allocations, row-only ($1 = IDs found in (2))
DELETE FROM allocations WHERE id = ANY($1::text[]);

-- (4) claim ($1 = the other candidates)
UPDATE allocations SET deleting = true WHERE id = ANY($1::text[]);
```

Return `ClaimResult{Selected, Claimed: IDs of (4), RowOnly: IDs of (3)}`.

### 8.2 `ListRetryAllocations(ctx, storageID, afterID, limit)`

```sql
SELECT id FROM allocations
WHERE deleting = true AND storage_id = $1 AND id > $2
ORDER BY id
LIMIT $3;
```

### 8.3 `DeleteClaimedAllocations(ctx, ids)`

```sql
DELETE FROM allocations WHERE id = ANY($1::text[]) AND deleting = true
RETURNING id;
```

Return the IDs of the deleted rows.

### 8.4 `AllocationBacklog(ctx, storageID, grace)`

```sql
SELECT
  (SELECT count(*) FROM allocations WHERE deleting = false AND storage_id = $1
      AND expires_at < now() - make_interval(secs => $2))               AS pending,
  (SELECT count(*) FROM allocations WHERE deleting = true)              AS retrying,
  (SELECT count(*) FROM allocations WHERE deleting = false AND storage_id <> $1
      AND expires_at < now() - make_interval(secs => $2))               AS unknown_storage,
  (SELECT count(*) FROM allocations)                                    AS rows,
  (SELECT COALESCE(EXTRACT(EPOCH FROM (now() - make_interval(secs => $2) - min(expires_at))), 0)
     FROM allocations WHERE deleting = false AND storage_id = $1
      AND expires_at < now() - make_interval(secs => $2))               AS oldest_overdue;
```

## 9. Worker algorithm (`internal/gc`)

### 9.1 One cycle of one sweep

```text
cycle(sweep):
    result := CycleResult{}

    # Retry phase, BR-GC-64
    after := ""
    loop:
        ids, err := List<Retry>(ctx_db, StorageID, after, BatchSize)
        if err: batchError(sweep, "retry_select", err); result.BatchError = true; return result
        if len(ids) == 0: break
        if deleteAndRemove(sweep, ids, &result) is a batch-level error: result.BatchError = true; return result
        after = last(ids)
        if len(ids) < BatchSize: break

    # Claim phase, BR-GC-61
    loop:
        if stop is closed: return result
        start := now
        claim, err := Claim<Sweep>(ctx_db, ...)
        if err: batchError(sweep, "claim", err); result.BatchError = true; return result
        record claim.RowOnly    (INFO log line and items_total for each ID)
        record claim.Drift      (WARN log line and refcount_drift_total for each record; sweep 1 only)
        if len(claim.Claimed) > 0:
            if deleteAndRemove(sweep, claim.Claimed, &result) is a batch-level error:
                result.BatchError = true; return result
        set last_success_timestamp_seconds{sweep} = now
        observe batch_duration_seconds{sweep} = now - start
        if claim.Selected < BatchSize: break

    update the gauges from <Sweep>Backlog (an error here: errors_total{stage="gauge"}, WARN, no stop)
    write the INFO cycle summary
    return result
```

### 9.2 `deleteAndRemove(sweep, ids)`

```text
results, err := Deleter.DeleteObjects(ctx_s3, ids)        # ctx_s3 has s3Timeout
if err != nil:                                            # ErrAuth for the call, or a cancellation
    batchError(sweep, "s3_delete", err); return batch-level error

ok, failed := split results by Error == nil
for each failed result:
    errors_total{sweep, stage="s3_delete"}++
    WARN log line (sweep, object_id, error)
    result.Failed++

if len(ok) > 0:
    removed, err := DeleteClaimed<Rows>(ctx_db, ok)
    if err != nil: batchError(sweep, "row_delete", err); return batch-level error
    for each ID in removed:
        INFO log line (object_id, storage_id, sweep, outcome="deleted")
        items_total{sweep, outcome="deleted"}++
        result.Deleted++

if one failed result wraps objectstore.ErrAuth: return batch-level error        # BR-GC-62, second case
if len(ok) == 0:                                                               # BR-GC-62, third case
    return batch-level error
return no error
```

The row delete for the successful IDs occurs before the method returns a batch-level error. Thus the IDs that the object store deleted do not wait for the next cycle.

The INFO line and the `deleted` counter apply only to the IDs that the row delete returned (`removed`). A different replica can remove a row first (BR-GC-65). Then that ID is not in `removed`, and this replica does not count it.

### 9.3 `batchError(sweep, stage, err)`

Increment `opentams_gc_errors_total{sweep, stage}`. Write an ERROR log line with `sweep`, `stage`, and `error`.

### 9.4 Loops

```text
Run(ctx, stop):
    start goroutine loop(SweepReleased) and goroutine loop(SweepAllocation)
    wait for both
    return nil

loop(sweep):
    repeat:
        if stop is closed: return
        cycle(sweep)
        select: stop closed -> return; ctx done -> return; after PollInterval -> continue

RunOnce(ctx):
    run cycle(SweepReleased) and cycle(SweepAllocation) at the same time, wait for both
    return Report{...}, nil
```

The first cycle starts immediately when the process starts.

### 9.5 Metrics (`internal/gc/metrics.go`)

Register the metrics on `Registry.NamespacedRegisterer()` (`pkg/metrics/metrics.go:94`) with `Subsystem: "gc"`. The namespace `opentams` comes from the registry. The full names are in the interface specification, section 4.

## 10. The `gc` command (`cmd/opentams/gc.go`)

```text
opentams gc [--once]
```

1. Load and validate the configuration (`config.Load`). An error: exit code 1.
2. Make the logger, the metrics registry, the database pool, and the object store as `serve.go` does (`cmd/opentams/serve.go:99-138`). Move the shared code to a function that both commands call. Do not copy it.
3. Call `metastore.VerifySchema`. An error: exit code 1.
4. Make the `gc.Worker` with `gc.Config` from the configuration, `metastore.New(pool)` as `Store`, and the `*objectstore.S3Store` as `Deleter`.
5. With `--once`: call `RunOnce`. Write the report as one INFO line. Exit code 1 if `Released.BatchError` or `Allocation.BatchError` is true. Else exit code 0. Open no listener.
6. Without `--once`:
   1. Make `health.New(health.Options{DB: ms, Object: obj})` (as `serve.go:180`).
   2. Make a gin engine with three routes:
      - `GET /healthz`: `health.HealthzHandler()`.
      - `GET /readyz`: 503 after the shutdown signal, else `health.ReadyzHandler(checker)`.
      - `GET /metrics`: `gin.WrapH(metricsep.Handler(reg.Gatherer()))` (as `internal/server/server.go:152`).
   3. Listen on `SERVER_PORT` with `ReadHeaderTimeout` 10 s.
   4. Start `Worker.Run(ctx, stop)`.
   5. On SIGTERM or SIGINT: close `stop`, set the shutdown flag, and start a timer of `SERVER_GRACEFUL_SHUTDOWN_PERIOD` that cancels `ctx`.
   6. When `Run` returns, shut down the listener and stop with exit code 0.

The `Long` help text of the command must state:

- The two sweeps.
- That the command runs until SIGTERM.
- The `--once` behaviour and its exit codes.
- The settings `GC_POLL_INTERVAL`, `GC_BATCH_SIZE`, `GC_ALLOCATION_GRACE`, `OPENTAMS_OBJECT_STORE_ID`, and the database and object-store settings.

Remove the old text about flow delete requests, CronJob, and sidecar. Remove `errGCNotImplemented`.

## 11. Configuration (`internal/config/config.go`)

| Variable | Field | Change |
|---|---|---|
| `GC_POLL_INTERVAL` | `GCPollInterval` | Default 5m → 15m. Validate: more than 0. |
| `GC_BATCH_SIZE` | `GCBatchSize` | Default 100 → 1000. Validate: 1 to 1000. |
| `GC_ALLOCATION_GRACE` | `GCAllocationGrace` (new) | Default 5m. Validate: 0 or more. |
| `OBJECT_STORE_PRESIGN_EXPIRY` | `ObjectStorePresignExpiry` | Validate: 300 s or more (BR-GC-09). |
| `OPENTAMS_OBJECT_STORE_ID` | `ObjectStoreID` (new) | Move the read from `cmd/opentams/serve.go:168-171` into `config.Load`. Default `default`. |

Each validation error uses the existing style: `fmt.Errorf("GC_BATCH_SIZE: must be between 1 and 1000, got %d", c.GCBatchSize)`.

## 12. Helm chart (`deployments/helm/opentams`)

### 12.1 Values (`values.yaml` and `values.schema.json`)

```yaml
gc:
  enabled: true
  replicaCount: 1
  pollInterval: 15m
  batchSize: 1000
  allocationGrace: 5m
  resources: {}
```

Add the same keys to `values.schema.json`, with these rules:

| Key | Rule |
|---|---|
| `enabled` | boolean |
| `replicaCount` | integer, minimum 0 |
| `batchSize` | integer, 1 to 1000 |
| `pollInterval`, `allocationGrace` | string |

### 12.2 Labels

GC pods must not match the selector of the API Service. Add a helper:

```yaml
{{- define "opentams.gcSelectorLabels" -}}
app.kubernetes.io/name: {{ include "opentams.name" . }}-gc
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}
```

Do not change `opentams.selectorLabels`. A change to the selector of an existing Deployment is not permitted by Kubernetes.

### 12.3 New templates (each one only if `.Values.gc.enabled`)

| File | Content |
|---|---|
| `gc-deployment.yaml` | Deployment `<fullname>-gc` with these settings: `replicas: .Values.gc.replicaCount`, the same image, service account, security contexts, env (`opentams.env`), and `/tmp` volume as `deployment.yaml`, and `args: ["gc"]`. Add the env values `GC_POLL_INTERVAL`, `GC_BATCH_SIZE`, and `GC_ALLOCATION_GRACE` from `.Values.gc`. Add port `http` (`.Values.server.port`), the liveness probe `/healthz`, the readiness probe `/readyz` (values from `.Values.probes`), and `resources: .Values.gc.resources`. No startup probe. No ingress. |
| `gc-service.yaml` | Service `<fullname>-gc`, type ClusterIP, port `http`, selector `opentams.gcSelectorLabels`. |
| `gc-servicemonitor.yaml` | Only if `.Values.serviceMonitor.enabled`. ServiceMonitor `<fullname>-gc`, selector `opentams.gcSelectorLabels`, endpoint `port: http`, `path: /metrics`, interval and timeout from `.Values.serviceMonitor`. |
| `gc-networkpolicy.yaml` | Only if `.Values.networkPolicy.enabled`. Ingress only from `.Values.networkPolicy.monitoringNamespaceSelector` on port `.Values.server.port`. |

Update `NOTES.txt:47`: the GC worker is deployed when `gc.enabled` is true.

## 13. Changes outside `internal/gc`

Make each change in the same pull request as the code. The column "Rule" names the reason.

| File | Line | Change | Rule |
|---|---|---|---|
| `migrations/000006_*` | new | Section 3 | BR-GC-50, BR-GC-51 |
| `migrations/migrations_test.go` | new tests | Tests for 000006 (test plan). Do not change TC-MIG-05. | BR-GC-50 |
| `internal/metastore/schema.go` | 22-23 | `ExpectedSchemaVersion = 6`, comment | BR-GC-52 |
| `internal/metastore/allocations.go` | new | Section 4.2 | BR-GC-01 to BR-GC-06 |
| `internal/metastore/gc.go` | new | Sections 7 and 8 | BR-GC-30 to BR-GC-46 |
| `internal/metastore/segments.go` | 96-124 | Remove `ErrSegmentObjectReaping`. Change comments from `reaping` to `deleting`. | BR-GC-74 |
| `internal/metastore/segments.go` | 224-264 | Section 5 | BR-GC-10 to BR-GC-17 |
| `internal/metastore/segments.go` | 284-311 | Insert only the accepted segments | BR-GC-15 |
| `internal/metastore/segments.go` | 572-580 | Section 6.2 | BR-GC-21 |
| `internal/metastore/segments.go` | 791-800 | Remove `IsObjectRegistered` if it has no other caller | BR-GC-03 |
| `internal/metastore/flows.go` | 425-468 | Section 6.1, and the comment | BR-GC-20 |
| `internal/apperror/apperror.go` | 27, 34, 59, 66 | Remove `ErrObjectIDExists` and `ErrObjectReaping`. Add `ErrObjectExists`, `ErrObjectNotAllocated`, `ErrObjectWrongFlow`, `ErrObjectDeleting`, each 400, with the titles of BR-GC-70 to BR-GC-73. | BR-GC-70 to BR-GC-74 |
| `internal/apperror/apperror_test.go` | 26, 109 | Update the tables | BR-GC-70 |
| `internal/service/storage/storage.go` | 18-24, 79-81, 135-160 | Section 4.1 | BR-GC-01 to BR-GC-08 |
| `internal/service/segment/segment.go` | 111-112, 200 | Remove the `ErrSegmentObjectReaping` cases | BR-GC-74 |
| `internal/httpx/handlers/segments.go` | 302-305 | Remove the 409 `object-reaping` case | BR-GC-16 |
| `internal/httpx/handlers/storage.go` | 40-46 | WARN, not ERROR, for a 400 `AppError` | BR-GC-70 |
| `internal/httpx/handlers/root.go` | 28-32 | Set `MinPresignedUrlTimeout` | BR-GC-09 |
| `internal/config/config.go` | 107, 144-147, validation | Section 11 | BR-GC-09, NFR-GC-P2, P3 |
| `cmd/opentams/serve.go` | 99-138, 163-176 | Move the shared setup to a function. Read the storage ID from the configuration. Pass it to the storage service. | Section 10 |
| `cmd/opentams/gc.go` | all | Section 10 | BR-GC-60 to BR-GC-69 |
| `tools/scaletest/loader/dbload.go` | 314, 531 | Column `reaping` → `deleting` | BR-GC-50 |
| `deployments/helm/opentams/*` | — | Section 12 | NFR-GC-S4 |
| `docs/requirements.md` | §6.3.1 (436-460) | Add `object-exists`, `object-not-allocated`, `object-wrong-flow`, `object-deleting`. Remove `object-id-exists`. | BR-GC-70 to BR-GC-74 |
| `docs/requirements.md` | §4.11 status note | Remove "not implemented" | — |
| `docs/configuration.md` | 97-110 | New defaults, `GC_ALLOCATION_GRACE`, `OPENTAMS_OBJECT_STORE_ID`, the 300 s minimum. Remove "not implemented" and "reconciliation". | Section 11 |
| `docs/conformance.md` | `GET /service`, `POST …/segments`, `POST …/storage` rows | `min_presigned_url_timeout` advertised. Per-segment `object-*` errors. Allocation enforcement. Divergence: upstream defines no allocation enforcement. | BR-GC-09, BR-GC-12 |
| `docs/architecture/metadata-model.md` | 121-131 | `deleting` in place of `reaping`. Add the `allocations` table. | BR-GC-50 |
| `docs/design/internal-metastore/functional-design/business-rules.md` | BR-META-07, BR-META-15 | BR-META-07: the new step 3 and the `deleting` name. BR-META-15: `DeleteFlow` takes the flow lock. | BR-GC-12, BR-GC-20 |
| `docs/design/internal-objectstore/functional-design/business-rules.md` | BR-OBJ-04 | `reaping` → `deleting` | BR-GC-50 |
| `docs/design/internal-service-storage/**` | all `IsObjectRegistered` and `ErrObjectIDExists` text | Section 4 | BR-GC-03 |
| `docs/design/internal-metastore/nfr-requirements/tech-stack-decisions.md` | 55 | `ErrObjectIDExists` → `ErrObjectExists` | BR-GC-70 |
| `docs/design/internal-service-segment/functional-design/functional-design.md` | `reaping` text | Per-segment `object-*` rejections | BR-GC-15 |
| `docs/design/internal-httpx-handlers/functional-design/functional-design.md` | `reaping` text | Remove the 409 path | BR-GC-16 |
| `docs/adr/0037-object-garbage-collection.md` | status | `accepted` when the code merges | — |
| `docs/adr/0041-track-storage-allocations-in-a-table.md` | status | `accepted` when the code merges | — |
| `docs/adr/0019-lazy-object-registration.md`, `0020-*.md`, `0040-*.md` | `reaping` text | Do not change the substance of an accepted ADR. Add only a link: "The column is `deleting` since migration 000006 (ADR-0041)." | ADR README |

The API contract (`api/opentams-api-v1.yaml`) does not change. The 400 responses use the generic `BadRequest` response. The per-segment errors use the existing `flow-segment-bulk-failure` body. The 409 response of `POST …/segments` already describes only the idempotency conflict. Thus `make api-bundle` and `make api-gen` are not necessary.
