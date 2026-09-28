---
unit: M1 internal/timerange
stage: Functional Design
status: Complete (retroactive backfill), revised for ADR-0038 and ADR-0039
---

# Business Rules — internal/timerange

> Rules marked **(pending)** follow a proposed ADR and are not implemented yet. Until the
> code lands, the code is the tie-breaker for current behaviour
> (see [`docs/adr/README.md`](../../../adr/README.md)).

## BR-TR-01: Timestamp format and value (pending)
A timestamp is `{sign?}{seconds}:{nanoseconds}`. `seconds` is a non-negative integer with
no leading zeros. `nanoseconds` is an integer in `[0, 999_999_999]` with no leading zeros.

The sign applies to the whole value: `value = sign × (seconds × 10⁹ + nanoseconds)`.
`-1:500000000` is −1.5 s, not −0.5 s. Source: [ADR-0038](../../../adr/0038-tams-timestamp-and-timerange-grammar.md)
rule 1, which follows the BBC `mediatimestamp` library.

## BR-TR-02: Negative zero (pending)
`-0:x` is valid. `-0:500000000` is −0.5 s. `-0:0` equals `0:0`.

This replaces the earlier rule that rejected every `-0:x`. That rule made every value
between −1 s and 0 s impossible to write.

## BR-TR-03: Internal form and bound types (pending)
`Timestamp{Seconds int64, Nanoseconds int32}` is floor-normalised. `Nanoseconds` is always
in `[0, 999_999_999]`, and the value is `Seconds × 10⁹ + Nanoseconds`. −1.5 s is stored
as `{Seconds: -2, Nanoseconds: 500000000}`. Only `ParseTimestamp` and `Timestamp.String`
deal with the sign.

A bound is `Inclusive`, `Exclusive`, or `Unbounded`. An unbounded bound has no timestamp
(`Start` or `End` is nil). Either side can be unbounded on its own.

## BR-TR-04: Empty ranges (pending)
A range is empty if it contains no instant:
- `()`.
- End equal to start, with at least one exclusive marker: `[5:0_5:0)`, `(5:0_5:0]`,
  `(5:0_5:0)`.
- End before start: `[10:0_5:0)`.

An empty range is a valid parse result, not an error (App Note 0008: it "should be
treated as an empty TimeRange by implementations"). Each caller decides what an empty
range means for its operation. The segment service rejects it (BR-SEG-02). A query
treats it as matching nothing (BR-META-21).

## BR-TR-05: Instantaneous form (pending)
A single timestamp is an instantaneous range, with `[]` markers or with no markers:
`[10:0]` and `10:0` both mean `[10:0_10:0]`. `(10:0)`, `(10:0]`, and `[10:0)` are parse
errors, because the schema says "Instantaneous TimeRanges cannot use exclusive markers".

## BR-TR-06: Omitted timestamps and markers (pending)
The grammar is `{start marker}{start timestamp}_{end timestamp}{end marker}`, and every
part is optional.
- An omitted timestamp makes that side unbounded. `(5:0_` starts after `5:0` and has no
  end. `_10:0)` has no start.
- A marker next to an omitted timestamp is ignored. `[_10:0)` equals `_10:0)`.
- An omitted marker next to a timestamp means inclusive. `0:0_10:0` equals `[0:0_10:0]`.
  App Note 0008 does not state this. ADR-0038 rule 5 takes it from `mediatimestamp`.
- `_` is eternity: unbounded on both sides.

## BR-TR-07: Overlap semantics
Two ranges overlap if they share at least one instant. Overlap is symmetric. Two ranges
that touch at a boundary do not overlap if at least one of the touching bounds is
exclusive. An empty range overlaps nothing, including itself.

`Overlaps` must give the same answer as comparing the two `NsBounds` results as
half-open intervals. A property test asserts this (BR-TR-10).

## BR-TR-08: Nanosecond precision required
When two timestamps have equal seconds, compare the nanoseconds. Without this, overlap and
containment are wrong for sub-second ranges.

## BR-TR-09: Input validation before ParseInt
`validInt` and `validNano` enforce the schema regex (digits only, no leading zeros,
nanoseconds ≤ 999_999_999) before `strconv.ParseInt`. This separates format errors from
range errors, so the error message names the real problem.

## BR-TR-10: NsBounds is the only conversion to nanoseconds (pending)
`TimeRange.NsBounds() (NsBounds, error)` converts a non-empty range into a half-open
`int64` nanosecond interval `[Lower, Upper)`:

| Bound | Result |
|---|---|
| `[` start at value `v` | `Lower = v` |
| `(` start at value `v` | `Lower = v + 1` |
| `]` end at value `v` | `Upper = v + 1` |
| `)` end at value `v` | `Upper = v` |
| Unbounded start | `LowerUnbounded = true` |
| Unbounded end | `UpperUnbounded = true` |

```go
type NsBounds struct {
    Lower          int64 // meaningful only when !LowerUnbounded
    Upper          int64 // meaningful only when !UpperUnbounded
    LowerUnbounded bool
    UpperUnbounded bool
}
```

An unbounded side is a flag, not a sentinel such as `math.MinInt64`.

The arithmetic is checked at each step: `Seconds × 10⁹`, `+ Nanoseconds`, and the `+ 1`
adjustment. A finite bound whose adjusted value does not fit in `int64` returns
`ErrOutOfRange`. The representable span is about 1677 to 2262 on the TAI timeline. An
empty range returns `ErrEmptyRange`.

No other package does nanosecond arithmetic on a timestamp. A CI check rejects the
literal `1_000_000_000` outside this package. There is no inverse function: bounds are
never rendered back to text ([ADR-0039](../../../adr/0039-timerange-stored-as-client-string-with-half-open-bounds.md)
rule 5).

Tests: a table covering every marker combination, `[0:0]`, `10:0`, one-sided ranges,
negative values inside one second, both overflow edges, and the spec example
`[1:0_2:0)` followed by `[2:0]`. A property test compares `Overlaps` with the bounds
comparison.

## BR-TR-11: Error sentinels (pending)
The package exports `ErrOutOfRange` and `ErrEmptyRange`, matched with `errors.Is`.
Callers map both to the `invalid-timerange` catalogue entry. The package does not import
`internal/apperror`.

## BR-TR-12: Canonical rendering is for derived values only (pending)
`TimeRange.String()` and `Timestamp.String()` produce a canonical string. The server uses
them only for values it derives, such as the flow timerange and `X-Paging-Timerange`. A
value that a client sent is returned as the client sent it (BR-META-10).

The canonical form:
- `[ts]` for an instantaneous range. The schema says "The short syntax is preferred".
- `_` for eternity, `()` for an empty range.
- A marker for every present timestamp, and no marker for an absent one: `[5:0_`,
  `_10:0)`.
- A timestamp as `{-}{seconds}:{nanoseconds}` of the absolute value. The value −1.5 s,
  stored as `{-2, 500000000}`, renders as `-1:500000000`.

## BR-TR-13: The grammar only widens (pending)
A string that the parser accepts once stays accepted. Stored client strings are parsed
again on read (BR-META-19), so a narrower parser would break existing rows. A change that
narrows the grammar needs a data migration and a new ADR.
