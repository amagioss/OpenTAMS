# OpenTAMS Configuration Reference

OpenTAMS reads configuration exclusively from environment variables — no config files, no YAML, no flags-vs-env precedence trickery. This document is the authoritative list.

The README's [Configuration table](../README.md#configuration) is a curated 9-row summary of the most-touched variables; this document is the full surface.

> Conventions used below:
>
> - **Required** has three values:
>   - **Yes** — the application refuses to start if missing in **any** mode (production or development).
>   - **Yes (prod only)** — required when `APP_ENV != development`; relaxed in `development` mode.
>   - **No** — optional everywhere.
> - Some variables come in **both-or-neither pairs** — supplying exactly one of the pair is a startup error. These are flagged in the row's Description.
> - **Default** is the value used when the variable is unset. Defaults are chosen so a fresh `make run` (which brings up storage via `make stack` then starts the server on the host) works with zero manual configuration.
> - **Validation cross-checks** beyond per-row required/range are listed in the [Cross-variable constraints](#cross-variable-constraints) section at the end of this document. Failing any of them produces an `errors.Join` of every failure, so an operator with three things wrong sees all three at once, not one at a time.

## Application meta

| Variable | Default | Required | Description |
|---|---|---|---|
| `APP_ENV` | `production` | No | One of `production` or `development`. `development` relaxes external-auth requirements (DevProvider accepts any token) and lowers the `DB_SSLMODE` default to `prefer`. Any value other than `development` is treated as production. Set to `production` (or simply leave unset) for any deployment that takes outside traffic. |
| `LOG_LEVEL` | `info` | No | One of `debug` / `info` / `warn` / `error`. Hot-reloadable: send `SIGHUP` to the process and the new value of `LOG_LEVEL` from the environment is picked up without a restart. |

## Server

| Variable | Default | Required | Description |
|---|---|---|---|
| `SERVER_PORT` | `8080` | No | TCP port the HTTP listener binds to. Validated to be in `1–65535`. |
| `SERVER_TLS_CERT_FILE` | (empty) | No (pair) | Path to a PEM-encoded TLS certificate. If both this and `SERVER_TLS_KEY_FILE` are set, OpenTAMS serves HTTPS instead of HTTP. Most production deployments terminate TLS in a load balancer or service mesh and leave both unset. **Both-or-neither pair with `SERVER_TLS_KEY_FILE`.** |
| `SERVER_TLS_KEY_FILE` | (empty) | No (pair) | Path to the matching PEM-encoded private key. **Both-or-neither pair with `SERVER_TLS_CERT_FILE`.** |
| `SERVER_GRACEFUL_SHUTDOWN_PERIOD` | `30s` | No | After SIGTERM, the server stops accepting new connections and gives in-flight requests up to this long to finish before forcibly closing them. Tune to your slowest legitimate handler. |

## Database (PostgreSQL)

| Variable | Default | Required | Description |
|---|---|---|---|
| `DB_HOST` | (empty) | **Yes** | PostgreSQL host. Required in all modes — there is no in-memory fallback. |
| `DB_PORT` | `5432` | No | Validated to be in `1–65535`. |
| `DB_NAME` | (empty) | **Yes** | Database name. Required in all modes. |
| `DB_USER` | (empty) | **Yes** | Username. Required in all modes. |
| `DB_PASSWORD` | (empty) | **Yes** | Password. Required in all modes. We never log this; `(*Config).Redacted()` is what `serve` prints at startup. |
| `DB_SSLMODE` | `require` (prod), `prefer` (dev) | No | Maps to libpq `sslmode`. Allowed: `disable` / `allow` / `prefer` / `require` / `verify-ca` / `verify-full`. Production deployments should use `verify-full` against a known CA; the default of `require` is permissive but encrypts in transit. |
| `DB_POOL_MIN` | `2` | No | `pgxpool` minimum connections. Must be ≥ 1, and must be strictly less than `DB_POOL_MAX` (see [Cross-variable constraints](#cross-variable-constraints)). |
| `DB_POOL_MAX` | `10` | No | `pgxpool` maximum connections. Tune to your Postgres `max_connections` budget; OpenTAMS holds at most this many connections per pod. |

Schema migrations are applied via `opentams migrate up` (or [`golang-migrate`](https://github.com/golang-migrate/migrate) directly). The server refuses to start if the schema version is missing, dirty, or older than what this OpenTAMS binary expects (REQ-DEV-05a).

## Object store (S3-compatible)

| Variable | Default | Required | Description |
|---|---|---|---|
| `OBJECT_STORE_BUCKET` | (empty) | **Yes** | Bucket name. OpenTAMS does not create the bucket; it must exist. Required in all modes. |
| `OBJECT_STORE_REGION` | (empty) | **Yes** | Region for SigV4 signing. For non-AWS S3-compatible stores (MinIO, Ceph, GCS-via-S3) any non-empty string works; `us-east-1` is conventional. Required in all modes. |
| `OBJECT_STORE_ENDPOINT` | (empty) | No | Override URL for non-AWS endpoints (`http://minio:9000`, etc.). Leave empty for AWS S3. |
| `OBJECT_STORE_ACCESS_KEY_ID` | (empty) | No (pair) | Access key. **Both-or-neither pair with `OBJECT_STORE_SECRET_ACCESS_KEY`** — supplying exactly one is a startup error. Both can be left empty for workloads using IAM instance-profile or IRSA (EKS) credentials, in which case the AWS SDK's default credential chain takes over. |
| `OBJECT_STORE_SECRET_ACCESS_KEY` | (empty) | No (pair) | Secret key. **Both-or-neither pair with `OBJECT_STORE_ACCESS_KEY_ID`.** Same IRSA/instance-profile escape hatch as above. |
| `OBJECT_STORE_PRESIGN_EXPIRY` | `1h` | No | Presigned URL TTL handed out by `POST /flows/{flowId}/storage`. Longer = more retry tolerance, shorter = tighter security posture. |
| `STORAGE_BACKEND_PROVIDER` | (empty) | **Yes** | Free-form provider label exposed via `GET /service/storage-backends` (e.g. `aws`, `gcp`, `minio`). Required in all modes. Surface for capability-discovery clients; OpenTAMS itself doesn't switch on it. |
| `STORAGE_BACKEND_PRODUCT` | `s3` | No | Free-form product label, same purpose. |

## Authentication

| Variable | Default | Required | Description |
|---|---|---|---|
| `AUTH_EXTERNAL_ISSUER_URL` | (empty) | **Yes (prod only)** | OIDC issuer URL for end-user / partner traffic. OpenTAMS fetches `<issuer>/.well-known/openid-configuration` and the JWKS at startup, then refreshes JWKS periodically. Optional in `APP_ENV=development` (the dev provider stands in). |
| `AUTH_EXTERNAL_AUDIENCE` | (empty) | **Yes (prod only)** | Audience claim required on incoming JWTs. Tokens with a non-matching `aud` are rejected. Optional in `development`. |
| `AUTH_INTERNAL_ISSUER_URL` | (empty) | No (pair) | Optional second OIDC issuer for service-to-service traffic from the same trust domain (e.g. Kubernetes ServiceAccount projected tokens). When set, both internal and external issuers are accepted. **Both-or-neither pair with `AUTH_INTERNAL_AUDIENCE`** — supplying exactly one is a startup error. |
| `AUTH_INTERNAL_AUDIENCE` | (empty) | No (pair) | Audience for the internal issuer. **Both-or-neither pair with `AUTH_INTERNAL_ISSUER_URL`.** |
| `AUTH_JWKS_TTL` | `15m` | No | How often the JWKS cache is refreshed against the issuer's discovery endpoint. The cache also refreshes on a kid miss; this knob is just the upper bound. |

In `APP_ENV=development`, all auth knobs are optional and the dev provider is selected automatically — any `Authorization: Bearer <whatever>` succeeds. The production code path errors out at startup if the external auth pair is missing, so dev configuration cannot accidentally ship to prod.

## Idempotency

These knobs control the `internal/idempotency` package's behaviour. The defaults are tuned for "human retry tolerance plus a generous safety margin".

| Variable | Default | Required | Description |
|---|---|---|---|
| `IDEMPOTENCY_KEY_TTL` | `1h` | No | How long a completed idempotency key is cached. A retry of the same key + same body within this window replays the cached response; outside the window the key is treated as new. Must be strictly greater than `IDEMPOTENCY_STALE_THRESHOLD`. Trade-off: longer = more retry safety against very slow clients, shorter = smaller `idempotency_keys` table. |
| `IDEMPOTENCY_STALE_THRESHOLD` | `60s` | No | A key acquired (`Acquire` returned `StatusAcquired`) but never `Complete`d or `Release`d after this duration is considered abandoned and reaped. Should be longer than your slowest reasonable handler latency to avoid false positives. Must be strictly less than `IDEMPOTENCY_KEY_TTL`. |
| `IDEMPOTENCY_REAPER_INTERVAL` | `1m` | No | How often the background reaper goroutine runs. Lower = faster cleanup of expired and stale keys at the cost of more reaper queries against Postgres. |

All three idempotency durations must be positive (`> 0`).

## Rate limiting (`SERVER_RATE_LIMIT_*`)

**Rate limiting is not implemented.** These variables are parsed and validated at
startup and then ignored. There is no rate-limiting middleware, no 429 response, and no
`Retry-After` header. Setting them changes nothing. Put a rate limiter in front of
OpenTAMS — an ingress controller or API gateway — if you need one.

| Variable | Default | Required | Description |
|---|---|---|---|
| `SERVER_RATE_LIMIT_RPS` | `1000` | No | Intended token-bucket refill rate. Currently ignored. |
| `SERVER_RATE_LIMIT_BURST` | `100` | No | Intended burst capacity above the RPS limit. Currently ignored. |

## Garbage collection (`internal/gc`)

**The garbage collector is not implemented.** These variables are parsed and validated
at startup and then ignored — no released build of OpenTAMS reconciles the object store
against the database, and `opentams gc` exits with an error. They are documented here so
an operator who sets them is not surprised when nothing happens. Until the GC worker
lands, objects orphaned by partial failures accumulate; plan for that in your storage
sizing, and do **not** substitute an S3 lifecycle policy (it cannot see cross-flow
references and will delete live data).

| Variable | Default | Required | Description |
|---|---|---|---|
| `GC_POLL_INTERVAL` | `5m` | No | How often the GC loop would run S3-vs-DB reconciliation. Currently ignored. |
| `GC_BATCH_SIZE` | `100` | No | Maximum objects considered per reconciliation pass. Currently ignored. |

## Putting it together

A minimal production environment for OpenTAMS:

```bash
APP_ENV=production
LOG_LEVEL=info

DB_HOST=postgres.prod.internal
DB_NAME=opentams
DB_USER=opentams
DB_PASSWORD=$(vault kv get -field=password ...)
DB_SSLMODE=verify-full

OBJECT_STORE_BUCKET=opentams-prod-media
OBJECT_STORE_REGION=us-east-1
# Credentials supplied via IRSA / instance profile — env keys left empty.

AUTH_EXTERNAL_ISSUER_URL=https://auth.example.com
AUTH_EXTERNAL_AUDIENCE=opentams-prod
```

Everything else falls through to defaults that we believe are reasonable for production. The exhaustive list of dev-vs-prod-different behaviour: `DB_SSLMODE` default (`require` vs `prefer`) and the `AUTH_EXTERNAL_*` requirement (required in production, optional in development). Every other variable behaves identically in both modes.

## Cross-variable constraints

These are validated at startup in addition to the per-row Required/range checks above. Failing any of them produces a startup error that names the offending pair.

| Constraint | Why it exists |
|---|---|
| `DB_POOL_MIN ≥ 1` | The pool must be willing to hold at least one connection or `pgxpool` cannot serve any request. |
| `DB_POOL_MIN < DB_POOL_MAX` | Equal min and max would mean a fixed-size pool that can't grow under load; if you genuinely want that, set `DB_POOL_MAX = DB_POOL_MIN + 1`. |
| `IDEMPOTENCY_STALE_THRESHOLD < IDEMPOTENCY_KEY_TTL` | The reaper would otherwise prune live in-flight rows before their TTL elapses, breaking the cached-response replay window. |
| `SERVER_PORT`, `DB_PORT ∈ 1..65535` | Port-range sanity check. |
| All three `IDEMPOTENCY_*` durations > 0 | Negative or zero durations would make the reaper either spin or never run. |
| `SERVER_TLS_CERT_FILE` and `SERVER_TLS_KEY_FILE` are both-or-neither | Half-configured TLS would silently degrade to plain HTTP, which is a security footgun. |
| `OBJECT_STORE_ACCESS_KEY_ID` and `OBJECT_STORE_SECRET_ACCESS_KEY` are both-or-neither | Same shape: half-configured static credentials would silently fall back to the AWS SDK default chain in a way the operator didn't intend. |
| `AUTH_INTERNAL_ISSUER_URL` and `AUTH_INTERNAL_AUDIENCE` are both-or-neither | A configured issuer with no audience would accept tokens for the wrong service, defeating the point of having a second issuer. |

## Where this is enforced in code

`internal/config/config.go` is the single source of truth. `Load()` reads every variable, applies defaults, runs every per-row requirement check (including the production-only ones), enforces the cross-variable constraints listed above, and returns either a valid `*Config` or an `errors.Join` of every validation failure (so an operator with three things wrong sees all three at once, not one at a time).

If you change the configuration surface, also update:

1. This document.
2. The README's Configuration summary table.
3. `deployments/docker/.env.example` (the local-development credentials that the Quickstart leans on).
4. The relevant section of [`docs/requirements.md`](requirements.md) (REQ-CONF-01 et al.) — that's the spec, not just code-side.
