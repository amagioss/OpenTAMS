# M12 internal/httpx/handlers — NFR Requirements

## Performance

- Handlers are thin — no blocking work beyond service/store calls. No new latency budget.
- oapi-codegen strict wrapper overhead negligible (<1µs) for I/O-bound API (dominant cost: Postgres, S3).

## Concurrency

- `Handler` struct is stateless after construction. Safe for concurrent use by Gin's goroutine pool.

## Error Handling

- All errors returned as `*apperror.AppError` — caught by `ErrorHandler` middleware.
- oapi-codegen param parsing errors (malformed UUID, bad query param) are handled via `GinServerOptions.ErrorHandler` — they fire before the handler runs and cannot be returned from the handler. The custom `ErrorHandler` writes ProblemDetails directly.
- `PostFlowSegments` partial success: return `PostFlowSegments200JSONResponse`, NOT an error.

## Observability

- No handler-level metrics (service layer records `opentams_app_service_operation_duration_seconds`).
- Per-request logging via `httplog.LoggerFromContext(ctx)`.

## Testing

- 100% statement coverage.
- Handlers tested without Gin — strict mode, call handler methods directly with typed request objects.
- Store/service mocks: hand-rolled.
- Generated code (`gen/api/`) not tested — `go build ./gen/api/...` in CI is sufficient.

## Tech Stack Additions

| Package | Purpose | Decision |
|---|---|---|
| `github.com/oapi-codegen/oapi-codegen/v2` | Code generation tool | `tools/tools.go` blank import — build-time only |
| `github.com/oapi-codegen/runtime` | Runtime types used by generated code (`openapi_types.UUID` etc.) | Runtime dependency — must be in `go.mod` |
| `github.com/google/uuid` | Already present | Path param types |
