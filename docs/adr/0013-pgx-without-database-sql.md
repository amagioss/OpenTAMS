---
status: "accepted"
date: 2026-08-31
decision-makers: OpenTAMS maintainers
consulted: OpenTAMS maintainers
informed: OpenTAMS contributors
---

# Use pgx directly rather than through `database/sql`

## Context and Problem Statement

`jackc/pgx` can be used two ways. It registers a `database/sql` driver, which gives the
standard library's interfaces and its ecosystem. It also exposes a native API with its own
connection pool and its own types.

`database/sql` is a lowest common denominator across every SQL database. It has no
knowledge of PostgreSQL arrays, `jsonb`, `int8range`, or `SQLSTATE` codes. OpenTAMS needs
all four.

Which API does the metastore use?

## Considered Options

* `database/sql` with the pgx driver
* The native pgx API, using `pgxpool`
* An ORM such as GORM or ent

## Decision Outcome

Chosen option: "The native pgx API, using `pgxpool`". Nine packages under `internal/` use
`pgxpool` directly. Nothing imports `database/sql`.

Three things drove it:

* **Error classification.** pgx returns `*pgconn.PgError` with the `SQLSTATE` code intact.
  `internal/metastore/segments.go:337` matches `23P01` (exclusion violation) to turn an
  overlapping Segment into the right HTTP status. Through `database/sql` that code arrives
  as text inside an error string, which leaves the mapping to string matching.
* **Native types.** `jsonb` columns and `int8range` values map without a conversion layer.
* **Batching.** `pgx.Batch` and `SendBatch` send a multi-Segment insert in one round trip.
  `database/sql` has no equivalent.

We rejected an ORM. The queries here are few, hand-tuned, and shaped by the exclusion
constraint and the keyset cursors. An ORM would hide exactly the details that matter and
add a layer to debug.

### Consequences

* Good, because a constraint violation becomes a precise HTTP status, driven by
  `SQLSTATE` rather than string matching.
* Good, because bulk Segment registration costs one round trip instead of one per Segment.
* Good, because there is no hidden query builder. Every statement is visible SQL.
* Bad, because the store is now PostgreSQL-only in practice. Supporting another SQL
  database would mean rewriting the metastore, not swapping a driver string. This narrows
  [ADR-0001](0001-cloud-agnostic-substrate.md): cloud-agnostic, not database-agnostic.
* Bad, because tooling that expects a `*sql.DB` cannot be used. `golang-migrate` is given
  its own connection for this reason.
* Bad, because contributors who know `database/sql` have a second API to learn.

## More Information

* Store implementation: [`internal/metastore/`](../../internal/metastore/).
* Error mapping: [`internal/apperror/apperror.go`](../../internal/apperror/apperror.go).
