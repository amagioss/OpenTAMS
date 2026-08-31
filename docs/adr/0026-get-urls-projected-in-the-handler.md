---
status: "accepted"
date: 2026-08-31
decision-makers: OpenTAMS maintainers
consulted: OpenTAMS maintainers
informed: OpenTAMS contributors
---

# Project `get_urls` in the handler

## Context and Problem Statement

A Segment on the wire carries `get_urls`: the places a client can fetch the media from. The
database does not hold that answer for every Segment.

There are two kinds. A controlled Segment has no stored URLs, and the server signs them on
demand from the object store. A BYOS Segment has URLs the client supplied, and they are
returned as they were stored, with `controlled: false` stamped on them.

Signing is per request. It needs the object store, the `?presigned` query parameter, and an
expiry. Which layer does it?

## Considered Options

* The service layer, so the projected Segment is complete before it leaves the domain
* The conversion layer, since it already maps domain Segments to wire Segments
* The handler, after conversion has produced the wire shape

## Decision Outcome

Chosen option: "The handler".

`projectSegmentURLs` in `internal/httpx/handlers/segments.go` overwrites `GetUrls` on each
wire Segment after conversion has run. Classification is derived from shape: an empty
`GetURLs` means controlled, a non-empty one means BYOS.

The other two layers were ruled out by constraints they already carry:

* The service layer holds no object store, and keeping it that way is what makes it
  storage-agnostic and testable without one. The same rule gives the GC sole ownership of
  deletion — see [ADR-0020](0020-gc-owns-object-lifetime.md).
* The conversion package is pure by contract. Its package documentation states it does no
  I/O, no logging, and no business logic, and the `conversion-is-pure` depguard rule in
  `.golangci.yml` enforces the import shape. Signing is I/O.

The handler is the only layer that already holds the object store, the request parameters,
and a logger, which is what this step needs.

Failures are per Segment. If signing fails for one Segment, its `get_urls` is left empty
and a warning is logged. The rest of the page is still returned. A signing error does not
take a list endpoint down.

`?presigned=false` short-circuits signing entirely: the object store returns an empty URL
and the HTTPS entry is skipped.

### Consequences

* Good, because the service and conversion layers keep their constraints. Neither gains an
  object-store dependency.
* Good, because a page survives a partial signing failure, which matters most on the list
  endpoints that return many Segments.
* Good, because the `?presigned` filter is applied where the request parameters already
  are.
* Bad, because a Segment is not fully formed until the handler has finished with it. A
  future caller that skips the handler gets Segments with no URLs.
* Bad, because a partial failure is quiet. The client sees an empty `get_urls` and cannot
  tell it apart from a Segment that genuinely has none, and only the server log records
  which happened.
* Bad, because signing cost scales with page size. A large page means one signing call per
  controlled Segment.

## More Information

* The projection, with its rules in the comment above it: [`internal/httpx/handlers/segments.go`](../../internal/httpx/handlers/segments.go).
* Purity contract for the conversion layer: [`internal/httpx/conversion/doc.go`](../../internal/httpx/conversion/doc.go).
* Why the server signs rather than serves: [ADR-0002](0002-server-outside-the-media-path.md).
