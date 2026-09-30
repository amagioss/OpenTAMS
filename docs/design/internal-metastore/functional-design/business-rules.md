---
unit: M4 internal/metastore
stage: Functional Design
status: Complete
---

# Business Rules — internal/metastore

> Rules marked **(pending)** follow a proposed ADR ([ADR-0039](../../../adr/0039-timerange-stored-as-client-string-with-half-open-bounds.md),
> [ADR-0040](../../../adr/0040-segment-timerange-invariants.md)) and are not implemented yet.
> Until the code lands, the code is the tie-breaker for current behaviour.

## Package Structure

```
internal/metastore/
├── store.go       — PostgresStore struct, New(), Ping()
├── schema.go      — ExpectedSchemaVersion const, VerifySchema(), schema sentinel errors
├── sources.go     — SourceStore methods
├── flows.go       — FlowStore methods
├── segments.go    — Store methods, the object-existence probe, and the
│                    timerange containment helpers
└── *_test.go      — per-file tests
```

---

## Interfaces

Three exported interfaces describe the shipped contract, all implemented by
`*PostgresStore`.
`*PostgresStore` also has a `Ping(ctx context.Context) error` method — not an exported interface.
The health handler (M13) defines its own local `pinger` interface; `*PostgresStore` satisfies it implicitly.

```go
type SourceStore interface {
    GetSource(ctx context.Context, id uuid.UUID) (*Source, error)
    ListSources(ctx context.Context, p ListSourcesParams) (*SourcePage, error)
    PutSourceTag(ctx context.Context, id uuid.UUID, name string, value json.RawMessage) error
    DeleteSourceTag(ctx context.Context, id uuid.UUID, name string) error
    PutSourceLabel(ctx context.Context, id uuid.UUID, label string) error
    DeleteSourceLabel(ctx context.Context, id uuid.UUID) error
    PutSourceDescription(ctx context.Context, id uuid.UUID, desc string) error
    DeleteSourceDescription(ctx context.Context, id uuid.UUID) error
}

type FlowStore interface {
    UpsertFlow(ctx context.Context, f *Flow) (created bool, err error)
    GetFlow(ctx context.Context, id uuid.UUID) (*Flow, error)
    ListFlows(ctx context.Context, p ListFlowsParams) (*FlowPage, error)
    DeleteFlow(ctx context.Context, id uuid.UUID) (zeroRefObjectIDs []string, err error)
    PutFlowTag(ctx context.Context, id uuid.UUID, name string, value json.RawMessage) error
    DeleteFlowTag(ctx context.Context, id uuid.UUID, name string) error
    PutFlowLabel(ctx context.Context, id uuid.UUID, label string) error
    DeleteFlowLabel(ctx context.Context, id uuid.UUID) error
    PutFlowDescription(ctx context.Context, id uuid.UUID, desc string) error
    DeleteFlowDescription(ctx context.Context, id uuid.UUID) error
    PutFlowReadOnly(ctx context.Context, id uuid.UUID, readOnly bool) error
    PutFlowCollection(ctx context.Context, id uuid.UUID, items []CollectionItem) error
    DeleteFlowCollection(ctx context.Context, id uuid.UUID) error
    PutFlowAvgBitRate(ctx context.Context, id uuid.UUID, rate int64) error
    DeleteFlowAvgBitRate(ctx context.Context, id uuid.UUID) error
    PutFlowMaxBitRate(ctx context.Context, id uuid.UUID, rate int64) error
    DeleteFlowMaxBitRate(ctx context.Context, id uuid.UUID) error
}

// The live segments contract. Request-shaped structs rather than
// positional arguments, and domain types rather than metastore types.
type Store interface {
    GetFlowForSegmentWrite(ctx context.Context, tx Tx, flowID uuid.UUID) (FlowGuard, error)
    GetFlowForSegmentRead(ctx context.Context, flowID uuid.UUID) (FlowGuard, error)

    InsertSegments(ctx context.Context, batch InsertBatch) (InsertResult, error)
    ListSegments(ctx context.Context, q ListQuery) (domain.SegmentPage, error)
    DeleteSegmentsByTimerange(ctx context.Context, q DeleteQuery) (DeleteResult, error)
}
```

`InsertSegments` takes an `InsertBatch` — `FlowID`, `Segments []domain.Segment`,
and the deployment's `ControlledStorageID` — and returns an `InsertResult` that
partitions the input into `AcceptedIndices` / `RejectedIndices` with parallel
`RejectReasons`. `ListSegments` and `DeleteSegmentsByTimerange` take `ListQuery`
and `DeleteQuery` in the same request-shaped style. Every segment value crossing
this boundary is an `internal/domain` type; the package declares no wire types and
no mirror structs.

Segment errors are sentinels, wrapped so callers use `errors.Is`:
`ErrFlowNotFound`, `ErrFlowReadOnly`, `ErrInvalidCursor`, `ErrBadInput`,
`ErrSegmentOverlap`, `ErrSegmentObjectReaping`, and
`ErrControlledStorageNotConfigured`. The older flow and source methods signal
through `apperror.AppError` codes instead; the segments slice moved to sentinels so
the service can translate at one boundary.

---

## Domain Types

This block copies the types that `internal/metastore` owns. The source of truth is the
code: `internal/metastore/flows.go` and `internal/metastore/segments.go`. When a type
changes, update this block in the same pull request.

The store takes and returns segments as `domain.Segment`, and returns failures and pages
as `domain.FailedSegment` and `domain.SegmentPage`. Those types belong to
`internal/domain` ([`segment.go`](../../../../internal/domain/segment.go),
[`segment_params.go`](../../../../internal/domain/segment_params.go)). This document does
not copy them. BR-META-10 says which `domain.Segment` fields the store writes.

```go
// internal/metastore/flows.go

type Source struct {
    ID          uuid.UUID
    Format      string
    Label       *string
    Description *string
    Tags        map[string]json.RawMessage // value is string or []string
    Created     time.Time
    Updated     time.Time
    // SourceCollection and CollectedBy derived at query time from flow relationships
}

type Flow struct {
    ID                uuid.UUID
    SourceID          uuid.UUID
    Format            string
    Codec             *string
    Container         *string
    Label             *string
    Description       *string
    Tags              map[string]json.RawMessage
    EssenceParameters json.RawMessage // JSONB, format-specific
    ContainerMapping  json.RawMessage // JSONB
    FlowCollection    []CollectionItem
    AvgBitRate        *int64
    MaxBitRate        *int64
    SegmentDuration   *string
    Generation        *int64
    MetadataVersion   *int64
    ReadOnly          bool
    Created           time.Time
    MetadataUpdated   time.Time
    SegmentsUpdated   *time.Time
    Timerange         *string // derived from the flow's first and last segments, canonical form (BR-META-12)
}

type CollectionItem struct {
    ID               uuid.UUID
    Role             *string
    ContainerMapping json.RawMessage
}

type ListSourcesParams struct {
    Limit     int
    PageFrom  *string // opaque cursor
    Label     *string
    Format    *string
    TagExists map[string]struct{}
    TagValues map[string]string
}

type ListFlowsParams struct {
    SourceID    *uuid.UUID
    Format      *string
    Label       *string
    Codec       *string
    FrameWidth  *int
    FrameHeight *int
    Timerange   *timerange.TimeRange // BR-META-21
    TagExists   map[string]struct{}
    TagValues   map[string]string
    Limit       int
    PageFrom    *string // opaque cursor
}

type SourcePage struct {
    Items      []*Source
    NextCursor *string
}

type FlowPage struct {
    Items      []*Flow
    NextCursor *string
}

// internal/metastore/segments.go

type InsertBatch struct {
    FlowID              uuid.UUID
    Segments            []domain.Segment
    ControlledStorageID string // BR-META-07, BR-SEG-07
}

type InsertResult struct {
    AcceptedIndices []int
    RejectedIndices []int // unused today: InsertSegments is all-or-nothing (BR-META-06)
    RejectReasons   []RejectReason
}

type RejectReason struct {
    Type   string
    Detail string
}

type ListQuery struct { // input to ListSegments, which returns domain.SegmentPage
    FlowID                 uuid.UUID
    Timerange              *timerange.TimeRange // BR-META-21
    ObjectID               *string
    ReverseOrder           bool
    VerboseStorage         bool
    AcceptGetURLs          []string
    AcceptStorageIDs       []string
    Presigned              *bool
    IncludeObjectTimerange bool
    Page                   string // opaque cursor
    Limit                  int    // 1..1000; the caller clamps (BR-META-20)
}

type DeleteQuery struct { // input to DeleteSegmentsByTimerange
    FlowID    uuid.UUID
    Timerange timerange.TimeRange // required; BR-META-08, BR-META-21
    ObjectID  *string
}

type DeleteResult struct {
    DeletedCount int64
}
```

---

## PostgreSQL Schema

### Core Tables

```sql
CREATE EXTENSION IF NOT EXISTS btree_gist;

CREATE TABLE sources (
    id          UUID PRIMARY KEY,
    format      TEXT NOT NULL,
    label       TEXT,
    description TEXT,
    created     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE source_tags (
    source_id UUID NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
    name      TEXT NOT NULL,
    value     JSONB NOT NULL,
    PRIMARY KEY (source_id, name)
);

CREATE TABLE flows (
    id                  UUID PRIMARY KEY,
    source_id           UUID NOT NULL REFERENCES sources(id),
    format              TEXT NOT NULL,
    codec               TEXT,
    container           TEXT,
    label               TEXT,
    description         TEXT,
    essence_parameters  JSONB,
    container_mapping   JSONB,
    avg_bit_rate        BIGINT,
    max_bit_rate        BIGINT,
    segment_duration    TEXT,
    generation          BIGINT,
    metadata_version    BIGINT,
    read_only           BOOLEAN NOT NULL DEFAULT false,
    created             TIMESTAMPTZ NOT NULL DEFAULT now(),
    metadata_updated    TIMESTAMPTZ NOT NULL DEFAULT now(),
    segments_updated    TIMESTAMPTZ,
    timerange           TEXT  -- denormalized from segments
);

CREATE TABLE flow_tags (
    flow_id UUID NOT NULL REFERENCES flows(id) ON DELETE CASCADE,
    name    TEXT NOT NULL,
    value   JSONB NOT NULL,
    PRIMARY KEY (flow_id, name)
);

CREATE TABLE flow_collection (
    flow_id           UUID NOT NULL REFERENCES flows(id) ON DELETE CASCADE,
    item_id           UUID NOT NULL,
    role              TEXT,
    container_mapping JSONB,
    sort_order        INT NOT NULL DEFAULT 0,
    PRIMARY KEY (flow_id, item_id)
);

CREATE TABLE objects (
    id              TEXT PRIMARY KEY,
    timerange       TEXT,
    key_frame_count INT,
    ref_count       INT NOT NULL DEFAULT 0
);

CREATE TABLE segments (
    id               BIGSERIAL PRIMARY KEY,
    flow_id          UUID NOT NULL REFERENCES flows(id) ON DELETE CASCADE,
    object_id        TEXT NOT NULL REFERENCES objects(id),
    timerange        TEXT NOT NULL,       -- exactly as the client sent it; never compared as text
    lower_ns         BIGINT NOT NULL,     -- half-open lower bound from timerange.NsBounds (inclusive)
    upper_ns         BIGINT NOT NULL,     -- half-open upper bound from timerange.NsBounds (exclusive); NOT NULL is pending
    ts_offset        TEXT NOT NULL DEFAULT '0:0',
    object_timerange TEXT,
    last_duration    TEXT,
    key_frame_count  INT,
    sample_offset    BIGINT,
    sample_count     BIGINT,
    get_urls         JSONB,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT no_segment_overlap EXCLUDE USING gist (
        flow_id WITH =,
        int8range(lower_ns, COALESCE(upper_ns, 9223372036854775807)) WITH &&
    ),
    -- pending: replaces segments_upper_ns_positive (migration 000005, edited in place)
    CONSTRAINT segments_bounds_nonempty CHECK (upper_ns > lower_ns)
);

CREATE INDEX segments_flow_id_lower ON segments (flow_id, lower_ns);
CREATE INDEX flows_source_id ON flows (source_id);
CREATE INDEX flows_created ON flows (created);
CREATE INDEX sources_created ON sources (created);
```

---

## Business Rules

### BR-META-01: Source implicit creation in UpsertFlow
`UpsertFlow` runs in a single transaction. If `flow.SourceID` does not exist in `sources`, the source is inserted with `id=flow.SourceID`, `format=flow.Format`, `created=now()`. If the source exists, `flow.Format` must equal `source.Format` — mismatch returns `apperror.ErrFormatMismatch`.

This is one instance of the lazy upstream-entity creation pattern (see BR-META-17). The flow service does not write to `sources`; the metastore creates the parent row on the first write that needs it, satisfying the `flows.source_id REFERENCES sources(id)` FK without requiring an explicit prior `POST /sources/{id}` from the client (sources do not have a creation endpoint in TAMS).

### BR-META-02: UpsertFlow returns created=true on INSERT, created=false on UPDATE
Determined by whether the flow row existed before the upsert. Used by the HTTP handler to return 201 vs 204.

### BR-META-03: Immutable fields on existing flows
`source_id` and `format` are never updated by UpsertFlow. If the incoming values differ from the stored values, return `apperror.ErrImmutableField`. Check occurs before the update.

### BR-META-04: Codec and essence_parameters immutable when flow has segments
When a flow has one or more segments, `codec` and `essence_parameters` cannot be changed. If incoming values differ from stored, return `apperror.ErrImmutableField`.

### BR-META-05: Overlap enforcement is application-level first; DB constraint is safety net only
`InsertSegments` enforces non-overlap itself rather than relying on the DB constraint. Both checks use the same rule: they compare the `timerange.NsBounds` results of two segments as half-open intervals (pending, [ADR-0040](../../../adr/0040-segment-timerange-invariants.md) rule 5). Two checks, in order:
1. **Within-batch**, before any transaction is opened: the store compares every pair of candidate segments by their bounds, `a.Lower < b.Upper && b.Lower < a.Upper`. This needs no round-trip, so a self-overlapping batch is rejected without touching Postgres.
2. **Against existing**, inside the transaction and after the flow row is locked: one `int8range(lower_ns, upper_ns) && int8range($lo, $hi)` probe per candidate against `segments` for that flow.

Today check 1 uses `timerange.Overlaps` and check 2 uses bounds from `nsRange`, which ignores the markers. The two disagree: the store rejects `[0:0_10:0]` and `[10:0_20:0)` in one batch and accepts them in two batches. One rule removes the disagreement.

The store trusts that the service has already rejected empty, unbounded, and out-of-range timeranges (BR-SEG-02). The `segments_bounds_nonempty` check and the exclusion constraint are backstops for a server defect, not validation.

Either check finding an overlap aborts the whole call with a wrapped `ErrSegmentOverlap` naming the colliding ranges.

The `no_segment_overlap` EXCLUSION constraint on `segments` is retained as defence-in-depth for concurrent transactions. A `23P01` exclusion violation on INSERT is also mapped to `ErrSegmentOverlap`, but that path is only reachable under a race between two writers on the same flow — the flow-row lock (BR-META-15) makes it rare.

### BR-META-06: InsertSegments is all-or-nothing
A batch either commits in full or persists nothing. There is no per-segment savepoint and no partial success: the first failure of any kind — overlap, an object claimed by the GC, an `object_timerange` violation, a controlled segment with no configured storage id, or a driver error — returns immediately and the deferred rollback discards every write the call had made, including in-flight ref-count upserts.

On success `InsertResult.AcceptedIndices` holds every input index and `RejectedIndices` is empty. The rejected-index machinery exists for a future partial-acceptance path; nothing populates it today, so the only entries a client sees in `failed_segments` are the ones the segment service rejects before the store is called (BR-SEG-02).

Whole-batch rejection on overlap is a deliberate divergence from TAMS `REQ-BEH-15`, recorded in [ADR-0024](../../../adr/0024-whole-batch-reject-on-segment-overlap.md).

### BR-META-07: InsertSegments lazily registers the object, stamps its backend, and refuses reaped rows
For each segment the referenced `object_id` is upserted into `objects` before the segment row is inserted:

```sql
INSERT INTO objects (id, ref_count, storage_id, reaping)
VALUES ($1, 1, $2, false)
ON CONFLICT (id) DO UPDATE
    SET ref_count = objects.ref_count + 1
    WHERE objects.reaping = false
RETURNING (xmax = 0) AS inserted
```

Three decisions are folded into that statement.

**Ordering.** The upsert must run before `INSERT INTO segments`. The `segments.object_id REFERENCES objects(id)` FK is enforced at INSERT time, so a missing parent row fails with `23503`. The `ON CONFLICT` branch handles repeat references from the same or another flow.

**Storage id (`$2`).** The metastore classifies a segment by shape: `len(seg.GetURLs) == 0` means the server owns the bytes (controlled) and `InsertBatch.ControlledStorageID` is stamped onto `objects.storage_id`; a segment carrying client URLs is BYOS and stores NULL. A controlled segment in a batch whose `ControlledStorageID` is empty is a deployment misconfiguration and fails with `ErrControlledStorageNotConfigured` rather than writing a NULL that would later read as BYOS. The value is supplied per call by the segment service, not read from the environment here (BR-SEG-07).

The same shape classifier is used on the read path, so `ListSegments` needs no join onto `objects` to tell the two apart.

**The `WHERE objects.reaping = false` predicate.** It closes the window in which the GC has claimed a zero-ref object and is deleting its bytes. When the predicate fails, `RETURNING` yields no row, and the store returns `ErrSegmentObjectReaping` — the handler renders 409 and the client retries with a fresh `object_id`. Without it, a segment could be registered against an object whose bytes are moments from deletion.

**Why the metastore does this and not the service**: `internal/service/storage` (`AllocateStorage`) is intentionally write-free against the metastore. It mints object ids and returns presigned PUT URLs only — no row in `objects` is created at `/storage` time. Lazy registration during `/segments` matches:
- REQ-BEH-17 — server does not validate that an object exists in storage before accepting the segment.
- REQ-REL-14 — orphans are defined as "object exists in S3 but never registered as a segment" (no DB row).
- BBC TAMS ADR 0027 — "objects are registered in TAMS as part of Flow Segment registration".

This is the same pattern used for sources in `UpsertFlow` (BR-META-01). See BR-META-17 for the consolidated rule.

**S3-orphan implication**: a client that calls `/storage`, uploads bytes, and never calls `/segments` produces an S3 key with no metadata row. This is by design; cleanup is handled by the GC reconciliation pass per REQ-GC-05 / spec §17 (S3 LIST diffed against `objects`, with `min_object_timeout` grace). The `internal/gc` worker that performs this reconciliation is not yet built.

**Known limitation**: the upsert does not populate `objects.timerange` or `objects.key_frame_count` from `Segment.ObjectTimerange` / `Segment.KeyFrameCount`. The columns exist and are intended for first-write-wins denormalisation (REQ-DATA-06). The containment rule in BR-META-19 reads the earliest prior `segments.object_timerange` instead.

### BR-META-08: DeleteSegmentsByTimerange is atomic and leaves object cleanup to the GC
The store removes every segment whose timerange is **contained in** the query, optionally narrowed by `object_id`. It removes them in one transaction, behind the same flow-row lock as an insert (pending, BR-META-21). TAMS says "Only delete Flow Segments that are completely covered by the given timerange". Today the code deletes every segment that *intersects* the query, which also removes boundary segments the client did not ask to delete. The `DELETE … RETURNING object_id` feeds a `GROUP BY`, so each object's `ref_count` is decremented once by the number of its segments that went away rather than once per row.

Objects that reach `ref_count = 0` are **left in place**. `DeleteResult` carries only `DeletedCount`; there is no released-object list for a caller to act on. The GC worker owns cleanup, reading `objects WHERE ref_count = 0 AND reaping = false`, deleting the bytes, and reaping the rows. The rationale for keeping the object store off this path is in BR-SEG-10.

### BR-META-09: DeleteFlow cascades and returns zero-ref object IDs
`DELETE FROM flows WHERE id=?` cascades to segments and flow_tags via FK. Before deletion, collect all `object_id` values from segments. After deletion, return those whose `ref_count` has reached zero.

### BR-META-10: Stored time values and bounds (pending)
- **Client strings.** `segments.timerange`, `ts_offset`, `object_timerange`, and `last_duration` hold exactly the string the client sent. The conversion layer puts that string in `domain.Segment.TimerangeRaw`, `TSOffsetRaw`, `ObjectTimerangeRaw`, and `LastDurationRaw` (BR-CONV-08). The service passes those fields through unchanged. `InsertSegments` writes the `*Raw` fields to the four columns, and `ListSegments` reads the columns back into the same fields. The store never writes `TimeRange.String()` for a client value. The store parses stored strings for semantic checks and never compares them as text: `10:0`, `[10:0]`, and `[10:0_10:0]` are one range.
- **Bounds.** `lower_ns` and `upper_ns` come only from `timerange.NsBounds` applied to the segment `timerange` (BR-TR-10). `ts_offset`, `object_timerange`, and `last_duration` have no bounds columns. The metastore has no conversion of its own. This change removes `nsRange` and the arithmetic in the `ListFlows` filter.
- **Never rendered.** The store uses bounds to filter, detect overlap, order, page, and select boundary rows. No code renders them back to text.

Today `nsRange` in `segments.go` ignores the markers, so `[0:0]` becomes `(0, 0)` and fails the check constraint with a 500. That defect, and the rule it broke, are the subject of [ADR-0039](../../../adr/0039-timerange-stored-as-client-string-with-half-open-bounds.md).

### BR-META-11: Cursor-based pagination
Cursors are opaque base64-encoded values encoding the last-seen row's sort key (`created` + `id` for sources/flows, `lower_ns` + `id` for segments). `ListFlows`, `ListSources`, `ListSegments` all support `page_from` cursor and `limit`. Default limit: 100. Max limit: 1000.

### BR-META-12: Flow timerange is denormalized
`flows.timerange` holds the span of the flow's segments as text. The store updates it inside every successful `InsertSegments` and `DeleteSegmentsByTimerange` transaction. It is NULL when the flow has no segments.

The value is derived, so the store renders it in canonical form (pending, BR-TR-12):
1. Select the stored `timerange` string of the segment with the lowest `lower_ns`, and of the segment with the highest `upper_ns`.
2. Parse both strings.
3. Join the start bound of the first to the end bound of the second, and render the result with `TimeRange.String()`.

`GetFlowTimerange` uses the same steps. Rendering happens in Go. Today both paths build the string from `lower_ns` and `upper_ns`, in SQL or with integer division. This loses the markers. It also renders a negative bound such as −1.5 s as `-1:-500000000`.

### BR-META-13: GetFlow loads tags and collection in one round-trip
`GetFlow` uses a single query joining `flows`, `flow_tags`, and `flow_collection`. Tags are aggregated as JSONB. Collection items ordered by `sort_order`.

### BR-META-14: Not-found errors
`GetFlow`, `GetSource`, `PutFlowTag`, and all sub-resource mutations return `apperror.ErrNotFound` when the target row does not exist.

### BR-META-15: read_only enforced by the store on all writes except PutFlowReadOnly
All write methods that operate on a flow check the flow's `read_only` flag before executing. If `read_only` is true, return `apperror.ErrReadOnly`.

Methods that enforce this check:
- `UpsertFlow` (update path only — creation is always allowed)
- `DeleteFlow`
- `InsertSegments`, `DeleteSegmentsByTimerange`
- `PutFlowTag`, `DeleteFlowTag`
- `PutFlowLabel`, `DeleteFlowLabel`
- `PutFlowDescription`, `DeleteFlowDescription`
- `PutFlowAvgBitRate`, `DeleteFlowAvgBitRate`
- `PutFlowMaxBitRate`, `DeleteFlowMaxBitRate`
- `PutFlowCollection`, `DeleteFlowCollection`

`PutFlowReadOnly` is explicitly exempt — it is the only operation allowed on a `read_only` flow.

Implementation: each enforced method runs `SELECT read_only FROM flows WHERE id = $1` within its transaction before proceeding.

On the segments path this read takes a row-level lock — `GetFlowForSegmentWrite` issues `SELECT read_only FROM flows WHERE id = $1 FOR UPDATE` and returns a `FlowGuard{FlowID, Exists, ReadOnly}` as the first step of both `InsertSegments` and `DeleteSegmentsByTimerange`. The lock is what makes the check sound: without it a concurrent `PUT /flows/{id}/read_only` could flip the flag between the read and the write, and the write would land on a flow that had just been frozen. It also serialises writers on the same flow, which is what keeps the overlap probe in BR-META-05 from racing.

`GetFlowForSegmentRead` is the unlocked counterpart used by `ListSegments` for the existence check only.

### BR-META-16: source_collection and collected_by derived at query time
`ListSources` and `GetSource` compute `source_collection` and `collected_by` from flow relationships via a lateral subquery. These are not stored columns.

### BR-META-17: Lazy upstream-entity creation pattern
The metastore is the sole writer of upstream parent rows that exist purely to satisfy FK constraints from child tables. Service-layer code does **not** preemptively insert parent rows.

Concrete instances:

| Child write | Parent table | Where the parent row is created |
|---|---|---|
| `UpsertFlow` (new flow) | `sources` (FK `flows.source_id`) | Inside `UpsertFlow` txn (BR-META-01) |
| `InsertSegments` (any segment) | `objects` (FK `segments.object_id`) | Inside `InsertSegments` per-segment savepoint (BR-META-07) |

Properties shared by all instances of the pattern:
1. **Single transaction**: the parent insert and the child insert run in the same transaction (or savepoint). A failure in either rolls back both.
2. **Idempotent**: parent insertion uses `INSERT ... ON CONFLICT` (objects) or a pre-check `SELECT ... THEN INSERT` (sources) so repeat writes don't error.
3. **No public service-layer parent writes**: there is no `POST /sources/{id}` in the API and `AllocateStorage` is deliberately write-free against the metastore. Clients drive the API only at the child level (flows, segments).
4. **Format/integrity invariants checked on conflict**: `UpsertFlow` rejects a flow whose format mismatches an existing source (`ErrFormatMismatch`); `InsertSegments` does not currently enforce object-level invariants beyond the FK, but the schema (`objects.timerange`, `objects.key_frame_count`) is shaped to support first-write-wins denormalization (REQ-DATA-06) when added.
5. **GC implication**: when the parent table acts as a reference index (objects), unreferenced parent rows accumulate at `ref_count=0` until the GC worker sweeps them; rows lost between presigned upload and segment registration are picked up by S3-vs-DB reconciliation (REQ-REL-14, REQ-GC-05, spec §17). When the parent table is purely structural (sources), no GC is needed — sources persist for the lifetime of their flows.

This pattern exists because the TAMS API surface is deliberately minimal — there is no `POST /sources` and `POST /storage` is byte-allocation only. Pushing parent creation into service layers would either (a) require duplicate write logic in every caller of the metastore, or (b) require the metastore to expose explicit `EnsureSource` / `EnsureObject` methods that callers must remember to invoke. The lazy-write-on-FK-need approach centralizes integrity in the metastore, where the FK is also defined.

### BR-META-18: Schema startup probe (REQ-DEV-05a)
The metastore exposes a `VerifySchema(ctx) (SchemaState, error)` method that callers (currently `cmd/opentams/serve.go`) invoke once at startup, immediately after the pool is opened and before any other DB-dependent component is wired. The probe queries `schema_migrations` and translates the result into typed sentinel errors:

| Observed state | Sentinel error | Notes |
|---|---|---|
| Table missing (SQLSTATE 42P01) | `ErrSchemaNotMigrated` | Caller must refuse to start |
| Table exists but no rows | `ErrSchemaNotMigrated` | Same recovery as above |
| `dirty = true` | `ErrSchemaDirty` | Error message includes the dirty version and the `migrate force <version>` hint |
| `version < ExpectedSchemaVersion` | `ErrSchemaTooOld` | Error message names both versions |
| `version == ExpectedSchemaVersion` | `nil` | Returned `SchemaState.Version` matches |
| `version > ExpectedSchemaVersion` | `nil` | Older binary on newer schema, supported under expand-contract |

`ExpectedSchemaVersion` is a hardcoded `int64` constant in `schema.go`. It is bumped manually whenever a new migration is added whose absence would break a request path the binary serves. This is deliberate: it is the single review-time moment that asks "does this migration preserve forward compatibility for the previous binary?" — embedding `migrations/` and auto-deriving the value would remove that gate.

The probe is startup-only and is **not** exposed via `/readyz` or `/health/details`. The schema cannot change without a server restart, so a per-request check would only add noise. Callers that want to log the observed state for fleet observability use the returned `SchemaState{Version, Dirty, Expected}`.

Tests live in `schema_test.go` and use the same testcontainer Postgres as the rest of the package; each test mutates `schema_migrations` inside its own per-test transaction and relies on Postgres's transactional DDL to roll back the mutation on `t.Cleanup`.

### BR-META-19: object_timerange is validated lazily against the earliest stored value
`Segment.ObjectTimerange` is a pointer: `nil` means the client did not supply one, and the metastore stores NULL. Nothing is ever computed on the client's behalf (REQ-DATA-06).

When a segment *does* carry one, `InsertSegments` looks up the earliest prior non-NULL `object_timerange` for the same `object_id`, across all flows, ordered by `created_at`. It parses both strings, because it never compares them as text (BR-META-10). It requires the incoming range to be **contained** within the stored range. A range extending beyond the stored one fails the batch with a wrapped `apperror.ErrInvalidObjectTimerange` naming both ranges.

Validation is skipped entirely when either side is absent: an object seen for the first time, or a segment that supplies no `object_timerange`, has nothing to contradict. This is a first-write-wins rule — the first client to describe an object's extent defines it, and later registrations may only address a sub-range of those bytes.

The check runs before the segment rows are written, so a violation rolls back the ref-count upserts along with everything else (BR-META-06).

### BR-META-20: ListSegments clamps, orders, and pages without trusting the caller
`ListQuery.Limit` is clamped into `1..1000`; a zero or negative value and anything above the cap both yield 1000 rows. The store asks Postgres for `limit + 1` rows and uses the extra row solely to decide whether a next cursor exists.

Ordering is `(lower_ns, id)`, ascending by default and descending when `ReverseOrder` is set; the cursor comparison flips with it. Cursors encode exactly that sort key, so paging is stable under concurrent inserts and never re-reads or skips a row the way an OFFSET would. A cursor that does not decode returns `ErrInvalidCursor` rather than silently restarting from the first page.

`ListSegments` first calls `GetFlowForSegmentRead` and returns `ErrFlowNotFound` for an unknown flow, which the handler renders as 404. TAMS `REQ-BEH-16` asks for an empty list instead; the divergence is deliberate and recorded in [`docs/conformance.md`](../../../conformance.md).

### BR-META-21: Every timerange query uses NsBounds and a named predicate (pending)
Every query that takes a timerange converts it with `timerange.NsBounds`. The store passes an unbounded side as a SQL `NULL` parameter. Each site uses exactly one predicate:

| Site | Predicate | SQL shape |
|---|---|---|
| `InsertSegments` overlap check (BR-META-05) | Overlap | `int8range(lower_ns, upper_ns) && int8range($lo, $hi)` |
| `ListSegments` | Overlap | `($hi IS NULL OR lower_ns < $hi) AND ($lo IS NULL OR upper_ns > $lo)` |
| `ListFlows` timerange filter, non-empty range | A segment of the flow overlaps | `EXISTS (SELECT 1 FROM segments WHERE flow_id = f.id AND <ListSegments shape>)` |
| `ListFlows` timerange filter, empty range | The flow has no segments | `NOT EXISTS (SELECT 1 FROM segments WHERE flow_id = f.id)` |
| `ListFlows` timerange filter, `_` or absent | No filter | no condition |
| `DeleteSegmentsByTimerange` (BR-META-08) | Containment | `($lo IS NULL OR lower_ns >= $lo) AND ($hi IS NULL OR upper_ns <= $hi)` |

An **empty** query timerange, including one whose end is before its start, matches as TAMS says. The store checks `TimeRange.IsEmpty()` before it calls `NsBounds`, so `ErrEmptyRange` never gets to a query caller:
- `ListSegments` returns an empty page.
- `DeleteSegmentsByTimerange` deletes nothing.
- `ListFlows` returns only flows that have no segments ("An empty timerange returns Flows with no content"). Today the store skips the filter for an empty range, and returns every flow.

**ListFlows interpretation.** TAMS says "Filter on Flows that overlap the given timerange", and does not say what "a Flow overlaps" means. OpenTAMS reads it as "a segment of the flow overlaps". A query that falls in a gap between segments does not match the flow. For example, a flow with segments `[0:0_10:0)` and `[20:0_30:0)` does not match `[12:0_15:0)`. This matches the per-segment wording of `GET /flows/{flowId}` and `GET /flows/{flowId}/segments`. `conformance.md` records the interpretation.

**HEAD and GET.** `HEAD /flows` uses the same filter as `GET /flows`, including the empty-range rule. The upstream text for `HEAD /flows` omits the sentence about an empty timerange. HEAD returns the headers of GET without a body, so the two must agree.

A query timerange whose bounds do not fit in `int64` returns `timerange.ErrOutOfRange`. The caller maps it to 400 `invalid-timerange`.

Tests: one case per site with a closed end, an exclusive start, an instantaneous range, a one-sided range, and an empty range. The TAMS example of `[1:0_2:0)` followed by `[2:0]` is a required case for `ListSegments`.
