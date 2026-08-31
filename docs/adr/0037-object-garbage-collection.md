---
status: "accepted"
date: 2026-08-31
decision-makers: OpenTAMS maintainers
consulted: OpenTAMS maintainers
informed: OpenTAMS contributors
---

# Reclaim Object storage from a separate worker, and ship without it

## Context and Problem Statement

[ADR-0020](0020-gc-owns-object-lifetime.md) removes byte deletion from every request path.
Something still has to reclaim the storage, and that something has to run somewhere.

The work is unlike serving. It is a periodic sweep over a table, it is slow, it is
interruptible, and it must never compete with request handling for a connection pool. It
also has a race to handle: an Object whose reference count is zero can gain a reference
again while the sweep is deciding to delete it.

Where does reclamation run, and what does a release ship?

## Considered Options

* A background goroutine inside the API server
* A separate worker, deployed as a CronJob or a long-running process
* No reclamation, relying on the object store's lifecycle rules

## Decision Outcome

Chosen option: "A separate worker", shipped in the same binary as a subcommand.

`opentams gc` is the entry point. Its help text describes the intended deployment: run
independently of the API server, as a Kubernetes CronJob or a sidecar, emitting a
structured summary per sweep.

The design is settled and the database supports it:

* `objects.ref_count` identifies candidates.
* `objects.reaping` is the claim flag. The worker sets it when claiming a zero-reference
  row, and the metastore's registration upsert refuses to increment a claimed row, so a
  client gets `ErrSegmentObjectReaping` and retries with a fresh Object ID. This is what
  closes the race — see [ADR-0019](0019-lazy-object-registration.md).
* Two partial indexes serve the two views: `objects_gc_claim_idx` for claiming, and
  `objects_gc_retry_idx` for rows claimed but not yet reaped.
* `objectstore.Store.DeleteObjects` is the delete primitive, per-ID idempotent, chunked at
  1000 IDs, and classifying failures as `ErrTransient`, `ErrAuth`, or `ErrInvalidInput` so a
  sweep knows which rows to reap and which to retry.

**The worker itself is not implemented.** `opentams gc` returns
`"gc worker is not yet implemented (M16)"`. `GC_POLL_INTERVAL` and `GC_BATCH_SIZE` are
parsed, validated, and ignored. This is stated in the README, in
[`../conformance.md`](../conformance.md), and in [`../configuration.md`](../configuration.md),
so an operator meets it before deploying rather than after.

Object-store lifecycle rules are explicitly not a substitute. A lifecycle rule sees one
object's age. It cannot see that a Segment on another Flow still references those bytes, so
it deletes live data. `../configuration.md` warns against this directly.

### Consequences

* Good, because the sweep cannot interfere with request handling. It has its own process,
  its own connection pool, and its own schedule.
* Good, because the claim flag makes the race explicit and recoverable, rather than leaving
  a Segment pointing at deleted media.
* Good, because reclamation can be paused, rescheduled, or run against one deployment
  without touching the servers.
* Good, because shipping it as a subcommand of the same binary keeps one image and one
  version.
* Bad, because every deployment today leaks storage. Deleting a Flow frees no bytes, and an
  operator must reconcile by hand or accept the growth.
* Bad, because the gap is not obvious from the API. A `DELETE` returns success, and nothing
  in the response says the bytes remain.
* Bad, because the sweep will read a table that grows with the Object count, and its cost
  at scale is unmeasured until the worker exists.

## More Information

* Command and its intended deployment: [`cmd/opentams/gc.go`](../../cmd/opentams/gc.go).
* Schema support and index rationale: [`migrations/000005_objects_storage_id_reaping.up.sql`](../../migrations/000005_objects_storage_id_reaping.up.sql).
* Why no request path deletes: [ADR-0020](0020-gc-owns-object-lifetime.md).
