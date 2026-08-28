---
status: "accepted"
date: 2026-08-25
decision-makers: OpenTAMS maintainers
consulted: OpenTAMS maintainers
informed: OpenTAMS contributors
---

# Vendor the TAMS JSON schemas as a modified copy

## Context and Problem Statement

The BBC TAMS specification publishes its data model as JSON Schema files. OpenTAMS needs
them at three points: to validate incoming requests, to generate Go types via
`oapi-codegen`, and to render API documentation.

Two properties of the upstream files make direct use impossible. They are written in
JSON Schema 2020-12, while the OpenAPI document that references them declares OpenAPI
3.0.3, which requires a draft-04-derived subset — `exclusiveMinimum` takes a boolean,
`unevaluatedProperties` does not exist, and `if`/`then`/`else` is not supported. And
`oapi-codegen` cannot generate a usable union type for `flow.json` without an OpenAPI
`discriminator`, which upstream does not provide.

Separately, OpenTAMS has behavioural divergences from the specification that have to be
expressed somewhere in the contract.

How should the upstream schemas be consumed?

## Considered Options

* Vendor a modified copy under `api/schemas/`, with the divergences documented
* Vendor an unmodified copy and convert the dialect at build time
* Reference the upstream files by URL from the OpenAPI document
* Move to OpenAPI 3.1, which uses JSON Schema 2020-12 natively

## Decision Outcome

Chosen option: "vendor a modified copy", because it is the only option that keeps the
build hermetic and the contract self-describing, and because some of the modifications
express real behavioural divergence that no mechanical conversion could produce.

`api/schemas/` holds 41 files derived from the BBC TAMS `8.0` tag
(`b386c43f97f05274d838066c62eb1d9dad351ea6`) plus one OpenTAMS-only file. 17 of the 41
are modified. The modifications fall into six classes — dialect conversion, `$ref`
rewrites, the `flow.json` discriminator, `readOnly` on server-managed fields, two
backports from TAMS `8.1`, and three documented behavioural divergences — enumerated in
[`api/schemas/README.md`](../../api/schemas/README.md).

Referencing upstream by URL was rejected because it makes the build depend on network
availability and on a file that can change under a moving ref, and because it cannot
carry the divergences. OpenAPI 3.1 would remove the dialect problem, but `oapi-codegen`'s
3.1 support was not sufficient for this contract when the choice was made, and the
discriminator and divergence modifications would still be needed.

### Consequences

* Good, because the build has no network dependency and the committed contract is
  exactly what the server implements.
* Good, because the divergences are visible in the schema files themselves, where a
  client author generating an SDK will actually encounter them.
* Bad, because upgrading to a newer TAMS version is a manual three-way merge: upstream
  changes, our dialect conversion, and our divergences. `api/schemas/README.md` exists
  so whoever does that merge knows which is which.
* Bad, because the copy can silently fall behind upstream. Nothing in CI compares
  `api/schemas/` against the pinned tag today; the README documents the `diff` command
  as a manual step.
* Bad, because carrying two `8.1` fields inside a set otherwise pinned to `8.0` makes
  "which version is this" a question with a paragraph for an answer rather than a tag.

## More Information

* Provenance, per-file modification classes, and the reproduction command:
  [`api/schemas/README.md`](../../api/schemas/README.md).
* Lint-rule suppressions the dialect conversion makes necessary: [`redocly.yaml`](../../redocly.yaml).
* Drift between the source spec, the committed bundle, and the generated Go types is
  caught by `make api-check`.
* The behavioural divergences have their own records:
  [ADR-0001](0001-whole-batch-reject-on-segment-overlap.md) (accepted and implemented),
  [ADR-0002](0002-rfc9457-problem-details-for-per-segment-failures.md) (proposed — the
  schema carries the divergence but the server does not yet implement it).
