# Business Rules — pkg/logger

## Level Rules
- BR-LOG-01: Messages below the current level are silently discarded — zero allocation on suppressed path
- BR-LOG-02: Level change via `atom.SetLevel()` takes effect immediately for all subsequent log calls
- BR-LOG-03: `ParseLevel` accepts exactly: `debug`, `info`, `warn`, `error` (case-insensitive). All other inputs are errors.
- BR-LOG-04: Default level is the caller's choice — `pkg/logger` imposes no default

## Sampling Rules
- BR-LOG-05: Sampling is OFF by default — every entry must be emitted (REQ-OBS-01 audit requirement)
- BR-LOG-06: Sampling is opt-in only via `WithSampling(initial, thereafter)`

## Output Rules
- BR-LOG-07: Output is always JSON — no text/console format
- BR-LOG-08: Timestamp field key is `"time"`, value is RFC 3339 with nanosecond precision
- BR-LOG-09: Level field key is `"level"`, value is lowercase (`"info"` not `"INFO"`)
- BR-LOG-10: Message field key is `"msg"`
- BR-LOG-11: No TAMS-specific fields are added by this package

## Context Rules
- BR-LOG-12: `FromContext` never returns nil — falls back through caller-supplied fallbacks then nop logger
- BR-LOG-13: `FromContext` prefers the context-stored logger over any fallback
- BR-LOG-14: `WithContext` creates a new context — the original context is not mutated

## Caller / Stacktrace Rules
- BR-LOG-15: Caller info is opt-in (`WithCaller()`) — default off to avoid allocation on hot path
- BR-LOG-16: Stacktrace is opt-in (`WithStacktrace(level)`) — threshold is inclusive (at and above)

## Concurrency Rules
- BR-LOG-17: All exported functions and the returned `*zap.Logger` are safe for concurrent use
- BR-LOG-18: `atom.SetLevel()` is atomic — concurrent writes see consistent level
