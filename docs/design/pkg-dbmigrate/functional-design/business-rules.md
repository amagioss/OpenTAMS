# pkg/dbmigrate — Functional Design

## Purpose

Reusable golang-migrate runner with zap logging. Zero TAMS domain knowledge.
Graduation path: `github.com/amagioss/go-commons` when 3+ Amagi services adopt.

## Functional Requirements

| ID | Requirement |
|---|---|
| FR-DBM-01 | `New(fs embed.FS, dsn string, log *zap.Logger, verbose bool) (*migrate.Migrate, error)` — creates migrate instance using `source/iofs` driver from caller-supplied embed.FS |
| FR-DBM-02 | `NewFromPath(path, dsn string, log *zap.Logger, verbose bool) (*migrate.Migrate, error)` — creates migrate instance using `source/file` driver from filesystem path; useful for development |
| FR-DBM-03 | `Up(m *migrate.Migrate, steps int) error` — `steps=0` applies all pending via `m.Up()`; `steps>0` applies exactly N via `m.Steps(steps)` |
| FR-DBM-04 | `Down(m *migrate.Migrate, steps int) error` — `steps` must be positive; calls `m.Steps(-steps)` |
| FR-DBM-05 | `Version(m *migrate.Migrate) (uint, bool, error)` — returns (version, dirty, error) |
| FR-DBM-06 | `Force(m *migrate.Migrate, v int) error` — sets schema_migrations to version v, clears dirty flag |
| FR-DBM-07 | `Close(m *migrate.Migrate, log *zap.Logger)` — calls `m.Close()`, logs srcErr and dbErr separately at Error level if non-nil |
| FR-DBM-08 | `migrate.ErrNoChange` treated as success in `Up` and `Down` — not returned as error |
| FR-DBM-09 | `logAdapter` wires `*zap.Logger` to `migrate.Logger` interface — `Printf` → `log.Sugar().Infof`; `Verbose()` returns the `verbose` field passed at construction |

## Package Surface

```go
package dbmigrate

func New(fs embed.FS, dsn string, log *zap.Logger, verbose bool) (*migrate.Migrate, error)
func NewFromPath(path, dsn string, log *zap.Logger, verbose bool) (*migrate.Migrate, error)
func Up(m *migrate.Migrate, steps int) error
func Down(m *migrate.Migrate, steps int) error
func Version(m *migrate.Migrate) (uint, bool, error)
func Force(m *migrate.Migrate, v int) error
func Close(m *migrate.Migrate, log *zap.Logger)
```

## Decision Log

| Decision | Rationale |
|---|---|
| `pkg/dbmigrate` not inline in `cmd/` | Zero TAMS domain — reusable pattern. Consistent with `pkg/logger`, `pkg/metrics`. |
| `BuildDSN` removed from package | DSN construction requires `*config.Config` — lives in `cmd/opentams/migrate.go` as `buildMigrateDSN`. Package stays domain-free. |
| embed.FS passed in (not hardcoded) | Caller supplies their SQL files — package stays domain-free |
| `source/iofs` not `source/file` | Self-contained binary; no disk path dependency at runtime |
| `NewFromPath` added | Development use case: run migrations from filesystem without rebuilding binary |
| `_ source/file` registered as side effect | `NewFromPath` requires the file driver; side effect documented in package doc comment |
| `verbose bool` in constructors, default false | Open-source package principle: default quiet. CLI callers (`up`, `down`) pass `true` to show per-migration progress; `version`, `force`, and tests pass `false`. |
| `steps=0` means all in `Up` | Unifies "up all" and "up N" without magic sentinel |
| `ErrNoChange` = success | Not an error condition; first-time vs repeated runs should behave identically |
| Wrappers for Up/Down/Version/Force are thin | Library is well-tested; wrappers add only ErrNoChange handling + logging |
