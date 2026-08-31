---
status: "accepted"
date: 2026-08-31
decision-makers: OpenTAMS maintainers
consulted: OpenTAMS maintainers
informed: OpenTAMS contributors
---

# Use PostgreSQL as the metadata store

## Context and Problem Statement

TAMS metadata is relational. A Source owns Flows, a Flow owns Segments, and a Segment
points at an Object that other Segments may also point at. The API asks questions that
follow those links, and two of the requirements are hard for a store with weak
consistency:

* Segments on one Flow must never overlap in time.
* An Object's reference count must be right, because it decides when media can be deleted.

Which store holds this?

## Considered Options

* A document store such as MongoDB
* A wide-column store such as DynamoDB or Cassandra
* PostgreSQL

## Decision Outcome

Chosen option: "PostgreSQL", because the two hard requirements are both consistency
problems, and PostgreSQL solves them with features we would otherwise write ourselves.

Two are decisive:

* **Range exclusion constraints.** `btree_gist` lets the database refuse an overlapping
  Segment. Application-level checking cannot close the race between two concurrent
  writers. See [ADR-0015](0015-gist-exclusion-constraint.md).
* **Transactions across tables.** Inserting a Segment and incrementing `objects.ref_count`
  is one transaction. In a store without that, a crash between the two writes leaves a
  reference count that no longer matches reality.

PostgreSQL is also a managed product on every major cloud and runs anywhere, which is what
[ADR-0001](0001-cloud-agnostic-substrate.md) requires.

The schema is five migrations under [`migrations/`](../../migrations/).

### Consequences

* Good, because the overlap rule is enforced by the database, so no application bug can
  produce overlapping Segments.
* Good, because reference counting is transactional, and correctness does not depend on a
  reconciliation job.
* Good, because operators already know how to run, back up, and monitor PostgreSQL.
* Bad, because writes go to one primary. Horizontal write scaling means sharding, which we
  have not designed.
* Bad, because the Segment table is the growth point. A busy Flow adds rows continuously,
  and partitioning is not implemented.
* Bad, because it is a stateful dependency. Every OpenTAMS deployment needs a database
  someone operates.

## More Information

* Schema and its rationale: [`../architecture/metadata-model.md`](../architecture/metadata-model.md).
* Driver choice: [ADR-0013](0013-pgx-without-database-sql.md).
* Schema evolution: [ADR-0017](0017-expand-contract-schema-evolution.md).
