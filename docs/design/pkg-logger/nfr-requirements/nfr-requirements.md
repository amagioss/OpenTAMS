# NFR Requirements — pkg/logger

## Performance
- NFR-LOG-P01: Zero allocation on suppressed log paths (below current level) — enforced by zap's core design
- NFR-LOG-P02: Zero allocation on enabled log paths when using typed fields (`zap.String`, `zap.Int`, etc.) — enforced by zap's zero-alloc encoder
- NFR-LOG-P03: No sampling by default — every entry emitted at ~700 req/s target (REQ-OBS-10 documents ~30GB/day at INFO; acceptable)
- NFR-LOG-P04: `atom.SetLevel()` is a single atomic store — negligible cost on SIGHUP

## Reliability
- NFR-LOG-R01: Level change must not drop in-flight log entries — `zap.AtomicLevel` guarantees this via atomic load/store
- NFR-LOG-R02: `FromContext` must never panic or return nil — nop fallback is the safety net
- NFR-LOG-R03: `zapcore.AddSync(w)` wraps the writer with a mutex — concurrent writes to the same `io.Writer` are safe

## Security
- NFR-LOG-S01: `pkg/logger` itself logs no sensitive data — callers are responsible for what they log
- NFR-LOG-S02: No secrets, credentials, or PII are passed through this package
- NFR-LOG-S03: Context key is an unexported typed int — no collision with other packages

## Maintainability
- NFR-LOG-M01: Zero TAMS domain knowledge in this package — graduation path to `go-commons` unobstructed
- NFR-LOG-M02: Public API surface is minimal — 5 functions + 3 option constructors
- NFR-LOG-M03: All public functions tested; race detector clean

## Reusability
- NFR-LOG-RE01: Package has no internal imports — only stdlib + `go.uber.org/zap`
- NFR-LOG-RE02: `io.Writer` injection enables testing without filesystem or network
- NFR-LOG-RE03: Option pattern allows callers to customise without API changes
