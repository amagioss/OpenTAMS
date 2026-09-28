---
status: "proposed"
date: 2026-09-28
decision-makers: OpenTAMS maintainers
consulted: OpenTAMS maintainers
informed: OpenTAMS contributors
---

# Store the client's timerange string, and derive half-open nanosecond bounds for comparison only

## Context and Problem Statement

[ADR-0014](0014-timerange-stored-as-string-and-bounds.md) stores a segment timerange as
text plus the integer columns `lower_ns` and `upper_ns`. The code does not do what that
ADR describes:

* **The conversion ignores the markers.** `metastore.nsRange` copies the two timestamps
  and ignores `[`, `(`, `]`, and `)`. `[0:0_10:0]`, `(0:0_10:0)`, and `[0:0_10:0)` all
  become `(0, 10000000000)`. A closed range loses its last nanosecond. `[0:0]` becomes
  `(0, 0)` and fails the `segments_upper_ns_positive` constraint with a 500.
* **Overlaps are missed.** `[0:0_10:0]` and `[10:0_20:0)` share `10:0`, but their
  stored bounds do not intersect, so the pre-check and the exclusion constraint both
  accept them.
* **There is more than one conversion.** The `ListFlows` timerange filter has its own
  arithmetic, and the flow timerange is rendered from the integer columns in SQL. Integer
  division truncates toward zero, so a negative bound renders as `-1:-500000000`.
* **The client's string is lost.** The metastore stores `TimeRange.String()`, not the
  string the client sent. A client that sends `[10:0]` reads back `[10:0_10:0]`.
* **DELETE uses the wrong predicate.** It removes every segment that overlaps the query.
  TAMS says "Only delete Flow Segments that are completely covered by the given
  timerange", and the OpenTAMS API document says the same.

The TAMS specification for `GET /flows/{flowId}/segments` says: "Service implementations
MUST take clusivity markers of the timerange into account."

How does OpenTAMS store a timerange, compare it, and give it back?

## Considered Options

* Keep ADR-0014, and correct `nsRange` only
* Store the client's string, derive half-open bounds in `internal/timerange` only, and
  use the bounds only for comparison
* Store a PostgreSQL range type

## Decision Outcome

Chosen option: "Store the client's string, derive half-open bounds in
`internal/timerange` only, and use the bounds only for comparison", because a single
owner of the conversion removes the class of defect, not only the one defect, and the
client gets back what it sent.

A PostgreSQL range type is rejected for the reason ADR-0014 gives: it cannot hold the
wire syntax, so the text column is necessary anyway.

The rules:

1. **Client values are stored as sent.** The segment `timerange`, `ts_offset`,
   `object_timerange`, and `last_duration` are stored exactly as the client sent them,
   and every response returns that exact string.
2. **Stored text is parsed, never compared.** `10:0`, `[10:0]`, and `[10:0_10:0]` are
   the same range. Equality, `GROUP BY`, and `DISTINCT` on a stored time string are not
   permitted. Semantic checks parse the string first.
3. **One conversion.** `timerange.NsBounds` is the only function that turns a
   `TimeRange` into `int64` nanoseconds. The result is half-open, `[lower, upper)`:

   | Marker | Bound |
   |---|---|
   | `[` start | `value` |
   | `(` start | `value + 1` |
   | `]` end | `value + 1` |
   | `)` end | `value` |

   The arithmetic is checked. A finite bound that does not fit in `int64` after the
   adjustment returns `ErrOutOfRange`. An unbounded side is reported explicitly, not as a
   sentinel value. No code outside `internal/timerange` does nanosecond arithmetic on a
   timestamp.
4. **Only the segment timerange is stored as bounds.** `ts_offset`, `object_timerange`,
   and `last_duration` are not converted. The segment `timerange` is already on the flow
   timeline, and `object_timerange` is on the object's timeline.
5. **Bounds are for comparison only.** They are used to filter, to detect overlap, to
   order and page, and to select the first and last segment of a flow. They are never
   rendered back to text.
6. **Stored bounds are never null.** Segment timeranges are always bounded
   ([ADR-0040](0040-segment-timerange-invariants.md)), so `lower_ns` and `upper_ns` are
   both `NOT NULL`. An unbounded side of a query timerange is passed as a SQL `NULL`
   parameter.
7. **Each operation uses one named predicate.**

   | Operation | Predicate | TAMS text |
   |---|---|---|
   | `GET` and `HEAD /flows/{flowId}/segments` | Overlap | "partially or wholly overlap the timerange specified" |
   | `GET /flows?timerange=` | Overlap with any segment of the flow | "Filter on Flows that overlap the given timerange" |
   | Segment registration | Overlap, which rejects the batch | "MUST NOT overlap any other Segment in the same Flow" |
   | `DELETE /flows/{flowId}/segments` | Containment | "completely covered by the given timerange" |

8. **An empty query timerange matches as TAMS says.** This includes an end before the
   start ([ADR-0038](0038-tams-timestamp-and-timerange-grammar.md)). `GET` segments
   returns an empty list. `DELETE` deletes nothing. `GET /flows` returns only flows with
   no segments ("An empty timerange returns Flows with no content").
9. **Derived values are rendered canonically.** The flow timerange is the start of the
   segment with the lowest `lower_ns` joined to the end of the segment with the highest
   `upper_ns`. Both are parsed from the stored client strings and rendered with
   `TimeRange.String()`. `X-Paging-Timerange` is built the same way from the earliest and
   latest items on the page, whatever the sort order.

### Consequences

* Good, because the conversion honours the markers, as the TAMS MUST requires, and
  `[0:0]` is `(0, 1)`.
* Good, because one function, with one test table, decides the bounds for every query.
  A CI check can reject nanosecond arithmetic outside `internal/timerange`.
* Good, because a client reads back its own string, and a derived value never passes
  through a lossy decode.
* Good, because DELETE now removes only what the client asked for.
* Bad, because the same fact is still stored twice. Only the write path keeps the text and
  the bounds consistent.
* Bad, because rows written before this change hold the re-rendered string and the old
  bounds. The original client string cannot be recovered. Operators of `v0.1.0-alpha.0`
  databases must recompute the bounds by hand.
* Bad, because every read path now handles `ErrOutOfRange` from a client's query
  timerange.

## More Information

* Supersedes [ADR-0014](0014-timerange-stored-as-string-and-bounds.md) when accepted.
* Grammar and value model: [ADR-0038](0038-tams-timestamp-and-timerange-grammar.md).
* Segment invariants and schema: [ADR-0040](0040-segment-timerange-invariants.md).
* Specification: `GET`, `POST`, and `DELETE /flows/{flowId}/segments` and `GET /flows` in
  the upstream `TimeAddressableMediaStore.yaml`, and
  [App Note 0012](https://github.com/bbc/tams/blob/main/docs/appnotes/0012-using-flow-segment-timeranges.md).
* Design: [`internal-timerange`](../design/internal-timerange/functional-design/business-rules.md)
  BR-TR-10 and BR-TR-11,
  [`internal-metastore`](../design/internal-metastore/functional-design/business-rules.md)
  BR-META-05, BR-META-08, BR-META-10, BR-META-12, and BR-META-21.
