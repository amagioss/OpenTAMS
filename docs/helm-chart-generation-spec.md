# OpenTAMS Helm Chart — Generation Plan

> Purpose: a precise, self-contained spec an LLM can execute to generate a
> Helm chart for the OpenTAMS API server. Derived from the
> running code (`internal/config/config.go`, `internal/server/server.go`,
> `pkg/jwtauth/jwtauth.go`, `cmd/opentams/*`), existing raw manifests
> (`deployments/kubernetes/*`), and `docs/requirements.md`
> (REQ-DEV-04/05/05a, REQ-SEC-07, REQ-REL-07, REQ-ARCH-12).

---

## 0. Decisions (locked)

| # | Decision | Choice |
|---|----------|--------|
| D1 | Chart scope | OpenTAMS **server only**. Internal-client onboarding is delivered as **documentation + a static example manifest** (`examples/internal-client.yaml`) demonstrating the projected-ServiceAccount-token + audience pattern — **not** a chart-rendered, values-gated resource. The client is a separate workload with its own lifecycle and image; it does not belong on the server chart's values surface. |
| D2 | Internal OIDC (`AUTH_INTERNAL_*`) | Generic, fully configurable values. **No platform default. Disabled by default.** Document the published-issuer requirement and the in-cluster issuer limitation. |
| D3 | Secrets & cloud identity | The chart **never creates a Secret**. It **mandates** an externally-provisioned `existingSecret` (IaC / ESO / Vault / SealedSecrets) and references it by name. Object-store keys live in that Secret only when not using Workload Identity (IRSA/GKE WI/AKS), which is the default (ambient cred chain). Required keys are documented in the README. |
| D4 | Backing services | **External only.** No bundled Postgres/MinIO. Dev/demo continues to use `deployments/docker/docker-compose.yml`. |
| D5 | Migrations | Forward-only **Helm `pre-install,pre-upgrade` hook Job** running `/opentams migrate up`. DB rollback stays manual (`migrate down`). |
| D6 | GC worker | **Omitted.** `opentams gc` returns "not implemented (M16)" and would crash-loop. Revisit when M16 lands. |

---

## 1. Hard facts about the app (do not deviate)

- **Image**: built from `build/Dockerfile`. `ENTRYPOINT ["/opentams"]`, default `CMD ["serve"]`, `EXPOSE 8080`. Runs as **uid/gid 65532**, distroless-style.
- **Single binary, subcommands**: `serve`, `migrate up|down|version|force`, `gc` (gc unimplemented).
- **One port, `:8080`** serves everything: API, `/healthz` (liveness), `/readyz` (readiness, checks DB), `/metrics` (Prometheus, **unauthenticated** — in public route group), `/health/details`. There is **no separate metrics port**.
- **No filesystem writes** by the server → `readOnlyRootFilesystem: true` is safe. Add a small `emptyDir` at `/tmp` defensively (optional).
- **Stateless** → horizontally scalable; no PVC for the server.
- **Schema startup gate (REQ-DEV-05a)**: the server refuses to boot if the DB schema is older than the binary expects. ⇒ migrations MUST be applied before the Deployment's pods start, and a failed migration MUST block the rollout.
- **TLS (REQ-SEC-07)**: terminated at ingress by default. Optional server-level TLS via `SERVER_TLS_CERT_FILE`/`SERVER_TLS_KEY_FILE` (both-or-neither) for ingress-less topologies.

### Auth model (REQ-ARCH-12) — critical
Two independent JWT issuers, validated via auth0 `jwks.NewCachingProvider(issuerURL, ttl)`:

- **External** (`AUTH_EXTERNAL_ISSUER_URL`, `AUTH_EXTERNAL_AUDIENCE`): **required in production** (`APP_ENV != development`). Primary human/API client surface.
- **Internal** (`AUTH_INTERNAL_ISSUER_URL`, `AUTH_INTERNAL_AUDIENCE`): **optional, both-or-neither**. Service-to-service callers.

**JWKS limitation the chart MUST document (and not paper over):** the validator fetches JWKS with the **default HTTP client** — no custom CA bundle, no bearer token (`pkg/jwtauth/jwtauth.go:86`). Therefore:
- It **cannot** validate tokens against the **in-cluster** API-server issuer `https://kubernetes.default.svc` (needs cluster CA + authenticated discovery).
- It **works** against a **publicly-published / cloud-managed OIDC issuer** (EKS `oidc.eks.<region>.amazonaws.com/id/…`, GKE, AKS, or any self-hosted issuer with a system-trusted TLS cert and anonymous JWKS).

⇒ The chart exposes `auth.internal.issuerURL`/`audience` as plain config, **disabled by default**, with docs stating: set the issuer to the cluster's *published* OIDC issuer, and project client ServiceAccount tokens with `audience == auth.internal.audience`. Making `kubernetes.default.svc` work is a **code change** (custom transport with cluster CA) — out of chart scope; note it as a known limitation.

---

## 2. Chart layout (generate exactly this tree)

```
deployments/helm/opentams/
├── Chart.yaml
├── values.yaml
├── values.schema.json
├── README.md                      # generated from this plan + values table
├── .helmignore
├── templates/
│   ├── _helpers.tpl
│   ├── NOTES.txt
│   ├── serviceaccount.yaml
│   ├── deployment.yaml            # server  (no ConfigMap, no Secret — see §6)
│   ├── service.yaml
│   ├── migrate-job.yaml           # pre-install/pre-upgrade hook
│   ├── ingress.yaml               # optional
│   ├── hpa.yaml                   # optional
│   ├── poddisruptionbudget.yaml   # optional
│   ├── servicemonitor.yaml        # optional (Prometheus Operator)
│   ├── networkpolicy.yaml         # optional
│   └── tests/
│       └── test-connection.yaml   # helm test: curl /readyz (exercises DB connectivity)
├── examples/
│   └── internal-client.yaml       # D1: static reference manifest, NOT chart-rendered
└── ci/
    ├── default-values.yaml        # for `ct`/helm test in CI
    └── full-values.yaml           # all toggles on, for lint/template coverage
```

---

## 3. `Chart.yaml`

```yaml
apiVersion: v2
name: opentams
description: Cloud-agnostic BBC TAMS v8.0 API server
type: application
version: 0.1.0            # chart SemVer — bump on chart changes
appVersion: "<binary version>"  # track the opentams release tag
kubeVersion: ">=1.25.0-0"
home: https://github.com/amagioss/opentams
sources:
  - https://github.com/amagioss/opentams
maintainers:
  - name: amagioss
keywords: [tams, media, broadcast, api]
# No dependencies (D4: external services only).
```

---

## 4. `values.yaml` — full schema

Grouped by concern. *Some* keys map to `config.go` env vars (table in §5, rendered by `opentams.env`); the rest configure Kubernetes resources (image, replicas, service, ingress, autoscaling, probes, security contexts, etc.) and are not env. Defaults shown are the **chart** defaults.

```yaml
# -- Container image
image:
  repository: ghcr.io/amagioss/opentams   # confirm registry before release
  tag: ""                 # defaults to .Chart.appVersion when empty
  pullPolicy: IfNotPresent
imagePullSecrets: []

nameOverride: ""
fullnameOverride: ""

# -- Replicas (ignored if autoscaling.enabled)
replicaCount: 2

# -- ServiceAccount (Workload Identity lives here — D3)
serviceAccount:
  create: true
  name: ""
  annotations: {}         # e.g. eks.amazonaws.com/role-arn, iam.gke.io/gcp-service-account
  # false: the OpenTAMS *server* never calls the kube API (it validates JWTs via JWKS
  # over HTTP), so it does not need its SA token mounted — least privilege. The
  # projected-token + audience flow is a CLIENT concern (see examples/internal-client.yaml),
  # and IRSA / GKE WI inject their own projected token regardless of this flag.
  automountServiceAccountToken: false

# -- App / logging
app:
  env: production         # APP_ENV: production|development
  logLevel: info          # debug|info|warn|error

# -- Server
server:
  port: 8080              # SERVER_PORT (containerPort + Service targetPort)
  tls:                    # optional server-level TLS (REQ-SEC-07 escape hatch)
    enabled: false
    existingSecret: ""    # kubernetes.io/tls secret; mounts tls.crt/tls.key
    certPath: /etc/opentams/tls/tls.crt   # -> SERVER_TLS_CERT_FILE
    keyPath: /etc/opentams/tls/tls.key    # -> SERVER_TLS_KEY_FILE
  # SERVER_GRACEFUL_SHUTDOWN_PERIOD and SERVER_RATE_LIMIT_* are intentionally NOT
  # surfaced — the binary's config.go defaults apply; tune via `extraEnv` if needed.

# -- Database (Postgres) — external (D4)
database:
  host: ""                # DB_HOST (required)
  port: 5432
  name: opentams          # DB_NAME
  user: opentams          # DB_USER
  sslMode: require        # DB_SSLMODE
  pool:
    min: 2
    max: 10
  # password sourced from secret (see `secrets` block)

# -- Object store (S3-compatible) — external (D4)
objectStore:
  bucket: ""              # OBJECT_STORE_BUCKET (required)
  region: ""              # OBJECT_STORE_REGION (required)
  endpoint: ""            # OBJECT_STORE_ENDPOINT (empty = AWS S3)
  presignExpiry: 1h
  backend:
    provider: s3          # STORAGE_BACKEND_PROVIDER (required)
    product: s3           # STORAGE_BACKEND_PRODUCT

# -- Auth (REQ-ARCH-12)
auth:
  external:               # required when app.env != development
    issuerURL: ""         # AUTH_EXTERNAL_ISSUER_URL
    audience: ""          # AUTH_EXTERNAL_AUDIENCE
  internal:               # D2: disabled by default; both-or-neither
    enabled: false
    issuerURL: ""         # AUTH_INTERNAL_ISSUER_URL (must be a PUBLISHED issuer; see README)
    audience: ""          # AUTH_INTERNAL_AUDIENCE

# NOTE: AUTH_JWKS_TTL, IDEMPOTENCY_* and the GC tunables (GC_*) are intentionally
# NOT chart values. The idempotency knobs are internal correctness tuning the binary
# already validates at startup; GC is not deployed (D6, unimplemented M16) so its env
# is dead config here. All remain settable via `extraEnv` and are listed in the
# README "Advanced tuning" section.

# -- Secrets strategy (D3): the chart NEVER creates a Secret.
# Provision it via IaC / ESO / Vault / SealedSecrets BEFORE installing.
# The chart only references it. Each mapped env var carries an `optional` flag:
#   optional: false -> secretKeyRef.optional=false; pod fails to start if key missing.
#   optional: true  -> secretKeyRef.optional=true;  pod starts even if key absent
#                      (this is the Workload-Identity case for object-store creds:
#                       unset env -> SDK ambient cred chain, IRSA/GKE WI/AKS).
secrets:
  existingSecret: ""      # REQUIRED. Must already exist.
  # The set of secret-backed env vars and whether each is required/optional is a
  # CHART-AUTHOR decision baked into the `opentams.env` helper (§6) — NOT an operator
  # knob. The Secret must provide these keys (verbatim names):
  #   DB_PASSWORD                    (required)
  #   OBJECT_STORE_ACCESS_KEY_ID     (optional — omit under Workload Identity)
  #   OBJECT_STORE_SECRET_ACCESS_KEY (optional — omit under Workload Identity)

# -- Migration pre-deploy hook (D5)
migration:
  enabled: true
  backoffLimit: 3
  activeDeadlineSeconds: 300
  resources: { requests: {cpu: 100m, memory: 64Mi}, limits: {cpu: 200m, memory: 128Mi} }

# -- Service
service:
  type: ClusterIP
  port: 80
  annotations: {}

# -- Ingress (TLS terminated here by default — REQ-SEC-07)
ingress:
  enabled: false
  className: ""
  annotations: {}
  hosts:
    - host: opentams.example.com
      paths: [{ path: /, pathType: Prefix }]
  tls: []                 # [{secretName: opentams-tls, hosts: [opentams.example.com]}]

# -- Autoscaling
autoscaling:
  enabled: false
  minReplicas: 2
  maxReplicas: 10
  targetCPUUtilizationPercentage: 70
  targetMemoryUtilizationPercentage: null

# -- PodDisruptionBudget
podDisruptionBudget:
  enabled: true
  minAvailable: 1

# -- Prometheus Operator ServiceMonitor
serviceMonitor:
  enabled: false
  interval: 30s
  scrapeTimeout: 10s
  labels: {}

# -- NetworkPolicy (default-deny ingress except from ingress controller + scrapers)
networkPolicy:
  enabled: false
  ingressNamespaceSelector: {}      # namespace of the ingress controller (allowed on server.port)
  monitoringNamespaceSelector: {}   # namespace of Prometheus/scrapers (allowed on server.port for /metrics)

# (D1) Internal-client onboarding is NOT a chart value. See examples/internal-client.yaml.

# -- Pod scheduling / overhead
resources:
  requests: {cpu: 100m, memory: 128Mi}
  limits:   {cpu: 500m, memory: 256Mi}
podAnnotations: {}
podLabels: {}
nodeSelector: {}
tolerations: []
affinity: {}
topologySpreadConstraints: []
priorityClassName: ""

# -- Security contexts (carry over from existing manifests; do not loosen)
podSecurityContext:
  runAsNonRoot: true
  runAsUser: 65532
  runAsGroup: 65532
  fsGroup: 65532
  seccompProfile: { type: RuntimeDefault }
containerSecurityContext:
  allowPrivilegeEscalation: false
  readOnlyRootFilesystem: true
  capabilities: { drop: ["ALL"] }

# -- Probes (overridable)
probes:
  liveness:  { path: /healthz, initialDelaySeconds: 15, periodSeconds: 30, failureThreshold: 3 }
  readiness: { path: /readyz,  initialDelaySeconds: 5,  periodSeconds: 10, failureThreshold: 3 }
  startup:   { enabled: true, path: /readyz, periodSeconds: 5, failureThreshold: 30 }

extraEnv: []              # [{name, value}] / [{name, valueFrom}]
extraVolumes: []
extraVolumeMounts: []
```

---

## 5. Env-var mapping (source of truth: `internal/config/config.go`)

All env is rendered inline by `opentams.env` (§6) — there is no ConfigMap. "Source": **inline** = `value:` from the structured values (which carry the defaults); **Secret** = `secretKeyRef` against the external `existingSecret`. **Deviation from the old raw manifests**: `DB_HOST`/`DB_NAME`/`DB_USER` are inline (not secret) — only the password and object-store keys come from the Secret.

| Env var | values path | Source | Chart default | Required |
|---------|-------------|--------|---------------|----------|
| `APP_ENV` | `app.env` | inline | production | — |
| `LOG_LEVEL` | `app.logLevel` | inline | info | no |
| `SERVER_PORT` | `server.port` | inline | 8080 | no |
| `SERVER_TLS_CERT_FILE` / `_KEY_FILE` | `server.tls.{certPath,keyPath}` | inline | /etc/opentams/tls/tls.{crt,key} | only if `server.tls.enabled` |
| `DB_HOST` | `database.host` | inline | — | **yes** |
| `DB_PORT` | `database.port` | inline | 5432 | no |
| `DB_NAME` | `database.name` | inline | opentams | **yes** (default satisfies) |
| `DB_USER` | `database.user` | inline | opentams | **yes** (default satisfies) |
| `DB_PASSWORD` | `existingSecret` (helper `$secretEnv`) | **Secret** | — | **yes** (`optional: false`) |
| `DB_SSLMODE` | `database.sslMode` | inline | require | no |
| `DB_POOL_MIN` / `DB_POOL_MAX` | `database.pool.*` | inline | 2 / 10 | no |
| `OBJECT_STORE_BUCKET` | `objectStore.bucket` | inline | — | **yes** |
| `OBJECT_STORE_REGION` | `objectStore.region` | inline | — | **yes** |
| `OBJECT_STORE_ENDPOINT` | `objectStore.endpoint` | inline | — | no |
| `OBJECT_STORE_ACCESS_KEY_ID` | `existingSecret` (helper `$secretEnv`) | **Secret** | — | no — `optional: true` (omit under Workload Identity) |
| `OBJECT_STORE_SECRET_ACCESS_KEY` | `existingSecret` (helper `$secretEnv`) | **Secret** | — | no — `optional: true` |
| `OBJECT_STORE_PRESIGN_EXPIRY` | `objectStore.presignExpiry` | inline | 1h | no |
| `STORAGE_BACKEND_PROVIDER` | `objectStore.backend.provider` | inline | s3 | **yes** (default satisfies) |
| `STORAGE_BACKEND_PRODUCT` | `objectStore.backend.product` | inline | s3 | no |
| `AUTH_EXTERNAL_ISSUER_URL` | `auth.external.issuerURL` | inline | — | **yes in prod** |
| `AUTH_EXTERNAL_AUDIENCE` | `auth.external.audience` | inline | — | **yes in prod** |
| `AUTH_INTERNAL_ISSUER_URL` | `auth.internal.issuerURL` | inline | — | only if `auth.internal.enabled` |
| `AUTH_INTERNAL_AUDIENCE` | `auth.internal.audience` | inline | — | only if `auth.internal.enabled` |

**Not surfaced as values** (binary default applies; set via `extraEnv` if needed): `SERVER_GRACEFUL_SHUTDOWN_PERIOD`, `SERVER_RATE_LIMIT_RPS`, `SERVER_RATE_LIMIT_BURST`, `AUTH_JWKS_TTL`, `IDEMPOTENCY_KEY_TTL`, `IDEMPOTENCY_STALE_THRESHOLD`, `IDEMPOTENCY_REAPER_INTERVAL`. **Dead config, omitted entirely**: `GC_POLL_INTERVAL`, `GC_BATCH_SIZE` (GC not deployed — D6).

**Helm-side validation** (fail render via `fail`/`required`):
- Required-non-empty: `database.host`, `objectStore.bucket`, `objectStore.region`, `objectStore.backend.provider` (last has a default, so it only fails if blanked).
- If `app.env != "development"`: `auth.external.issuerURL` and `auth.external.audience` required.
- If `auth.internal.enabled`: both `auth.internal.issuerURL` and `auth.internal.audience` required (both-or-neither mirrors `config.go:185`).
- `secrets.existingSecret` is **required** (chart never creates a Secret — D3). Render fails if empty. README documents the Secret must contain at least `DB_PASSWORD`, plus the object-store key pair when not using Workload Identity (those keys are `optional: true`, so not enforced at render time).
- `server.tls.enabled` ⇒ `server.tls.existingSecret` required.

### `values.schema.json` (JSON Schema draft-07)
Catches type/shape errors at render time, complementing the `fail`/`required` logic above. Required and constrained fields:
- **Required (non-empty string)**: `secrets.existingSecret`, `database.host`, `objectStore.bucket`, `objectStore.region`.
- **Enums**: `app.env` ∈ `[production, development]`; `app.logLevel` ∈ `[debug, info, warn, error]`; `database.sslMode` ∈ `[disable, allow, prefer, require, verify-ca, verify-full]`; `service.type` ∈ `[ClusterIP, NodePort, LoadBalancer]`.
- **Integers with bounds**: `server.port`/`database.port` 1–65535; `replicaCount` ≥ 0; `database.pool.min` ≥ 1; `database.pool.max` ≥ 1.
- **Booleans**: the `*.enabled` toggles, `serviceAccount.create`, `serviceAccount.automountServiceAccountToken`.
- `extraEnv` is an array of objects each requiring `name`.
- Cross-field rules that JSON Schema can't express (prod-requires-external-auth, both-or-neither internal auth, tls-requires-existingSecret, `existingSecret` required) stay in the template `fail` logic — do **not** duplicate them here.

---

## 6. Template specifications

### `_helpers.tpl`
Standard helpers: `opentams.name`, `opentams.fullname`, `opentams.chart`, `opentams.labels`, `opentams.selectorLabels`, `opentams.serviceAccountName`, `opentams.image` (tag defaults to `.Chart.AppVersion`). Include `app.kubernetes.io/*` recommended labels + `helm.sh/chart`.

#### `opentams.env` — data-driven env builder (single source for Deployment + migrate Job)
There is **no ConfigMap and no Secret template**. Both pod specs include this one helper to render their full `env:` list. Non-secret values come from the structured values (which carry the **defaults** — see below); secret-backed values come from the external `existingSecret`, each with an **`optional` flag**.

```gotemplate
{{/*
opentams.env — emits a complete `env:` list. Call with the root context:
    env:
      {{- include "opentams.env" . | nindent <n> }}
*/}}
{{- define "opentams.env" -}}
{{- /* 1. Non-secret config: ENV_NAME -> value (from structured values; defaults live there). */ -}}
{{- $cfg := dict
    "APP_ENV"                          .Values.app.env
    "LOG_LEVEL"                        .Values.app.logLevel
    "SERVER_PORT"                      (.Values.server.port      | toString)
    "DB_HOST"                          .Values.database.host
    "DB_PORT"                          (.Values.database.port | toString)
    "DB_NAME"                          .Values.database.name
    "DB_USER"                          .Values.database.user
    "DB_SSLMODE"                       .Values.database.sslMode
    "DB_POOL_MIN"                      (.Values.database.pool.min | toString)
    "DB_POOL_MAX"                      (.Values.database.pool.max | toString)
    "OBJECT_STORE_BUCKET"              .Values.objectStore.bucket
    "OBJECT_STORE_REGION"              .Values.objectStore.region
    "OBJECT_STORE_ENDPOINT"            .Values.objectStore.endpoint
    "OBJECT_STORE_PRESIGN_EXPIRY"      .Values.objectStore.presignExpiry
    "STORAGE_BACKEND_PROVIDER"         .Values.objectStore.backend.provider
    "STORAGE_BACKEND_PRODUCT"          .Values.objectStore.backend.product
    "AUTH_EXTERNAL_ISSUER_URL"         .Values.auth.external.issuerURL
    "AUTH_EXTERNAL_AUDIENCE"           .Values.auth.external.audience
-}}
{{- /* Intentionally omitted (binary defaults apply; tune via extraEnv):
       SERVER_GRACEFUL_SHUTDOWN_PERIOD, SERVER_RATE_LIMIT_RPS/BURST, AUTH_JWKS_TTL,
       IDEMPOTENCY_*, GC_* (GC not deployed). */ -}}
{{- /* 1a. Conditional knobs — only emitted when enabled (both-or-neither, etc.). */ -}}
{{- if .Values.auth.internal.enabled -}}
  {{- $_ := set $cfg "AUTH_INTERNAL_ISSUER_URL" .Values.auth.internal.issuerURL -}}
  {{- $_ := set $cfg "AUTH_INTERNAL_AUDIENCE"   .Values.auth.internal.audience -}}
{{- end -}}
{{- if .Values.server.tls.enabled -}}
  {{- $_ := set $cfg "SERVER_TLS_CERT_FILE" .Values.server.tls.certPath -}}
  {{- $_ := set $cfg "SERVER_TLS_KEY_FILE"  .Values.server.tls.keyPath -}}
{{- end -}}
{{- /* 2. Emit non-secret: stable (sorted) order; skip empties so binary default applies. */ -}}
{{- range $k := keys $cfg | sortAlpha -}}
{{- $v := get $cfg $k -}}
{{- if ne (toString $v) "" }}
- name: {{ $k }}
  value: {{ $v | quote }}
{{- end -}}
{{- end -}}
{{- /* 3. Secret-backed vars (external existingSecret; D3). The list + each entry's
       `optional` flag is a CHART-AUTHOR decision (developer knob), not a value. */ -}}
{{- $sec := .Values.secrets.existingSecret | required "secrets.existingSecret is required (chart never creates a Secret)" -}}
{{- $secretEnv := list
    (dict "name" "DB_PASSWORD"                    "optional" false)
    (dict "name" "OBJECT_STORE_ACCESS_KEY_ID"     "optional" true)
    (dict "name" "OBJECT_STORE_SECRET_ACCESS_KEY" "optional" true)
-}}
{{- range $s := $secretEnv }}
- name: {{ $s.name }}
  valueFrom:
    secretKeyRef:
      name: {{ $sec }}
      key: {{ $s.name }}
      optional: {{ $s.optional }}
{{- end }}
{{- /* 4. Operator passthrough. */ -}}
{{- with .Values.extraEnv }}
{{ toYaml . }}
{{- end -}}
{{- end -}}
```

Design notes:
- **Defaults come from `values.yaml`** — the structured values (`app.env: production`, `database.sslMode: require`, …) *are* the chart defaults; operators override them the usual way. Where a key has no chart default (e.g. `database.host`), it resolves empty and is skipped so the binary's own `config.go` default applies. (Per-entry default fields were rejected as over-engineered; values.yaml is the single place defaults live.)
- **Secret `optional` flag is a developer knob** — the `$secretEnv` list and each entry's `optional` are defined by the chart author in the helper, **not** surfaced in `values.yaml`. `optional: true` (object-store keys) lets the pod start when the key is absent, i.e. the Workload-Identity case (unset env → SDK ambient cred chain); `optional: false` (`DB_PASSWORD`) hard-fails a pod missing its key. Operators only supply `secrets.existingSecret`; they cannot flip required↔optional. Changing the set is a chart change (one line in `$secretEnv`).
- **No drift** — the same `include "opentams.env" .` feeds both the Deployment and the migrate Job.

### No ConfigMap, no Secret template
- **No Secret (D3):** `secrets.existingSecret` is provisioned out-of-band (IaC / ESO / Vault / SealedSecrets) and must exist in the namespace before `helm install`. Both the Deployment and the migrate hook reference it by name (via `secretKeyRef` in `opentams.env`).
- **No ConfigMap:** all non-secret env is rendered inline by `opentams.env` into each pod spec. This **removes the hook-ordering problem entirely** — there is no shared config object that must exist before the pre-install Job; each pod carries its own env.
- **Rollout on config change:** because env is inline in the Deployment pod template, any value change mutates the pod template and triggers a rollout natively. No `checksum/config` annotation is needed (it can still be added as a no-op safety net if desired).

> Rejected alternative: migration as an init container — runs per-replica; golang-migrate's advisory lock makes it safe, but it muddies the "separate pre-deploy step" REQ-DEV-05 wants and reruns on every pod start. Keep the hook Job as primary; with inline env it has no ordering dependency.

### `deployment.yaml`
- `replicas` from `replicaCount` unless `autoscaling.enabled` (then omit field).
- Pod & container security contexts from values (the locked defaults above).
- `env`: `{{- include "opentams.env" . | nindent 12 }}` — the single generic builder (non-secret values inline + secret-backed `secretKeyRef` + `extraEnv`). No `envFrom`.
- `containerPort: {{ .Values.server.port }}`.
- Probes: from `values.probes` (defaults: liveness `/healthz`, readiness `/readyz`, optional startup `/readyz` — startup covers slow first DB connect + schema gate). All paths/timings overridable.
- `volumeMounts`: `/tmp` emptyDir (read-only root FS); TLS secret mount when `server.tls.enabled`; `extraVolumeMounts`.
- ServiceAccount, imagePullSecrets, nodeSelector, affinity, tolerations, topologySpreadConstraints, priorityClassName, `extraEnv`.
- No `checksum/*` annotations needed — env is inline in the pod template, so changes roll out natively. (The external Secret is not chart-managed; rotating it requires a manual restart, e.g. `kubectl rollout restart` — note this in README.)

### `migrate-job.yaml` (D5)
- Hook annotations: `helm.sh/hook: pre-install,pre-upgrade`, `helm.sh/hook-weight: "0"`, `helm.sh/hook-delete-policy: before-hook-creation,hook-succeeded`. `command: ["/opentams","migrate","up"]`.
- Same `env` via `include "opentams.env" .` (identical to the Deployment — no drift), same pod/container security context, `resources` from `migration.resources`, `restartPolicy: OnFailure`, `backoffLimit` / `activeDeadlineSeconds` from values, `ttlSecondsAfterFinished: 300`.
- Gated by `migration.enabled`.

### `service.yaml`
ClusterIP; `port: service.port` → `targetPort: server.port`. Add Prometheus scrape annotations only if `serviceMonitor.enabled=false` (operator-less fallback): `prometheus.io/scrape: "true"`, `prometheus.io/port: "{{ server.port }}"`, `prometheus.io/path: /metrics`.

### `ingress.yaml`
Standard `networking.k8s.io/v1`, gated by `ingress.enabled`; TLS block from `ingress.tls` (REQ-SEC-07 default termination point). Backend → service port.

### `hpa.yaml`, `poddisruptionbudget.yaml`
Standard, gated. HPA `autoscaling/v2` with CPU (+ optional memory) metrics.

### `servicemonitor.yaml`
`monitoring.coreos.com/v1`, gated by `serviceMonitor.enabled`. Endpoint port = service port, `path: /metrics`. Note `/metrics` is unauthenticated — fine for in-cluster scrape; document not to expose it via the public Ingress.

### `networkpolicy.yaml`
Gated. Default-deny ingress; allow from ingress-controller namespace (`ingressNamespaceSelector`) on `server.port`, and from monitoring namespace for scrapes.

### `examples/internal-client.yaml` (D1) — static reference, NOT a chart template
This file is **not** rendered by Helm and has **no values**. It is a plain, copy-pasteable manifest (literal values, no `{{ }}`) that a *consumer team* adapts for their own workload. It shows the **projected ServiceAccount token** pattern for an internal caller:
```yaml
volumes:
  - name: opentams-token
    projected:
      sources:
        - serviceAccountToken:
            audience: <auth.internal.audience>   # MUST equal the server's AUTH_INTERNAL_AUDIENCE
            expirationSeconds: 3600
            path: token
# container mounts it at /var/run/secrets/tokens
```
README explains: the client reads `/var/run/secrets/tokens/opentams-token/token` and sends it as `Authorization: Bearer …`; OpenTAMS validates it against `auth.internal.issuerURL` (the cluster's **published** OIDC issuer). Include the explicit caveat that the in-cluster `kubernetes.default.svc` issuer will not work with the current JWKS fetch.

> Rationale (D1): the internal client is a separate workload with its own image and lifecycle; rendering it from the server chart would couple two independent release cadences and force a dummy client image into every install. Keep it as documentation. If an *end-to-end auth smoke test* is wanted, add it as a `helm test` pod under `templates/tests/` or a CI fixture — a test concern, not a values-surface concern.

### `NOTES.txt`
Post-install: how to reach the service, whether the migration hook ran, auth mode in effect (external required? internal enabled?), and warnings (e.g., resolved `APP_ENV=development` ⇒ auth disabled).

### `tests/test-connection.yaml`
`helm.sh/hook: test` pod that curls `http://<svc>:<port>/readyz` and expects 200. `/readyz` (not `/healthz`) is chosen deliberately — it checks metadata-store connectivity (REQ-REL-07), so a passing test proves the Service resolves **and** the app can reach its DB, a far more meaningful "release works" signal than liveness alone. Add `helm.sh/hook-delete-policy: hook-succeeded,before-hook-creation` to clean up the test pod.

---

## 7. README content requirements

- Quick start (external Postgres + S3), values table (§5 + §4), config model (structured values map to env; defaults live in `values.yaml`; long-tail tuning via `extraEnv`), auth setup (external mandatory in prod; internal + projected-token pattern + **in-cluster issuer limitation**), Workload Identity setup (IRSA/GKE WI annotations + leaving object-store secret keys out), migration hook behavior + manual rollback (`/opentams migrate down N`), Secret-rotation note (`kubectl rollout restart`), and an explicit "GC worker not deployed (M16)" note.
- **Mandatory `existingSecret` section**: the chart does not create Secrets. Provide a worked example (and recommended IaC/ESO/Vault flow) for creating the Secret with required keys — `DB_PASSWORD` always, plus `OBJECT_STORE_ACCESS_KEY_ID` / `OBJECT_STORE_SECRET_ACCESS_KEY` only when not using Workload Identity (those keys are `optional: true`). State that the Secret must exist **before** `helm install` (it is read by the pre-install migrate hook).
- **Advanced tuning via `extraEnv`**: list the env vars the chart deliberately does *not* surface as first-class values, with their `config.go` defaults, and show how to override them through `extraEnv` — `SERVER_GRACEFUL_SHUTDOWN_PERIOD` (30s), `SERVER_RATE_LIMIT_RPS` (1000), `SERVER_RATE_LIMIT_BURST` (100), `AUTH_JWKS_TTL` (15m), `IDEMPOTENCY_KEY_TTL` (1h), `IDEMPOTENCY_STALE_THRESHOLD` (60s), `IDEMPOTENCY_REAPER_INTERVAL` (1m). Explain the rationale: the chart leaves these to the binary so a future change to a binary default isn't silently pinned by the chart. `GC_*` is not listed — GC is not deployed.

---

## 8. Validation / CI gates (the chart is not done until these pass)

1. `helm lint deployments/helm/opentams` — clean.
2. `helm template` with `ci/default-values.yaml` AND `ci/full-values.yaml` — renders without error; pipe through `kubeconform -strict -summary` (or `kubeval`).
3. Render assertions (manual or `helm-unittest`):
   - prod + missing external auth ⇒ render **fails**.
   - `auth.internal.enabled` with `issuerURL` or `audience` empty ⇒ render **fails**.
   - secret entry with `optional: false` (e.g. `DB_PASSWORD`) renders `secretKeyRef.optional: false`; object-store keys render `optional: true`.
   - `secrets.existingSecret` empty ⇒ render **fails** (chart never creates a Secret).
   - `secrets.existingSecret` set ⇒ **no Secret object is ever rendered** (grep the template output: zero `kind: Secret`); Deployment + migrate Job reference it via per-key `secretKeyRef` (not `envFrom`/`secretRef`).
   - migrate Job carries `pre-install,pre-upgrade` hook annotations; its `env` is byte-identical to the Deployment's (both render `opentams.env`).
   - no `kind: ConfigMap` is ever emitted (env is inline).
   - `autoscaling.enabled` ⇒ Deployment has no `replicas`; HPA rendered.
4. (Optional) `ct lint`/`ct install` against kind with an ephemeral Postgres + MinIO supplied as test fixtures (not chart deps).

---

## 9. Acceptance criteria → requirement traceability

| Requirement | Satisfied by |
|-------------|--------------|
| REQ-DEV-04 (orchestration manifests: deployment, service, config, secrets) | Deployment, Service + inline env via `opentams.env` + mandated external `existingSecret` (provisioning is IaC's responsibility, D3) |
| REQ-DEV-05 (migration as pre-deploy step, failed migration blocks rollout) | migrate-job hook (D5) + schema startup gate |
| REQ-DEV-05a (schema version startup probe) | startup/readiness probes + hook ordering |
| REQ-SEC-07 (TLS at ingress default, optional server TLS) | ingress.tls + `server.tls.*` |
| REQ-REL-07 (`/readyz` checks DB) | readiness probe |
| REQ-ARCH-12 (external + service-to-service JWT auth) | `auth.external/internal` config + projected-token example |
| Test-plan #32 (internal JWT accepted when configured) | `auth.internal.enabled` config + `examples/internal-client.yaml` pattern (optionally a `helm test`/CI auth smoke test) |
| Test-plan #54 (manifests can deploy the server) | full chart + CI install |

---

## 10. Out of scope / known limitations (state in README, do not silently work around)

- **In-cluster `kubernetes.default.svc` OIDC issuer is unsupported** by the current JWKS fetch (needs a code change: custom HTTP transport with cluster CA + token). Internal auth requires a *published* issuer.
- **GC worker** not deployed (unimplemented — M16).
- **DB rollback** is not automated by Helm rollback; operator runs `/opentams migrate down N`.
- **No bundled Postgres/MinIO** (D4) — bring your own managed services; use docker-compose for local dev.
- **Secret provisioning is out of scope** (D3) — the chart only references a mandated `existingSecret`. Creating/rotating it is IaC / secret-manager responsibility; the README documents the required keys and a worked example.
- **Migration ordering depends on Helm hooks** (D5). `helm install`/`helm upgrade` and hook-aware GitOps (Argo CD sync-phases, Flux Helm controller) honor it. **Plain `helm template | kubectl apply` does NOT** — hook annotations become inert, so the migrate Job runs concurrently with the Deployment and the schema startup gate may crash-loop pods until it finishes. Render-and-apply users must run `migrate up` as a separate step first, or switch migration to an init container (the rejected alternative in §6). Document this in the README.

---

## 11. Suggested generation order (checklist for the executing LLM)

- [ ] `Chart.yaml`, `.helmignore`, `values.yaml`, `values.schema.json`
- [ ] `_helpers.tpl`
- [ ] `opentams.env` helper (structured-values env builder + per-secret `optional`) + validation `fail`/`required` (incl. mandatory `existingSecret`). No ConfigMap, no Secret template.
- [ ] ServiceAccount, Service
- [ ] Deployment (probes from `values.probes`, security contexts, env via `opentams.env` — NO envFrom, NO checksum annotations, /tmp emptyDir, TLS mount)
- [ ] migrate-job (hook)
- [ ] Ingress, HPA, PDB, ServiceMonitor, NetworkPolicy (all gated)
- [ ] `examples/internal-client.yaml` (static, no values) + projected-token docs in README
- [ ] NOTES.txt, tests/test-connection.yaml
- [ ] ci/*.yaml fixtures
- [ ] README.md
- [ ] Run §8 validation gates; fix until clean
```
