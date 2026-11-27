# Functional Design — internal/service/storage

## Purpose

Service layer for `POST /flows/{flowId}/storage` (REQ-BEH-24). Sits between HTTP
handlers (M12) and the metastore/objectstore backends. Responsible for:
- Fetching the flow (for codec and read_only state)
- Enforcing read_only access control
- Allocating object IDs (limit mode: server-assigned UUIDs; object_ids mode: client-provided)
- Generating presigned upload URLs via the objectstore
- Recording each issued object ID as an allocation (`allocations` table, [ADR-0041](../../../adr/0041-track-storage-allocations-in-a-table.md))

The allocation flow is specified in the GC design, [logic model section 4](../../internal-gc/functional-design/business-logic-model.md#4-allocation-at-post-flowsflowidstorage), and rules BR-GC-01 to BR-GC-08 of the [GC business rules](../../internal-gc/functional-design/business-rules.md). This document summarises it.

## Dependencies

| Dependency | Interface | Purpose |
|---|---|---|
| `internal/metastore` | local `storage.FlowReader` (`GetFlow`) | Fetch flow for codec + read_only check |
| `internal/metastore` | local `storage.FlowReader` (`AllocateObjects`) | Record the allocations in one transaction |
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

Constructor:

```go
New(store FlowReader, obj ObjectStore, m *service.AppServiceMetrics, storageID string, allocTTL time.Duration) StorageService
```

`serve` passes `cfg.ObjectStoreID` (`OPENTAMS_OBJECT_STORE_ID`) as `storageID`, and `cfg.ObjectStorePresignExpiry` (`OBJECT_STORE_PRESIGN_EXPIRY`) as `allocTTL`.

## Local store interface

```go
type FlowReader interface {
    GetFlow(ctx context.Context, id uuid.UUID) (*metastore.Flow, error)
    AllocateObjects(ctx context.Context, p metastore.AllocateParams) error
}
```

`metastore.PostgresStore` satisfies `FlowReader`. `AllocateObjects` is in `internal/metastore/allocations.go`.

## Business Rules

### AllocateStorage

**BR-SVC-STG-01 — Get flow**
- Call `store.GetFlow(ctx, flowID)`.
- On `ErrNotFound` or any error: propagate to caller.

**BR-SVC-STG-02 — read_only check (REQ-BEH-11)**
- After GetFlow, check `flow.ReadOnly`. If true: return `apperror.New(apperror.ErrReadOnly, "flow is read-only")`.
- The service enforces this inline, before any URL or allocation is made.

**BR-SVC-STG-03 — Content-type derivation (REQ-BEH-25b)**
- `contentType = *flow.Codec` if `flow.Codec != nil`, else `"application/octet-stream"`.
- Used as the Content-Type binding in `GenerateUploadURL` and returned in `MediaObject.ContentType`.

**BR-SVC-STG-04 — ID list and URL generation**
- limit mode: generate `*req.Limit` new UUIDv4 values (`uuid.New().String()`).
- object_ids mode: use `req.ObjectIDs`.
- For each ID, in request sequence, call `obj.GenerateUploadURL(ctx, objectID, contentType)`.
- On URL generation error: propagate immediately. No allocation is written (BR-GC-05).

**BR-SVC-STG-05 — Record the allocations (BR-GC-01 to BR-GC-04)**
- Call `store.AllocateObjects(ctx, metastore.AllocateParams{FlowID, StorageID: storageID, TTL: allocTTL, IDs, Requested: <object_ids mode>})`.
- The store writes one `allocations` row for each ID, with `expires_at = now() + allocTTL`, in one transaction. It locks the IDs in ascending byte order (BR-GC-06).
- object_ids mode, for each ID (BR-GC-03):
  - An `objects` row with `deleting = false`: `metastore.ErrObjectExists`.
  - An `objects` row or an `allocations` row with `deleting = true`: `metastore.ErrObjectDeleting`.
  - An `allocations` row for a different flow: `metastore.ErrObjectWrongFlow`.
  - An `allocations` row for the same flow: the store sets a new `expires_at`. The request gets a new URL.
- Each store error is a `*metastore.ObjectError{ID, Err}`. `errors.Is` works through `Unwrap`.

**BR-SVC-STG-06 — Error mapping, all or nothing (BR-GC-04)**

| Store error | `apperror` code | HTTP |
|---|---|---|
| `metastore.ErrObjectExists` | `ErrObjectExists` (`object-exists`) | 400 |
| `metastore.ErrObjectDeleting` | `ErrObjectDeleting` (`object-deleting`) | 400 |
| `metastore.ErrObjectWrongFlow` | `ErrObjectWrongFlow` (`object-wrong-flow`) | 400 |
| other | unchanged | 500 |

- The service gets the ID with `errors.As(err, &oe)` and puts it in the detail text (BR-GC-70 to BR-GC-73).
- On the first error, the store rolls back. The request allocates no ID. The client must fix the invalid ID and retry.

## Decisions

**D-SVC-STG-01 — Mutual exclusion validation in M12 handler**
`ErrStorageModeConflict` (when both/neither of `limit`/`object_ids` are present) is a request schema concern; M12 handler returns 400 before calling the service. Service assumes exactly one of `Limit` or `ObjectIDs` is set.

**D-SVC-STG-02 — read_only check in service, not store**
`AllocateObjects` does not run `flowWritable`. Service checks `flow.ReadOnly` after GetFlow. This is consistent with REQ-BEH-11.

**D-SVC-STG-03 — The registration check is cross-flow**
An object ID with an `objects` row is "in use" for all flows, and gets `object-exists`. A flow-scoped check would be insufficient for shared objects (REQ-BEH-21). An allocation is flow-scoped: a different flow gets `object-wrong-flow`.

**D-SVC-STG-04 — Nil codec defaults to "application/octet-stream"**
REQ-BEH-25b binds the upload URL to the flow's codec. If codec is absent (format allows it), `"application/octet-stream"` is the fallback. Object stores accept this for binary blobs.

**D-SVC-STG-05 — No partial success**
One rejected ID fails the request. Rationale: partial allocation creates inconsistent state (some IDs allocated, some not) that the caller cannot easily recover from. All-or-nothing is simpler and more predictable.

**D-SVC-STG-06 — local FlowReader interface**
Service defines its own `FlowReader` interface (2 methods) rather than importing the full `metastore.FlowStore`. Same interface-segregation principle as M8/M9.

**D-SVC-STG-07 — No logger dependency**
Unlike DeleteFlow / DeleteSegments, storage allocation has no WARN-log path — all errors propagate. Logger excluded from constructor to keep the interface minimal. The handler logs a 400 `AppError` at WARN.

**D-SVC-STG-08 — Shared AppServiceMetrics, app_service="storage"**
Same pattern as M8 (flow) and M9 (segment). `New()` accepts `*service.AppServiceMetrics`.

**D-SVC-STG-09 — AllocateStorage writes allocations, not objects**
`AllocateStorage` writes only `allocations` rows. It does not insert into `objects` or `segments`. The `objects` row for a media object is still created lazily by `InsertSegments` on the first segment that references it (`internal/metastore` BR-META-07 / BR-META-17, [ADR-0019](../../../adr/0019-lazy-object-registration.md)). At that point the same transaction deletes the allocation row. Rationale for the allocation record ([ADR-0041](../../../adr/0041-track-storage-allocations-in-a-table.md)):

- **Unregistered uploads become visible.** Before the change, a client that uploaded bytes and never registered a segment left an S3 key with no row. Sweep 2 of the GC now finds such bytes through the allocation row and deletes them after `expires_at + GC_ALLOCATION_GRACE` (REQ-GC-09).
- **Allocation enforcement.** A controlled segment must use an allocated or registered object ID (REQ-GC-10). Segments with `get_urls` (BYOS) need no allocation.
- **Concurrency.** Two concurrent `POST /storage` calls in `object_ids` mode for the same ID on two flows cannot both succeed: the second gets `object-wrong-flow`. On the same flow, both succeed and the allocation gets the later expiry.

**Bytes with no row.** Bytes with no `objects` row and no `allocations` row (for example, from an upload that started before migration 000006) are not found by the GC. A reconciliation with a bucket listing is later work (REQ-REL-14).

## Test strategy

- Unit tests with hand-rolled mocks of `storage.FlowReader` and `storage.ObjectStore`.
- No real DB or container required.
- 100% statement coverage (REQ-TEST-02).
- Cases:
  - GetFlow error (not found, other)
  - read_only flow → ErrReadOnly
  - limit mode: success, `AllocateObjects` called with `Requested = false`
  - limit mode: URL generation error, no `AllocateObjects` call
  - object_ids mode: success, `AllocateObjects` called with `Requested = true`
  - object_ids mode: `ErrObjectExists`, `ErrObjectDeleting`, `ErrObjectWrongFlow` → the matching `apperror` code, with the ID in the detail
  - object_ids mode: URL generation error
  - `AllocateObjects` other error → 500
  - observe() nil error path (success cases)
  - observe() AppError path (apperror propagation)
  - observe() non-AppError path (internal error from URL generation)

The store tests of `AllocateObjects` are in the GC test plan.
