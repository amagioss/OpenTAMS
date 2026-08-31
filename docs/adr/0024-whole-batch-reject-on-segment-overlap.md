---
status: "accepted"
date: 2026-08-25
decision-makers: OpenTAMS maintainers
consulted: OpenTAMS maintainers
informed: OpenTAMS contributors
---

# Reject the whole batch when Flow Segments overlap

## Context and Problem Statement

`POST /flows/{flowId}/segments` accepts a single Segment or an array of up to 1000.
Segments on a Flow must not have overlapping timeranges, and a batch can violate that
in two ways: a submitted Segment can overlap one already registered on the Flow, or two
Segments within the same array can overlap each other.

The TAMS specification, and OpenTAMS's own [REQ-BEH-15](../requirements.md), originally
specified first-wins ordering for the within-batch case: process the array in order,
accept the earlier Segment, report the later one in the `failed_segments` array of a
200 partial-success response.

What should the server do when a batch contains an overlap?

## Considered Options

* Whole-batch reject — any overlap from either source rejects the entire batch with 422
* First-wins within batch, as specified — accept the earlier Segment, report the later in a 200 partial-success body
* Split the two cases — first-wins for within-batch overlap, 422 for against-existing overlap
* Accept the non-overlapping subset and let the client reconcile from the response

## Decision Outcome

Chosen option: "whole-batch reject", because an overlapping batch is a defect in the
caller, and every partial-success option quietly hides it.

Any overlap — within-batch or against-existing — rejects the entire batch with 422
`segment-overlap`. No Segments are persisted. The response is RFC 9457 Problem Details
citing the first detected colliding pair; there is no `failed_segments` array on the
422 path.

The 200 partial-success path still exists, but only for **non-overlap** per-segment
failures such as a missing field or an unparseable timerange. Those are per-item data
errors where the surviving items are still individually correct.

This is a deliberate divergence from the TAMS specification. It changes what a client
observes, so it is documented in the API contract description for the endpoint, in
[`docs/conformance.md`](../conformance.md), and in REQ-BEH-15 itself.

### Consequences

* Good, because a client with an internally inconsistent batch learns about it. Under
  first-wins the client sees a 200, and the dropped Segment is a line in an array it may
  not inspect — the bug ships.
* Good, because the outcome is atomic: after a 422 the Flow is exactly as it was, so a
  retry of the corrected batch needs no reconciliation of what did and did not land.
* Good, because one rule covers both overlap sources. First-wins only ever addressed the
  within-batch case, so a split rule would have made the response shape depend on which
  kind of overlap the server happened to detect first.
* Bad, because a 1000-Segment batch with one bad Segment must be resubmitted in full.
  Ingest clients that build batches from a stream pay for this in retry bandwidth.
* Bad, because it diverges from the specification, so a TAMS client written against the
  spec and not against OpenTAMS will mis-handle the 422 until it is updated. This is the
  main reason the divergence is called out in the endpoint description rather than left
  to the conformance matrix.
* Bad, because `failed_segments` now means "non-overlap failures only", which is a
  narrower contract than the schema name suggests.

## More Information

* Implemented in `internal/metastore/segments.go` (detection), forwarded unchanged by
  `internal/service/segment/segment.go`, mapped to 422 in
  `internal/httpx/handlers/segments.go`.
* Contract: `api/opentams-api-v1.yaml`, `POST /tams/v1/flows/{flowId}/segments`.
* Requirements: [REQ-BEH-15](../requirements.md); design detail in
  [`docs/design/internal-service-segment/functional-design/functional-design.md`](../design/internal-service-segment/functional-design/functional-design.md)
  and [`docs/design/internal-metastore/`](../design/internal-metastore/), where the
  rule is recorded as `BR-SEG-03` and `BR-META-06` respectively.
* Test cases: `SCN-SEG-03`, `SCN-SEG-04`, and `SCN-SEG-05` in
  `internal/service/segment/segment_test.go`; `SCN-HTTP-04` in
  `internal/httpx/handlers/segments_test.go`; `SCN-META-03`, `SCN-META-04`, and
  `SCN-META-05` in `internal/metastore/segments_test.go`.
