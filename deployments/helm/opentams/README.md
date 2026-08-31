# OpenTAMS Helm Chart

Production-grade Helm chart for the **OpenTAMS API server** — a cloud-agnostic
BBC TAMS v8.0 implementation.

The chart deploys the **server only**. Postgres and the S3-compatible object
store are **external** (bring your own managed services). For local dev/demo use
`deployments/docker/docker-compose.yml`.

## TL;DR

```bash
# 1. Provision the Secret out-of-band (see "Secrets" below) — the chart never creates one.
kubectl create secret generic opentams-secrets \
  --from-literal=DB_PASSWORD='...' \
  --from-literal=OBJECT_STORE_ACCESS_KEY_ID='...' \
  --from-literal=OBJECT_STORE_SECRET_ACCESS_KEY='...'

# 2. Install.
helm install opentams deployments/helm/opentams \
  --set secrets.existingSecret=opentams-secrets \
  --set database.host=postgres.example.com \
  --set objectStore.bucket=my-tams-bucket \
  --set objectStore.region=us-east-1 \
  --set auth.external.issuerURL=https://issuer.example.com/ \
  --set auth.external.audience=opentams
```

## Requirements

- Kubernetes >= 1.25
- An external Postgres database and an S3-compatible object store
- A pre-provisioned Kubernetes Secret (see [Secrets](#secrets))

## Config model

Most operator-facing config is expressed as **structured values** (e.g.
`database.host`, `app.env`) that the chart renders inline into each pod's `env:`
via the `opentams.env` helper. There is **no ConfigMap**: env is inline, so any
value change mutates the pod template and triggers a rollout natively.

Defaults live in `values.yaml`. Where a value is left empty and the binary has
its own default (`internal/config/config.go`), the chart omits the env var so
the binary default applies.

### Advanced tuning via `extraEnv`

The chart deliberately does **not** surface every env var as a first-class
value, so a future change to a binary default isn't silently pinned by the
chart. Override these through `extraEnv` if needed:

| Env var | `config.go` default |
|---------|--------------------|
| `SERVER_GRACEFUL_SHUTDOWN_PERIOD` | 30s |
| `AUTH_JWKS_TTL` | 15m |
| `IDEMPOTENCY_KEY_TTL` | 1h |
| `IDEMPOTENCY_STALE_THRESHOLD` | 60s |
| `IDEMPOTENCY_REAPER_INTERVAL` | 1m |

```yaml
extraEnv:
  - name: AUTH_JWKS_TTL
    value: "5m"
```

`GC_*` is not listed — the GC worker is not implemented and is not deployed.
`SERVER_RATE_LIMIT_RPS` / `SERVER_RATE_LIMIT_BURST` are read by the binary but
ignored — OpenTAMS has no rate limiter. Put one in front of the service
(ingress controller or API gateway) if you need one.

## Secrets

**The chart never creates a Secret.** Provision it via IaC / External Secrets
Operator / Vault / SealedSecrets **before** `helm install` (it is read by the
pre-install migrate hook), and pass its name as `secrets.existingSecret`.

Required keys (verbatim names):

| Key | Required | Notes |
|-----|----------|-------|
| `DB_PASSWORD` | **yes** | pod fails to start if missing |
| `OBJECT_STORE_ACCESS_KEY_ID` | optional | omit under Workload Identity |
| `OBJECT_STORE_SECRET_ACCESS_KEY` | optional | omit under Workload Identity |

The object-store keys are referenced with `optional: true` — when absent, the
pod still starts and the AWS SDK falls back to its ambient credential chain
(IRSA / GKE Workload Identity / AKS).

**Secret rotation** is not chart-managed; after rotating the Secret run:

```bash
kubectl rollout restart deployment/<release>-opentams
```

## Workload Identity

Default posture is **ambient credentials** (no object-store keys in the Secret).
Annotate the ServiceAccount and leave the keys out:

```yaml
serviceAccount:
  annotations:
    eks.amazonaws.com/role-arn: arn:aws:iam::123456789012:role/opentams
    # or: iam.gke.io/gcp-service-account: opentams@project.iam.gserviceaccount.com
```

## Auth

Two independent JWT issuers (REQ-ARCH-12):

- **External** (`auth.external.*`) — **required when `app.env != development`**.
  Primary human/API client surface.
- **Internal** (`auth.internal.*`) — optional, **disabled by default**,
  both-or-neither. Service-to-service callers. When enabled, an explicit
  `auth.internal.allowedSubjects` allowlist is **required** (fails closed).

### Internal auth + projected ServiceAccount tokens

A client projects a ServiceAccount token with `audience == auth.internal.audience`,
reads it from `/var/run/secrets/tokens/...`, and sends it as
`Authorization: Bearer ...`. OpenTAMS validates it against
`auth.internal.issuerURL`. See `examples/internal-client.yaml`.

Validation alone is not enough: the token's `sub`
(`system:serviceaccount:<namespace>:<name>`) must also appear verbatim in
`auth.internal.allowedSubjects`. Any internal-issuer token whose subject is not
on the allowlist is rejected with **403 Forbidden**. The allowlist is mandatory
when internal auth is enabled — there is no "allow all internal" mode.

> **Limitation — in-cluster issuer unsupported.** The JWKS validator uses the
> default HTTP client (no cluster CA, no bearer token —
> `pkg/jwtauth/jwtauth.go`). The in-cluster issuer
> `https://kubernetes.default.svc` therefore **does not work**. Set
> `auth.internal.issuerURL` to the cluster's **published** OIDC issuer
> (EKS/GKE/AKS managed issuer, or any self-hosted issuer with a system-trusted
> TLS cert and anonymous JWKS). Making the in-cluster issuer work is a code
> change, out of chart scope.

## TLS

TLS is terminated at the Ingress by default (`ingress.tls`, REQ-SEC-07). For
ingress-less topologies, enable server-level TLS:

```yaml
server:
  tls:
    enabled: true
    existingSecret: opentams-tls   # kubernetes.io/tls secret
```

## Migrations

A forward-only `pre-install,pre-upgrade` hook Job runs `/opentams migrate up`
before the Deployment's pods start. A failed migration blocks the rollout (the
server refuses to boot on a stale schema — REQ-DEV-05/05a).

DB rollback is **manual**:

```bash
kubectl run opentams-migrate-down --rm -it --image=<image> -- migrate down 1
```

> **`helm template | kubectl apply` does NOT honor hooks.** The hook annotations
> become inert, so the migrate Job runs concurrently with the Deployment and
> pods may crash-loop on the schema gate until it finishes. Render-and-apply
> users must run `migrate up` as a separate step first. Hook-aware GitOps
> (Argo CD sync-phases, Flux Helm controller) and plain `helm install/upgrade`
> honor it.

## Values

| Key | Default | Description |
|-----|---------|-------------|
| `image.repository` | `ghcr.io/amagioss/opentams` | Image repo |
| `image.tag` | `""` | Defaults to `.Chart.AppVersion` |
| `image.pullPolicy` | `IfNotPresent` | |
| `replicaCount` | `2` | Ignored if `autoscaling.enabled` |
| `serviceAccount.create` | `true` | |
| `serviceAccount.annotations` | `{}` | Workload Identity annotations |
| `serviceAccount.automountServiceAccountToken` | `false` | Server doesn't call kube API |
| `app.env` | `production` | `APP_ENV` (`production`/`development`) |
| `app.logLevel` | `info` | `LOG_LEVEL` |
| `server.port` | `8080` | `SERVER_PORT` + containerPort |
| `server.tls.enabled` | `false` | Optional server-level TLS |
| `server.tls.existingSecret` | `""` | `kubernetes.io/tls` secret |
| `database.host` | `""` | **Required** — `DB_HOST` |
| `database.port` | `5432` | `DB_PORT` |
| `database.name` | `opentams` | `DB_NAME` |
| `database.user` | `opentams` | `DB_USER` |
| `database.sslMode` | `require` | `DB_SSLMODE` |
| `database.pool.min` / `.max` | `2` / `10` | `DB_POOL_MIN`/`MAX` |
| `objectStore.bucket` | `""` | **Required** — `OBJECT_STORE_BUCKET` |
| `objectStore.region` | `""` | **Required** — `OBJECT_STORE_REGION` |
| `objectStore.endpoint` | `""` | `OBJECT_STORE_ENDPOINT` (empty = AWS S3) |
| `objectStore.presignExpiry` | `1h` | `OBJECT_STORE_PRESIGN_EXPIRY` |
| `objectStore.backend.provider` | `s3` | `STORAGE_BACKEND_PROVIDER` |
| `objectStore.backend.product` | `s3` | `STORAGE_BACKEND_PRODUCT` |
| `auth.external.issuerURL` | `""` | Required in prod |
| `auth.external.audience` | `""` | Required in prod |
| `auth.internal.enabled` | `false` | |
| `auth.internal.issuerURL` | `""` | Must be a published issuer |
| `auth.internal.audience` | `""` | |
| `auth.internal.allowedSubjects` | `[]` | Required when internal enabled; allowed `sub` claims |
| `secrets.existingSecret` | `""` | **Required** — chart never creates a Secret |
| `migration.enabled` | `true` | pre-install/upgrade hook Job |
| `migration.backoffLimit` | `3` | |
| `migration.activeDeadlineSeconds` | `300` | |
| `service.type` | `ClusterIP` | |
| `service.port` | `80` | |
| `ingress.enabled` | `false` | |
| `autoscaling.enabled` | `false` | |
| `autoscaling.minReplicas` / `.maxReplicas` | `2` / `10` | |
| `autoscaling.targetCPUUtilizationPercentage` | `70` | |
| `podDisruptionBudget.enabled` | `true` | |
| `podDisruptionBudget.minAvailable` | `1` | |
| `serviceMonitor.enabled` | `false` | Prometheus Operator |
| `networkPolicy.enabled` | `false` | |
| `resources` | requests 100m/128Mi, limits 500m/256Mi | |
| `extraEnv` | `[]` | Advanced tuning passthrough |

See `values.yaml` for the complete annotated surface and `values.schema.json`
for type/enum constraints.

## Out of scope / known limitations

- **In-cluster `kubernetes.default.svc` OIDC issuer is unsupported** — internal
  auth needs a published issuer.
- **GC worker not deployed** (M16 unimplemented).
- **DB rollback** is not automated by Helm rollback — run `migrate down N`.
- **No bundled Postgres/MinIO** — bring your own managed services.
- **Secret provisioning is out of scope** — the chart references a mandated
  `existingSecret`.
- **`helm template | kubectl apply` does not honor the migration hook** — run
  `migrate up` separately first.
