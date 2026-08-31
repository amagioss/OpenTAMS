---
status: "accepted"
date: 2026-08-31
decision-makers: OpenTAMS maintainers
consulted: OpenTAMS maintainers
informed: OpenTAMS contributors
---

# Author the contract at OpenAPI 3.0.3 and commit the bundled document

## Context and Problem Statement

The API contract lives in `api/opentams-api-v1.yaml`. It refers to the vendored BBC
schemas under `api/schemas/` through external `$ref`s, one file per schema. That layout is
good for humans and bad for tools: `oapi-codegen` and most documentation renderers want a
single self-contained document.

Two questions follow. Which OpenAPI version do we author in, and does the resolved
document get committed?

## Considered Options

* OpenAPI 3.1, which uses JSON Schema 2020-12 and matches the vendored schemas' native
  dialect
* OpenAPI 3.0.3, converting the schemas to the dialect it requires
* Resolve `$ref`s at build time and commit nothing
* Resolve `$ref`s with `redocly bundle` and commit the result

## Decision Outcome

Chosen options: "OpenAPI 3.0.3" and "commit the bundled document".

`oapi-codegen` v2 targets OpenAPI 3.0. Authoring in 3.1 would mean converting on every
build, so we author in the version the generator reads.

3.0.3 requires the older JSON Schema dialect, where an exclusive bound is written as
`exclusiveMinimum: true` next to `minimum: 0`. The vendored schemas are converted to that
form. This is one of the modification classes recorded in
[ADR-0005](0005-vendored-tams-schemas-are-modified.md).

`make api-bundle` produces `api/opentams-api-bundled.yaml`, and that file is committed.
`make api-check` regenerates the bundle and the Go types into a scratch directory and
compares. It passes today, so the source spec, the bundle, and `gen/api/` agree.

### Consequences

* Good, because a consumer who wants the contract gets one file with no schema tree to
  resolve. Redoc, `oapi-codegen`, and a reviewer reading a diff all use the same artefact.
* Good, because the diff of a contract change is visible in review. A change to a shared
  schema shows every operation it touches.
* Good, because the build has no network dependency on schema resolution.
* Bad, because two files describe the same contract and can disagree. `make api-check`
  exists only to catch that, and it runs in the Contract workflow rather than in
  `make ci`, because it needs Node.
* Bad, because 3.0.3 costs us 3.1 features. The most visible loss is `examples` on a
  schema and full `nullable` semantics.
* Bad, because the dialect conversion makes Redocly's example validator report false
  errors, so `no-invalid-media-type-examples` is turned off in `redocly.yaml`.

## More Information

* Lint configuration and every suppressed rule, with its reason: [`redocly.yaml`](../../redocly.yaml).
* Provenance and modification classes for the schemas: [`../../api/schemas/README.md`](../../api/schemas/README.md).
* The generation step that consumes the bundle: [ADR-0007](0007-strict-server-code-generation.md).
