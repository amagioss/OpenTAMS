# NFR Requirements — internal/service/storage

## Performance

- **NFR-SVC-STG-P1**: AllocateStorage is synchronous. Latency = 1 GetFlow + (1 IsObjectRegistered × N in object_ids mode) + N GenerateUploadURL calls. No goroutines, no retries.
- **NFR-SVC-STG-P2**: IsObjectRegistered calls are sequential with fail-first semantics. No parallel DB fan-out.
- **NFR-SVC-STG-P3**: Prometheus observation wraps the full AllocateStorage call duration.

## Reliability / Error Handling

- **NFR-SVC-STG-R1**: All errors propagate immediately — no retries, no fallbacks.
- **NFR-SVC-STG-R2**: No partial allocation. First GenerateUploadURL failure aborts the entire call.
- **NFR-SVC-STG-R3**: IsObjectRegistered fail-first: first already-registered objectID returns ErrObjectIDExists immediately.
- **NFR-SVC-STG-R4**: No WARN-log path. Logger excluded from constructor.

## Security

- **NFR-SVC-STG-S1**: read_only check is mandatory in the service layer (GenerateUploadURL bypasses the store-level flowWritable guard).
- **NFR-SVC-STG-S2**: Content-type is server-derived from flow.Codec and bound at presign time; callers cannot override it.

## Observability

- **NFR-SVC-STG-O1**: `opentams_app_service_operation_duration_seconds{app_service="storage",operation="allocate"}` — histogram.
- **NFR-SVC-STG-O2**: `opentams_app_service_errors_total{app_service="storage",operation="allocate",error_code=<code>}` — counter on error. error_code = apperror.Code string or "internal".
- **NFR-SVC-STG-O3**: Metrics instance injected via *service.AppServiceMetrics; not registered inside this package.

## Maintainability / Testability

- **NFR-SVC-STG-M1**: 100% statement coverage (REQ-TEST-02).
- **NFR-SVC-STG-M2**: Hand-rolled mocks for FlowReader and ObjectStore (function-field pattern, same as M8/M9).
- **NFR-SVC-STG-M3**: No real DB or network in unit tests.
- **NFR-SVC-STG-M4**: storageService is unexported; StorageService interface is the only exported handle.
