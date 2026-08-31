---
status: "accepted"
date: 2026-08-31
decision-makers: OpenTAMS maintainers
consulted: OpenTAMS maintainers
informed: OpenTAMS contributors
---

# Register an Object lazily, when the first Segment references it

## Context and Problem Statement

A Segment points at an Object. One Object can be referenced by Segments on several Flows,
so OpenTAMS keeps a row per Object with a reference count that decides when the media
becomes eligible for deletion.

TAMS has no "create Object" call in the Phase 1 surface. The client asks for a storage
allocation, uploads bytes to a presigned URL, and then registers Segments. Nothing in that
sequence tells the server an Object now exists, except the Segment registration itself.

When does the `objects` row appear?

## Considered Options

* Create the row when storage is allocated
* Require an explicit registration call before a Segment can reference an Object
* Create or increment the row as part of Segment registration

## Decision Outcome

Chosen option: "Create or increment the row as part of Segment registration".

`internal/metastore/segments.go` runs one statement per referenced Object inside the
Segment insert transaction:

```sql
INSERT INTO objects (id, ref_count, storage_id, reaping)
VALUES ($1, 1, $2, false)
ON CONFLICT (id) DO UPDATE
    SET ref_count = objects.ref_count + 1
    WHERE objects.reaping = false
RETURNING (xmax = 0) AS inserted
```

Three details carry weight:

* The upsert is the whole operation. First reference inserts with `ref_count = 1`, later
  references increment. There is no read-then-write, so concurrent registrations against
  one Object cannot lose a count.
* `RETURNING (xmax = 0)` reports whether this call inserted or updated, without a second
  query.
* The `WHERE objects.reaping = false` predicate is the race guard. If the GC worker has
  claimed a zero-reference Object for deletion, the predicate fails, no row is returned,
  and the store raises `ErrSegmentObjectReaping`. The client retries with a fresh Object
  ID rather than adopting bytes that are about to be erased.

Allocating storage creates no row. A client that allocates and never registers leaves
nothing behind in the database.

### Consequences

* Good, because reference counting has no window. The count and the Segment row commit in
  one transaction.
* Good, because an abandoned upload costs one orphaned object in the store and nothing in
  the database.
* Good, because the reaping predicate makes the interaction with GC explicit and
  recoverable, instead of leaving a Segment pointing at deleted media.
* Bad, because `ErrSegmentObjectReaping` is a transient error a client must handle. It is
  rare, and rare errors are the ones clients get wrong.
* Bad, because the count is only as good as the write paths. Any future path that inserts a
  Segment without this upsert corrupts the count silently.
* Bad, because `storage_id` is set on first insert and is immutable afterwards, so a
  mistake at that moment cannot be corrected without a manual fix.

## More Information

* The upsert and its error handling: [`internal/metastore/segments.go`](../../internal/metastore/segments.go).
* Column and index rationale: [`migrations/000005_objects_storage_id_reaping.up.sql`](../../migrations/000005_objects_storage_id_reaping.up.sql).
* What happens when the count reaches zero: [ADR-0020](0020-gc-owns-object-lifetime.md).
