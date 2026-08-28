# Business Logic Model — pkg/logger

## Responsibilities
Context-agnostic structured JSON logger. Zero TAMS domain knowledge.
TAMS-specific fields (request_id, flow_id, auth_subject) are injected at call site by `internal/httpx/middleware`.

## Core Functions

### Logger Construction
- Accept `io.Writer` and initial `zapcore.Level`
- Accept zero or more `Option` values for optional behaviour
- Return `(*zap.Logger, zap.AtomicLevel)` — caller owns both
- The `AtomicLevel` is the hook for SIGHUP-triggered level changes

### Level Management
- Initial level set at construction
- Runtime level change via `atom.SetLevel(level)` — no restart required
- Level parsing from string (`"debug"` / `"info"` / `"warn"` / `"error"`, case-insensitive)
- Rejection of unknown strings with descriptive error

### Context Propagation
- Store a `*zap.Logger` in a `context.Context` (one per request — child logger with request-scoped fields)
- Retrieve from context; fall through to caller-supplied fallbacks; final fallback is nop logger
- Callers never receive nil

### Options
- `WithSampling(initial, thereafter int)` — opt-in rate limiting of log volume
- `WithCaller()` — add file:line field (opt-in, allocates)
- `WithStacktrace(atLevel)` — auto-attach stack trace at and above level (opt-in)

## Data Flow

```
main()
  └── logger.New(os.Stderr, lvl, opts...)
        └── returns (*zap.Logger base, zap.AtomicLevel atom)

HTTP request arrives
  └── middleware
        └── base.With(request_id, flow_id, ...) → reqLogger (child)
              └── logger.WithContext(ctx, reqLogger)

Handler / Service / Metastore
  └── logger.FromContext(ctx)           → reqLogger (request-scoped)
  └── logger.FromContext(ctx, s.log)    → reqLogger if present, else s.log (background path)

SIGHUP signal
  └── atom.SetLevel(newLevel)           → immediate, no restart
```

## Field Schema (REQ-OBS-01)
| Field | Key | Always present |
|---|---|---|
| Timestamp | `time` | Yes — RFC 3339 nanosecond |
| Level | `level` | Yes — lowercase |
| Message | `msg` | Yes |
| Caller | `caller` | Only with `WithCaller()` |
| Stack trace | `stacktrace` | Only with `WithStacktrace()` at threshold |
| TAMS fields | injected by middleware | No — never in pkg/logger |
