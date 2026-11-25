---
unit: M16 internal/gc
stage: NFR Requirements
status: Draft (not implemented)
---

# Tech Stack Decisions — object garbage collection

The GC change adds no new module dependency. Each item uses a library that the repository already uses.

| Need | Library | Where it is used today |
|---|---|---|
| PostgreSQL access, transactions, `FOR UPDATE SKIP LOCKED` | `github.com/jackc/pgx/v5` (ADR-0013) | `internal/metastore` |
| Object-store delete | `github.com/aws/aws-sdk-go-v2/service/s3`, through `objectstore.S3Store.DeleteObjects` | `internal/objectstore` |
| Metrics | `github.com/prometheus/client_golang`, through `pkg/metrics` | `internal/service/metrics.go`, `internal/server` |
| Admin HTTP endpoints | `github.com/gin-gonic/gin`, through `internal/httpx/health` and `internal/httpx/metrics` | `internal/server` |
| Command | `github.com/spf13/cobra` | `cmd/opentams` |
| Logs | `go.uber.org/zap`, through `pkg/logger` | All packages |
| Migrations | `github.com/golang-migrate/migrate/v4` v4.19.1 | `migrations`, `pkg/dbmigrate` |
| Integration tests | `testcontainers-go` (PostgreSQL, MinIO) | `internal/dbtest`, `internal/objectstore` |

## Decision: no job-queue library

*Decision: the GC uses SQL with `FOR UPDATE SKIP LOCKED` on its own tables. Rationale: the work items are rows that already exist (`objects`, `allocations`). A queue library copies them into a second store and adds a dependency. Rejected: a job-queue library on PostgreSQL. Rejected: a message broker.*

## Decision: no leader election

*Decision: all replicas do the same work. `SKIP LOCKED` divides the claims. Rationale: a duplicate retry is harmless (BR-GC-65). A leader adds a lease, a failover time, and more tests. Rejected: a Kubernetes lease for one active replica.*

## Decision: the GC uses the S3 multi-object delete

*Decision: one `DeleteObjects` call for each batch of up to 1000 keys. Rationale: AWS S3 and the Google Cloud Storage XML API both accept up to 1000 keys in one call. MinIO also accepts it. Rejected: one `DeleteObject` call for each key (up to 1000 times more calls).*
