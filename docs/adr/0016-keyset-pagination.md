---
status: "accepted"
date: 2026-08-31
decision-makers: OpenTAMS maintainers
consulted: OpenTAMS maintainers
informed: OpenTAMS contributors
---

# Paginate with opaque keyset cursors, never with `OFFSET`

## Context and Problem Statement

`GET /sources`, `GET /flows`, and `GET /flows/{flowId}/segments` all return pages. Segment
lists are the hard case: a busy Flow gains rows while a client is paging through it.

`LIMIT` with `OFFSET` is the obvious approach and has two defects. The database counts and
discards every skipped row, so page 500 costs more than page 1. Worse, an insert before
the cursor position shifts every later row, so a client silently skips or repeats entries.

How does OpenTAMS paginate?

## Considered Options

* `LIMIT` and `OFFSET`, with the page number in the query
* Keyset pagination, exposing the sort key to the client
* Keyset pagination, with the key wrapped in an opaque cursor

## Decision Outcome

Chosen option: "Keyset pagination, with the key wrapped in an opaque cursor". The string
`OFFSET` does not appear anywhere in the repository.

A page is bounded by the last row it returned, not by a count of rows skipped. Sources and
Flows page on `(created, id)`; the pair is needed because `created` is not unique.
Segments page on `(flow_id, lower_ns)`, which is the index declared in
`migrations/000001_init.up.sql`.

`encodeCursor` in `internal/metastore/store.go:67` marshals the key to JSON and
base64url-encodes it. `NextCursor` is `nil` on the last page, and a page that returns
nothing has no cursor.

The cursor is opaque on purpose. Clients pass back what they received. Nothing in the API
promises what is inside, so the sort key can change without breaking a client that stores
one.

### Consequences

* Good, because page cost is constant. The query seeks the index to the cursor position
  instead of counting from the start.
* Good, because concurrent inserts cannot make a client skip or repeat a row. The cursor
  names a position in the ordering, not a count.
* Good, because the cursor's contents can change later. Only the encoder and decoder know
  the shape.
* Bad, because clients cannot jump to page N. Keyset pagination is sequential, and
  a numbered pager cannot be built on it.
* Bad, because there is no total count. Returning one would need a second query that scans
  the whole result.
* Bad, because base64 of JSON only looks opaque. Anyone can decode it, so it must never
  carry anything sensitive, and today it carries a timestamp and a UUID.
* Bad, because every list query needs a matching index. A new sort order needs a migration,
  not just a changed `ORDER BY`.

## More Information

* Encoder and decoder: [`internal/metastore/store.go`](../../internal/metastore/store.go).
* Supporting indexes: [`migrations/000001_init.up.sql`](../../migrations/000001_init.up.sql).
