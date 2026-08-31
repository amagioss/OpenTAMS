# NFR Requirements — internal/service/flow

## Performance
- `UpsertFlow` adds only synchronous in-memory validation before a single store call. Must not push flow PUT above the REQ-PERF p99 target of < 100ms.
- `GetFlow` with `IncludeTimerange=true` adds one extra Postgres round-trip (`GetFlowTimerange`). Both use the same pool; net impact negligible.
- `DeleteFlow` objectstore deletes are sequential per zero-ref object. Acceptable in Phase 1 (flow deletes are infrequent). Phase 2 can batch if needed.
- `ListFlows` JSONB filters (`frame_width`, `frame_height`) require a GIN index on `essence_parameters` in the M4 patch migration — without it, queries degrade to full-table scans at scale.

## Metrics naming
Service-layer metrics use the shared family pattern:
- `opentams_app_service_operation_duration_seconds{app_service="flow", operation="..."}`
- `opentams_app_service_operation_errors_total{app_service="flow", operation="...", error_code="..."}`

`app_service` avoids collision with Kubernetes `service` label. This family pattern is followed project-wide where it fits naturally; not mandated for metrics that don't map to a service+operation model.

## Scalability
Service is stateless. Inherits horizontal scaling from REQ-ARCH-03. No new state introduced.

## Reliability
- All store and objectstore calls propagate the caller's `context.Context` (REQ-REL-15). No fire-and-forget.
- Objectstore delete failures in `DeleteFlow` are WARN-logged and swallowed. Orphans handled by GC (REQ-REL-14).
- Service errors surface to the handler as-is; the handler maps apperror codes to HTTP status.

## Security
- `vfr`/`frame_rate` consistency check in `UpsertFlow` validates before any store write, preventing malformed video flows from reaching the DB.

## Maintainability
- `FlowService` is an interface; handlers (M12) depend on the interface, enabling unit testing with mocked service.
- Service tests use hand-rolled mocks of `metastore.FlowStore` and `objectstore.Store` — no real DB or container required.
- 100% statement coverage required (REQ-TEST-02).
