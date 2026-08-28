# cmd/opentams migrate subcommand — NFR Requirements (M17)

## Reliability

| ID | Requirement |
|---|---|
| NFR-MIG-R1 | `dbmigrate.Close` called via defer after `New` succeeds — both src/db errors logged |
| NFR-MIG-R2 | Config load failure exits non-zero with structured error before any DB connection attempt |

## Observability

| ID | Requirement |
|---|---|
| NFR-MIG-O1 | Logger from `pkg/logger.New(os.Stderr, level)` — structured JSON output |
| NFR-MIG-O2 | DSN never logged — `buildMigrateDSN` result passed directly to `dbmigrate.New`, not stored in a logged variable |
| NFR-MIG-O3 | Version logged after successful `up`/`down`/`force` via `dbmigrate.Version` |

## Maintainability

| ID | Requirement |
|---|---|
| NFR-MIG-M1 | `cmd/opentams/cmd_test.go` tests cobra command structure (no subcommand → help, subcommands registered) |
| NFR-MIG-M2 | Unit-level migration logic tested in `pkg/dbmigrate/dbmigrate_test.go` via testcontainers |
| NFR-MIG-M3 | CLI integration tests in `cmd/opentams/migrate_integration_test.go` with `//go:build integration` — covers `up`, `version`, `down`, `force` end-to-end against a real Postgres container; run with `go test -tags=integration` |
| NFR-MIG-M4 | TC-CMD-MIGRATE-01 in `cmd_test.go` — stub "not implemented" replaced with help-output check |
