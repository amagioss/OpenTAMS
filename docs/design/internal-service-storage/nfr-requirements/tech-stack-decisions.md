# Tech Stack Decisions — internal/service/storage

## Metrics

| Decision | Choice | Rationale |
|---|---|---|
| Metrics instance | Inject *service.AppServiceMetrics | Shared across M8/M9/M10; registered once at app startup |
| app_service label | "storage" | Distinguishes storage allocation from flow/segment operations |
| Operation label | "allocate" | Single operation in this service |

## Dependencies

| Dependency | Decision | Rationale |
|---|---|---|
| Logger | None | No WARN-log path exists; all errors propagate. Excluding logger keeps constructor minimal (D-SVC-STG-07) |
| UUID generation | github.com/google/uuid.New() | Already a project dependency; used in M4/M6 |
| FlowReader interface | Local (2 methods: GetFlow, IsObjectRegistered) | Interface segregation — same principle as M8/M9 |
| ObjectStore interface | Local (1 method: GenerateUploadURL) | Narrower than internal/objectstore.ObjectStore |

## Testing

| Decision | Choice | Rationale |
|---|---|---|
| Mocking strategy | Hand-rolled with function fields | Consistent with M8/M9; no gomock/testify dependency |
| Mock: FlowReader | mockFlowReader{getFlow, isObjectRegistered func fields} | Covers all call paths |
| Mock: ObjectStore | mockObjectStore{generateUploadURL func field} | Covers success + error paths |
| DB/network | None required | All dependencies injected via interfaces |

## Error Handling

| Decision | Choice | Rationale |
|---|---|---|
| Error propagation | Return as-is (no wrapping) | Callers (M12 handlers) inspect apperror.Code directly |
| error_code for metrics | apperror.Code string or "internal" | Consistent with M8/M9 observe() pattern |
