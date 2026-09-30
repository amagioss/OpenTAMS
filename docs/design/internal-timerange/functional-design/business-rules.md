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
`-1:500000000` is −1.5 s, not −0.5 s. Source: [ADR-0038](../../../adr/0038-tams-timestamp-and-timerange-format.md)
rule 1, which follows the BBC `mediatimestamp` library.

## BR-TR-02: Negative zero (pending)
`-0:x` is valid. `-0:500000000` is −0.5 s. `-0:0` equals `0:0`.

This replaces the earlier rule that rejected every `-0:x`. That rule made every value
between −1 s and 0 s impossible to write.

## BR-TR-03: Internal form and bound types (pending)
`Timestamp{Seconds int64, Nanoseconds int32}` is floor-normalised. `Nanoseconds` is always
in `[0, 999_999_999]`, and the value is `Seconds × 10⁹ + Nanoseconds`. The package stores
−1.5 s as `{Seconds: -2, Nanoseconds: 500000000}`. Only `ParseTimestamp` and `Timestamp.String`
deal with the sign.

A bound is `Inclusive`, `Exclusive`, or `Unbounded`. An unbounded bound has no timestamp
(`Start` or `End` is nil). Either side can be unbounded on its own.

## BR-TR-04: Empty ranges (pending)
A range is empty if it contains no instant:
- `()`
- End equal to start, with at least one exclusive marker: `[5:0_5:0)`, `(5:0_5:0]`,
  `(5:0_5:0)`
- End before start: `[10:0_5:0)`.

An empty range is a valid parse result, not an error (App Note 0008: it "should be
treated as an empty TimeRange by implementations"). Each caller decides what an empty
range means for its operation. The segment service rejects it (BR-SEG-02). A query
treats it as matching nothing (BR-META-21).

## BR-TR-05: Instantaneous form (pending)
A single timestamp is an instantaneous range, with `[]` markers or with no markers:
`[10:0]` and `10:0` both mean `[10:0_10:0]`. `(10:0)`, `(10:0]`, and `[10:0)` are parse
errors, because the schema says "Instantaneous TimeRanges cannot use exclusive markers".

The `timerange.json` regex matches `(10:0)`, so spec validation lets it through. The
parser rejects it because of the schema description, not the regex. BR-CONV-09 turns
that parse error into a request-level 400. BBC `mediatimestamp` accepts `(10:0)` and
drops the markers. OpenTAMS follows the schema text here, because the library applies
only where the specification is silent
([ADR-0038](../../../adr/0038-tams-timestamp-and-timerange-format.md) rule 9).

## BR-TR-06: Omitted timestamps and markers (pending)
The TimeRange format is `{start marker}{start timestamp}_{end timestamp}{end marker}`, and every
part is optional. These rules apply:
- An omitted timestamp makes that side unbounded. `(5:0_` starts after `5:0` and has no
  end. `_10:0)` has no start.
- The parser ignores a marker next to an omitted timestamp. `[_10:0)` equals `_10:0)`.
- An omitted marker next to a timestamp means inclusive. `0:0_10:0` equals `[0:0_10:0]`.
  App Note 0008 does not state this. ADR-0038 rule 5 takes it from `mediatimestamp`.
- `_` is eternity, which is unbounded on both sides.

## BR-TR-07: Overlap semantics
Two ranges overlap if they share at least one instant. Overlap is symmetric. Two ranges
that touch at a boundary do not overlap if at least one of the touching bounds is
exclusive. An empty range overlaps nothing, including itself.

`Overlaps` must give the same answer as the `NsBounds` comparison for every pair of
ranges, with these rules:
- If either range is empty, `NsBounds` returns `ErrEmptyRange`, and `Overlaps` must
  return false.
- An unbounded side counts as open. `a` and `b` overlap if (`a.LowerUnbounded` or
  `b.UpperUnbounded` or `a.Lower < b.Upper`) and (`b.LowerUnbounded` or
  `a.UpperUnbounded` or `b.Lower < a.Upper`).
- If either range returns `ErrOutOfRange`, the pair is outside the property.

A property test asserts this over generated ranges, including empty and one-sided ones
(BR-TR-10).

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
// NsBounds is the half-open interval [Lower, Upper) in int64 nanoseconds.
type NsBounds struct {
    Lower          int64 // inclusive; meaningful only when !LowerUnbounded
    Upper          int64 // exclusive; meaningful only when !UpperUnbounded
    LowerUnbounded bool
    UpperUnbounded bool
}
```

An unbounded side is a flag, not a sentinel such as `math.MinInt64`.

`NsBounds` checks each arithmetic step: `Seconds × 10⁹`, `+ Nanoseconds`, and the `+ 1`
adjustment. A finite bound whose adjusted value does not fit in `int64` returns
`ErrOutOfRange`. The representable values are `math.MinInt64` to `math.MaxInt64`
nanoseconds, which is `-9223372036:854775808` to `9223372036:854775807` on the wire. On
the TAI timeline, that is 1677-09-21T00:12:43.145224192 to 2262-04-11T23:47:16.854775807.
An empty range returns `ErrEmptyRange`.

No other package does nanosecond arithmetic on a timestamp. A CI step in the lint
workflow enforces this. The step fails if this command prints a line:

```sh
git grep -nwE '1_000_000_000|1000000000|1e9' -- 'internal/*.go' 'pkg/*.go' 'cmd/*.go' ':!*_test.go' ':!internal/timerange/'
```

The check covers the server code: non-test Go files under `internal/`, `pkg/`, and
`cmd/`, including SQL text inside Go strings. It excludes tests and this package. It
also excludes `tools/`, because those binaries are clients and load generators and are
not on the storage path. The check finds a literal, not every conversion, so review is
still necessary. Use `-w`, not `\b`: `git grep -E` does not support `\b`, and a pattern
with `\b` matches nothing. There is no
inverse function, because no code renders bounds back to text ([ADR-0039](../../../adr/0039-timerange-stored-as-client-string-with-half-open-bounds.md)
rule 5).

Tests: a table covers every marker combination, `[0:0]`, `10:0`, and one-sided ranges.
It also covers negative values inside one second, both overflow edges, and the spec
example of `[1:0_2:0)` followed by `[2:0]`. A property test compares `Overlaps` with the
bounds comparison, as BR-TR-07 defines it.

## BR-TR-11: Error sentinels (pending)
The package exports `ErrOutOfRange` and `ErrEmptyRange`. Callers match them with
`errors.Is`.
Callers map both to the `invalid-timerange` catalogue entry. The package does not import
`internal/apperror`.

## BR-TR-12: Canonical rendering is for derived values only (pending)
`TimeRange.String()` and `Timestamp.String()` produce a canonical string. The server uses
them only for values that it derives, such as the flow timerange and
`X-Paging-Timerange`. The server returns a client value exactly as the client sent it
(BR-META-10).

The canonical form follows these rules:
- `String()` writes `[ts]` for an instantaneous range, because the schema says "The
  short syntax is preferred".
- It writes `_` for eternity and `()` for an empty range.
- It writes a marker for every present timestamp, and no marker for an absent
  timestamp: `[5:0_`, `_10:0)`.
- It writes a timestamp as `{seconds}:{nanoseconds}` of the absolute value, with a
  leading `-` only if the value is less than zero. The value −1.5 s, stored as
  `{-2, 500000000}`, renders as `-1:500000000`. The value zero renders as `0:0`, so
  `String()` never writes `-0:0`, even for a client that sent `-0:0`.

## BR-TR-13: The accepted format only widens (pending)
A string that the parser accepts once stays accepted. The metastore parses stored client
strings again on read (BR-META-19). As a result, a narrower parser breaks existing rows. A change that
narrows the accepted format needs a data migration and a new ADR.

## BR-TR-14: Durations use the Timestamp type (pending)
TAMS uses the Timestamp type for durations too: `min_object_timeout` in `service.json` is
a `timestamp.json` value, and so are `ts_offset` and `last_duration`. The package exports
`TimestampFromDuration(d time.Duration) Timestamp`. The result is floor-normalised like
every other `Timestamp`, and callers render it with `Timestamp.String()`.

`durationToTAI` in `internal/httpx/handlers/root.go` builds this string with its own
arithmetic. It is removed, and `GetService` calls `TimestampFromDuration` instead. The old
function renders a negative duration as `-1:-500000000`. The configured presign expiry is
positive, so that defect cannot occur today.
