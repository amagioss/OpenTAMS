---
status: "accepted"
date: 2026-08-31
decision-makers: OpenTAMS maintainers
consulted: OpenTAMS maintainers
informed: OpenTAMS contributors
---

# Store a timerange as its wire string plus normalised integer bounds

## Context and Problem Statement

TAMS timeranges are text on the wire, in a syntax the specification defines. The syntax
carries information beyond the two endpoints: whether each end is inclusive or exclusive,
whether an end is absent, and how the value was written.

The database needs the opposite thing. Overlap detection, ordering, and range queries all
want two comparable numbers.

Storing only the string means parsing on every query. Storing only the numbers means the
value a client reads back is not the one it wrote.

## Considered Options

* Store the wire string only, and parse when a query needs bounds
* Store normalised integers only, and re-render the string on read
* Store both, with the string as the authority and the integers derived from it
* Use PostgreSQL's native range type as the stored column

## Decision Outcome

Chosen option: "Store both".

The `segments` table carries `timerange TEXT NOT NULL` alongside `lower_ns BIGINT NOT NULL`
and `upper_ns BIGINT`. The text is what the client sent and what the client reads back.
The integers are nanoseconds, derived by `internal/timerange` at write time, and they are
what the database compares.

`upper_ns` is nullable, and that is how an open-ended range is stored. Queries substitute
`COALESCE(upper_ns, 9223372036854775807)` where a bound is required.

We rejected PostgreSQL's native range type as the stored column, because it cannot hold
the wire syntax and would still need the text column beside it. The range is built in the
query instead, with `int8range(lower_ns, COALESCE(upper_ns, ...))`.

### Consequences

* Good, because a client reads back the exact string it wrote. No round trip through a
  normal form changes the representation.
* Good, because overlap and ordering are integer comparisons on indexed columns. The
  database never parses TAMS syntax.
* Good, because `(flow_id, lower_ns)` is a plain B-tree index, which is what Segment
  listing and pagination need.
* Bad, because the same fact is stored twice. A row whose text and integers disagree is
  possible, and only the write path prevents it.
* Bad, because the derivation runs at write time, so a fix to the parser does not correct
  rows already stored. That needs a backfill migration.
* Bad, because `COALESCE` with `math.MaxInt64` appears in every query that needs an upper
  bound. It is a sentinel, and it is easy to forget in a new query.

## More Information

* Parsing and rendering: [`internal/timerange/`](../../internal/timerange/).
* Column definitions: [`migrations/000001_init.up.sql`](../../migrations/000001_init.up.sql).
* The constraint built from these columns: [ADR-0015](0015-gist-exclusion-constraint.md).
