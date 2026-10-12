# Functional Design — internal/service/segment

## Purpose

Service layer between the HTTP handlers and the metastore for Segment operations.
It validates what the OpenAPI layer cannot, forwards accepted work to the
metastore, classifies failures, and emits the per-operation metrics and logs.

## Interface

```go
type Service interface {
    RegisterBatch(ctx context.Context, req domain.RegisterParams) (domain.RegisterResult, error)
    List(ctx context.Context, req domain.ListParams) (domain.SegmentPage, error)
    Delete(ctx context.Context, req domain.DeleteParams) (domain.DeleteResult, error)
}
```

`Service` (`segment.go:26`) is exported as an interface so handlers depend on
the interface; the concrete `segmentService` struct is unexported. Every parameter
and result type is from `internal/domain` — a pure leaf package with no driver, no
logger, and no metrics — so neither this package nor the metastore ever sees a
generated wire type.

## Dependencies

`Deps` (`segment.go:44`) is the constructor-injection bundle:

| Field | Purpose |
|---|---|
| `Meta metastore.Store` | All segment persistence |
| `Logger *zap.Logger` | Per-failed-segment WARN logs; defaults to no-op when nil |
| `Metrics *service.AppServiceMetrics` | Shared Prometheus families; MUST be non-nil |
| `ControlledStorageID string` | Deployment-wide controlled-storage backend id, forwarded verbatim into every `metastore.InsertBatch` |

**There is no `ObjectStore` field.** The service never calls the object store on
any path — see BR-SEG-10.

## Business rules

### RegisterBatch

**BR-SEG-01 — Empty batch is a caller error**
`len(req.Segments) == 0` returns `ErrInvalidRequest` without touching the metastore.

**BR-SEG-02 — Service-level per-segment validation**
Before the metastore call, each segment is checked for an empty `ObjectID`. A
segment failing this is moved to `Failed` with `Type` from the `schema-validation`
catalogue entry, `Title` `"Schema Validation Failed"`, and `Status` 400 — it is
never sent to the store. Every supplied `GetURL` also has `Controlled` stamped
`false`; the conversion layer already does this for the HTTP path, so this is a
safety net for direct callers.

The service also examines each segment timerange ([ADR-0040](../../../adr/0040-segment-timerange-invariants.md) rules 1 and 2). A segment
fails if its timerange:
- Is empty, including an end before the start
- Has no start or no end (`_`, `(5:0_`, `[5:0_`, `_10:0)`)
- Has an exclusive start (`(0:0_10:0)`)
- Has a bound that does not fit in `int64` nanoseconds (`timerange.ErrOutOfRange`).

The service moves a failing segment to `Failed`. It sets `Type` from the
`invalid-timerange` catalogue entry, `Title` to `"Invalid Timerange"`, and `Status` to
400. It sets `Reason` to a message that names the rule, for example
`segment timerange must have an inclusive start and a bounded end`. The service never
sends the segment to the store. The end can be inclusive or exclusive. `[0:0]` is valid,
and is the TAMS form for a sample without a duration.

These are per-segment failures in a 200 response, not a 400 for the request. TAMS says
that "processing should continue" and that "A 200 response should be returned listing
the failed Segments". A timerange string that fails the schema regex never gets to this
check, because spec validation rejects the whole request with 400.

**BR-SEG-03 — Overlap rejects the whole batch**
The service does not compute timerange overlap. `metastore.InsertSegments` detects
it — both within-batch and against-existing — and returns `ErrSegmentOverlap` for
either source. The service forwards that error unwrapped and returns an empty
`RegisterResult`. There is no first-wins partition and no retry of the
non-overlapping subset: retrying would require the service to know which segments
overlapped and drop them, which re-introduces first-wins ordering one layer up.
The caller retries with a corrected batch.

Overlap detection is metastore-side because the metastore holds the existing-segment
index; detecting it here would mean re-fetching every segment on the flow or
duplicating the `internal/timerange` predicate. This diverges from TAMS
`REQ-BEH-15` deliberately — see
[ADR-0024](../../../adr/0024-whole-batch-reject-on-segment-overlap.md).

**BR-SEG-04 — Outcome classification**
When the metastore call succeeds, the result is partitioned into `Accepted` and
`Failed` and one of three outcomes is set:

| Condition | `Outcome` | Handler response |
|---|---|---|
| `len(Failed) == 0 && len(Accepted) > 0` | `OutcomeAllAccepted` | 201, no body |
| both non-empty | `OutcomePartial` | 200 with `failed_segments` |
| `len(Accepted) == 0 && len(Failed) > 0` | `OutcomeAllRejected` | 200 with `failed_segments`, empty `accepted` |

The overlap path bypasses this enum entirely and is rendered as a 422
ProblemDetails with no `failed_segments`. `RegisterOutcome` starts at `iota+1` so
the zero value is invalid.

Because `metastore.InsertSegments` is all-or-nothing (BR-META-06), the only
failures that reach `Failed` today are the ones this service raises itself in
BR-SEG-02.

**BR-SEG-05 — Error translation at the package boundary**
The service declares its own sentinels — `ErrFlowNotFound`, `ErrFlowReadOnly`,
`ErrInvalidRequest` — and maps `metastore.ErrFlowNotFound` / `ErrFlowReadOnly` onto
them. It deliberately does **not** redeclare `ErrSegmentOverlap` or
`ErrSegmentObjectReaping`; those flow up from the metastore unwrapped so there is a
single source for each.

**BR-SEG-06 — Failure entries carry the problem-type URI, not a private enum**
`domain.FailedSegment` carries `Type`, `Title`, and `Reason`. The service stamps
`Type` and `Title` together at the moment of classification and the HTTP layer
copies them verbatim rather than re-classifying. The alternative — a domain-private
enum mapped to a URI in the handler — would duplicate the failure catalogue across
two packages, so a new failure category would need coordinated edits in both. A URI
in a domain type is a small wire-shaped leak accepted for that reduction in
coupling.

**BR-SEG-07 — The controlled-storage identifier is service-owned and forwarded per call**
`Deps.ControlledStorageID` is forwarded verbatim into `metastore.InsertBatch` on
every `RegisterBatch`. The metastore stamps it onto `objects.storage_id` for
controlled segments and stores NULL for BYOS ones (BR-META-07).

It lives here rather than in the metastore because it is deployment configuration:
holding it in the persistence layer would couple that layer to environment reads,
force every metastore test to hardcode a value, and block a future per-flow or
per-segment backend choice from evolving without rippling into the metastore.

An empty `ControlledStorageID` is legal for BYOS-only deployments; a controlled
segment submitted to such a deployment fails with
`metastore.ErrControlledStorageNotConfigured` on first controlled write.

**BR-SEG-08 — Deprecated and pass-through fields are stored, never interpreted**
`SampleOffset` and `SampleCount` (deprecated in TAMS v8) are persisted and returned
unchanged. No business logic reads them. `TSOffset`, `ObjectTimerange`, and
`LastDuration` round-trip as the exact string the client sent, including negative values
(BR-META-10). The service does no timeline arithmetic. `ObjectTimerange` is
a pointer whose `nil` means "the client did not supply one"; the service never
synthesises a value. Validating a supplied `object_timerange` against the one
already recorded for that object is a metastore invariant (BR-META-19), not a
service concern.

### List

**BR-SEG-09 — Forward with translation; pagination is the metastore's**
`List` maps `domain.ListParams` onto `metastore.ListQuery` field-for-field and
forwards. Filtering, ordering, limit clamping, and cursor handling live in the
metastore; the cursor is opaque here and is never decoded, validated, or re-encoded.
`metastore.ErrFlowNotFound` becomes `ErrFlowNotFound` — which the handler renders as
404, a deliberate divergence from `REQ-BEH-16` (see below).
`ErrInvalidCursor` is classified as `schema-validation`.

The service classifies a query `timerange` whose bounds do not fit in `int64`
nanoseconds as `invalid-timerange` (400). An empty query timerange is not an error. It
returns an empty page (BR-META-21).

The read path does not consult `ControlledStorageID`. Controlled-vs-BYOS is derived
from the segment's own shape (`len(seg.GetURLs) == 0` ⇒ controlled), so no join onto
`objects` and no service-held config is needed to serve a read.

### Delete

**BR-SEG-10 — Metadata removal only; the GC owns object cleanup**
`Delete` forwards to `metastore.DeleteSegmentsByTimerange` and returns
`DeleteResult{DeletedCount}`. The store deletes only segments that the query timerange
contains, and an empty query timerange deletes nothing (BR-META-08 and
BR-META-21). The metastore decrements `objects.ref_count` inside
the same transaction; rows that reach zero stay in `objects` until the GC worker
sweeps them. The service does not call the object store and does not return released
object IDs — `ReleasedObjects` was removed from `DeleteResult` when reaping moved to
the GC.

Inline deletion was rejected: a 10k-segment delete would put 10k object-store round
trips on a user-visible request that cannot roll them back, and BYOS segments have no
bytes the server may delete at all. Batching also needs the GC's cross-request view
of `ref_count = 0`, which a single request does not have. A retry loop inside the
service would additionally duplicate state the GC already holds in the
`(ref_count = 0, reaping = false)` view.

The GC worker (`internal/gc`) is not yet built; until it lands, zero-ref rows and
their bytes accumulate. See [`docs/conformance.md`](../../../conformance.md).

### All write paths

**BR-SEG-11 — `read_only` is checked under a row lock, not by the service**
Both `RegisterBatch` and `Delete` rely on the metastore reading the flow row with
`SELECT … FOR UPDATE` inside the write transaction and returning `ErrFlowReadOnly`
(BR-META-15). A service-side pre-check would leave a window in which a concurrent
`PUT /flows/{id}/read_only` flips the flag between the read and the write.

**BR-SEG-12 — The service does not participate in idempotency**
It does not read `X-Idempotency-Key`, hash bodies, or import `internal/idempotency`.
The handler owns that: on a cache hit the service is never called; on a first call
the handler drives `Acquire` → service → `Complete` or `Release`. The service is
therefore safe to call twice for the same logical request — its only side effects
are the database mutations its input describes, and it holds no internal state.

**BR-SEG-13 — Flow-level denormalisation happens in the write transaction**
`flows.segments_updated` and the computed `flows.timerange` are refreshed by the
metastore inside the same transaction as the segment write (BR-META-12). The service
issues no second call.

**BR-SEG-14 — Database constraints are backstops, not validation**
The service rejects every client input that can break a database constraint before it
calls the store. BR-SEG-02 covers segments, and the query checks under `List` and
`Delete` cover queries. Thus a constraint violation (SQLSTATE class `23`) is a server
defect. The server returns 500, and the handler releases the idempotency key
(BR-IDMP-02). See
[ADR-0040](../../../adr/0040-segment-timerange-invariants.md) rule 3.

## Observability

**Per-operation** (`observe`, called by all three methods): a duration histogram
labelled `("segment", op)` and, on error, an error counter labelled
`("segment", op, code)`. `op` is one of `register`, `list`, `delete`. The families
themselves are `internal/service.AppServiceMetrics` —
`opentams_app_service_operation_duration_seconds`, `…_errors_total`, `…_total`, and
`…_items_total`, all shared across services and labelled by `app_service`. The
service emits; the metric names and label cardinality are the metrics package's
decision.

`classifyErr` resolves the metric code in a fixed order: service and metastore
sentinels first, then an `*apperror.AppError` code via `errors.As`, then
`"internal"` as the catch-all.

**Register-specific**: one WARN per failed segment carrying `domain`, `action`,
`outcome`, `failure_type`, `object_id`, `timerange`, and `flow_id`. There is no
additional aggregate WARN — the counts live on the metric families. Outcome
counters use the stable labels `all_accepted`, `partial`, `all_rejected`, and
`unknown`; dashboards depend on these strings.

## Error catalogue reference

Problem type URIs and titles are owned by `internal/apperror`, which exposes
`ErrorCode` string constants and an internal `catalogue` map from code to
`{status, title}`. The service obtains a type URI by constructing an `AppError`
and reading `ToProblemDetails("", "").Type`. There are no `apperror.Type*` or
`apperror.Title*` constants; the catalogue map is the single registry.

## Spec divergences

| Rule | TAMS requirement | OpenTAMS behaviour | Recorded in |
|---|---|---|---|
| BR-SEG-03 | `REQ-BEH-15` — first-wins ordering, overlapping entries reported per segment | Any overlap rejects the whole batch: 422, nothing persisted, no `failed_segments` | [ADR-0024](../../../adr/0024-whole-batch-reject-on-segment-overlap.md), [`conformance.md`](../../../conformance.md) |
| BR-SEG-09 | `REQ-BEH-16` — `GET /flows/{flowId}/segments` on an unknown flow returns an empty list | Returns 404 `flow not found` | [`conformance.md`](../../../conformance.md) |
| BR-SEG-02 | The segment schema accepts any `TimeRange`. App Note 0012 describes a bounded range with an inclusive start. | An unbounded, empty, or exclusive-start segment timerange is a per-segment `invalid-timerange` failure | [ADR-0040](../../../adr/0040-segment-timerange-invariants.md), [`conformance.md`](../../../conformance.md) |

## Out of scope

- **Segment modification.** There is no `UpdateSegment`; TAMS v8 has no PUT for a
  segment (`REQ-BEH-23`). Moving a segment on the timeline is DELETE + POST.
- **Cross-flow operations.** Every call is scoped to one `FlowID`.
- **Authorisation.** The auth middleware gates the handler; the service trusts that
  a call it receives is authenticated. Phase 1 authorisation is all-or-nothing, so
  there is no per-segment check.
- **URL synthesis.** The service stores only client-supplied uncontrolled URLs. The
  handler builds the `get_urls` a reader sees, so the service stays
  storage-agnostic.
- **TAMS `error.json` compatibility.** The error contract is RFC 9457 throughout;
  see [ADR-0023](../../../adr/0023-rfc9457-problem-details-for-per-segment-failures.md)
  for the one remaining site.

## Test strategy

Unit tests use a hand-rolled mock of `metastore.Store`; no database or container is
required. `segment_test.go` covers the register, list, and delete paths;
`segment_integration_test.go` exercises the same contract against a real
Postgres via testcontainers and is skipped under `-short`.

## Known gap

`RegisterResult.Failed` carries an RFC 9457 subset — `Type`, `Title`, and the
`Reason` that becomes `error.detail` — and the HTTP layer renders all three
verbatim through `conversion.RegisterFailureToAPI`. `Status` is set here (400 for a
validation failure, 422 for an overlap) but has no slot on the wire; a client
distinguishes failure kinds by the `type` URI. See
[ADR-0023](../../../adr/0023-rfc9457-problem-details-for-per-segment-failures.md).
