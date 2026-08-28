---
unit: M1 internal/timerange
stage: Functional Design
status: Complete (retroactive backfill)
---

# Business Logic Model — internal/timerange

## Purpose

Shared domain module for TAI timestamp and timerange parsing, validation, and predicate logic. Used by the metadata store layer for overlap enforcement and by HTTP handlers for query parameter parsing.

## Core Concepts

### Timestamp
A TAI instant with nanosecond precision. Format: `{sign?}{seconds}:{nanoseconds}`. Seconds may be negative; nanoseconds are always non-negative and in [0, 999_999_999].

### TimeRange
A TAI time interval in TAMS bracket notation. Bounds are inclusive `[`/`]` or exclusive `(`/`)`. Special forms: eternity `_` (unbounded both ends), never `()` (empty, no bounds).

## Data Model

```
Timestamp { Seconds int64, Nanoseconds int32 }

BoundType = Inclusive | Exclusive | Unbounded

TimeRange {
    Start     *Timestamp  // nil when StartType == Unbounded
    StartType BoundType
    End       *Timestamp  // nil when EndType == Unbounded
    EndType   BoundType
}
```

## Operations

| Operation | Description |
|---|---|
| `ParseTimestamp(s)` | Parse `{sign?}{sec}:{ns}` string → Timestamp or error |
| `Timestamp.String()` | Canonical string representation |
| `Parse(s)` | Parse bracket-notation timerange string → TimeRange or error |
| `TimeRange.String()` | Canonical bracket-notation string |
| `TimeRange.IsEmpty()` | True if range contains no points |
| `TimeRange.IsEternity()` | True if unbounded on both ends |
| `TimeRange.Contains(ts)` | True if timestamp falls within range |
| `TimeRange.Overlaps(other)` | True if two ranges share at least one point |
