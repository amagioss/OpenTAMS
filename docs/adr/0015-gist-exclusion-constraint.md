---
status: "accepted"
date: 2026-08-31
decision-makers: OpenTAMS maintainers
consulted: OpenTAMS maintainers
informed: OpenTAMS contributors
---

# Enforce Segment non-overlap with a GiST exclusion constraint

## Context and Problem Statement

Segments on one Flow must not overlap in time. This is `REQ-BEH-15`, and it is the rule
that makes a Flow's timeline unambiguous.

Checking it in the application means reading the neighbouring Segments, deciding, and then
inserting. Two concurrent writers can both read, both decide the range is free, and both
insert. The window is small and it is real, and the result is a corrupt timeline that no
later request can detect.

Where is the rule enforced?

## Considered Options

* Check in the application before inserting
* Check in the application, inside a transaction that locks the Flow row
* Enforce it in the database with an exclusion constraint
* Enforce it in the database, and check in the application as well

## Decision Outcome

Chosen option: "Enforce it in the database, and check in the application as well".

`migrations/000001_init.up.sql` enables `btree_gist` and declares:

```sql
CONSTRAINT no_segment_overlap EXCLUDE USING gist (
    flow_id WITH =,
    int8range(lower_ns, COALESCE(upper_ns, 9223372036854775807)) WITH &&
)
```

PostgreSQL refuses any insert whose range intersects an existing range on the same Flow,
under concurrency, without the application taking a lock.

The application still checks first. It does so to produce a good error, not for
correctness. A pre-check reports which Segment conflicts and why; the constraint reports
only that one did. `internal/metastore/segments.go:337` catches `SQLSTATE 23P01` and maps
it to the same rejection, so the race loser gets the same answer as a request that lost
the pre-check.

Overlap rejects the whole batch. That is a separate decision, recorded in
[ADR-0024](0024-whole-batch-reject-on-segment-overlap.md).

### Consequences

* Good, because the invariant holds no matter what the application does. A bug in the
  service layer cannot write an overlapping Segment.
* Good, because no row locks are needed, so concurrent writers to one Flow do not
  serialise against each other.
* Good, because the GiST index that backs the constraint also serves range queries.
* Bad, because the rule now lives in two places. A change to overlap semantics means a
  migration and a code change, and the two can disagree between deploys.
* Bad, because the constraint costs write throughput. Every insert does a GiST index probe,
  and that index is larger than the B-tree it sits beside.
* Bad, because the database's error is opaque. The mapping at `segments.go:337` is what
  turns it into a usable message, and it is easy to miss on a new insert path.

## More Information

* Column derivation: [ADR-0014](0014-timerange-stored-as-string-and-bounds.md).
* The batch-level rule: [ADR-0024](0024-whole-batch-reject-on-segment-overlap.md).
* Business rules: [`../design/internal-metastore/functional-design/business-rules.md`](../design/internal-metastore/functional-design/business-rules.md).
