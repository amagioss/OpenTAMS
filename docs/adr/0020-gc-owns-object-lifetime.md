---
status: "accepted"
date: 2026-08-31
decision-makers: OpenTAMS maintainers
consulted: OpenTAMS maintainers
informed: OpenTAMS contributors
---

# Give the garbage collector sole ownership of Object lifetime

## Context and Problem Statement

Deleting a Flow removes its Segments. Some of the Objects those Segments referenced now
have a reference count of zero, and their media bytes are no longer reachable through the
API.

The request handling the delete could erase those bytes before it responds. That makes the
API call slow and unbounded, because the number of Objects depends on the Flow's history
and not on the request. It also makes the call partly irreversible in the middle: if the
object store fails after some deletes, the transaction has already removed the metadata
that said which bytes belonged where.

Who deletes media bytes?

## Considered Options

* Delete inline, in the request that drops the last reference
* Delete inline, but asynchronously, in a goroutine the request starts
* Delete in a separate garbage-collection worker, driven by the reference count

## Decision Outcome

Chosen option: "Delete in a separate garbage-collection worker".

No request path deletes media. `flowService.DeleteFlow` calls `store.DeleteFlow` and
returns as soon as the metastore transaction commits. The flow service holds no object
store at all — its constructor comment states this, and the struct has no such field. The
segment service does the same.

`objectstore.Store.DeleteObjects` exists and is the GC worker's batch-delete primitive. It
is per-ID idempotent, chunked at the S3 limit of 1000 IDs per call, and classifies
failures as `ErrTransient`, `ErrAuth`, or `ErrInvalidInput` so a sweep can tell which rows
to reap and which to leave for the next pass. Its only non-test caller is the GC worker.

The schema carries the GC's working state: `objects.ref_count`, the `reaping` claim flag,
and two partial indexes, `objects_gc_claim_idx` for the claim path and
`objects_gc_retry_idx` for rows already claimed.

BYOS Objects are outside this entirely. OpenTAMS records where the bytes are and never
assumes ownership of them, so GC skips them.

### Consequences

* Good, because `DELETE` latency depends on the metadata transaction alone, not on how
  many Objects a Flow accumulated.
* Good, because an object-store outage cannot fail or stall a metadata delete.
* Good, because deletion is retryable. A failed sweep leaves the row claimed and tries
  again, where an inline delete would have lost the record of what to remove.
* Good, because there is one deletion path to audit rather than one per request handler.
* Bad, because bytes outlive their metadata. Storage cost does not fall at the moment a
  client deletes a Flow.
* Bad, because the guarantee is only as good as the worker that carries it out. See
  [ADR-0037](0037-object-garbage-collection.md) for the worker's design and its delivery
  state.
* Bad, because an operator who needs space back has to reconcile the object store against
  the `objects` table by hand.

## More Information

* The delete primitive and its error classes: [`internal/objectstore/objectstore.go`](../../internal/objectstore/objectstore.go).
* Service-side rules: [`../design/internal-service-flow/functional-design/functional-design.md`](../design/internal-service-flow/functional-design/functional-design.md).
* Reference counting: [ADR-0019](0019-lazy-object-registration.md).
