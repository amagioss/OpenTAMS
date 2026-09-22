# OpenTAMS Implementation Plan

---

## Repository Structure

Follows [golang-standards/project-layout](https://github.com/golang-standards/project-layout) + CNCF open-source conventions (LICENSE, governance, DCO, `.github/` workflows, `hack/`).

```
OpenTAMS/
│
├── cmd/
│   └── opentams/
│       └── main.go                  # CLI entry point (M15) — thin cobra wiring only
│
├── pkg/                             # Generic, context-agnostic libraries — importable by other Amagi services
│   ├── logger/                      # slog wrapper: JSON format, level switching, SIGHUP reload
│   │                                # Zero TAMS domain knowledge; TAMS injects its own fields at call site
│   └── metrics/                     # Prometheus helper: registry, standard process/runtime collectors
│                                    # Zero TAMS metric names; each module registers its own counters
│
├── internal/                        # All private application code (not importable externally)
│   ├── apperror/                    # M2 — domain error types + code constants
│   ├── auth/                        # M6 — JWT/OIDC + DevMode providers
│   ├── config/                      # M3 — env-var config loading + validation
│   ├── gc/                          # M16 — GC sweep, daemon runner, one-shot runner
│   ├── idempotency/                 # M7 — idempotency key store (Postgres impl)
│   ├── metastore/                   # M4 — metadata store interface + Postgres impl
│   ├── objectstore/                 # M5 — object store interface + S3 impl
│   ├── server/                      # M14 — router wiring, lifecycle, graceful shutdown
│   ├── service/
│   │   ├── flow/                    # M8 — flow lifecycle orchestration
│   │   ├── segment/                 # M9 — segment insert/query/delete + idempotency
│   │   └── storage/                 # M10 — presigned URL allocation
│   ├── httpx/
│   │   ├── handlers/                # M12 — per-resource handler files
│   │   ├── health/                  # M13 — /healthz /readyz /health/details /metrics
│   │   └── middleware/              # M11 — auth, rate-limit, logging, request-id, panic
│   │                                #        injects TAMS fields (request_id, flow_id, auth_subject)
│   │                                #        into the context-agnostic pkg/logger
│   └── timerange/                   # M1 — TAI timerange parse/validate/overlap/contain
│
├── api/                             # Machine-readable API contract (source of truth)
│   ├── openapi/
│   │   └── opentams-api-v1.yaml
│   └── schemas/                     # JSON Schema files referenced by the OpenAPI spec
│       └── *.json
│
├── migrations/                      # M17 — golang-migrate numbered SQL files
│   ├── 000001_initial_schema.up.sql
│   ├── 000001_initial_schema.down.sql
│   └── ...
│
├── deployments/                     # M19/M20 — environment-specific deployment configs
│   ├── docker/
│   │   └── docker-compose.yml       # Dev: postgres + minio + migrate + serve + gc
│   └── kubernetes/
│       ├── deployment-serve.yaml
│       ├── deployment-gc.yaml
│       ├── job-migrate.yaml
│       ├── service.yaml
│       ├── servicemonitor.yaml      # Prometheus scrape target
│       ├── configmap.yaml
│       └── secret.yaml
│
├── build/                           # M18 — packaging + CI artifacts
│   ├── Dockerfile                   # Multi-stage, distroless final, multi-arch
│   └── .dockerignore
│
├── docs/                            # Human-readable documentation
│   ├── appnotes/                    # Application notes (mirrors BBC/tams convention)
│   ├── requirements.md
│   └── adr/                        # Architecture Decision Records (one .md per decision)
│       └── 001-error-format.md
│
├── hack/                            # CNCF convention: dev-only scripts not part of the build
│   ├── gen-jwks.sh                  # Generate dev JWKS for local auth testing
│   ├── seed-data.sh                 # Populate local instance with test fixtures
│   └── run-integration-tests.sh     # Wrapper: docker compose up → run tests → down
│
├── scripts/                         # Reusable CI/CD scripts (called from Makefile + GH Actions)
│   ├── build.sh
│   ├── lint.sh
│   └── check-migrations.sh
│
├── test/                            # Integration + e2e test suites (separate from unit tests)
│   ├── integration/                 # Tests that require Postgres + MinIO (docker-compose)
│   │   ├── flows_test.go
│   │   ├── segments_test.go
│   │   └── testutil/               # Shared helpers: DB setup, MinIO client, JWT factory
│   └── testdata/                    # Static fixtures: sample segments, JWT payloads
│
├── .github/
│   ├── workflows/
│   │   ├── ci.yml                   # Lint + unit tests + integration tests on PR
│   │   └── release.yml              # Build + push multi-arch image on tag
│   ├── ISSUE_TEMPLATE/
│   │   ├── bug_report.md
│   │   └── feature_request.md
│   └── PULL_REQUEST_TEMPLATE.md
│
├── .golangci.yml                    # Linter config (errcheck, staticcheck, gosec, etc.)
├── .gitignore
├── CHANGELOG.md                     # Kept by hand or generated; semver entries
├── CODE_OF_CONDUCT.md               # CNCF Contributor Covenant v2.0
├── CONTRIBUTING.md                  # Dev setup, PR process, DCO sign-off requirement
├── GOVERNANCE.md                    # Maintainers, TSC, decision process
├── LICENSE                          # Apache 2.0
├── MAINTAINERS                      # CNCF convention: list of current maintainers
├── Makefile                         # Targets: build test lint migrate docker run-dev
├── README.md
├── SECURITY.md                      # Vulnerability reporting process
├── go.mod                           # module github.com/amagioss/opentams
└── go.sum
```

### Key decisions

| Area | Choice | Rationale |
|---|---|---|
| `pkg/` for logger + metrics | Importable by other Amagi services | Both packages are context-agnostic — zero TAMS domain knowledge. TAMS injects its own fields at the call site. Graduation path to `github.com/amagioss/go-commons` when 3+ consumers exist. |
| `internal/` for all app code | No `pkg/` for domain code | Everything else is service-specific and not intended for external import. |
| `api/` at root | Not nested under `internal/` | Machine-readable contract belongs at root per golang-standards; tooling (linters, code-gen) expects it here. |
| `test/` separate from `internal/` | Unit tests live alongside source (`_test.go`); integration tests in `test/integration/` | Integration tests need docker-compose; keeping them separate allows `go test ./internal/...` to run without infra. |
| `hack/` vs `scripts/` | `hack/` = dev-only one-offs; `scripts/` = stable CI scripts | CNCF convention (Kubernetes, Prometheus, etc.) |
| `deployments/` not `deploy/` | Matches golang-standards naming | Consistent with other CNCF Go projects. |
| `docs/adr/` | One markdown file per architecture decision | Lightweight ADR process; no special tooling required. |
| `MAINTAINERS` file | CNCF projects use this alongside `GOVERNANCE.md` | Machine-readable for CNCF tooling. |
| `go.mod` module path | `github.com/amagioss/opentams` | Matches the GitHub org + repo name. |

---

## Context

Cloud-agnostic implementation of BBC TAMS v8.0 API. Specs are complete (Skill 1 done).

**Sources of truth (only):**
- `requirements.md` — requirements & 63 acceptance criteria
- `opentams-api-v1.yaml` + `schemas/*.json` — external HTTP contract (27 paths)

**Stack:** Go · PostgreSQL · S3-compatible object store · JWT/OIDC auth

**Plan file policy (per Skill 2):** Module responsibilities, public API sketches, data flows, dependencies, and test surfaces are defined upfront. Exact Go signatures, struct fields, and SQL DDL emerge during the per-module build cycle (Step 4). Pre-specifying causes cascading rework.

---

## Step 1 — Test Plan (first deliverable)

Covered in §7 below at high level. Detailed test plan with concrete request/response samples is generated as the first artifact after plan approval.

## Step 2 — Architecture Decisions (to be sparred)

Open questions to resolve before build cycle begins:
- HTTP framework: `chi` vs stdlib `net/http` (leaning chi for middleware ergonomics)
- DB driver: `pgx` vs `database/sql + lib/pq` (leaning pgx for COPY + LISTEN + performance)
- Logging: `slog` (stdlib Go 1.21+) vs `zerolog` (leaning slog)
- JWT: `go-jose` vs `golang-jwt` (leaning go-jose for JWKS caching)
- Migrations: `golang-migrate` vs `goose` (leaning golang-migrate)
- S3: `aws-sdk-go-v2` (consensus)
- CLI: `cobra` vs stdlib `flag` (leaning cobra for subcommand ergonomics)
- Validation: `go-playground/validator` vs hand-rolled per-endpoint

## Step 3 — Component Decomposition (detailed design below)

Build order is strictly bottom-up by dependency. Each module is independently testable.

---

# Module Designs

## Layer 0 — Foundation (zero internal deps)

### M1. `timerange` package

**Responsibility:** Pure logic for TAMS bracket notation timeranges (`[start_end)`, `(start_end]`, etc.). TAI nanosecond timestamps. No I/O.

**Owns:**
- Parsing/serialization of TAMS timerange strings
- Overlap detection (used by GET segments, segment insert validation)
- Containment detection (used by DELETE segments strict-containment semantics)
- Validation (start < end, bracket sanity)
- Duration arithmetic

**Does NOT own:**
- Any storage logic
- Any HTTP concerns

**Public API sketch:**
- `Parse(string) (TimeRange, error)`
- `TimeRange.String() string`
- `TimeRange.IsValid() bool`
- `Overlaps(a, b TimeRange) bool`
- `Contains(outer, inner TimeRange) bool`

**Dependencies:** stdlib only

**Test surface:**
- Exhaustive bracket combinations (`[)`, `(]`, `[]`, `()`)
- Boundary conditions (touching, overlapping, identical, nested)
- Negative timestamps (for ts_offset handling)
- Invalid inputs (malformed, reversed, missing brackets)

---

### M2. `apperror` package

**Responsibility:** Internal error representation used across all layers. Decouples domain errors from HTTP concerns.

**Owns:**
- `AppError` type with machine-readable code + human-readable detail
- Error code constants (validation, not-found, format-mismatch, immutable-field, segment-overlap, read-only, unauthenticated, forbidden, idempotency-conflict, backend-unavailable, etc.)
- Error code → RFC 9457 `type` URI mapping

**Does NOT own:**
- HTTP status codes (that's the handler's concern; handler holds the mapping table)
- Logging (caller logs)

**Public API sketch:**
- `New(code, detail string) *AppError`
- `Is(err error, code string) bool`
- Code constants enumerated

**Dependencies:** stdlib only

**Test surface:** Error wrapping, code comparison, detail preservation.

---

### M3. `config` package

**Responsibility:** Env-var-based configuration (12-factor). Loads once at startup, validates, fails fast.

**Owns:**
- Env var schema (names, types, defaults, required flags)
- Validation: type parsing, range checks, conditional requirements (e.g., `AUTH_INTERNAL_*` pair-required)
- Startup-effective configuration struct consumed by all downstream modules
- Redaction for logging (secrets masked)

**Does NOT own:**
- Runtime reconfiguration (only `LOG_LEVEL` is runtime — handled by the logger module via SIGHUP, config module doesn't know)

**Public API sketch:**
- `LoadServe() (*ServeConfig, error)` — for `serve` command
- `LoadGC() (*GCConfig, error)` — for `gc` command
- `LoadMigrate() (*MigrateConfig, error)` — for `migrate` command
- `Redacted() map[string]string` — for startup log

**Dependencies:** stdlib `os`, `time`, `strconv`

**Test surface:**
- All required vars missing → clear error listing all missing
- Invalid types (non-int port, bad duration) → clear error
- `AUTH_INTERNAL_ISSUER_URL` without `AUTH_INTERNAL_AUDIENCE` → error
- Defaults applied correctly
- Secrets redacted in `Redacted()` output

---

## Layer 1 — Store Interfaces + Implementations

### M4. `metastore` package (interface + PostgreSQL impl)

**Responsibility:** Persistent metadata store for sources, flows, segments, tags, collections, flow_storage. Enforces invariants that are database-natural (uniqueness, non-overlap, ref counts, transactional atomicity).

**Owns:**
- CRUD for Source, Flow, Segment entities
- Segment non-overlap invariant (DB-level exclusion constraint using GiST)
- Per-flow serialization on InsertSegments (advisory lock or `SELECT FOR UPDATE` on flow row)
- Flow.timerange materialization (atomic with segment insert/delete)
- Soft-delete support for DELETE flow / DELETE segments (mark segments, return orphaned object IDs to handler)
- Reference counting for objects across flows
- Cursor-based pagination (opaque cursor encoding)
- Schema migrations (separate `migrate` CLI subcommand)
- Tag/collection sub-resource queries (needed for filtering)

**Does NOT own:**
- HTTP concerns (returns AppError, not status codes)
- Object storage (deletion of actual S3 objects is the handler's job)
- Business rules that span multiple stores (e.g., "format must match source format" — lives in flow service)

**Public API sketch (interface):**
- Sources: Get, List, Put (upsert), Delete, SetTag, DeleteTag
- Flows: Get, List, Put (upsert), UpdateFields (partial), SetReadOnly, Delete (returns orphaned object IDs), SetTag, DeleteTag, SetCollection, DeleteCollection, GetTimerange
- Segments: Get (overlap semantics), Insert (batch, returns FailedSegments list), Delete (containment semantics, returns orphaned object IDs)
- FlowStorage: Get, Set

**GC-specific sub-interface** (`gcstore`): ListFlowsWithDanglingObjects, ListDanglingSegments, MarkSegmentsForDeletion, PurgeSegments. Same PostgreSQL struct satisfies both interfaces.

**Dependencies:** `pgx`, `timerange`, `apperror`

**Test surface:**
- CRUD per entity
- Non-overlap enforcement (single insert, batch insert, concurrent inserts on same flow)
- Immutability enforcement (format, source_id always; codec, essence_parameters after first segment)
- Orphan detection correctness (shared objects not returned as orphans)
- Cursor stability under concurrent inserts
- Tag filtering (`tag.X=Y`, `tag_exists.X=true`)
- Timerange materialization atomic with segment ops
- Soft-delete: marked segments excluded from reads, returned by GC queries
- Migration up/down + schema version check at startup

---

### M5. `objectstore` package (interface + S3 impl)

**Responsibility:** Presigned URL generation and batch object deletion against S3-compatible stores.

**Owns:**
- Generate presigned GET URL (time-limited download)
- Generate presigned PUT URL (time-limited upload)
- Batch delete with partial-failure reporting
- Idempotent delete semantics (missing keys = success)
- Health check (HEAD bucket)

**Does NOT own:**
- Object existence tracking (metastore holds refs)
- URL expiry policy (handler passes duration from config)

**Public API sketch:**
- `GeneratePresignedGetURL(ctx, bucket, key, expiry) (string, error)`
- `GeneratePresignedPutURL(ctx, bucket, key, expiry) (string, error)`
- `DeleteObjects(ctx, bucket, keys) (DeleteResult, error)` — DeleteResult has Deleted[] + Failed[]
- `HealthCheck(ctx) error`

**Dependencies:** `aws-sdk-go-v2`, `apperror`

**Test surface:**
- URL generation is local-only (no network call) — verified via mock
- Batch delete partial failure reporting
- Health check against MinIO in integration tests
- Non-existent key delete treated as success

---

### M6. `auth` package (interface + JWT + DevMode impls)

**Responsibility:** Authenticate requests, return verified identity. Two implementations: production (JWT/OIDC) and development (no-op).

**Owns:**
- Token validation: signature, issuer, audience, expiry, not-before
- JWKS fetching with in-memory cache + TTL + background refresh
- External issuer (always) + optional internal issuer (K8s ServiceAccount)
- Identity struct: subject, issuer, audience, scopes
- DevMode: accepts any non-empty token, returns synthetic identity

**Does NOT own:**
- Request context binding (middleware's job)
- Authorization decisions (Phase 1: all-or-nothing, Phase 2: RBAC)

**Public API sketch:**
- `Authenticate(ctx, token string) (*Identity, error)` — the sole interface method
- `NewJWTProvider(cfg) (AuthProvider, error)`
- `NewDevModeProvider() AuthProvider`

**Dependencies:** `go-jose` (or equivalent), `apperror`

**Test surface:**
- Valid external JWT → success
- Valid internal JWT (when enabled) → success
- Expired, wrong issuer, wrong audience, missing, malformed → specific errors
- JWKS cache: miss triggers fetch, hit uses cache, stale triggers refresh
- DevMode: any non-empty token accepted; empty rejected

---

### M7. `idempotency` package (interface + PostgreSQL impl)

**Responsibility:** Store and retrieve idempotency records for `POST /flows/{flowId}/segments`. Enables safe retries and enforces 409 on key reuse with different body or in-flight request.

**Owns:**
- Store: key → (request body hash, status code, response body, created_at, expires_at)
- TTL-based eviction (24h, hardcoded constant per requirement)
- Body hash computation (SHA-256 over canonicalized body)
- In-flight marker (stored record with StatusCode=0) — distinguishes "still running" from "completed"

**Does NOT own:**
- The idempotency logic flow itself (handler orchestrates: check → execute → store)

**Public API sketch:**
- `Get(ctx, key) (*Record, error)` — CodeNotFound for missing/expired
- `Set(ctx, key, record) error` — overwrites
- `Delete(ctx, key) error`
- `CanonicalHash(body []byte) string`

**Dependencies:** `pgx`, `apperror`

**Test surface:**
- Set/Get round-trip
- TTL expiry → Get returns CodeNotFound
- In-flight marker vs completed record distinction
- Body hash determinism (same body → same hash; key ordering irrelevant for JSON)

---

## Layer 2 — Domain Services

### M8. `service/flow` package

**Responsibility:** Flow lifecycle orchestration. Bridges handler and stores. Implements business rules that span stores.

**Owns:**
- PUT /flows: validation → implicit source creation (if new source_id) → upsert → immutability checks
- DELETE /flows: soft-delete via metastore → attempt object deletion → purge successful objects → leave failures for GC
- Sub-resource updates: tags, label, description, read_only, flow_collection, bitrates
- Read-only enforcement gate (blocks writes to read-only flows)
- Flow format ↔ Source format compatibility check

**Does NOT own:**
- HTTP concerns (handler maps errors)
- Segment operations (separate service)

**Public API sketch:**
- `PutFlow(ctx, flow) (*Flow, created bool, error)` — created=true for 201 vs 204
- `DeleteFlow(ctx, id) error`
- `UpdateField(ctx, id, patch) error`
- `SetTag / DeleteTag / SetReadOnly / SetCollection / DeleteCollection`
- `Get / List` (thin pass-through to metastore)

**Dependencies:** `metastore`, `objectstore`, `timerange`, `apperror`

**Test surface:**
- PUT with new source_id creates source implicitly (idempotent)
- PUT with existing source_id of different format → 400
- PUT changing immutable field on flow with segments → 400
- DELETE with shared objects → shared objects not deleted
- DELETE with object store failure → segments remain soft-deleted (GC recovers)
- All writes on read-only flow → 403 (except SetReadOnly)

---

### M9. `service/segment` package

**Responsibility:** Segment insert/query/delete orchestration with idempotency, partial-success, and orphan cleanup.

**Owns:**
- POST /segments: X-Idempotency-Key extraction → idempotency check → batch insert → result caching → 201/200/400 selection
- GET /segments: timerange overlap query, pagination, presigned GET URL generation per segment
- DELETE /segments: timerange containment delete → object cleanup attempt → orphan recovery fallback
- Partial success response construction (failed_segments array per schema)

**Does NOT own:**
- Non-overlap enforcement (metastore does it at DB level)
- URL signing logic (objectstore does it)

**Public API sketch:**
- `PostSegments(ctx, flowID, idempKey, bodyHash, segments) (statusCode int, body any, error)`
- `GetSegments(ctx, flowID, query) (*SegmentsPage, error)` — includes presigned GET URLs
- `DeleteSegments(ctx, flowID, tr, objectIDFilter) error`

**Dependencies:** `metastore`, `objectstore`, `idempotency`, `timerange`, `apperror`

**Test surface:**
- Idempotency: fresh key (201), retry same body (cached), different body (409), in-flight (409)
- Partial success: mixed valid/invalid batch → 200 + failed_segments
- All-fail batch → 400
- Overlap rejection (metastore error passthrough)
- Read-only flow → 403 on insert/delete
- DELETE: orphaned objects deleted; shared objects preserved
- DELETE: object store failure → segments soft-deleted, GC picks up

---

### M10. `service/storage` package

**Responsibility:** Allocate storage for a flow — return presigned upload URLs (and object IDs in `object_ids` mode).

**Owns:**
- POST /flows/{id}/storage: mode detection (`limit` vs `object_ids`) → UUID generation for new objects → presigned PUT URLs → response construction
- Flow storage binding (bucket + prefix per flow, stored in metastore)
- Object store health check (returns 503 when backend unavailable)

**Does NOT own:**
- Segment registration (that's segment service's job, separately)

**Public API sketch:**
- `AllocateStorage(ctx, flowID, request) (*StorageResponse, error)`

**Dependencies:** `metastore`, `objectstore`, `apperror`

**Test surface:**
- `limit` mode: N URLs + N object_ids returned
- `object_ids` mode: client-supplied IDs → PUT URLs for each; already-registered ID rejected
- Both `limit` and `object_ids` set → 400
- Neither set → 400
- Object store down → 503

---

## Layer 3 — HTTP Layer

### M11. `httpx/middleware` package

**Responsibility:** Cross-cutting request concerns in a composable middleware chain.

**Owns (per requirement):**
- **Request ID:** accept client-provided `X-Request-ID` or generate UUID; bind to context; echo on response; include in log lines
- **Auth middleware:** extract Bearer token, call AuthProvider, bind Identity to context, emit 401 on failure
- **Rate limiting:** token bucket (global; per-identity and per-source deferred per REQ-RATE-02 layers); 429 with jittered `Retry-After`
- **Structured logging:** one JSON log line per request (method, path template, status, duration, request_id, auth_subject, bytes_in, bytes_out)
- **Content negotiation:** enforce Accept; 406 on unsupported
- **Body size limits:** REQ-SEC-10 (configurable, 1000 segments max on batch)
- **Graceful shutdown:** signal-aware; in-flight drain
- **Panic recovery:** → 500 + log

**Does NOT own:**
- Business logic (delegates to services)

**Public API sketch:** Chain of `func(http.Handler) http.Handler`

**Dependencies:** `auth`, `apperror`, logger, rate limiter lib (`golang.org/x/time/rate`)

**Test surface:**
- Missing/invalid token → 401 from auth middleware
- Malformed Accept → 406
- Rate limit exceeded → 429 with Retry-After
- Request ID propagation through all middleware + handler + response header + log
- Body size limit enforcement
- Panic in handler → 500 + structured error + stack trace logged

---

### M12. `httpx/handlers` package

**Responsibility:** Translate HTTP requests to service calls; translate service responses/errors to HTTP responses per OpenAPI spec.

**Structure (one handler file per resource group):**
- `handlers_service.go` — GET /, /service, /service/storage-backends
- `handlers_sources.go` — /sources, /sources/{id}, sub-resources (tags, label, description)
- `handlers_flows.go` — /flows, /flows/{id}, sub-resources (tags, label, description, read_only, flow_collection, max_bit_rate, avg_bit_rate)
- `handlers_segments.go` — /flows/{id}/segments (GET, POST, DELETE, HEAD)
- `handlers_storage.go` — /flows/{id}/storage (POST)
- `handlers_health.go` — /healthz, /readyz, /health/details, /metrics

**Owns:**
- Request parsing (path vars, query params, headers, body)
- JSON schema validation at the boundary
- Error mapping: AppError code → HTTP status + RFC 9457 body (`application/problem+json`)
- Pagination header construction (`Link`, `X-Paging-Limit`, `X-Paging-NextKey`)
- HEAD support for every GET endpoint

**Does NOT own:**
- Business logic (services own it)
- Middleware concerns

**Public API sketch:** `chi.Router` registration function per group

**Dependencies:** All services, `apperror`

**Test surface:**
- Every endpoint: happy path + every documented error code (from OpenAPI)
- HEAD returns same headers as GET, no body
- RFC 9457 body on every non-2xx: type, title, status, detail, instance, request_id
- Pagination header correctness
- 404 for unknown paths (router-level)
- 406 for unsupported Accept
- Unknown fields in body accepted and ignored (forward compat)

---

### M13. `httpx/health` package

**Responsibility:** Liveness, readiness, detailed health, metrics endpoints.

**Owns:**
- `/healthz`: 200 if process alive — no dependency checks
- `/readyz`: 200 if metadata store reachable, 503 otherwise
- `/health/details`: per-dependency breakdown (metadata store, object store)
- `/metrics`: Prometheus text exposition — no auth
- Background health checker: periodic dependency pings, results cached
- Blip tracking: 3 consecutive failures → flagged; 2 successes → resolved

**Does NOT own:**
- What metrics to emit (each module registers its own)

**Public API sketch:**
- `NewHealthChecker(deps) *HealthChecker` (runs background loop)
- Handler functions for each endpoint

**Dependencies:** `metastore`, `objectstore`, `prometheus/client_golang`

**Test surface:**
- `/healthz` 200 always when process alive
- `/readyz` reflects actual DB state
- Blip counter: N failures → flag; 2 successes → resolved
- Metrics endpoint returns valid Prometheus format, no auth required

---

### M14. `server` package

**Responsibility:** Wire everything together. Startup, routing, lifecycle.

**Owns:**
- Router construction (chi, routes registered per handler group)
- Middleware chain composition
- HTTP server with configured timeouts (read, write, idle, shutdown)
- Startup validation: config → DB ping → object store ping → schema version check → ready
- Graceful shutdown on SIGTERM: stop accepting → drain → exit
- SIGHUP handling for `LOG_LEVEL` runtime change

**Does NOT own:**
- Any business logic

**Dependencies:** All above modules

**Test surface:**
- Startup fails fast on missing config (exit 2)
- Startup fails on DB unreachable (exit 1)
- Graceful shutdown: in-flight request completes before exit
- SIGHUP changes log level without restart

---

## Layer 4 — CLI & GC

### M15. `cmd/opentams` package

**Responsibility:** CLI entry point. Three subcommands.

**Owns:**
- `opentams serve` → loads ServeConfig → starts server
- `opentams gc [--once]` → loads GCConfig → runs daemon or one-shot sweep
- `opentams migrate [--dry-run]` → loads MigrateConfig → applies migrations
- `opentams --version` / `--help`
- Exit code policy: 0 clean / 1 runtime failure / 2 config error

**Does NOT own:**
- Any logic — thin wrappers

**Dependencies:** `config`, `server`, `gc`, migrations lib

---

### M16. `gc` package

**Responsibility:** Garbage collection sweep logic (reclaim orphaned objects, purge soft-deleted segments, expired idempotency records).

**Owns:**
- `Sweep(ctx, gcstore, objectstore) (SweepResult, error)` — single sweep unit
- Daemon runner: sleep-after-sweep loop (prevents overlap naturally)
- One-shot runner: single sweep → exit
- Per-item failure handling: log + count, don't fail sweep
- Expired idempotency record eviction (periodic)

**Does NOT own:**
- Scheduling policy (Kubernetes CronJob, Docker daemon — deployment concern)
- HTTP concerns

**Public API sketch:**
- `Sweep(ctx, deps) (SweepResult, error)`
- `RunDaemon(ctx, interval, sweepFn)`
- `RunOnce(ctx, sweepFn) error`

**Dependencies:** `metastore` (GC sub-interface), `objectstore`, `idempotency`, `apperror`

**Test surface:**
- Sweep deletes dangling objects, purges segments
- Partial failure: some objects fail delete → remain marked, logged, counted
- Sweep cannot start (DB down) → returns error → exit 1
- Daemon sleep-after-sweep prevents overlap
- One-shot exits 0 even with ErrorCount > 0

---

### M17. `migrations` directory

**Responsibility:** SQL schema versioning.

**Owns:**
- Numbered up/down migration files per `golang-migrate` convention
- Initial schema: sources, flows, segments, tags, flow_collection, flow_storage, idempotency_keys
- Indices: segment non-overlap GiST, object_id lookup, tag lookup, cursor pagination
- Schema version tracking (migration tool provides it)

**Does NOT own:**
- Business logic
- Data migration (Phase 1 is greenfield)

**Test surface:**
- Each migration has corresponding down; up+down+up idempotent
- Startup checks schema version, exits if incompatible

---

## Layer 5 — Packaging & Ops

### M18. `Dockerfile`

- Multi-stage: builder (full Go toolchain) → final (distroless or alpine)
- Non-root user
- No shell in final image
- Multi-arch (amd64, arm64)
- Single binary with all three subcommands

### M19. `docker-compose.yml` (dev)

- `postgres:15`
- `minio/minio` (S3-compatible)
- `opentams-migrate` (init job)
- `opentams-serve`
- `opentams-gc` (daemon mode)
- Single `docker compose up` per REQ-DEV-02/03

### M20. Kubernetes manifests

- `Deployment` for serve (multi-replica, stateless)
- `Deployment` for gc (single replica)
- `Job` / `initContainer` for migrate
- `Service` + `ServiceMonitor` (Prometheus scrape)
- `ConfigMap` + `Secret` for config
- Optional `HPA` for serve replicas

---

# Cross-Cutting Concerns

## Observability
- **Logs:** slog JSON handler, dimensional schema (timestamp, level, domain, action, outcome, request_id, flow_id, auth_subject, duration_ms)
- **Metrics:** Prometheus via `client_golang`. HTTP path label uses route template (never actual path) to bound cardinality. Full metric list per REQ-OBS-05.
- **Request correlation:** `request_id` in context → logs, error bodies, response header

## Error flow
Handler receives AppError from services → maps code to HTTP status via internal table → constructs RFC 9457 `application/problem+json` body with request_id → returns response.

## Concurrency
- Per-flow serialization on segment inserts via DB-level row lock or advisory lock
- Connection pool limits enforced; exhaustion returns 503 fast (no queue)
- Context timeout propagation on all dependency calls

## Security
- JWT validated at middleware boundary (external always, internal optional)
- Media upload/download bypasses API auth (time-limited URLs)
- Secrets via env vars or mounted files
- Body size limits at middleware boundary
- TLS optional (terminated at ingress in K8s; available via config for standalone)

---

# Dependency Graph (build order)

```
M1 timerange ──┐
M2 apperror  ──┼──> M4 metastore ────┐
M3 config    ──┘                     │
                M5 objectstore ──────┤
                M6 auth ─────────────┤
                M7 idempotency ──────┤
                                     ├──> M8 flow service ─────┐
                                     ├──> M9 segment service ──┤
                                     ├──> M10 storage service ─┤
                                                               ├──> M11 middleware ─┐
                                                               ├──> M12 handlers ───┤
                                                               ├──> M13 health ─────┤
                                                                                    ├──> M14 server ──> M15 CLI
                                                               M16 gc ──────────────┤
                                                               M17 migrations ──────┘
                                                                                    │
                                                                                    └──> M18–M20 packaging
```

---

# Per-Module Build Cycle (Skill 2 Step 4)

For each module in the dependency graph:
1. Present context + relevant subset of test plan
2. Write test harness → user review → HITL gate
3. Finalize interface (may differ from sketch — that's expected, log decision)
4. Write implementation
5. Run tests to green
6. English code review → HITL gate
7. Refactor if requested, re-run tests
8. Log decisions inline, mark done, move on

---

# Integration (Skill 2 Step 5)

After all modules built:
1. Full e2e workflow tests (write/read/delete lifecycles)
2. Every acceptance criterion (1–63) mapped to a passing test or documented deferral
3. Performance benchmarks against REQ-PERF targets
4. Spec conformance: every OpenAPI path + every documented error code exercised
5. Container build + deployment smoke test

---

# Verification Plan

| What | How |
|---|---|
| Unit tests | `go test ./... -race` |
| Integration tests | docker-compose-based suite hitting real Postgres + MinIO |
| OpenAPI conformance | Schemathesis or OpenAPI-validated request/response per endpoint |
| Performance | Load script (k6 or similar) against documented targets |
| Container | Build image, run, hit /healthz and /readyz |
| Migrations | Up + down + up idempotency test per migration |

---

# Immediate Next Steps After Plan Approval

1. **Step 1 deliverable:** Generate detailed test plan with concrete request/response samples for all 27 endpoints + 63 acceptance criteria
2. Step 2: Architecture sparring on the open library choices
3. Step 3: Confirm component decomposition (already drafted above)
4. Step 4: Begin per-module build cycle at M1 (timerange)
