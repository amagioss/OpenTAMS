# Tech Stack Decisions — internal/service/flow

## Metrics
**Decision**: `pkg/metrics` Prometheus histogram + counter using a shared family pattern:
- `opentams_app_service_operation_duration_seconds{app_service="flow", operation="upsert"}`
- `opentams_app_service_operation_errors_total{app_service="flow", operation="upsert", error_code="not-found"}`

`app_service` label (not `service`) avoids collision with Kubernetes `service` label conventions. The `opentams_app_service_*` family is a project-wide pattern — follow it where it fits naturally, but do not force it on metrics that don't map cleanly to a service+operation model (e.g. health checks, pool stats).
**Rationale**: Single metric family covers flow, segment, and storage service layers; dashboards can compare across services in one panel without multiple metric names.
**Rejected**: Per-service metric names (e.g. `opentams_flow_service_duration_seconds`) — redundant subsystem in name, harder to compare across service layers in one query.

## Logger
**Decision**: `pkg/logger` (zap-backed structured logger).
**Rationale**: Project-wide decision. Needed in `DeleteFlow` for WARN-level objectstore failure logging (BR-SVC-FLOW-06).
**Rejected**: N/A — project standard.

## Test mocking strategy
**Decision**: Hand-rolled interface mocks for `metastore.FlowStore` and `objectstore.Store`. No mock generation library.
**Rationale**: Both are small interfaces. Hand-rolled mocks are explicit, readable, and don't add a code-generation dependency. Same pattern as M4 and M7.
**Rejected**: `gomock`/`testify/mock` — adds dependency and generated boilerplate for small interfaces.

## GIN index on essence_parameters (M4 patch)
**Decision**: Add `CREATE INDEX flows_essence_gin ON flows USING gin (essence_parameters)` in a new migration (000003) as part of the M4 patch.
**Rationale**: `frame_width`/`frame_height` JSONB path queries (`essence_parameters->>'frame_width'`) degrade to full-table scans without a GIN index at scale (8.6M+ flows at target throughput).
**Rejected**: No index — acceptable only for very low cardinality deployments; cannot be the default.

## vfr/frame_rate validation placement
**Decision**: Service layer (`UpsertFlow`), parsing `EssenceParameters` JSONB before calling the store.
**Rationale**: User decision. Store is pure data access; validation logic belongs at the service boundary where domain rules are enforced.
**Rejected**: Handler layer — handler should be HTTP-only (parse, validate HTTP concerns, delegate); Store layer — store has no knowledge of format-specific rules.
