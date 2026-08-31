# Functional Design — internal/service/flow

## Purpose

Service layer between HTTP handlers (M12) and the metastore/objectstore backends.
Enforces domain business rules that belong above the store layer but below HTTP concerns.

## Dependencies

| Dependency | Interface used | Purpose |
|---|---|---|
| `internal/metastore` | `metastore.FlowStore` | All flow and sub-resource persistence |
| `internal/objectstore` | `flow.ObjectStore` (subset: `DeleteObjects`) | Zero-ref object cleanup on flow delete |
| `pkg/logger` | `*zap.Logger` | WARN-level logging for objectstore failures |
| `internal/service` | `*service.AppServiceMetrics` | Shared Prometheus metric families (injected at startup) |
| `internal/timerange` | `timerange.Parse`, `timerange.Intersect` | Timerange computation and intersection in GetFlow |
| `internal/apperror` | `apperror.New` | Catalogued error responses |

## Interface: FlowService

```
UpsertFlow(ctx, *Flow) (created bool, err error)
GetFlow(ctx, id, includeTimerange bool, trFilter *timerange.TimeRange) (*Flow, error)
ListFlows(ctx, ListFlowsParams) (*FlowPage, error)
DeleteFlow(ctx, id) error

PutFlowTag / DeleteFlowTag
PutFlowLabel / DeleteFlowLabel
PutFlowDescription / DeleteFlowDescription
PutFlowReadOnly
PutFlowCollection / DeleteFlowCollection
PutFlowAvgBitRate / DeleteFlowAvgBitRate
PutFlowMaxBitRate / DeleteFlowMaxBitRate
```

`FlowService` is exported as an interface so handlers depend on the interface, not the struct.
The concrete `flowService` struct is unexported.

## Business Rules

### UpsertFlow

**BR-SVC-FLOW-01 — vfr/frame_rate consistency (REQ-DATA-04)**
- Applies only to video flows (`format` contains `"video"`) with non-empty `EssenceParameters`.
- Parse `EssenceParameters` JSON. If parse fails → `ErrSchemaValidation`.
- If `vfr` is absent or `false` AND `frame_rate` is absent → `ErrVFRFrameRateConflict`.
- If `vfr` is `true` → `frame_rate` is optional.
- Validation runs **before** any store write. On failure, the store is never called.

**BR-SVC-FLOW-02 — Delegation**
- All other immutable-field enforcement, source existence, format-mismatch, and codec-lock logic lives in `metastore.UpsertFlow` (store layer).
- Service does not duplicate store-layer checks.

**BR-SVC-FLOW-03 — Implicit Source creation (REQ-DATA-01)**
- Handled entirely by `metastore.UpsertFlow`. Service passes through.

### GetFlow

**BR-SVC-FLOW-04 — include_timerange=false**
- Call `store.GetFlow` only. Return as-is. No extra DB round-trip.

**BR-SVC-FLOW-05 — include_timerange=true**
- Call `store.GetFlow`, then `store.GetFlowTimerange`.
- If `GetFlowTimerange` returns nil (no segments), `Flow.Timerange` stays nil.
- If `GetFlowTimerange` returns a value, set `Flow.Timerange` to that value.

**BR-SVC-FLOW-06 — timerange filter with include_timerange=true**
- Parse the computed timerange string via `timerange.Parse`.
- Compute `timerange.Intersect(computed, *trFilter)`.
- Set `Flow.Timerange` to the intersection string (may be `"()"` if no overlap).

### ListFlows

**BR-SVC-FLOW-07 — Delegation**
- Pure pass-through to `store.ListFlows`. All filter and pagination logic lives in the store layer.

### DeleteFlow

**BR-SVC-FLOW-08 — Metadata removal**
- Call `store.DeleteFlow`. The store removes all segment metadata and decrements
  `objects.ref_count`. It returns only an error.
- On store error (including ErrNotFound for non-existent flow), propagate immediately.

**BR-SVC-FLOW-09 — Object lifetime is not this service's job (REQ-BEH-10)**
- The flow service holds no objectstore dependency and deletes no media bytes.
  `DeleteFlow` returns once the metastore transaction commits.
- Objects whose `ref_count` reaches zero become eligible for the GC worker
  (REQ-REL-14). The GC worker owns `objectstore.Store.DeleteObjects`.
- The GC worker is not implemented, so zero-ref objects accumulate today. See
  [`conformance.md`](../../../conformance.md).

### Sub-resource operations (PutFlowTag, PutFlowLabel, etc.)

**BR-SVC-FLOW-10 — Pass-through**
- All sub-resource methods delegate directly to the corresponding `metastore.FlowStore` method.
- `read_only` enforcement is in the store layer (with the exception of `PutFlowReadOnly` itself).
- No metrics wrapping on sub-resource methods (does not map cleanly to the service+operation model).

## Decisions

**D-SVC-FLOW-01 — vfr/frame_rate validation placement: service layer**
Rationale: Store is pure data access; format-specific validation belongs at the service boundary.
Rejected: Handler layer (HTTP-only concern); Store layer (no format knowledge).

**D-SVC-FLOW-02 — ObjectStore interface segregation**
Service defines its own `ObjectStore` interface (one method: `DeleteObjects`).
`objectstore.ObjectStore` (4 methods) satisfies it; service tests mock only what is used.

**D-SVC-FLOW-03 — No idempotency in M8**
Idempotency (REQ-RATE-09) applies only to `POST /segments`. PUT /flows is inherently idempotent at the store level (last-write-wins). No idempotency key handling in the flow service.

**D-SVC-FLOW-04 — Shared AppServiceMetrics injection**
`New()` accepts `*service.AppServiceMetrics` (pre-created at app startup via `service.NewAppServiceMetrics`).
The metric families (`opentams_app_service_operation_duration_seconds`, `opentams_app_service_operation_errors_total`) are registered once and shared across flow, segment, and storage service layers.
Rejected: each service registers its own metric family — fails on second registration with the same registry.

**D-SVC-FLOW-05 — Metrics scope**
Metrics (`dur`, `errs`) are recorded for the four main operations: `upsert`, `get`, `list`, `delete`.
Sub-resource methods are not wrapped with metrics — they are thin single-call delegations that do not map naturally to the service+operation model.

## Metastore gaps patched for M8

The following additions to M4 were required before M8 could be built:

| Addition | Details |
|---|---|
| `ListFlowsParams.FrameWidth *int` | Filter by `essence_parameters->>'frame_width'` |
| `ListFlowsParams.FrameHeight *int` | Filter by `essence_parameters->>'frame_height'` |
| `ListFlowsParams.Timerange *timerange.TimeRange` | Filter flows with overlapping segments |
| `FlowStore.GetFlowTimerange(ctx, id) (*string, error)` | Returns `MIN/MAX(lower_ns/upper_ns)` as TAMS bracket notation; nil if no segments |
| `migrations/000003_gin_index.up.sql` | GIN index on `flows.essence_parameters` for frame_width/frame_height queries |

## Test strategy

- Unit tests with hand-rolled mocks of `metastore.FlowStore` and `flow.ObjectStore`.
- No real DB or container required.
- 100% statement coverage required (REQ-TEST-02). Achieved.
- Coverage of all vfr/frame_rate branches, all GetFlow include_timerange paths, all DeleteFlow objectstore paths (success, failure, empty zero-refs).
