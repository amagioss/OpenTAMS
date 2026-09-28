---
status: "proposed"
date: 2026-09-28
decision-makers: OpenTAMS maintainers
consulted: OpenTAMS maintainers
informed: OpenTAMS contributors
---

# Parse timestamps and timeranges by the TAMS grammar, and use the BBC reference library where the grammar is silent

## Context and Problem Statement

TAMS defines the timestamp and timerange syntax in `timestamp.json`, `timerange.json`,
and [App Note 0008](https://github.com/bbc/tams/blob/main/docs/appnotes/0008-timestamps-in-TAMS.md).
The parser in `internal/timerange` accepts a narrower and partly different language:

| Input | TAMS meaning | Parser today |
|---|---|---|
| `-1:500000000` | −1.5 s | −0.5 s |
| `-0:500000000` | −0.5 s | Rejected as "negative zero" |
| `(5:0_`, `[0:0_`, `_10:0)` | One side extends to infinity | Rejected as "bad brackets" |
| `10:0` | Instantaneous, same as `[10:0]` | Rejected |
| `[_10:0)` | Marker ignored, start unbounded | Rejected |
| `[10:0_5:0)` | Empty range | Parse error |

The negative-timestamp error also inverts the order of two timestamps inside one negative
second, so overlap and containment are wrong for negative ranges.

App Note 0008 does not state two rules: how the sign applies, and what a missing marker
next to a timestamp means. What does OpenTAMS do for those two?

## Considered Options

* Follow App Note 0008, and follow the BBC reference library `mediatimestamp` where the
  note is silent
* Follow App Note 0008, and reject every form the note does not define
* Keep the current parser and record each difference as a divergence

## Decision Outcome

Chosen option: "Follow App Note 0008, and follow `mediatimestamp` where the note is
silent", because it accepts every string a conformant client can send and gives each one
the meaning BBC's own code gives it.

The rules:

1. **Timestamp value.** The sign applies to the whole value:
   `value = sign × (seconds × 10⁹ + nanoseconds)`. `-1:500000000` is −1.5 s.
   `-0:500000000` is −0.5 s and is valid. `-0:0` is valid and equals `0:0`. This follows
   `mediatimestamp`, which computes `sign * int(sec * MAX_NANOSEC + ns)`.
2. **Internal form.** `Timestamp{Seconds, Nanoseconds}` is floor-normalised:
   `Nanoseconds` is in `[0, 999_999_999]` and the value is
   `Seconds × 10⁹ + Nanoseconds`. −1.5 s is `{Seconds: -2, Nanoseconds: 500000000}`.
   Only `ParseTimestamp` and `String` deal with the sign.
3. **Grammar.** `{start marker}{start timestamp}_{end timestamp}{end marker}`, where each
   part is optional, plus `_` (eternity), `()` (empty), and a single timestamp with `[]`
   or with no markers (instantaneous).
4. **Omitted timestamp.** The range extends to infinity on that side. A marker on that
   side is ignored (App Note 0008: "The marker is ignored and should normally be omitted
   when omitting the Timestamp").
5. **Omitted marker next to a timestamp.** The bound is inclusive. `0:0_10:0` is
   `[0:0_10:0]`. This follows `mediatimestamp`, where only `(` and `)` clear
   inclusivity.
6. **Empty ranges.** End before start, or end equal to start with an exclusive marker,
   parses as an empty range, not as an error (App Note 0008: such a range "should be
   treated as an empty TimeRange by implementations"). Each caller decides what an empty
   range means for its operation.
7. **Canonical rendering.** `String()` is used only for values the server derives, never
   for values a client sent (ADR-0039). It writes `[ts]` for an instantaneous range
   (the schema says "The short syntax is preferred"), `_` for eternity, `()` for empty,
   a marker for every present timestamp, and no marker for an absent one.
8. **The grammar only widens.** A string that the parser accepts once must stay accepted.
   Stored client strings are parsed again on read, so a narrower parser would break
   existing rows.

### Consequences

* Good, because every string that matches the TAMS schema regex parses, and parses to
  the value BBC's reference code gives it.
* Good, because comparison and arithmetic stay plain integer operations on a
  floor-normalised value. The sign is handled in two functions only.
* Good, because the parser no longer rejects valid timestamps between −1 s and 0 s.
* Bad, because the parser now returns empty ranges that it rejected before. Every caller
  must handle them. ADR-0039 and ADR-0040 say how.
* Bad, because two rules rest on a library, not on the specification text. If BBC
  clarifies App Note 0008 differently, this ADR must be superseded.
* Bad, because rule 8 makes a future tightening of the grammar a data migration, not a
  code change.

## More Information

* Requirements: `REQ-TEST-03` in [`../requirements.md`](../requirements.md).
* Specification: [`timestamp.json`](../../api/schemas/timestamp.json),
  [`timerange.json`](../../api/schemas/timerange.json),
  [App Note 0008](https://github.com/bbc/tams/blob/main/docs/appnotes/0008-timestamps-in-TAMS.md).
* Reference library: [`mediatimestamp`](https://github.com/bbc/rd-apmm-python-lib-mediatimestamp),
  `immutable/timestamp.py` (`from_sec_nsec`, `__init__`) and `immutable/timerange.py`
  (`from_str`).
* Design: [`internal-timerange` business rules](../design/internal-timerange/functional-design/business-rules.md)
  BR-TR-01 to BR-TR-06 and BR-TR-12 to BR-TR-13.
* Used by: [ADR-0039](0039-timerange-stored-as-client-string-with-half-open-bounds.md),
  [ADR-0040](0040-segment-timerange-invariants.md).
