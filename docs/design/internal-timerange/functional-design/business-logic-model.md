---
unit: M1 internal/timerange
stage: Functional Design
status: Complete (retroactive backfill), revised for ADR-0038 and ADR-0039
---

# Business Logic Model — internal/timerange

> Items marked **(pending)** follow a proposed ADR and are not implemented yet.

## Purpose

Shared domain module for TAI timestamps and timeranges: parsing, validation, predicates,
and the one conversion to nanosecond bounds. The metadata store uses it for overlap
enforcement and every timerange query. HTTP handlers use it to parse query parameters.

## Core Concepts

### Timestamp
A TAI instant with nanosecond precision. Wire format: `{sign?}{seconds}:{nanoseconds}`.
The sign applies to the whole value, so `-1:500000000` is −1.5 s (BR-TR-01, pending).

### TimeRange
A TAI interval in TAMS notation. Each bound is inclusive (`[`, `]`), exclusive (`(`,
`)`), or unbounded (timestamp omitted). Special forms: eternity `_`, empty `()`, and the
instantaneous form `[ts]` or `ts` (BR-TR-04 to BR-TR-06, pending).

### NsBounds (pending)
The half-open `int64` nanosecond interval `[Lower, Upper)` of a non-empty range, with an
explicit flag for each unbounded side. It exists only so that the database can compare
ranges. It is never rendered back to text (BR-TR-10).

## Data Model

```
Timestamp { Seconds int64, Nanoseconds int32 }
// floor-normalised: Nanoseconds in [0, 999_999_999],
// value = Seconds*1e9 + Nanoseconds; -1.5 s = {-2, 500000000}   (pending)

BoundType = Inclusive | Exclusive | Unbounded

TimeRange {
    Start     *Timestamp  // nil when StartType == Unbounded
    StartType BoundType
    End       *Timestamp  // nil when EndType == Unbounded
    EndType   BoundType
}

NsBounds {                  // (pending)
    Lower, Upper                   int64
    LowerUnbounded, UpperUnbounded bool
}
```

## Operations

| Operation | Description |
|---|---|
| `ParseTimestamp(s)` | Parse `{sign?}{sec}:{ns}` into a floor-normalised `Timestamp`, or return an error |
| `Timestamp.String()` | Canonical string, sign-magnitude |
| `Parse(s)` | Parse TAMS timerange notation into a `TimeRange`, or return an error. An empty range is a result, not an error. |
| `TimeRange.String()` | Canonical string, for server-derived values only (BR-TR-12) |
| `TimeRange.IsEmpty()` | True if the range contains no instant |
| `TimeRange.IsEternity()` | True if unbounded on both sides |
| `TimeRange.Contains(ts)` | True if the timestamp is in the range |
| `TimeRange.Overlaps(other)` | True if the two ranges share an instant. Agrees with the `NsBounds` comparison. |
| `TimeRange.NsBounds()` (pending) | Half-open `int64` bounds, or `ErrOutOfRange` / `ErrEmptyRange` (BR-TR-10) |
