---
status: "accepted"
date: 2026-08-25
decision-makers: OpenTAMS maintainers
consulted: OpenTAMS maintainers
informed: OpenTAMS contributors
---

# Report per-segment failures as an RFC 9457 Problem Details subset

> **Status: accepted — every error OpenTAMS serves now uses RFC 9457 vocabulary.**
> Every HTTP error response is `application/problem+json` carrying `ProblemDetails` (14
> call sites; the content type is set in `internal/httpx/middleware/middleware.go`). The
> source schema `api/schemas/flow-segment-bulk-failure.json` was converted to the same
> vocabulary in commit `2599d2f` on 2026-05-14, and `internal/domain.FailedSegment`
> already carried `Type`, `Title`, and `Reason`.
>
> The toolchain tail landed on 2026-08-27: `api/opentams-api-bundled.yaml` was re-bundled,
> `gen/api/` regenerated, and `RegisterFailureToAPI` in
> `internal/httpx/conversion/segment.go` rewritten to populate `type` / `title` / `detail`
> directly. `api.Error` is now referenced by no non-test code in the repository, and
> `make api-check` reports the contract, bundle, and generated code in agreement.
>
> The TAMS `error` schema (`api/schemas/error.json`) survives in the spec, referenced by
> `deletion-request.json` and `webhook-get.json`. Both are surfaces OpenTAMS does not
> serve — webhooks are unrouted and `flow-delete-requests` never creates a record — so no
> response carries that shape. They are left as the TAMS specification defines them.

## Context and Problem Statement

OpenTAMS returns every HTTP-level error as RFC 9457 `application/problem+json`:
`type`, `title`, `detail`, `status`, `instance`, plus a `request_id`. `type` is a stable
URI from a documented catalogue, and clients are told to branch on it rather than on
`status` or `detail` text.

`POST /flows/{flowId}/segments` has one reporting channel that sits outside that. When a
batch partially succeeds the response is **200, not an error response at all** — but its
body carries a `failed_segments` array, and each entry describes why that one Segment
failed. The upstream TAMS `flow-segment-bulk-failure` schema types that entry as the TAMS
`error` object: `type`, `summary`, `time`, optional `traceback`.

So this is not a case of the API having two competing error contracts. It has one, and
one leftover field that predates it. The consequence for a client is still real: the same
class of failure arrives in a different shape depending on whether it was the only
failure (4xx `ProblemDetails`) or one of several (200 `failed_segments[].error`), and the
`type` values in the two shapes are drawn from different vocabularies.

## Considered Options

The choice between these was settled API-wide when the problem-type catalogue was
adopted; they are recorded for provenance, not as a live menu.

* Embed an RFC 9457 subset (`type`, `title`, `detail`) in each failed-segment entry
* Keep the TAMS `error` object (`type`, `summary`, `time`) as specified
* Embed the full Problem Details object, including `status`, `instance`, and `request_id`
* Return only a `type` URI per entry and require a second request for the detail

## Decision Outcome

Chosen option: "embed an RFC 9457 subset", to bring the one remaining site onto the
vocabulary the rest of the API already uses, at the cost of one schema divergence from
upstream TAMS.

Each `failed_segments` entry carries a required `error` object with exactly `type`,
`title`, and `detail`, all required. `type` is a URI from the same problem catalogue the
top-level Problem Details responses use, and `title` matches that catalogue's title for
the type.

The parent-level fields are deliberately **not** repeated per entry:

* `status` — the HTTP status is 200; a per-entry status would be inventing one.
* `instance` and `request_id` — properties of the request, identical for every entry.

### Consequences

* Good, because a client writes one error handler. The `type` URI it already matches on
  for 4xx responses is the same URI it matches on inside `failed_segments`.
* Good, because `title` and `detail` separate the stable human-readable label from the
  per-instance explanation, which the TAMS `error` object's single `summary` field
  conflates.
* Good, because dropping `time` removes a field the server was never populating
  meaningfully. It was being marshalled as the zero time.
* Good, because it retires `api.Error` entirely. No non-test code references it, and it
  has no other reason to exist in the generated package.
* Bad, because it diverges from the TAMS `flow-segment-bulk-failure` schema, so
  `api/schemas/flow-segment-bulk-failure.json` is a modified copy of the upstream file
  rather than a mirror — see [ADR-0005](0005-vendored-tams-schemas-are-modified.md).
* Bad, because a TAMS client written against the upstream schema will look for
  `error.summary` and find nothing. The field is required in the response, so the failure
  is a missing-key error rather than a silent empty string.
* Bad, because `traceback` is gone. It was optional and OpenTAMS never emitted it, but a
  client that read it defensively now has one less place to look.

## More Information

The tail landed on 2026-08-27 in a single change, kept separate from unrelated work
because it alters the wire format clients receive on a 200 partial success:

1. `make api-bundle` — re-bundled; `api/opentams-api-bundled.yaml` moved by 93 lines,
   which also carried across the whole-batch-overlap and 201/200/400/422 status
   corrections that had been sitting in the source spec unbundled.
2. `make api-gen` — regenerated; `gen/api/opentams.gen.go` moved by 735 lines.
3. `failedEntry` and `RegisterFailureToAPI` rewritten in
   `internal/httpx/conversion/segment.go`. `domain.FailedSegment` already carried every
   field needed, so no domain or service change was involved. The Title-into-Type
   fallback the old mapping needed — `api.Error` had no `title` slot — was deleted.
4. Five test assertions updated: `internal/httpx/conversion/segment_test.go` (three,
   including the JSON snapshot) and `internal/httpx/handlers/segments_test.go` (two).
   Two `require.NotNil` checks on `Error` were dropped; it is a value, not a pointer.

`make api-check` now reports the contract, bundle, and generated code in agreement; it
was failing on both artifacts before. Running step 1 alone leaves `gen/` stale; running
steps 1–2 without step 3 breaks the build in `conversion`. See
[`api/schemas/README.md`](../../api/schemas/README.md).

`domain.FailedSegment.Status` — 400 for a validation failure, 422 for an overlap — has no
slot in the emitted entry and is dropped, consistent with the parent-level reasoning
above. A client distinguishes failure kinds by the `type` URI.
