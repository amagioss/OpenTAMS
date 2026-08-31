# Functional Design — internal/httpx/conversion

## Purpose

The only place where generated wire types (`gen/api`) and domain types
(`internal/domain`) meet on the `/flows/{flowId}/segments` path. Handlers decode
through it and render through it; the service and the metastore never see a wire
type at all.

Without this boundary every layer ends up importing the generated package, and the
OpenAPI shape becomes load-bearing on the service and the persistence layer. That
tangle is what the package exists to prevent.

## Surface

| Direction | Function |
|---|---|
| wire → domain | `RegisterParamsFromAPI`, `ListParamsFromAPI`, `DeleteParamsFromAPI`, `SegmentFromAPI` |
| domain → wire | `SegmentToAPI`, `SegmentPageToAPI`, `RegisterAcceptedToAPI`, `RegisterFailureToAPI` |

Shape mappers (`*ToAPI`) cannot fail and return a value alone. Decoders
(`*FromAPI`) return `(value, error)`, and every error wraps
`apperror.ErrSchemaValidation` with the offending field named in the detail —
`segments[3]: timerange: …` — so the handler renders a 400 without re-inspecting
the cause. No new sentinel codes are minted here.

## Business rules

**BR-CONV-01 — The package is pure.**
No logger, no metrics, no clock, no goroutines, no I/O, no state. Every function is
a function of its arguments. The `conversion-is-pure` depguard rule in
`.golangci.yml` denies imports of the service, metastore, object store,
idempotency, logger, metrics, `database/sql`, and `net/http`; `SCN-CONV-16` and
`SCN-HTTP-41` are the runtime backstops for the symbol-level half depguard cannot
see.

**BR-CONV-02 — The POST body union is decoded array-first.**
`PostFlowSegmentsJSONRequestBody` is a `oneOf` of `[FlowSegmentPost]` and a bare
`FlowSegmentPost`. `RegisterParamsFromAPI` tries `AsFlowSegmentPostBody1` first and
falls back to `AsFlowSegmentPost`, wrapping a single segment into a one-element
batch so the service has exactly one shape to handle. The union carries no
discriminator field — the shape *is* the discriminator — so try-and-fall-back is
the only available strategy, and the bulk case is fast-pathed because it is the
documented common one.

**BR-CONV-03 — Client-supplied `get_urls` are stamped uncontrolled.**
Every `GetURL` decoded from a POST body gets `Controlled = false` regardless of what
the wire said. The POST schema has no `controlled` field, and a client must not be
able to claim authority over a server-managed URL. Server-managed URLs are never
posted; the handler synthesises them at read time. One writer per direction:
conversion stamps on the way in, the handler projects on the way out.

**BR-CONV-04 — Absent means absent.**
`ObjectTimerange == nil` iff the wire omitted `object_timerange`; the package never
synthesises a value. `TSOffset` round-trips verbatim including negative offsets —
the mappers are shape-only and do not editorialise about the timeline.

**BR-CONV-05 — `limit` is normalised here, not downstream.**
A missing `limit` becomes the server default of 100; a value above 1000 is clamped
to 1000; a value below 1 is rejected as `schema-validation`. The wire's "absent"
and the domain's "default" are exactly the kind of mismatch this layer exists to
absorb. The metastore clamps again at its own boundary, since it must hold for
direct callers too.

**BR-CONV-06 — Status codes are not conversion's business.**
The package produces body pieces — `api.FlowSegment`,
`api.FlowSegmentBulkFailure` — and the handler chooses the status and the
strict-server response variant. Neither re-derives the other's decision.

**BR-CONV-07 — `checkGeneratedInvariant` guards the unreachable.**
oapi-codegen `From*` calls on generated structs cannot fail in practice. Where one
is used, its error is passed to `checkGeneratedInvariant`, which panics with a
documented prefix. This marks a broken generated contract as a bug rather than
letting it surface as a silently empty field.

## Round-trip contract

`SegmentFromAPI(SegmentToAPI(s))` equals `s` except for `FlowID`, which is
path-derived, and `GetURLs`, which the handler projects. Drift between the two
directions would be silent — it shows up as ghost data, not as an error — so the
round-trip is asserted in tests rather than left to review.

## Per-segment failure shape

`RegisterFailureToAPI` maps each `domain.FailedSegment` onto an RFC 9457 subset —
`type`, `title`, `detail` — matching the vocabulary every other error in the API uses.
`Reason` becomes `detail`. `Status` has no slot and is dropped: the enclosing response
is a 200, so a per-entry status would contradict the status line. See
[ADR-0023](../../../adr/0023-rfc9457-problem-details-for-per-segment-failures.md).

## Out of scope

- **`get_urls` projection** — the handler builds reader-facing URLs.
- **Cursor encoding** — minted by the metastore, copied through as an opaque string.
- **Idempotency, auth, logging, metrics** — all upstream of the first call into this
  package.
- **Flow variants** — the package covers segments only. Flow's per-variant `oneOf`
  will follow the same pattern when those handlers move onto this boundary.
