---
unit: M3 internal/config
stage: Functional Design
status: Complete
---

# Business Rules — internal/config

## BR-CFG-01: Environment variables only
All 28 parameters loaded from env vars. No config files. 12-factor app principle (REQ-CFG-01).

## BR-CFG-02: Always-required fields
`DB_HOST`, `DB_NAME`, `DB_USER`, `DB_PASSWORD`, `OBJECT_STORE_BUCKET`, `OBJECT_STORE_REGION` are required in all environments.

## BR-CFG-03: External auth required in production; internal auth optional
`AUTH_EXTERNAL_ISSUER_URL` and `AUTH_EXTERNAL_AUDIENCE` are required unless `APP_ENV=development`. Default `APP_ENV` is `production`, so external auth is required by default.

`AUTH_INTERNAL_ISSUER_URL` and `AUTH_INTERNAL_AUDIENCE` are **optional** in every mode. They register a second issuer for service-to-service callers (e.g. Kubernetes ServiceAccount tokens, an M2M IdP). Plain Docker / VM topologies with no separate service-to-service IdP simply omit the pair — service callers authenticate via the external issuer, optionally with a distinct audience claim. Rationale and rejected alternatives are documented in `docs/requirements.md` REQ-SEC-01 (v1.2 decision block).

## BR-CFG-04: Pair rules
`OBJECT_STORE_ACCESS_KEY_ID` and `OBJECT_STORE_SECRET_ACCESS_KEY` must be set together or not at all. Same for `SERVER_TLS_CERT_FILE` and `SERVER_TLS_KEY_FILE`. Same for `AUTH_INTERNAL_ISSUER_URL` and `AUTH_INTERNAL_AUDIENCE` — half-configuring the internal issuer would silently break service-to-service calls with "no_match" 401s, so the validator fails fast at startup instead.

## BR-CFG-05: Enum validation
`APP_ENV` ∈ {`production`, `development`}. `LOG_LEVEL` ∈ {`debug`, `info`, `warn`, `error`}. `DB_SSLMODE` ∈ {`disable`, `allow`, `prefer`, `require`, `verify-ca`, `verify-full`}. Invalid enum returns default value AND an error — prevents cascade of downstream errors from a bad `APP_ENV` or `DB_SSLMODE`.

## BR-CFG-06: Port range validation
`SERVER_PORT` and `DB_PORT` must be in [1, 65535]. Validated at startup per REQ-CFG-03.

## BR-CFG-07: Pool sizing validation
`DB_POOL_MIN` must be ≥ 1. `DB_POOL_MIN` must be < `DB_POOL_MAX`. Driver rejects invalid pool config at connect time with opaque errors — catching it here satisfies REQ-CFG-03.

## BR-CFG-08: All errors collected together
All validation errors are collected before returning. Operator sees every problem in one startup attempt (REQ-CFG-03).

## BR-CFG-09: Secret redaction
`DB_PASSWORD`, `OBJECT_STORE_ACCESS_KEY_ID`, `OBJECT_STORE_SECRET_ACCESS_KEY` are replaced with `[redacted]` in the value returned by `Redacted()`. The original `Config` is not mutated (REQ-USE-04).

## BR-CFG-10: LOG_LEVEL runtime switching
`LOG_LEVEL` is loaded at startup as the initial value. Runtime switching on `SIGHUP` is handled by `pkg/logger`, not by this package (REQ-CFG-04).

## BR-CFG-11: DB_SSLMODE — secure-by-default in production, compatible-by-default in development
`DB_SSLMODE` defaults to `require` when `APP_ENV` resolves to `production` (fail closed on plaintext PostgreSQL — no operator footgun where the metadata store quietly accepts cleartext connections in prod). Defaults to `prefer` when `APP_ENV=development` so a local Postgres without TLS still boots out of the box. Operators set `verify-full` when the metadata store presents a CA-issued certificate they want validated. Both defaults are derived **after** `APP_ENV` is resolved, so the `APP_ENV` enum-fallback path (invalid value → "production" default + error) still selects the safer SSL default. Wired into the pgx DSN by `cmd/opentams/serve.go`; this package only carries the validated value (REQ-CFG-03).
