# Tech Stack Decisions — pkg/logger

## Logging Library: `go.uber.org/zap` v1.27.1

**Decision**: zap over `log/slog` (stdlib) and `zerolog`

| Factor | slog | zap | zerolog |
|---|---|---|---|
| Allocation on hot path | Higher (any-based args) | Zero | Zero |
| Runtime level switch | `slog.LevelVar` (good) | `zap.AtomicLevel` (good) | Global only |
| JSON output | Built-in | Built-in | Built-in |
| Dependency | None | External | External |
| Ecosystem maturity | New (Go 1.21) | Dominant in production | Moderate |
| Sampler control | Manual | Built-in `zapcore.NewSamplerWithOptions` | Limited |

**Rationale**: At OpenTAMS target throughput (~700 req/s), zap's zero-allocation design prevents GC pressure on the hot request path. `zap.AtomicLevel` is the cleanest mechanism for SIGHUP-triggered level changes. Industry-dominant for production Go services.

**Considered and rejected**:
- `slog`: Higher allocation per log call; `slog.LevelVar` works but slog has less ecosystem support for zero-alloc typed fields
- `zerolog`: Global-only level, no clean per-logger level switching

## Encoder Configuration

| Field | Choice | Rationale |
|---|---|---|
| Time format | RFC 3339 nanosecond | REQ-OBS-01 requires microsecond precision; nanosecond is a superset |
| Time key | `"time"` | REQ-OBS-01 dimensional schema; overrides zap default `"ts"` (unix float) |
| Level key | `"level"` | REQ-OBS-01 dimensional schema |
| Level format | Lowercase | Modern log aggregation systems (Loki, Elasticsearch) prefer lowercase |
| Message key | `"msg"` | REQ-OBS-01 dimensional schema |
| Sampling | Off by default | REQ-OBS-01 — every request must produce a log entry |

## Context Propagation: `context.Context`

**Decision**: Store `*zap.Logger` in context, not raw key-value pairs

**Rationale**: Storing the child logger (already enriched with request-scoped fields via `.With(...)`) means downstream modules call `logger.FromContext(ctx)` and immediately get a fully-enriched logger. No per-call field attachment overhead. Alternative (Elixir-style KV accumulation) required every log call to unpack context, bundle into a `metadata` nested object — breaking REQ-OBS-01's flat field schema.
