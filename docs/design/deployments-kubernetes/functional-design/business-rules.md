# M20 deployments/kubernetes/ — Functional Design

## Functional Requirements

| ID | Requirement |
|----|-------------|
| FR-K8S-01 | `Deployment` for `opentams serve` — 2 replicas, image from registry |
| FR-K8S-02 | `Job` for `opentams migrate up` — applied before Deployment; `kubectl wait` gates progression |
| FR-K8S-03 | `Service` (ClusterIP) exposes port 80 → 8080 |
| FR-K8S-04 | `Secret` template for credentials — `REPLACE_ME` placeholders, populated via external secret manager |
| FR-K8S-05 | `ConfigMap` for non-secret env vars injected via `envFrom: configMapRef` |
| FR-K8S-06 | Readiness probe: `GET /readyz` — gates traffic until schema + deps healthy |
| FR-K8S-07 | Liveness probe: `GET /healthz` — restarts hung pods |
| FR-K8S-08 | Resource requests/limits on all containers |
| FR-K8S-09 | No Postgres or MinIO manifests — production uses managed services (RDS, S3) |

## Manifest Files

| File | Kind | Namespace |
|------|------|-----------|
| `namespace.yaml` | Namespace | — |
| `configmap.yaml` | ConfigMap | opentams |
| `secret.yaml` | Secret | opentams |
| `migrate-job.yaml` | Job | opentams |
| `deployment.yaml` | Deployment | opentams |
| `service.yaml` | Service | opentams |
| `README.md` | — | — |

## Apply Order

```
namespace → configmap + secret → migrate-job → (wait complete) → deployment + service
```

## ConfigMap vs Secret Split

| ConfigMap | Secret |
|-----------|--------|
| `APP_ENV` | `DB_HOST` |
| `SERVER_PORT` | `DB_NAME` |
| `DB_PORT` | `DB_USER` |
| `DB_SSLMODE` | `DB_PASSWORD` |
| `STORAGE_BACKEND_PROVIDER` | `OBJECT_STORE_*` |
| — | `AUTH_EXTERNAL_*` |

Both injected via `envFrom` — app sees plain `os.Getenv()`, no file mounts.

## Design Decisions

| Decision | Choice | Rejected | Rationale |
|----------|--------|----------|-----------|
| Templating | Plain YAML | Helm | Minimal scope; Helm adds complexity without benefit at current scale |
| Migration | Job + `kubectl wait` | Init container in Deployment | Job is independently retryable and auditable; init container couples migration to pod lifecycle |
| Managed infra | No Postgres/MinIO manifests | In-cluster deployments | Production uses RDS + S3; in-cluster databases need StatefulSets, PVCs, backup ops — out of scope |
| Replicas | 2 | 1 | Zero-downtime rolling updates require ≥2; `maxUnavailable: 0` guarantees no traffic disruption |
