# M19 deployments/docker/docker-compose.yml — Functional Design

## Functional Requirements

| ID | Requirement |
|----|-------------|
| FR-DC-01 | `opentams` service uses `build/Dockerfile` via build context |
| FR-DC-02 | `postgres:16-alpine` with named volume for data persistence |
| FR-DC-03 | `minio/minio` (pinned release tag) as S3-compatible object store — `latest` rejected; pinned for reproducible local dev |
| FR-DC-04 | `migrate` service runs `opentams migrate up` as one-shot init (`restart: no`) |
| FR-DC-05 | `opentams` depends on `migrate: service_completed_successfully` + `minio-init: service_completed_successfully` |
| FR-DC-06 | All secrets via `.env` file — no hardcoded values in compose YAML |
| FR-DC-07 | Postgres healthcheck: `pg_isready -U $DB_USER -d $DB_NAME` |
| FR-DC-08 | MinIO healthcheck: `curl -f http://localhost:9000/minio/health/live` |
| FR-DC-09 | `minio-init` service creates bucket via `mc mb --ignore-existing` |
| FR-DC-10 | Ports exposed: `8080` (API), `9000` (MinIO S3 API), `9001` (MinIO console) |

## Service Dependency Graph

```
postgres (healthcheck) ──► migrate (restart:no) ──► opentams (serve)
minio (healthcheck) ──► minio-init (restart:no) ──────────────────►┘
```

## Design Decisions

| Decision | Choice | Rejected | Rationale |
|----------|--------|----------|-----------|
| Migration ordering | `service_completed_successfully` condition | Init container sidecar | Compose v2 native; cleaner than sidecar pattern |
| `APP_ENV` | `development` | `production` | Auth vars not required; `DB_SSLMODE=disable` (no TLS between containers) |
| MinIO credentials | Reuse `OBJECT_STORE_ACCESS_KEY_ID/SECRET_ACCESS_KEY` as MinIO root | Separate vars | DRY; MinIO root creds ARE the S3 access key in local dev |
| `OBJECT_STORE_ENDPOINT` | `http://minio:9000` | Default (AWS) | Points app at local MinIO container, not AWS |
| Bucket creation | `minio-init` with `mc` | Auto-create in app | Separation of concerns; app should not create infrastructure |

## Environment Variables

Non-secret vars set directly in compose `environment:`. Secret vars sourced from `.env`:

| Variable | Source | Notes |
|----------|--------|-------|
| `APP_ENV` | compose literal | `development` |
| `DB_HOST` | compose literal | `postgres` (service name) |
| `OBJECT_STORE_ENDPOINT` | compose literal | `http://minio:9000` |
| `DB_NAME/USER/PASSWORD` | `.env` | — |
| `OBJECT_STORE_*` | `.env` | — |
| `STORAGE_BACKEND_PROVIDER` | `.env` | `s3` |
