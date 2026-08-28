# cmd/opentams migrate subcommand — Functional Design (M17)

## Purpose

Implements `opentams migrate` cobra subcommand using `pkg/dbmigrate`.
Migrations embedded in binary via `migrations/embed.go`.

## Functional Requirements

| ID | Requirement |
|---|---|
| FR-MIG-01 | `opentams migrate up [N]` — apply all pending (N omitted) or exactly N migrations |
| FR-MIG-02 | `opentams migrate down N` — rollback N migrations (N required, must be positive) |
| FR-MIG-03 | `opentams migrate version` — print current version and dirty flag to stdout |
| FR-MIG-04 | `opentams migrate force N` — force schema_migrations to version N, clear dirty flag |
| FR-MIG-05 | Migrations embedded in binary via `migrations.FS` (`//go:embed *.sql`) — no external path |
| FR-MIG-06 | DB config loaded via `config.Load()` — same env vars as `serve` (DB_HOST, DB_PORT, DB_NAME, DB_USER, DB_PASSWORD, DB_SSLMODE) |
| FR-MIG-07 | `opentams migrate` with no subcommand → shows help, exits 0 |
| FR-MIG-08 | Exit non-zero on failure; structured error logged via zap before exit |
| FR-MIG-09 | Logger created via `pkg/logger.New(os.Stderr, level)` — consistent with `serve` |
| FR-MIG-10 | `verbose=true` passed to `dbmigrate.New` for `up` and `down` — per-migration log lines visible to operators; `verbose=false` for `version` and `force` — no per-migration noise |
| FR-MIG-11 | `buildMigrateDSN(cfg *config.Config) string` constructs `pgx5://` DSN with URL-encoded credentials via `url.UserPassword` — lives in `cmd/`, not `pkg/dbmigrate` |

## Subcommand Structure

```
opentams migrate              → help
opentams migrate up           → apply all pending
opentams migrate up 2         → apply 2 steps
opentams migrate down 1       → rollback 1 step
opentams migrate version      → print version
opentams migrate force 3      → force version 3
```

## Decision Log

| Decision | Rationale |
|---|---|
| `pkg/dbmigrate` not inline | Zero domain knowledge → reusable; consistent with `pkg/` pattern |
| embed not `--path` flag | Self-contained binary; standard practice for containerised services |
| Cobra subcommands not positional args | Idiomatic cobra; each action gets its own `--help` |
| `down N` requires N | Accidental full rollback protection |
| No `migrate` without subcommand error | Shows help; less surprising than error for top-level command |
| TC-CMD-MIGRATE-01 updated | Old stub test replaced — no longer expects "not implemented" error |
