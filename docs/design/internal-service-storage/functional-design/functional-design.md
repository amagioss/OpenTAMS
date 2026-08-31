# Functional Design — internal/service/storage

## Purpose

Service layer for `POST /flows/{flowId}/storage` (REQ-BEH-24). Sits between HTTP
handlers (M12) and the metastore/objectstore backends. Responsible for:
- Fetching the flow (for codec and read_only state)
- Enforcing read_only access control
- Allocating object IDs (limit mode: server-assigned UUIDs; object_ids mode: client-provided)
- Validating object_ids are not already registered as segments
- Generating presigned upload URLs via the objectstore

## Dependencies

| Dependency | Interface | Purpose |
|---|---|---|
| `internal/metastore` | local `storage.FlowReader` (`GetFlow`) | Fetch flow for codec + read_only check |
| `internal/metastore` | local `storage.FlowReader` (`IsObjectRegistered`) | Check object_id not already in segments (M4 patch) |
| `internal/objectstore` | local `storage.ObjectStore` (`GenerateUploadURL`) | Generate presigned PUT URLs |
| `internal/service` | `*service.AppServiceMetrics` | Shared Prometheus metrics |

No `*zap.Logger` dependency: all errors in the allocation path are propagated immediately; there is no WARN-logging path (unlike DeleteFlow / DeleteSegments).

## Types

```go
// AllocateRequest is the service-layer input. Exactly one of Limit or ObjectIDs
// is set — the handler validates mutual exclusion before calling the service.
type AllocateRequest struct {
    Limit     *int
    ObjectIDs []string
}

// MediaObject is one element of the allocation response.
type MediaObject struct {
    ObjectID    string
    PutURL      string
    ContentType string
}
```

## Interface: StorageService

```go
AllocateStorage(ctx context.Context, flowID uuid.UUID, req AllocateRequest) ([]MediaObject, error)
```

`StorageService` is exported as an interface. The concrete `storageService` struct is unexported.

## Local store interface

```go
type FlowReader interface {
    GetFlow(ctx context.Context, id uuid.UUID) (*metastore.Flow, error)
    IsObjectRegistered(ctx context.Context, objectID string) (bool, error)
}
```

`metastore.PostgresStore` satisfies `FlowReader`. `IsObjectRegistered` is a new M4 patch method.

## Business Rules

### AllocateStorage

**BR-SVC-STG-01 — Get flow**
- Call `store.GetFlow(ctx, flowID)`.
- On `ErrNotFound` or any error: propagate to caller.

**BR-SVC-STG-02 — read_only check (REQ-BEH-11)**
- After GetFlow, check `flow.ReadOnly`. If true: return `apperror.New(apperror.ErrReadOnly, "flow is read-only")`.
- `GenerateUploadURL` is a pure objectstore call; `flowWritable` in the store layer does not cover it. Service enforces this inline.

**BR-SVC-STG-03 — Content-type derivation (REQ-BEH-25b)**
- `contentType = *flow.Codec` if `flow.Codec != nil`, else `"application/octet-stream"`.
- Used as the Content-Type binding in `GenerateUploadURL` and returned in `MediaObject.ContentType`.

**BR-SVC-STG-04 — limit mode: UUID allocation + URL generation**
- For `i := 0; i < *req.Limit; i++`: generate a new UUID (`uuid.New().String()`) as the `objectID`.
- Call `obj.GenerateUploadURL(ctx, objectID, contentType)`.
- On URL generation error: propagate immediately (no partial success).
- Return the full `[]MediaObject` slice.

**BR-SVC-STG-05 — object_ids mode: registration check (REQ-BEH-25a)**
- For each `objectID` in `req.ObjectIDs`:
  - Call `store.IsObjectRegistered(ctx, objectID)`.
  - If true: return `apperror.New(apperror.ErrObjectIDExists, "object_id already registered as a segment: <id>")`.
- If all checks pass: call `obj.GenerateUploadURL(ctx, objectID, contentType)` for each.
- On URL generation error: propagate immediately.
- Return the full `[]MediaObject` slice.

**BR-SVC-STG-06 — Fail-first on registration conflict**
- In `object_ids` mode, the first registered objectID encountered triggers an immediate return.
- No partial allocation on conflict: the client must fix the invalid ID and retry.

## Decisions

**D-SVC-STG-01 — Mutual exclusion validation in M12 handler**
`ErrStorageModeConflict` (when both/neither of `limit`/`object_ids` are present) is a request schema concern; M12 handler returns 400 before calling the service. Service assumes exactly one of `Limit` or `ObjectIDs` is set.

**D-SVC-STG-02 — read_only check in service, not store**
`GenerateUploadURL` writes nothing to the metastore so `flowWritable` (the store-level guard) never runs. Service checks `flow.ReadOnly` after GetFlow. This is consistent with REQ-BEH-11.

**D-SVC-STG-03 — IsObjectRegistered: cross-flow check via segments table**
Implementation: `SELECT EXISTS(SELECT 1 FROM segments WHERE object_id = $1)`. Cross-flow scope is correct: an object registered as a segment on ANY flow is "in use" from the system's perspective and cannot be re-uploaded. A flow-scoped check would be insufficient for shared objects (REQ-BEH-21).

**D-SVC-STG-04 — Nil codec defaults to "application/octet-stream"**
REQ-BEH-25b binds the upload URL to the flow's codec. If codec is absent (format allows it), `"application/octet-stream"` is the fallback. Object stores accept this for binary blobs.

**D-SVC-STG-05 — No partial success in object_ids mode**
Fail-first on the first already-registered objectID. Rationale: partial allocation creates inconsistent state (some IDs allocated, some not) that the caller cannot easily recover from. All-or-nothing is simpler and more predictable.

**D-SVC-STG-06 — local FlowReader interface**
Service defines its own `FlowReader` interface (2 methods) rather than importing the full `metastore.FlowStore` (18 methods). Same interface-segregation principle as M8/M9.

**D-SVC-STG-07 — No logger dependency**
Unlike DeleteFlow / DeleteSegments, storage allocation has no WARN-log path — all errors propagate. Logger excluded from constructor to keep the interface minimal.

**D-SVC-STG-08 — Shared AppServiceMetrics, app_service="storage"**
Same pattern as M8 (flow) and M9 (segment). `New()` accepts `*service.AppServiceMetrics`.

**D-SVC-STG-09 — AllocateStorage performs no metastore writes**
`AllocateStorage` does not insert into `objects`, `segments`, or any other metastore table. It only:
1. Reads the flow (`GetFlow`) for codec + read_only state.
2. (object_ids mode only) Reads `segments` via `IsObjectRegistered` to enforce REQ-BEH-25a.
3. Generates presigned PUT URLs via the objectstore.

The `objects` row for a media object is created lazily by `InsertSegments` on the first segment that references it (see `internal/metastore` BR-META-07 / BR-META-17). Rationale:

- **Spec alignment.** REQ-BEH-17 explicitly states the server does not validate object existence in storage. REQ-REL-14 defines an orphaned object as "uploaded to S3 but never registered as a segment" — a definition that requires the absence of a metadata row, which only holds if `/storage` does not insert one. BBC TAMS ADR 0027 confirms: "Objects are registered in TAMS as part of Flow Segment registration."
- **Symmetry with uncontrolled segments.** Segments registered with `get_urls` (uncontrolled storage, never go through `/storage`) take the same `InsertSegments` lazy-registration code path. Eager creation at `/storage` would split the implementation along controlled vs uncontrolled lines.
- **No write amplification.** Eager creation would add one DB write per allocated id, doubling DB writes per controlled segment (one at `/storage`, one at `/segments`).
- **Concurrency note.** A consequence: two concurrent `POST /storage` calls in `object_ids` mode for the same id can both succeed and both return PUT URLs (last-writer-wins on S3). The pre-check `IsObjectRegistered` only catches *post-registration* reuse. This is a known narrow race; mitigations (advisory lock, dedicated allocation table) are out of scope and tracked as a follow-up. In practice the BBC-cited use case for `object_ids` mode (store-to-store replication, ADR 0034) involves a single coordinated client.

**S3 orphan handling.** A client that obtains a PUT URL, uploads bytes, and then crashes or never calls `/segments` produces an S3 key with no metadata row. This is the orphan case defined in REQ-REL-14 and is cleaned up by the GC reconciliation worker (REQ-GC-05, spec §17): an S3 LIST diffed against the `objects` table, with `min_object_timeout` grace before deletion. The reconciliation worker is a separate milestone (`internal/gc`); until it lands, orphans accumulate in S3 indefinitely.

## Metastore patch required for M10

| Addition | Details |
|---|---|
| `IsObjectRegistered(ctx, objectID string) (bool, error)` on `PostgresStore` | `SELECT EXISTS(SELECT 1 FROM segments WHERE object_id = $1)` |
| Test: TC-META-SEG-XX | IsObjectRegistered returns false for absent objectID, true for registered one |

The method is added to a local `storage.FlowReader` interface; `metastore.PostgresStore` satisfies it.
The method does not need to be added to `SegmentStore` or `FlowStore` — only the local interface in the storage service package references it.

## Test strategy

- Unit tests with hand-rolled mocks of `storage.FlowReader` and `storage.ObjectStore`.
- No real DB or container required.
- 100% statement coverage (REQ-TEST-02).
- Cases:
  - GetFlow error (not found, other)
  - read_only flow → ErrReadOnly
  - limit mode: success
  - limit mode: URL generation error
  - object_ids mode: objectID already registered → ErrObjectIDExists
  - object_ids mode: success
  - object_ids mode: URL generation error
  - observe() nil error path (success cases)
  - observe() AppError path (apperror propagation)
  - observe() non-AppError path (internal error from URL generation)
