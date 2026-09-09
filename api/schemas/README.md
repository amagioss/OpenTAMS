# Vendored TAMS JSON Schemas

## Provenance

These schema files originate from the BBC TAMS specification.

| | |
|---|---|
| Upstream | [github.com/bbc/tams](https://github.com/bbc/tams) |
| Tag | `8.0` |
| Commit | [`b386c43f97f05274d838066c62eb1d9dad351ea6`](https://github.com/bbc/tams/tree/b386c43f97f05274d838066c62eb1d9dad351ea6/api/schemas) |
| Upstream path | `api/schemas/` |
| Upstream licence | Apache 2.0 |
| Vendored | 41 files from upstream, plus 1 OpenTAMS-only file |

To reproduce the comparison:

```bash
git clone https://github.com/bbc/tams /tmp/tams
git -C /tmp/tams checkout b386c43f97f05274d838066c62eb1d9dad351ea6
diff -r /tmp/tams/api/schemas api/schemas
```

## These files are modified

**Do not treat them as an upstream mirror.** 17 of the 41 vendored files differ from
the upstream `8.0` tag, and one file does not exist upstream at all. The modifications
fall into six classes.

### 1. JSON Schema dialect (mechanical, no semantic change)

The upstream schemas are JSON Schema 2020-12. OpenAPI 3.0.3 — the version this API
contract declares — requires a draft-04-derived subset. Three rewrites were applied:

| Upstream (2020-12) | Vendored (OpenAPI 3.0.3) |
|---|---|
| `"exclusiveMinimum": 0` | `"exclusiveMinimum": true, "minimum": 0` |
| `"unevaluatedProperties": false` | `"additionalProperties": false` |
| `if` / `then` / `else` | removed; enforced in application code instead |

The `if`/`else` removal affects `flow-video.json` only, where upstream expresses "`vfr:
false` implies no `frame_rate`" as a conditional. OpenAPI 3.0.3 cannot express it, so
the rule is enforced by the server and surfaces as the `vfr-frame-rate-conflict`
problem type.

Redocly's example validator reads these files as 2020-12 and reports every numeric
bound as invalid. It is wrong about the declared OpenAPI version; the rule is disabled
with that justification in [`redocly.yaml`](../../redocly.yaml).

### 2. Cross-reference rewrites (mechanical)

Upstream links of the form `../schemas/timerange#top` are rewritten to
`#/schemas/timerange` so they resolve inside the bundled document.

### 3. Discriminator for code generation

`flow.json` gains an OpenAPI `discriminator` mapping `format` to the concrete flow
schema. `oapi-codegen` needs it to emit a usable union type. Upstream relies on
`oneOf` alone.

### 4. `readOnly` on server-managed fields

`flow-core.json` and `source.json` mark server-computed fields (`created`, `updated`,
`segments_updated`, and similar) `readOnly: true`, so generated request types omit
them. Upstream conveys the same constraint in prose.

### 5. Backports from TAMS 8.1

`service.json` carries `min_object_timeout` and `min_presigned_url_timeout`, which were
introduced in TAMS `8.1`. `timerange.json` carries the `8.1` description of the
"eternity" (`_`) and "never" (`()`) forms plus `minLength: 1`.

OpenTAMS serves `min_object_timeout`. It does **not** currently set
`min_presigned_url_timeout`, although it does issue presigned URLs — see
[`docs/conformance.md`](../../docs/conformance.md).

### 6. Documented OpenTAMS behavioural divergences

These change what the contract requires, not just how it is phrased. Each is recorded
as an architecture decision under [`docs/adr/`](../../docs/adr/).

| File | Divergence |
|---|---|
| `flow-segment-bulk-failure.json` | Per-segment `error` is an RFC 9457 Problem Details subset (`type` / `title` / `detail`) and is required, rather than the TAMS `error` type (`type` / `summary` / `time`). See [ADR-0023](../../docs/adr/0023-rfc9457-problem-details-for-per-segment-failures.md). |
| `flow-segment-post.json` | `object_timerange` has no server-computed default. Upstream defaults it to `timerange - ts_offset`; OpenTAMS stores nothing when the client omits it. |
| `flow-storage-post.json` | Exactly one of `limit` or `object_ids` is required, and both are capped at 100 objects per request (`limit` by `maximum`, `object_ids` by `maxItems`). Upstream bounds neither and says the server truncates a `limit` above its own maximum; OpenTAMS rejects with 400 instead, per [REQ-SEC-10](../../docs/requirements.md), so a truncated allocation is never mistaken for a complete one. |

## The OpenTAMS-only file

`flow-segment-post-body.json` has no upstream counterpart. It models the
"single object or array of objects" request body of `POST /flows/{flowId}/segments`,
which upstream expresses inline in the OpenAPI document rather than as a schema file.

## Changing these files

A change here changes the public API. Update `../opentams-api-v1.yaml` if needed, then:

```bash
make api-lint     # redocly lint against redocly.yaml
make api-bundle   # regenerate api/opentams-api-bundled.yaml
make api-gen      # regenerate gen/api/
make api-check    # verify the committed bundle and generated code are up to date
```

`make api-check` is the one that catches drift. The bundled document and the generated
Go types are committed, so they can — and do — fall out of step with the source spec.

`make api-check` passes. The source spec, the committed bundle, and `gen/api/` agree,
and re-running `make api-bundle && make api-gen` reproduces both byte for byte.

The [Contract workflow](../../.github/workflows/contract.yml) runs `make api-check` on
every pull request. It is not part of `make ci`, because it needs Node and the
version-pinned `@redocly/cli`.
