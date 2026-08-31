---
unit: M1 internal/timerange
stage: Functional Design
status: Complete (retroactive backfill)
---

# Business Rules — internal/timerange

## BR-TR-01: Timestamp format
Timestamps are `{sign?}{seconds}:{nanoseconds}`. Seconds are non-negative integers with no leading zeros. Nanoseconds are non-negative integers in [0, 999_999_999] with no leading zeros. The sign prefix `-` applies only to seconds.

## BR-TR-02: Negative zero is invalid
`-0:x` is rejected. It cannot round-trip: `ParseTimestamp(ts.String())` must equal `ts` for all valid timestamps. `-0` has the same value as `0` but a different string representation.

## BR-TR-03: Bound semantics
Three bound types: `Inclusive` (`[`/`]`), `Exclusive` (`(`/`)`), `Unbounded` (no bracket — only for eternity). Unbounded bounds have no associated timestamp (`Start`/`End` pointer is nil).

## BR-TR-04: Empty range rules
A TimeRange is empty if it contains no points:
- Canonical never `()` — both bounds nil, both Exclusive.
- `[ts_ts)`, `(ts_ts]`, `(ts_ts)` where start == end — exclusive bound makes it empty.
- A TimeRange where end < start is invalid (parse error, not an empty range).

> *Decision: Only reject end < start strictly. end == start with exclusive bound is a valid empty range (not a parse error). Rationale: clients may construct zero-duration ranges as a valid degenerate case. Rejected: treating end <= start as always invalid (too strict — excludes valid empty ranges).*

## BR-TR-05: Instantaneous form
`[ts]` (single timestamp, no underscore) expands to `[ts_ts]`. Only valid with `[]` brackets — `(ts)` and `(ts]` are parse errors.

## BR-TR-06: Eternity
The string `_` represents an unbounded range on both ends. Contains every timestamp. Cannot be produced by the bracket form.

## BR-TR-07: Overlap semantics
Two ranges overlap if they share at least one point. Overlap is symmetric. Boundary touching with at least one exclusive bound is NOT an overlap. An empty range overlaps nothing, including itself.

## BR-TR-08: Nanosecond precision required
When comparing timestamps with equal seconds, nanoseconds must be compared. Omitting nanosecond comparison causes incorrect overlap and containment results for sub-second ranges.

## BR-TR-09: Input validation before ParseInt
`validInt` and `validNano` enforce spec regex (no leading zeros, digit-only, nanoseconds ≤ 999_999_999) before `strconv.ParseInt`. This separates format errors from range errors and avoids ambiguity in error messages.
