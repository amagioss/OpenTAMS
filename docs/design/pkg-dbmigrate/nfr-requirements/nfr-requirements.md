# pkg/dbmigrate — NFR Requirements

## Reliability

| ID | Requirement |
|---|---|
| NFR-DBM-R1 | Both errors from `m.Close()` checked separately — srcErr and dbErr are independent failure modes |
| NFR-DBM-R2 | `New` wires `migrateLogAdapter` to `m.Log` — all migrate-internal steps visible in structured logs |

## Observability

| ID | Requirement |
|---|---|
| NFR-DBM-O1 | Logger passed in from caller (`*zap.Logger`) — no logger constructed inside package |
| NFR-DBM-O2 | DSN never logged — password exposure risk |
| NFR-DBM-O3 | `ErrNoChange` logged at Info ("already at head, no migrations applied") not Error |
| NFR-DBM-O4 | `Close` logs srcErr / dbErr each with field `zap.Error` at Error level |

## Maintainability

| ID | Requirement |
|---|---|
| NFR-DBM-M1 | Integration tests use testcontainers — real Postgres, one container per `TestMain`, fresh schema per test via `CREATE DATABASE` |
| NFR-DBM-M2 | Production code has no dependency on `internal/*` — only stdlib + `golang-migrate/v4` + `go.uber.org/zap`. Test code (`_test` package) may import `internal/metastore` for `ExpectedSchemaVersion`. |
| NFR-DBM-M3 | `_ source/file` driver registration documented as a package-level side effect in the package doc comment |
| NFR-DBM-M4 | `verbose=false` default: package is a library — callers opt in to per-migration log lines explicitly |
