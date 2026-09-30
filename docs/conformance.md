# OpenTAMS Conformance to TAMS v8.0

This document tracks OpenTAMS's conformance to the [BBC TAMS v8.0 specification](https://bbc.github.io/tams/main/index.html), surface by surface. It is the authoritative answer to "is feature X usable today, and if not, when?" for evaluators and operators considering OpenTAMS for production.

The columns:

| Column | Meaning |
|---|---|
| **Status** | One of: `Implemented` (works as the spec defines), `Not implemented` (spec'd but not yet built; tracked for a future release), `Out of scope` (intentionally not in plan) |
| **Notes** | Where it differs from a naive reading of the spec, or where a known limitation lives |

Last reviewed against TAMS v8.0 on 2026-06-09, against OpenTAMS HEAD `38570fc`.

> **HEAD note.** HEAD is supported on every GET-able resource *and* subresource, returning the same headers as GET with no body. For brevity the tables below list HEAD only on collection/item endpoints; every item subresource (`tags`, `tags/{name}`, `description`, `label`, `read_only`, `flow_collection`, `max_bit_rate`, `avg_bit_rate`, and the `/` root, `/service`, `/service/storage-backends`) also answers HEAD.

---

## Service surface

| Endpoint | Status | Notes |
|---|---|---|
| `GET /tams/v1/` | Implemented | Returns service metadata (name, API version, supported formats, link header for paginatable resources). |
| `GET /tams/v1/service` | Implemented | Service info for capability discovery. Advertises `min_object_timeout` (from `OBJECT_STORE_PRESIGN_EXPIRY`). Does **not** advertise `min_presigned_url_timeout`, although OpenTAMS does issue presigned URLs — both fields are TAMS 8.1 additions carried into the vendored 8.0 schemas (see [`api/schemas/README.md`](../api/schemas/README.md)). |
| `POST /tams/v1/service` | Not implemented | Spec allows updating service metadata. OpenTAMS serves service metadata as read-only today. |
| `GET /tams/v1/service/storage-backends` | Implemented | Lists configured storage backends; OpenTAMS exposes one (the configured S3-compatible bucket) per deployment. Multi-backend tenancy is Out of scope for v0.1.0. |
| `GET /tams/v1/service/webhooks` | Not implemented | Webhook registration list/create. See [Real-time / streaming](#real-time--streaming). |
| `HEAD /tams/v1/service/webhooks` | Not implemented | As above. |
| `POST /tams/v1/service/webhooks` | Not implemented | Register a webhook. As above. |
| `GET /tams/v1/service/webhooks/{webhookId}` | Not implemented | Per-webhook read. As above. |
| `HEAD /tams/v1/service/webhooks/{webhookId}` | Not implemented | As above. |
| `PUT /tams/v1/service/webhooks/{webhookId}` | Not implemented | Replace a webhook registration. As above. |
| `DELETE /tams/v1/service/webhooks/{webhookId}` | Not implemented | Delete a webhook registration. As above. |

## Sources

| Endpoint | Status | Notes |
|---|---|---|
| `GET /tams/v1/sources` | Implemented | Filtering by `format`, `label`, `tag.{name}`, `tag_exists.{name}` query params; cursor pagination via `page` + `limit` query params + `X-Paging-NextKey` response header (mirrored as a non-canonical RFC 8288 `Link: <>; rel="next"; key="<cursor>"` for clients that prefer the standard header — see [pagination cursor format](#pagination-cursor-format) below). |
| `HEAD /tams/v1/sources` | Implemented | Returns the same `Link` header as `GET` for cursor-only callers. |
| `GET /tams/v1/sources/{sourceId}` | Implemented | |
| `HEAD /tams/v1/sources/{sourceId}` | Implemented | |
| `GET /tams/v1/sources/{sourceId}/tags` | Implemented | |
| `GET /tams/v1/sources/{sourceId}/tags/{name}` | Implemented | |
| `PUT /tams/v1/sources/{sourceId}/tags/{name}` | Implemented | |
| `DELETE /tams/v1/sources/{sourceId}/tags/{name}` | Implemented | |
| `GET /tams/v1/sources/{sourceId}/description` | Implemented | |
| `PUT /tams/v1/sources/{sourceId}/description` | Implemented | |
| `DELETE /tams/v1/sources/{sourceId}/description` | Implemented | |
| `GET /tams/v1/sources/{sourceId}/label` | Implemented | |
| `PUT /tams/v1/sources/{sourceId}/label` | Implemented | |
| `DELETE /tams/v1/sources/{sourceId}/label` | Implemented | |

> **Source lifecycle.** Per spec, Sources have no explicit create/delete endpoints. A Source is created implicitly by `PUT /flows/{flowId}` referencing it, and deleted implicitly when the last Flow referencing it is removed. OpenTAMS follows this model — there is intentionally no `POST /sources` or `DELETE /sources/{sourceId}`.

## Flows

| Endpoint | Status | Notes |
|---|---|---|
| `GET /tams/v1/flows` | Implemented | Filterable by `source_id`, `timerange`, `format`, `codec`, `label`, `tag.{name}`, `tag_exists.{name}`, `frame_width`, `frame_height`. Pagination as Sources. (No `mime_type` filter — the spec uses `codec`.) **`timerange` matches a flow if one of its segments overlaps the range, so a range inside a gap between segments does not match.** TAMS does not define "Flows that overlap". An empty `timerange` returns only flows with no segments, as TAMS says for this parameter ("An empty timerange returns Flows with no content"). Today OpenTAMS ignores an empty `timerange` and returns every flow (pending, ADR-0039). |
| `HEAD /tams/v1/flows` | Implemented | Same filters as `GET`, including the empty-`timerange` rule. The upstream text for `HEAD /flows` omits that rule. OpenTAMS applies it, because HEAD returns the headers of GET. |
| `GET /tams/v1/flows/{flowId}` | Implemented, with a divergence | Supports `include_timerange` and `timerange` query params. **A `timerange` that does not parse returns 400 `invalid-timerange`. The upstream spec declares no 400 for this endpoint** (pending, BR-CONV-09). Before this change, the server ignored the parameter and returned the flow unfiltered. |
| `HEAD /tams/v1/flows/{flowId}` | Implemented | |
| `PUT /tams/v1/flows/{flowId}` | Implemented | Create or replace. The Flow's `id` in the body must match the path. |
| `DELETE /tams/v1/flows/{flowId}` | Implemented | Soft-delete: creates a `flow-delete-request`. Synchronous deletion of metadata; underlying object reaping happens via M16 GC (deferred). |
| `GET /tams/v1/flows/{flowId}/tags` | Implemented | |
| `GET /tams/v1/flows/{flowId}/tags/{name}` | Implemented | |
| `PUT /tams/v1/flows/{flowId}/tags/{name}` | Implemented | |
| `DELETE /tams/v1/flows/{flowId}/tags/{name}` | Implemented | |
| `GET /tams/v1/flows/{flowId}/description` | Implemented | |
| `PUT /tams/v1/flows/{flowId}/description` | Implemented | |
| `DELETE /tams/v1/flows/{flowId}/description` | Implemented | |
| `GET /tams/v1/flows/{flowId}/label` | Implemented | |
| `PUT /tams/v1/flows/{flowId}/label` | Implemented | |
| `DELETE /tams/v1/flows/{flowId}/label` | Implemented | |
| `GET /tams/v1/flows/{flowId}/read_only` | Implemented | |
| `PUT /tams/v1/flows/{flowId}/read_only` | Implemented | Setting `true` makes the Flow's segment list immutable; further `POST /segments` returns 409. |
| `GET /tams/v1/flows/{flowId}/flow_collection` | Implemented | |
| `PUT /tams/v1/flows/{flowId}/flow_collection` | Implemented | |
| `DELETE /tams/v1/flows/{flowId}/flow_collection` | Implemented | |
| `GET /tams/v1/flows/{flowId}/max_bit_rate` | Implemented | |
| `PUT /tams/v1/flows/{flowId}/max_bit_rate` | Implemented | |
| `DELETE /tams/v1/flows/{flowId}/max_bit_rate` | Implemented | |
| `GET /tams/v1/flows/{flowId}/avg_bit_rate` | Implemented | |
| `PUT /tams/v1/flows/{flowId}/avg_bit_rate` | Implemented | |
| `DELETE /tams/v1/flows/{flowId}/avg_bit_rate` | Implemented | |

## Segments

| Endpoint | Status | Notes |
|---|---|---|
| `GET /tams/v1/flows/{flowId}/segments` | Implemented, with a divergence | Query params: `timerange` (intersection filter), `object_id`, `reverse_order`, `verbose_storage`, `accept_get_urls`, `accept_storage_ids`, `presigned`, `include_object_timerange`, plus `page`/`limit` pagination. **On an unknown flow OpenTAMS returns 404, where the spec asks for an empty list** (REQ-BEH-16) — see below. `Accept: text/event-stream` for streamed reads is **Not implemented**. |
| `HEAD /tams/v1/flows/{flowId}/segments` | Implemented | |
| `POST /tams/v1/flows/{flowId}/segments` | Implemented, with a divergence | Requires `X-Idempotency-Key` (REQ-IDEM-01). Body is a single segment or a JSON array of up to 1000. Per-segment failures come back as `flow-segment-bulk-failure`. **Overlapping timeranges reject the whole batch with 422 and no `failed_segments`, rather than the spec's first-wins ordering** (REQ-BEH-15). See [Deliberate divergences on this path](#deliberate-divergences-on-this-path). **A segment timerange must be bounded, non-empty, and start-inclusive, or the segment is a per-segment `invalid-timerange` failure** (pending, ADR-0040). See [Deliberate divergences on this path](#deliberate-divergences-on-this-path). |
| `DELETE /tams/v1/flows/{flowId}/segments` | Implemented, with a known defect | Bulk delete by query params (`timerange`, `object_id`). Returns the count, and creates a `flow-delete-request` if the operation is asynchronous. **The spec deletes only segments completely covered by `timerange`. Today OpenTAMS deletes every segment that intersects it, including boundary segments.** The fix is pending (ADR-0039). |

### Deliberate divergences on this path

**Overlapping segments reject the whole batch.** TAMS asks an implementation to accept
the segments it can and report the rest per entry. OpenTAMS refuses the entire request
with a 422 `segment-overlap` ProblemDetails and persists nothing — within-batch and
against-existing overlaps alike. An overlap is a caller bug, and first-wins ordering
hides it behind a partially-applied write whose outcome depends on array order. The
rationale and the rejected alternatives are in
[ADR-0024](adr/0024-whole-batch-reject-on-segment-overlap.md).

*What breaks for a conformant client:* one that posts an overlapping batch and expects
`failed_segments` gets a 422 with none, and none of its segments land. Retry with a
corrected batch.

**`GET` on an unknown flow returns 404.** The spec asks for an empty list. OpenTAMS
distinguishes "this flow has no segments" from "this flow does not exist", because
silently returning `[]` for a mistyped flow id turns a client bug into a plausible
empty result.

*What breaks for a conformant client:* one that treats 404 as fatal must instead read it
as "no such flow". A client that polls for segments on a flow it created will never see
this.

**Per-segment failure entries carry an RFC 9457 subset, not the TAMS `error` object.**
Each `failed_segments[].error` is `type`/`title`/`detail` — the same vocabulary as every
other error in the API — rather than the upstream `type`/`summary`/`time`. Recorded in
[ADR-0023](adr/0023-rfc9457-problem-details-for-per-segment-failures.md).

*What breaks for a conformant client:* one written against the upstream TAMS schema looks
for `error.summary` and finds nothing. Because the field is required, that surfaces as a
missing-key error rather than an empty string. `error.time` and the optional
`error.traceback` are also gone; OpenTAMS never populated either meaningfully.

**Segment timeranges must be bounded, non-empty, and start-inclusive** (pending,
[ADR-0040](adr/0040-segment-timerange-invariants.md)). The segment schema accepts any
`TimeRange`. TAMS App Note 0012 describes a segment timerange as starting, inclusively, at
the first sample and ending at the last sample, and says that "samples don't generally
exist into infinity". OpenTAMS makes that description a rule. A segment timerange can be empty, have no start
or no end, or have an exclusive start. OpenTAMS reports such a segment in
`failed_segments` with type `invalid-timerange`, and processes the other segments in the
batch. OpenTAMS accepts an inclusive end, so `[0:0]` is valid.

*What breaks for a conformant client:* a client can register an open-ended segment such
as `[5:0_`, or a segment with an exclusive start such as `(0:0_10:0)`. That client gets
the segment back in `failed_segments`. Send a bounded range with an inclusive start.

**Objects are not reclaimed on delete.** `DELETE /segments` removes metadata and
decrements the object ref-count inside its transaction; deleting the bytes is the
garbage collector's job, and the GC is not built. See the note on garbage collection
below.

## Storage allocation

| Endpoint | Status | Notes |
|---|---|---|
| `POST /tams/v1/flows/{flowId}/storage` | Implemented | Two modes: `{"limit": N}` (server picks `object_id`s) and `{"object_ids": [...]}` (client picks; idempotent across retries). The two are mutually exclusive — supplying both is 400. Both modes are capped at 100 objects per request; above that the request is rejected with 400 rather than truncated, so ask again for the rest. `limit` below 1 is also 400. Presigned URL TTL is `OBJECT_STORE_PRESIGN_EXPIRY` (default 1h). |

## Media objects

| Endpoint | Status | Notes |
|---|---|---|
| `GET /tams/v1/objects/{objectId}` | Not implemented | Media Object resource. GET returns the Flows that reference the object (`referenced_by_flows` reverse lookup), filterable via `flow_tag.{name}` / `flow_tag_exists.{name}` query params. Object metadata is currently reachable only via segments; the object-centric reverse lookup is not yet built. |
| `HEAD /tams/v1/objects/{objectId}` | Not implemented | As above. |
| `POST /tams/v1/objects/{objectId}/instances` | Not implemented | Register a Media Object instance (controlled instance, or add an uncontrolled URL). |
| `DELETE /tams/v1/objects/{objectId}/instances` | Not implemented | Delete a Media Object instance (by `storage_id` or `label`). |

## Flow delete requests

| Endpoint | Status | Notes |
|---|---|---|
| `GET /tams/v1/flow-delete-requests` | Not implemented | Endpoint is routed and returns 200 with an empty list, but the underlying lifecycle (request → in-progress → done) is part of M16 GC, which is not yet built. Today, `DELETE /flows/{flowId}` synchronously removes metadata and the `flow-delete-request` row is bookkeeping only. |
| `HEAD /tams/v1/flow-delete-requests` | Not implemented | As above. |
| `GET /tams/v1/flow-delete-requests/{requestId}` | Not implemented | Returns 404 because no records are ever created. The endpoint shape is correct so client code that handles 404 will work unchanged once GC lands. |
| `HEAD /tams/v1/flow-delete-requests/{requestId}` | Not implemented | As above. |

## Health and operations

| Endpoint | Status | Notes |
|---|---|---|
| `GET /healthz` | Implemented | Liveness probe; never reaches storage, returns 200 if the process is up. |
| `GET /readyz` | Implemented | Readiness probe; checks DB connectivity, S3 reachability, and JWKS warm state. Returns a bare 200 or 503 with **no body** — use `/health/details` when you need to know which dependency is failing. |
| `GET /health/details` | Implemented | Verbose readiness response with per-dependency latency and status. Surface for operator dashboards; not for automated pollers. |
| `GET /metrics` | Implemented | Prometheus exposition. See [`docs/configuration.md`](configuration.md) for the metric naming conventions (community-portable `http_*` names; OpenTAMS-prefixed `opentams_*` for service-internal metrics). |

## Authentication

| Concern | Status | Notes |
|---|---|---|
| Bearer JWT, external OIDC issuer | Implemented | Configured via `AUTH_EXTERNAL_ISSUER_URL` + `AUTH_EXTERNAL_AUDIENCE`. JWKS rotation handled automatically by `pkg/jwtauth`. |
| Bearer JWT, optional internal OIDC issuer | Implemented | Configured via `AUTH_INTERNAL_*`. Useful for service-to-service traffic from the same trust domain (e.g. Kubernetes ServiceAccount tokens). |
| URL token auth (`url_token_auth`, `access_token` in query string) | Not implemented | Spec `securityScheme` for presigned-style URL access. OpenTAMS does not accept query-string tokens today. |
| HTTP Basic auth (`basic_auth`) | Not implemented | Spec `securityScheme`. OpenTAMS does not accept HTTP Basic credentials today. |
| Dev-mode auth (any token accepted) | Implemented | OpenTAMS-specific, not a TAMS `securityScheme`. Auto-selected when `APP_ENV=development` and external issuer is unconfigured. **Never** reaches production code paths — the production config-load path errors out without external auth configured. |
| Authorization (RBAC, scope mapping) | Out of scope | TAMS v8.0 does not specify authorisation; OpenTAMS authenticates only. Layer your own policy via a sidecar / mesh / API-gateway. |

## Real-time / streaming

| Concern | Status | Notes |
|---|---|---|
| Webhook registration (`/service/webhooks`, `/service/webhooks/{webhookId}`) | Not implemented | Spec nests webhooks under `/service/`. Registration config schema is `api/schemas/webhook.json` (`event_type` enum + delivery filters such as `accept_get_urls`, `accept_storage_ids`, `presigned`, `verbose_storage`). JSON schemas exist under `api/schemas/webhook-*.json` for forward compatibility, but the v1 OpenAPI spec (`api/opentams-api-v1.yaml`) deliberately excludes webhook paths — its header reads "This file covers Phase 1 scope only. Phase 2 features (webhooks, …)". No webhook routes are registered on the server. Clients that need notifications should poll segments by `timerange` until this lands. |
| Webhook event delivery (outbound payloads) | Not implemented | Spec defines 8 outbound event types in the OpenAPI top-level `webhooks:` section: `flows/created`, `flows/updated`, `flows/deleted`, `flows/segments_added`, `flows/segments_deleted`, `sources/created`, `sources/updated`, `sources/deleted`. `flows/segments_added` payloads additionally carry `get_urls`, gated by the registration's `accept_get_urls` / `accept_storage_ids` / `presigned` / `verbose_storage` options. None are emitted today. |
| `text/event-stream` for streamed segment reads | Not implemented | The OpenAPI spec calls it out; OpenTAMS supports the polling JSON path only today. |
| Live ingest hints (segment chunking, ts_offset semantics) | Implemented | The data shape is honoured; OpenTAMS does not enforce live-vs-archive distinctions, treats both uniformly. |

## Time-range format

> The rules in this section follow [ADR-0038](adr/0038-tams-timestamp-and-timerange-format.md)
> and [ADR-0039](adr/0039-timerange-stored-as-client-string-with-half-open-bounds.md).
> These ADRs are proposed and not implemented yet. The list at the end of this section
> gives the current differences.

OpenTAMS accepts the TAMS timestamp and time-range format from `timestamp.json`,
`timerange.json`, and App Note 0008:

- Timestamps are `{sign?}{seconds}:{nanoseconds}`. The sign applies to the whole value:
  `-1:500000000` is −1.5 s, and `-0:500000000` is −0.5 s.
- The parser accepts `[start_end)`, `[start_end]`, `(start_end)`, and `(start_end]`.
- The parser accepts one-sided ranges. `(5:0_` has no end, and `_10:0)` has no start.
  The parser ignores a marker next to an omitted timestamp.
- A missing marker next to a timestamp means inclusive: `0:0_10:0` is `[0:0_10:0]`. App
  Note 0008 does not say this. OpenTAMS follows the BBC `mediatimestamp` library.
- The parser accepts instantaneous ranges: `[10:0]` and `10:0`.
- `_` is eternity. `()` is empty. A range whose end is before its start, or whose equal
  ends have an exclusive marker, is also empty.
- An instant is a whole nanosecond. A range that contains no whole nanosecond, such as
  `(0:0_0:1)`, is empty. The BBC `mediatimestamp` library treats time as continuous and
  calls that range non-empty. The difference applies only to exclusive ranges narrower
  than two nanoseconds.

In a query, an empty range matches nothing: `GET` segments returns an empty list,
`DELETE` deletes nothing, and `GET /flows?timerange=` returns only flows with no segments.
The `GET /flows` rule comes from the `timerange` query parameter of that endpoint in the
upstream `TimeAddressableMediaStore.yaml`: "An empty timerange returns Flows with no
content".
For a segment timerange, see the rule under [Segments](#segments).

OpenTAMS returns every time string that a client sends (`timerange`, `ts_offset`,
`object_timerange`, `last_duration`) exactly as sent. Values the server derives, such as a flow's
`timerange` and `X-Paging-Timerange`, use a canonical form with `[ts]` for an
instantaneous range. OpenTAMS compares timeranges at nanosecond resolution on the TAI
timeline from 1677-09-21T00:12:43.145224192 to 2262-04-11T23:47:16.854775807, which is
the `int64` nanosecond range (`-9223372036:854775808` to `9223372036:854775807`).
OpenTAMS rejects a bound outside that span with
`invalid-timerange`.

**Current differences, which ADR-0038 and ADR-0039 fix:**

- The parser reads a negative timestamp with non-zero nanoseconds with the wrong value
  (`-1:500000000` as −0.5 s). It rejects `-0:x`.
- The parser rejects one-sided ranges, a bare timestamp, a marker on an omitted
  timestamp, and an end before the start, with 400.
- The store ignores the markers when it stores a segment timerange. As a result, `[0:0]`
  fails with 500, and the store accepts some overlapping segments.
- The server returns a re-rendered string. A client that sends `[10:0]` reads back
  `[10:0_10:0]`.

See `internal/timerange/timerange.go` for the parser and
`internal/timerange/timerange_test.go` for the accepted and rejected shapes.

## Tag names

Tag names are free-form strings and travel in the path: `/flows/{flowId}/tags/{name}`.
Percent-encode any character that is not safe in a path segment.

One known defect: **a tag name containing a literal `%` cannot be addressed**. The router
percent-decodes the path segment and the parameter binder then decodes it a second time,
so `%25` arrives as `%`, fails to parse as an escape, and the request is rejected with
`400`. By the same double decode, a name containing a literal `%20` is folded to a space.
Names are otherwise unrestricted, and a literal `+` is preserved.

## Pagination cursor format

OpenTAMS surfaces pagination through two parallel response headers:

- **`X-Paging-NextKey`** — opaque cursor string. Pass it back as the `page` query parameter on the next request (the spec's canonical cursor param name).
- **`Link`** — a non-canonical RFC 8288 form: `<>; rel="next"; key="<cursor>"`. The URI slot is empty (`<>`) and the cursor lives in the `key=` parameter. Real RFC 8288 puts the next URL inside the angle brackets; OpenTAMS uses the non-canonical shape so the same pagination model works for cursor-based callers without rebuilding the full URL on the server.

Both headers carry the same cursor. Use whichever your client library prefers. Companion headers `X-Paging-Limit`, `X-Paging-Count`, and `X-Paging-ReverseOrder` describe the page itself.

`X-Paging-NextKey` and `Link` are **absent** on the last page — their presence is the signal that another page exists. Test for presence, not for an empty value. The companion headers behave the other way: `X-Paging-Limit`, `X-Paging-Count`, `X-Paging-Timerange`, and `X-Paging-Reverse-Order` are always sent, including at zero values, so a client never has to distinguish "absent" from "zero".

## JSON response encoding

Response bodies are encoded with Go's `encoding/json` defaults, which **HTML-escape** the characters `&`, `<`, and `>` as the unicode escapes `\u0026`, `\u003c`, and `\u003e`. This is most visible in presigned object-store URLs, whose signed query strings are full of `&` — they come back as `...X-Amz-Date=...\u0026X-Amz-Expires=...`.

This is **valid JSON**. Every conforming parser decodes the escapes back to `&`/`<`/`>` transparently — browsers, `jq`, Postman's *Pretty* view, any SDK, and `tamsctl -o yaml`/`-o table`. It only surprises consumers that read the **raw bytes**: `curl` output piped to a file, or copy-pasting from a *Raw* response view, where a literal `\u0026` is not directly usable in a shell.

To get a usable URL from raw output, pipe through a JSON parser rather than copying the escaped string:

```bash
curl -s -H "Authorization: Bearer <token>" "<endpoint>/flows/<id>/segments?...presigned=true" \
  | jq -r '.[0].get_urls[0].url'
```

`tamsctl -o json` decodes the escapes for you, so its output is copy-paste ready.

The escaping originates in the oapi-codegen strict-server response encoders. It is intentionally left as-is — see the maintainer note in [`docs/development/codebase.md`](development/codebase.md) for why we don't change the server.

## Metadata schemas

| Schema (under `api/schemas/`) | Status | Notes |
|---|---|---|
| `flow-core.json` (common Flow fields) | Implemented | |
| `flow-{video,audio,data,image,multi}.json` (per-format specialisations) | Implemented | All five format variants validated by oapi-codegen-generated request types. |
| `flow-collection.json` | Implemented | Multi-flow grouping for synchronised playback. |
| `flow-segment.json`, `flow-segment-post-body.json`, `flow-segment-post.json` | Implemented | |
| `flow-segment-bulk-failure.json` | Implemented | Returned by `POST /segments` when partial failure occurs. |
| `flow-storage-post.json`, `flow-storage.json` | Implemented | Storage-allocation request and response shapes. |
| `source.json`, `object-core.json`, `object.json` | Implemented | |
| `tags.json`, `timerange.json`, `timestamp.json` | Implemented | |
| `error.json` | Implemented, partially populated | Every 4xx/5xx response carries the correct status and `Content-Type: application/problem+json`. The body is filled in on the segments paths only; other handlers return a bare `{}` — see [Error bodies](#error-bodies) below. Note this is the OpenTAMS `ProblemDetails` mapping; the upstream TAMS `error` object itself is used by no response OpenTAMS serves. |
| `webhook-*.json` | Not implemented | Schemas present in spec; handlers stubbed (see Real-time / streaming above). |
| `mime-type.json`, `content-format.json`, `container-mapping.json` | Implemented | Validation only; OpenTAMS does not interpret or transcode based on these. MXF container-mapping pass-through — the field is stored and returned, but no MXF-specific business rules are enforced today (Phase 2). |
| `service.json`, `service-post.json`, `storage-backend.json`, `storage-backends-list.json` | Implemented | |
| `event-stream-common.json` | Not implemented | Used only by `text/event-stream` paths, none of which are built yet. |
| `http-request.json` | Implemented | The HTTP-action descriptor type used by `flow-storage.json` for the `put_url` field (and by Phase 2 webhook delivery records once that surface lands). Carries the URL, method, and any required headers/body for a client-side HTTP action. |
| `deletion-request.json` | Not implemented | Schema exists; the underlying garbage-collection lifecycle is not built. |

## Error bodies

**Most error responses are an empty problem document.** OpenTAMS returns the correct HTTP
status and `Content-Type: application/problem+json` on every error, but only the segments
handlers populate the body. The other 123 error sites — `internal/httpx/handlers/flows.go`
(77), `sources.go` (38), `storage.go` (4), `unimplemented.go` (2) — serialise as `{}`,
with no `type`, `title`, `status`, `detail`, `instance`, or `request_id`.

*What breaks for a conformant client:* status-code handling works everywhere. Anything
that reads the body — matching on the `type` URI, showing `detail` to a user, correlating
by `request_id` — works on `/flows/{flowId}/segments` and gets nothing elsewhere. A client
should treat the problem document as best-effort and fall back to the status code.

This is a gap, not a deliberate divergence: the `ProblemDetails` schema, the type
catalogue in `internal/apperror`, and the middleware that renders them all exist and are
used by the segments path. The remaining handlers were written against the generated
empty-response types and never updated.

**A malformed path UUID is answered three different ways.** There is no single rule:

| Path | Verb | Response |
|---|---|---|
| `/sources/{sourceId}` | all | 404, empty body |
| `/flows/{flowId}` | GET, HEAD, DELETE | 404, empty body |
| `/flows/{flowId}` | PUT | 400, empty body |
| `/flows/{flowId}/segments` | GET | 400 `invalid-uuid`, populated body |
| `/flows/{flowId}/segments` | HEAD | 400, empty body |
| `/flows/{flowId}/segments` | POST, DELETE | 404, populated body |

*What breaks for a conformant client:* a client that sends a mistyped id cannot infer
from the status whether the id was malformed or the resource was absent, and the answer
changes per endpoint. Treat 400 and 404 on these paths as equally likely for a bad id,
and validate ids client-side rather than relying on the distinction.

Five catalogued type URIs are likewise never emitted by any handler:
`storage-mode-conflict`, `not-acceptable`, `request-too-large`, `rate-limited`, and
`dependency-unavailable`. The conditions they describe either cannot arise (no rate
limiter, no content negotiation) or surface as a generic 500.

## Scope and what is not yet built

OpenTAMS targets the full TAMS v8.0 surface. Two distinct kinds of "not done" appear above:

- **`Not implemented`** — spec features we intend to build but haven't yet. Today this covers media objects (`/objects/{objectId}`, `/objects/{objectId}/instances`), webhook registration and event delivery (`/service/webhooks*`), `POST /service`, `text/event-stream` streamed reads, the flow-delete-request lifecycle (which depends on garbage collection), and the `url_token_auth` / `basic_auth` security schemes.
- **`Out of scope`** — spec features we believe are genuinely outside what TAMS asks of an implementer, not "we didn't get to it" (e.g. authorisation/RBAC, multi-backend tenancy).

Two operational capabilities are also absent, and neither is a TAMS surface, so neither
appears in the tables above:

- **Garbage collection.** `opentams gc` exits with an error and `GC_*` configuration is
  ignored. Objects orphaned by partial failures accumulate until deleted by hand. Do not
  substitute an S3 lifecycle policy — it cannot see cross-flow references and will delete
  live data.
- **Rate limiting and request body-size limits.** OpenTAMS has no rate limiter, returns
  no 429, and sets no `Retry-After`. Put a limiter in front of the service (ingress
  controller or API gateway) if you need one.

If you find a row marked `Out of scope` that you think OpenTAMS should support, please open an issue: the dialogue is the contribution.

## Reporting a deviation

If OpenTAMS misbehaves against the TAMS v8.0 specification — wrong status code, wrong field shape, missing header, etc. — file it as a bug with the **TAMS v8.0 spec section number** and the request/response shape that demonstrates the deviation. The bug template ([`.github/ISSUE_TEMPLATE/bug_report.yml`](../.github/ISSUE_TEMPLATE/bug_report.yml)) prompts for both. Spec deviations are the highest-priority class of bug we accept.
