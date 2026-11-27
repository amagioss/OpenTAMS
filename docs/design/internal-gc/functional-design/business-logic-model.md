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
| `internal/gc/gc.go` | `Config`, `Worker`, `New`, `Run`, `RunOnce`, `Report`, and the cycle algorithm of section 9. The two sweeps use the same algorithm. |
| `internal/gc/sweep_released.go` | Sweep 1: the `Store` methods, the row-only outcome, and the gauges that the algorithm uses |
| `internal/gc/sweep_allocation.go` | Sweep 2: the `Store` methods, the row-only outcome, and the gauges that the algorithm uses |
| `internal/gc/metrics.go` | `Metrics`, `NewMetrics` |
| `internal/gc/*_test.go` | Unit tests with fakes of `Store` and `Deleter` |
| `internal/metastore/allocations.go` | `AllocateObjects` and the allocation errors |
| `internal/metastore/gc.go` | The methods of `gc.Store` on `*PostgresStore` |
| `internal/metastore/gc_test.go`, `allocations_test.go`, `segments_alloc_test.go`, `deletes_gc_test.go`, `gc_registration_race_test.go`, `gc_fixtures_test.go` | Integration tests against PostgreSQL (testcontainers) |
| `cmd/opentams/gc.go` | The `gc` command, the admin listener, the signal handling |
| `cmd/opentams/setup.go` | The configuration load, the logger, the metrics registry, the database pool, and the object store. `serve` and `gc` use these functions. |
| `cmd/opentams/gc_integration_test.go`, `gc_e2e_integration_test.go` | The tests of sections 7 and 8 of the test plan |
| `internal/objtest/objtest.go` | The MinIO container of the integration and performance tests |
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
	dbTimeout  = 30 * time.Second // BR-GC-68
	s3Timeout  = 60 * time.Second // BR-GC-68
	claimLease = 5 * time.Minute  // BR-GC-65
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
	ReclaimRetryObjects(ctx context.Context, storageID string, lease time.Duration, cycleStart time.Time, limit int) ([]string, error)
	DeleteClaimedObjects(ctx context.Context, ids []string) ([]string, error)
	ReleasedBacklog(ctx context.Context, storageID string) (Backlog, error)

	ClaimExpiredAllocations(ctx context.Context, storageID string, grace time.Duration, limit int) (ClaimResult, error)
	ReclaimRetryAllocations(ctx context.Context, storageID string, lease time.Duration, cycleStart time.Time, limit int) ([]string, error)
	DeleteClaimedAllocations(ctx context.Context, ids []string) ([]string, error)
	AllocationBacklog(ctx context.Context, storageID string, grace time.Duration) (Backlog, error)
}

// ClaimResult is the result of one claim transaction.
type ClaimResult struct {
	Selected  int           // rows that the SELECT returned; drives the drain rule
	Claimed   []string      // IDs set to deleting = true, in ascending byte order
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

// NewMetrics registers the metrics of interface spec section 4 on reg.
// cmd/opentams passes Registry.NamespacedRegisterer() (section 9.5).
func NewMetrics(reg prometheus.Registerer) (*Metrics, error)

// Run starts the two loops and blocks. When stop is closed, the loops
// claim no new batch and return after the current batch. ctx is the hard
// limit: cmd/opentams cancels it SERVER_GRACEFUL_SHUTDOWN_PERIOD after stop.
// Run returns nil after a shutdown.
func (w *Worker) Run(ctx context.Context, stop <-chan struct{}) error

// RunOnce does one cycle of each sweep at the same time and returns.
func (w *Worker) RunOnce(ctx context.Context) (Report, error)
```

`New` returns an error if `BatchSize` is outside 1 to 1000, if `PollInterval` is 0 or less, if `AllocationGrace` is less than 0, or if `StorageID` is empty.

`Worker` has the unexported fields `dbTimeout`, `s3Timeout`, `claimLease`, and `now func() time.Time`. `New` sets them from the constants and from `time.Now`. Only tests in package `gc` change them. All times of the worker come from `w.now()`: the cycle start, the claim time, the lease check, `last_success_timestamp_seconds`, and the batch duration.

## 3. Schema

### 3.1 Migration 000006 (`000006_allocations_and_deleting.up.sql`)

```sql
-- BR-GC-50. Rename the GC claim flag. PostgreSQL updates the predicates
-- of objects_gc_claim_idx and objects_gc_retry_idx automatically.
ALTER TABLE objects RENAME COLUMN reaping TO deleting;

-- BR-GC-65. Time of the claim. NULL while deleting = false.
ALTER TABLE objects ADD COLUMN IF NOT EXISTS claimed_at TIMESTAMPTZ;
UPDATE objects SET claimed_at = now() WHERE deleting = true;

-- BR-GC-01. One row for each object ID with an issued upload URL that no
-- segment has registered. No foreign key to flows (BR-GC-08).
CREATE TABLE IF NOT EXISTS allocations (
    id          TEXT        PRIMARY KEY,
    flow_id     UUID        NOT NULL,
    storage_id  TEXT        NOT NULL,
    expires_at  TIMESTAMPTZ NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleting    BOOLEAN     NOT NULL DEFAULT false,
    claimed_at  TIMESTAMPTZ
);

-- Sweep 2 claim (BR-GC-40).
CREATE INDEX IF NOT EXISTS allocations_gc_claim_idx
    ON allocations (expires_at, id) WHERE deleting = false;

-- Sweep 2 retry reclaim (BR-GC-64).
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
ALTER TABLE objects DROP COLUMN IF EXISTS claimed_at;
ALTER TABLE objects RENAME COLUMN deleting TO reaping;
```

### 3.2 Schema after the change

| Table | Columns that the GC uses |
|---|---|
| `objects` | `id TEXT PK`, `ref_count INT`, `storage_id TEXT NULL`, `deleting BOOLEAN`, `claimed_at TIMESTAMPTZ NULL` |
| `allocations` | `id TEXT PK`, `flow_id UUID`, `storage_id TEXT`, `expires_at TIMESTAMPTZ`, `created_at TIMESTAMPTZ`, `deleting BOOLEAN`, `claimed_at TIMESTAMPTZ NULL` |
| `segments` | `object_id TEXT NOT NULL REFERENCES objects(id)` (unchanged), new index `segments_object_id_idx` |

Invariant of `objects` and `allocations`: `deleting = true` if and only if `claimed_at IS NOT NULL`. Only the GC sets the two values, and it sets them in the same statement.

`metastore.ExpectedSchemaVersion` = 6 (`internal/metastore/schema.go:23`). Change the comment "Current head is migration 000005 (objects_storage_id_reaping)" to "Current head is migration 000006 (allocations_and_deleting)".

## 4. Allocation at `POST /flows/{flowId}/storage`

### 4.1 Service flow (`internal/service/storage/storage.go`)

1. `GetFlow`. Missing flow: 404. Read-only flow: 403 `read-only`. (No change.)
2. `validateAllocateSize`. (No change.)
3. Make the ID list: mode `limit` generates N UUIDv4 values. Mode `object_ids` uses the supplied list.
4. For each ID, in request sequence, call `GenerateUploadURL`. On an error, return it (BR-GC-05).
5. Call `store.AllocateObjects(ctx, metastore.AllocateParams{FlowID, StorageID: s.storageID, TTL: s.allocTTL, IDs, Requested: mode == object_ids})`.
6. Map the store errors (table in 4.3). Return the objects in request sequence.

The interface `storage.FlowReader` changes: remove `IsObjectRegistered`, add `AllocateObjects`. Remove `metastore.IsObjectRegistered` if no other code calls it.

The storage service needs the configured storage ID and the presign time. The constructor (`internal/service/storage/storage.go:81`) becomes `New(store FlowReader, obj ObjectStore, m *service.AppServiceMetrics, storageID string, allocTTL time.Duration) StorageService`. `serve.go` (`cmd/opentams/serve.go:178`) passes `cfg.ObjectStoreID` (section 11) and `cfg.ObjectStorePresignExpiry`.

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

// ObjectError carries the object ID of an allocation error.
type ObjectError struct {
	ID  string
	Err error // one of the three sentinels
}

func (e *ObjectError) Error() string { return fmt.Sprintf("metastore: object %q: %v", e.ID, e.Err) }
func (e *ObjectError) Unwrap() error { return e.Err }

func (s *PostgresStore) AllocateObjects(ctx context.Context, p AllocateParams) error
```

Each allocation error is `&ObjectError{ID: id, Err: <sentinel>}`. `errors.Is(err, ErrObjectExists)` works through `Unwrap`.

Algorithm, in one transaction:

1. Sort a copy of `p.IDs` in ascending byte order with `slices.Sort` (BR-GC-06).
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

The service gets the ID with `errors.As(err, &oe)` (`oe *metastore.ObjectError`). It makes the detail text of BR-GC-70 to BR-GC-73 with `oe.ID`.

The handler (`internal/httpx/handlers/storage.go:40-46`) returns the `*apperror.AppError` as the error value. The error middleware renders it with its catalogued status (`internal/httpx/middleware/middleware.go:59-72`). Change the log level of this path from ERROR to WARN for an `*apperror.AppError` with status 400.

## 5. Segment registration (`InsertSegments`)

The steps of `InsertSegments` are in `internal/metastore/segments.go`: step 1 (flow lock), step 2 (overlap checks), step 3 (object upsert, `:226-262`), step 3.5 (object timerange, `:264-299`), step 4 (segment insert, `:301-333`), and step 5 (flow refresh, `:335-338`). Steps 1 and 2 do not change. Step 3 changes as in 5.1 to 5.3. These changes apply to the other steps:

- If no segment is accepted after step 3, roll back the transaction. Return `InsertResult{RejectedIndices, RejectReasons}` with a nil error. Do not do steps 3.5, 4, and 5 (BR-GC-15).
- Step 3.5 examines only the accepted segments (BR-GC-15).
- Step 4 inserts only the accepted segments.

### 5.1 Groups

1. Make groups of the segments by `object_id`. A group is controlled if one or more of its segments has no `get_urls`. Else it is BYOS. n is the number of segments in the group (BR-GC-11).
2. Sort the groups by `object_id` in ascending byte order with `slices.Sort` (BR-GC-23).
3. Process each group with 5.2 (controlled) or 5.3 (BYOS).
4. If a group gets a rejection, add each segment index of the group to `InsertResult.RejectedIndices`, with a `RejectReason{Code: <apperror code>, Detail: <text>}` at the same position. Do not write the segments of a rejected group. Add the other indices to `AcceptedIndices` after step 4.

Add the field `Code apperror.ErrorCode` to `RejectReason` (`internal/metastore/segments.go:66-69`). The metastore already imports `internal/apperror`. The store sets `Code` (`apperror.ErrObjectNotAllocated`, `ErrObjectWrongFlow`, or `ErrObjectDeleting`) and `Detail`. It leaves `Type` empty. In the service (`internal/service/segment/segment.go:212-240`), if `Code` is set, take `Type` and `Title` from `apperror.New(Code, Detail).ToProblemDetails("", "")`. Else keep the current behaviour. `FailedSegment.Status` stays 400. The response does not show it (BR-GC-15).

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

A row with `deleting = true`: reject, `object-deleting`. Else (`$2` is n):

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
SELECT read_only FROM flows WHERE id = $1 FOR UPDATE;
-- no row: return apperror.New(apperror.ErrNotFound, "flow not found") (no change)
-- read_only = true: return apperror.New(apperror.ErrReadOnly, "flow is read-only")

-- (2)
WITH deleted AS (
    DELETE FROM segments WHERE flow_id = $1 RETURNING object_id
)
SELECT object_id, count(*) FROM deleted GROUP BY object_id ORDER BY object_id COLLATE "C";

-- (3) lock in ascending byte order, then decrease (BR-GC-23)
SELECT id FROM objects WHERE id = ANY($1::text[]) ORDER BY id COLLATE "C" FOR UPDATE;
UPDATE objects AS o
   SET ref_count = o.ref_count - d.n
  FROM unnest($1::text[], $2::int[]) AS d(id, n)
 WHERE o.id = d.id;

-- (4)
DELETE FROM flows WHERE id = $1;
```

Do steps (3) only if step (2) returned one or more rows. `$1` and `$2` of step (3) are parallel arrays in ascending byte order. The cascade of step (4) deletes `flow_tags` and other child rows as today. It finds no segment rows.

The handler (`internal/httpx/handlers/flows.go:400-415`) does not change. It returns the `*apperror.AppError` of `ErrReadOnly` as the error value, and the error middleware renders 403 `read-only`, as for `PutFlowTag`.

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
UPDATE objects SET deleting = true, claimed_at = now() WHERE id = ANY($1::text[]);
```

Return `ClaimResult{Selected: rows of (1), Claimed: IDs of (4), RowOnly: IDs of (3), Drift: rows of (2)}`. Sort `Claimed` and `RowOnly` with `slices.Sort`. Skip a statement if its ID list is empty.

### 7.2 `ReclaimRetryObjects(ctx, storageID, lease, cycleStart, limit)`, one statement (BR-GC-64)

```sql
UPDATE objects SET claimed_at = now()
WHERE id IN (
    SELECT id FROM objects
    WHERE deleting = true
      AND storage_id = $1
      AND claimed_at < now() - make_interval(secs => $2)
      AND claimed_at < $3
    ORDER BY id
    LIMIT $4
    FOR UPDATE SKIP LOCKED)
RETURNING id;
```

`$2` is `lease.Seconds()`. `$3` is `cycleStart`. Sort the returned IDs with `slices.Sort`.

### 7.3 `DeleteClaimedObjects(ctx, ids)`, one transaction

```sql
-- (1) lock in ascending byte order (BR-GC-23)
SELECT id FROM objects WHERE id = ANY($1::text[]) AND deleting = true
ORDER BY id COLLATE "C" FOR UPDATE;

-- (2)
DELETE FROM objects WHERE id = ANY($1::text[]) AND deleting = true
RETURNING id;
```

Return the IDs of the deleted rows, sorted with `slices.Sort`.

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
UPDATE allocations SET deleting = true, claimed_at = now() WHERE id = ANY($1::text[]);
```

Return `ClaimResult{Selected, Claimed: IDs of (4), RowOnly: IDs of (3)}`. Sort `Claimed` and `RowOnly` with `slices.Sort`.

### 8.2 `ReclaimRetryAllocations(ctx, storageID, lease, cycleStart, limit)`, one statement (BR-GC-64)

The statement of 7.2, with the table `allocations` in place of `objects`.

### 8.3 `DeleteClaimedAllocations(ctx, ids)`, one transaction

The two statements of 7.3, with the table `allocations` in place of `objects`.

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
    cycleStart := w.now()
    ended := "empty"

    # Retry phase, BR-GC-64
    loop:
        if stop is closed or ctx is done: ended = "shutdown"; go to finish
        ids, err := Reclaim<Retry>(ctx_db, StorageID, claimLease, cycleStart, BatchSize)
        claimedAt := w.now()
        if err: batchError(sweep, "reclaim", err, count=true); ended = "batch_error"; go to finish
        if len(ids) > 0:
            if deleteAndRemove(sweep, ids, claimedAt, &result) is a batch-level error:
                ended = "batch_error"; go to finish
        if len(ids) < BatchSize: break

    # Claim phase, BR-GC-61
    loop:
        if stop is closed or ctx is done: ended = "shutdown"; go to finish
        start := w.now()
        claim, err := Claim<Sweep>(ctx_db, ...)
        claimedAt := w.now()
        if err: batchError(sweep, "claim", err, count=true); ended = "batch_error"; go to finish
        record claim.RowOnly    (INFO log line and items_total for each ID)
        record claim.Drift      (WARN log line and refcount_drift_total for each record; sweep 1 only)
        if len(claim.Claimed) > 0:
            if deleteAndRemove(sweep, claim.Claimed, claimedAt, &result) is a batch-level error:
                ended = "batch_error"; go to finish
        set last_success_timestamp_seconds{sweep} = w.now()
        observe batch_duration_seconds{sweep} = w.now() - start
        if claim.Selected < BatchSize: break

finish:                                                     # the only exit point, BR-GC-60
    if stop is closed or ctx is done: ended = "shutdown"
    result.BatchError = (ended == "batch_error")
    if ctx is not done:
        update the gauges from <Sweep>Backlog (an error here: errors_total{stage="gauge"}, WARN, no stop)
    write the INFO line "gc cycle completed" with ended_by = ended and the counts of result
    return result
```

`ctx_db` has `dbTimeout`. Each loop examines `stop` before each reclaim and before each claim. A batch that started completes, or `ctx` cancels it (BR-GC-67).

### 9.2 `deleteAndRemove(sweep, ids, claimedAt, &result)`

```text
if w.now() - claimedAt > claimLease / 2:                  # BR-GC-65
    WARN "gc batch skipped" (sweep, ids = len(ids), age_ms)
    return no error                                       # the rows stay; a later cycle reclaims them

results, err := Deleter.DeleteObjects(ctx_s3, ids)       # ctx_s3 has s3Timeout
if err != nil:                                            # ErrAuth for the call, or a cancellation
    batchError(sweep, "s3_delete", err, count=true); return batch-level error

ok, failed := split results by Error == nil
for each failed result:
    errors_total{sweep, stage="s3_delete"}++
    WARN "gc object delete failed" (sweep, object_id, error)
    result.Failed++

if len(ok) > 0:
    removed, err := DeleteClaimed<Rows>(ctx_db, ok)
    if err != nil: batchError(sweep, "row_delete", err, count=true); return batch-level error
    for each ID in removed:
        INFO "gc object removed" (object_id, storage_id, sweep, outcome="deleted")
        items_total{sweep, outcome="deleted"}++
        result.Deleted++

if one failed result wraps objectstore.ErrAuth:                                 # BR-GC-62, second case
    batchError(sweep, "s3_delete", that error, count=false); return batch-level error
if len(ok) == 0:                                                               # BR-GC-62, third case
    batchError(sweep, "s3_delete", the first error, count=false); return batch-level error
return no error
```

The row delete for the successful IDs occurs before the method returns a batch-level error. Thus the IDs that the object store deleted do not wait for a later cycle.

The INFO line and the `deleted` counter apply only to the IDs that the row delete returned (`removed`). A different process can remove a row first (BR-GC-65). Then that ID is not in `removed`, and this process does not count it.

### 9.3 `batchError(sweep, stage, err, count)`

Write one ERROR line `gc batch error` with `sweep`, `stage`, and `error`. If `count` is true, increment `opentams_gc_errors_total{sweep, stage}` by 1. `count` is false only when BR-GC-63 already counted each failed ID (BR-GC-62).

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

`NewMetrics(reg prometheus.Registerer) (*Metrics, error)` registers the metrics on `reg` with `Subsystem: "gc"`, in the style of `NewAppServiceMetrics` (`internal/service/metrics.go:37`). `cmd/opentams` passes `Registry.NamespacedRegisterer()` (`pkg/metrics/metrics.go:94`). The namespace `opentams` comes from the registry. The full names are in the interface specification, section 4.

## 10. The `gc` command (`cmd/opentams/gc.go`)

```text
opentams gc [--once]
```

Put the body of the command in this function, so that tests can call it:

```go
// runGC returns the exit code. stop closes on SIGTERM or SIGINT.
func runGC(ctx context.Context, cfg *config.Config, log *zap.Logger, once bool, stop <-chan struct{}) int
```

The cobra `RunE` does these steps:

- Load the configuration. An error gives exit code 1.
- Make the logger as `serve.go` does (`cmd/opentams/serve.go:71`).
- Connect the signals to `stop` and to the cancel of `ctx` (step 6.5).
- Call `runGC`.

Tests call `runGC` with a `zaptest/observer` logger and their own `stop` channel.

The steps of `runGC`:

1. (In `RunE`.) Load and validate the configuration (`config.Load`). An error: exit code 1.
2. Make the metrics registry, the database pool, and the object store as `serve.go` does (`cmd/opentams/serve.go:99-138`). Move the shared code to a function that both commands call. Do not copy it.
3. Call `metastore.VerifySchema`. An error: exit code 1.
4. Make the `gc.Worker` with `gc.Config` from the configuration, `metastore.New(pool)` as `Store`, and the `*objectstore.S3Store` as `Deleter`.
5. With `--once`: call `RunOnce`. Write the report as one INFO line. Exit code 1 if `Released.BatchError` or `Allocation.BatchError` is true. Else exit code 0. Open no listener.
6. Without `--once`:
   1. Make `health.New(health.Options{DB: ms, Object: obj})` (as `serve.go:180`).
   2. Make a gin engine with three routes:
      - `GET /healthz`: `health.HealthzHandler()`.
      - `GET /readyz`: 503 after the shutdown signal, else `health.ReadyzHandler(checker)`.
      - `GET /metrics`: `gin.WrapH(metricsep.Handler(reg.Gatherer()))` (as `internal/server/server.go:152`).
   3. Listen on `SERVER_PORT` with `ReadHeaderTimeout` 10 s. The listener always uses plain HTTP. `gc` ignores `SERVER_TLS_CERT_FILE` and `SERVER_TLS_KEY_FILE`. Rationale: the three endpoints carry no client data, and the GC pod has no certificate volume (section 12).
   4. Start `Worker.Run(ctx, stop)`.
   5. On SIGTERM or SIGINT: close `stop`, set the shutdown flag, and start a timer of `SERVER_GRACEFUL_SHUTDOWN_PERIOD` that cancels `ctx`.
   6. When `Run` returns, shut down the listener and stop with exit code 0.

The `Long` help text of the command must state:

- The two sweeps.
- That the command runs until SIGTERM.
- The `--once` behaviour and its exit codes.
- The settings `GC_POLL_INTERVAL`, `GC_BATCH_SIZE`, `GC_ALLOCATION_GRACE`, `OPENTAMS_OBJECT_STORE_ID`, and the database and object-store settings.

Remove the old text about flow delete requests, CronJob, and sidecar. Remove `errGCNotImplemented`.

The tests of the command set these variables, with values from the test containers: `APP_ENV=development`, `DB_HOST`, `DB_PORT`, `DB_NAME`, `DB_USER`, `DB_PASSWORD`, `DB_SSLMODE=disable`, `OBJECT_STORE_BUCKET`, `OBJECT_STORE_REGION`, `OBJECT_STORE_ENDPOINT`, `OBJECT_STORE_ACCESS_KEY_ID`, `OBJECT_STORE_SECRET_ACCESS_KEY`, and `STORAGE_BACKEND_PROVIDER`. With `APP_ENV=development`, `config.Load` does not require `AUTH_EXTERNAL_ISSUER_URL` and `AUTH_EXTERNAL_AUDIENCE` (`internal/config/config.go:172-180`).

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

GC objects must not match the selector of the API Service (`templates/service.yaml`) or of the API ServiceMonitor (`templates/servicemonitor.yaml:12-14`). Both select `opentams.selectorLabels`. Add two helpers:

```yaml
{{- define "opentams.gcSelectorLabels" -}}
app.kubernetes.io/name: {{ include "opentams.name" . }}-gc
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "opentams.gcLabels" -}}
helm.sh/chart: {{ include "opentams.chart" . }}
{{ include "opentams.gcSelectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}
```

Each GC object uses `opentams.gcLabels` for its metadata labels. No GC object uses `opentams.labels` or `opentams.selectorLabels`.

Do not change `opentams.selectorLabels`. A change to the selector of an existing Deployment is not permitted by Kubernetes.

### 12.3 New templates (each one only if `.Values.gc.enabled`)

| File | Content |
|---|---|
| `gc-deployment.yaml` | Deployment `<fullname>-gc`. Metadata labels `opentams.gcLabels`. `replicas: .Values.gc.replicaCount`. Selector `opentams.gcSelectorLabels`. Pod labels `opentams.gcSelectorLabels` plus `.Values.podLabels`. Pod annotations `.Values.podAnnotations`. Copy these fields from `deployment.yaml` with the same values: `imagePullSecrets`, `serviceAccountName`, `automountServiceAccountToken`, `securityContext` (pod), `priorityClassName`, `nodeSelector`, `affinity`, `tolerations`, `topologySpreadConstraints`, `image`, `imagePullPolicy`, `securityContext` (container), `env` (`opentams.env`), the `/tmp` volume, `extraVolumes`, and `extraVolumeMounts`. Add `args: ["gc"]` and the env values `GC_POLL_INTERVAL`, `GC_BATCH_SIZE`, and `GC_ALLOCATION_GRACE` from `.Values.gc`. Add port `http` (`.Values.server.port`), the liveness probe path `/healthz` and the readiness probe path `/readyz` (the timing values from `.Values.probes.liveness` and `.Values.probes.readiness`), and `resources: .Values.gc.resources`. Do not add the TLS volume, a startup probe, an autoscaler, or a PodDisruptionBudget. The default update strategy (RollingUpdate) is correct, because the claim lease makes two pods at the same time safe (BR-GC-65). |
| `gc-service.yaml` | Service `<fullname>-gc`. Metadata labels `opentams.gcLabels`. Annotations with the same rule as `service.yaml:7-17`: `.Values.service.annotations`, and the three `prometheus.io/*` annotations if `.Values.serviceMonitor.enabled` is false. Type ClusterIP. Port `http`: `port` and `targetPort` are `.Values.server.port`. Selector `opentams.gcSelectorLabels`. |
| `gc-servicemonitor.yaml` | Only if `.Values.serviceMonitor.enabled`. ServiceMonitor `<fullname>-gc`. Metadata labels `opentams.gcLabels` plus `.Values.serviceMonitor.labels`. Selector `matchLabels: opentams.gcSelectorLabels`. Endpoint `port: http`, `path: /metrics`, interval and timeout from `.Values.serviceMonitor`. |
| `gc-networkpolicy.yaml` | Only if `.Values.networkPolicy.enabled`. Metadata labels `opentams.gcLabels`. Pod selector `opentams.gcSelectorLabels`. Ingress only from `.Values.networkPolicy.monitoringNamespaceSelector` on port `.Values.server.port`. |

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
| `internal/metastore/segments.go` | 57-69 | Add `Code apperror.ErrorCode` to `RejectReason` | BR-GC-15 |
| `internal/metastore/segments.go` | 226-262 | Section 5 (step 3) | BR-GC-10 to BR-GC-17 |
| `internal/metastore/segments.go` | 264-338 | Roll back if no segment is accepted. Steps 3.5 and 4 use only the accepted segments. | BR-GC-15 |
| `internal/metastore/segments.go` | 572-580 | Section 6.2 | BR-GC-21 |
| `internal/metastore/segments.go` | 791-800 | Remove `IsObjectRegistered` if it has no other caller | BR-GC-03 |
| `internal/metastore/flows.go` | 425-468 | Section 6.1, and the comment | BR-GC-20 |
| `internal/apperror/apperror.go` | 27, 34, 59, 66 | Remove `ErrObjectIDExists` and `ErrObjectReaping`. Add `ErrObjectExists`, `ErrObjectNotAllocated`, `ErrObjectWrongFlow`, `ErrObjectDeleting`, each 400, with the titles of BR-GC-70 to BR-GC-73. | BR-GC-70 to BR-GC-74 |
| `internal/apperror/apperror_test.go` | 26, 109, 159-166 | Update the tables. TC-ERR-09 tests `ErrObjectReaping` (409): remove that case. | BR-GC-70, BR-GC-74 |
| `internal/service/storage/storage.go` | 18-24, 79-81, 135-160 | Section 4.1 (constructor with `storageID` and `allocTTL`, `ObjectError` mapping) | BR-GC-01 to BR-GC-08 |
| `internal/service/segment/segment.go` | 111-112, 200 | Remove the `ErrSegmentObjectReaping` cases | BR-GC-74 |
| `internal/service/segment/segment.go` | 212-240 | Take `Type` and `Title` from `RejectReason.Code` (section 5.1) | BR-GC-15 |
| `internal/httpx/handlers/segments.go` | 302-305 | Remove the 409 `object-reaping` case | BR-GC-16 |
| `internal/httpx/handlers/segments.go` | 609-614 | The fallback of the 409 case uses `object-deleting` | BR-GC-74 |
| `internal/httpx/handlers/storage.go` | 40-46 | WARN, not ERROR, for a 400 `AppError` | BR-GC-70 |
| `internal/httpx/handlers/root.go` | 28-32 | Set `MinPresignedUrlTimeout` | BR-GC-09 |
| `internal/config/config.go` | 107, 144-147, validation | Section 11 | BR-GC-09, NFR-GC-P2, P3 |
| `cmd/opentams/serve.go` | 99-138, 163-178 | Move the shared setup to a function. Read the storage ID from the configuration. Pass it and the presign time to `storage.New`. | Section 10, 4.1 |
| `cmd/opentams/gc.go` | all | Section 10 | BR-GC-60 to BR-GC-69 |
| `internal/metastore/store_test.go` | new helpers | `resetGCTables(t)`: `TRUNCATE objects, allocations, segments, flows CASCADE` on `testPool`. `seedAllocation(t, flowID, ids...)`: one allocation row for each ID, `storage_id 'default'`, `expires_at = now() + interval '1 hour'`. | Test plan section 0 |
| `internal/metastore/segments_test.go`, `segments_timerange_test.go`, `flows_test.go`, `internal/service/segment/segment_integration_test.go` | each controlled registration | Call `seedAllocation` before each `InsertSegments` call with a controlled segment. Else the call gets `object-not-allocated`. | BR-GC-12 |
| `internal/metastore/segments_test.go`, `flows_test.go`, `segments_perf_test.go`, `internal/httpx/handlers/segments_perf_test.go`, `internal/service/segment/segment_perf_test.go`, `tools/scaletest/dbstats/capture_integration_test.go`, `tools/scaletest/bench/buckets_integration_test.go` | each SQL with `reaping` | Column `reaping` → `deleting` (for example `segments_test.go:421,781`, `flows_test.go:211`, `segments_perf_test.go:73`) | BR-GC-50 |
| `tools/scaletest/loader/dbload.go` | 314, 531 | Column `reaping` → `deleting`. Write `storage_id = 'default'` on each object row, so that sweep 1 deletes controlled objects in scale case S3. | BR-GC-50, BR-GC-30 |
| `tools/scaletest/loadgen/executor.go` | 178-235 | Before each registration, call `POST …/storage` with `object_ids` for the IDs of that registration (`client.go:140`, add the `object_ids` mode). Record the allocation in its own bucket. Count a 200 response with `failed_segments` as an error. | BR-GC-12, scale case S1 |
| `docs/requirements.md` | REQ-BEH-17 (247) | The server still does not validate the content of media objects. A controlled segment must use an allocated object ID (REQ-GC-10). | BR-GC-12 |
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
