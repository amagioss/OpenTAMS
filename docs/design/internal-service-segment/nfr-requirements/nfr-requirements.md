# NFR Requirements — internal/service/segment

## Performance
- `RegisterSegments` adds zero computation beyond the store call (no service-level validation). Must not push POST /segments above the REQ-PERF p99 target.
- `DeleteSegments` objectstore deletes are sequential per zero-ref object. Acceptable in Phase 1 (segment deletes are infrequent). Phase 2 can batch if needed — same trade-off as `DeleteFlow` in M8.
- `ListSegments` is a thin pass-through; no performance concern at the service layer.

## Metrics naming
Service-layer metrics follow the shared family pattern (established in M8):
- `opentams_app_service_operation_duration_seconds{app_service="segment", operation="..."}`
- `opentams_app_service_operation_errors_total{app_service="segment", operation="...", error_code="..."}`

Operations: `register`, `list`, `delete`. No sub-resource operations in the segment service.

## Scalability
Service is stateless. Inherits horizontal scaling from REQ-ARCH-03. No new state introduced.

## Reliability
- All store and objectstore calls propagate the caller's `context.Context` (REQ-REL-15). No fire-and-forget.
- Objectstore delete failures in `DeleteSegments` are WARN-logged and swallowed. Orphans handled by GC (REQ-REL-14). Same pattern as `DeleteFlow` in M8.
- Service errors surface to the handler as-is; the handler maps apperror codes to HTTP status.

## Security
- No service-level security logic. `read_only` enforcement is in the store layer (M4 BR-META-15). Service passes through store errors including `ErrReadOnly`.

## Maintainability
- `SegmentService` is an interface; handlers (M12) depend on the interface, enabling unit testing with mocked service.
- Service tests use hand-rolled mocks of `metastore.SegmentStore` and `segment.ObjectStore` — no real DB or container required.
- 100% statement coverage required (REQ-TEST-02).
