# Tech Stack Decisions — internal/service/segment

## Metrics
**Decision**: Shared `service.AppServiceMetrics` family; `app_service="segment"`.
- `opentams_app_service_operation_duration_seconds{app_service="segment", operation="register|list|delete"}`
- `opentams_app_service_operation_errors_total{app_service="segment", operation="...", error_code="..."}`

**Rationale**: Same family as M8 — registered once at app startup, injected via `New()`. Avoids duplicate registration conflicts when both flow and segment services are active. `app_service="segment"` distinguishes from `"flow"` in dashboards.
**Rejected**: Per-service metric families — duplicate registration error when both M8 and M9 are wired up (confirmed in M8 refactor).

## Logger
**Decision**: `pkg/logger` (zap-backed structured logger).
**Rationale**: Project-wide decision. Needed in `DeleteSegments` for WARN-level objectstore failure logging (BR-SVC-SEG-06).
**Rejected**: N/A — project standard.

## Test mocking strategy
**Decision**: Hand-rolled interface mocks for `metastore.SegmentStore` and `segment.ObjectStore`. No mock generation library.
**Rationale**: Both are small interfaces. Hand-rolled mocks are explicit, readable, and don't add a code-generation dependency. Same pattern as M8.
**Rejected**: `gomock`/`testify/mock` — adds dependency and generated boilerplate for small interfaces.

## ObjectStore interface
**Decision**: Local `segment.ObjectStore` interface with one method: `DeleteObjects(ctx, []string) error`.
**Rationale**: Same interface segregation pattern as M8 `flow.ObjectStore`. Service tests mock only what is used. `objectstore.ObjectStore` (4 methods) satisfies the interface implicitly.
**Rejected**: Import `objectstore.ObjectStore` directly — over-specification; violates interface segregation.
