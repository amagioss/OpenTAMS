---
status: "proposed"
date: 2026-09-28
decision-makers: OpenTAMS maintainers
consulted: OpenTAMS maintainers
informed: OpenTAMS contributors
---

# Require a bounded, non-empty, start-inclusive segment timerange, and keep the database constraints as backstops

## Context and Problem Statement

[ADR-0015](0015-gist-exclusion-constraint.md) says the exclusion constraint makes
non-overlap hold "no matter what the application does". That is not true. The constraint
compares the bounds that the application writes. If the bounds are wrong, it compares
wrong numbers. An empty `int8range(x, x)` overlaps nothing, so an empty row passes it.

Migration `000005` added `CHECK (upper_ns IS NULL OR upper_ns > 0)`. That check is wrong
in both directions. It rejects a valid segment that ends before `0:0`, because TAMS
permits negative timestamps. It accepts an empty range such as `(5000000000, 5000000000)`.
Its comment says that zero-width ranges cannot occur. They can.

The two overlap checks also disagree. The within-batch check uses `TimeRange.Overlaps`,
which honours the markers. The check against stored rows uses the integer bounds. So two
segments can be rejected in one batch and accepted in two.

Last, the parser accepts timeranges that cannot describe a segment. TAMS
[App Note 0012](https://github.com/bbc/tams/blob/main/docs/appnotes/0012-using-flow-segment-timeranges.md)
says:

> The Flow Segment `timerange` start is inclusive and is the Timestamp of the first
> sample contained in the Flow Segment.
>
> If the samples have a duration then the `timerange` end is exclusive and is the last
> sample Timestamp plus duration.
>
> If the samples don't have a duration then the `timerange` end is the last sample
> Timestamp and is inclusive.

and "samples don't generally exist into infinity but a query could use infinity to apply
no limits". The segment schema regex does not enforce any of this.

Which segment timeranges does OpenTAMS accept, and what do the database constraints
guarantee?

## Considered Options

* Accept every timerange that matches the schema regex, and enforce only non-overlap
* Require a bounded, non-empty, start-inclusive segment timerange in the service, and
  keep the database constraints as backstops
* Keep ADR-0015 and the `000005` check as they are

## Decision Outcome

Chosen option: "Require a bounded, non-empty, start-inclusive segment timerange in the
service, and keep the database constraints as backstops", because it matches what TAMS
says a segment is, and it gives the exclusion constraint an invariant it can rely on.

The rules:

1. **Segment timerange policy.** A segment timerange must have a start and an end, the
   start must be inclusive, and the range must not be empty. The end can be inclusive or
   exclusive. This is an OpenTAMS policy. It is consistent with App Note 0012, but the
   schema regex does not require it.
2. **How a violation is reported.** A segment that breaks rule 1, or whose bounds do not
   fit in `int64` nanoseconds, is a per-segment failure with type `invalid-timerange` in a
   200 `flow-segment-bulk-failure` response. It is not a 400 for the whole request. TAMS
   says that "processing should continue" and that "A 200 response should be returned
   listing the failed Segments". A string that fails the schema regex is still a 400 for
   the whole request, from spec validation.
3. **Validation comes first.** The service rejects every invalid segment before it calls
   the store. No client input can reach a database constraint. A constraint violation is
   therefore a server defect. It returns 500, and the idempotency key is released
   (BR-IDMP-02, unchanged).
4. **Schema.** `upper_ns` is `NOT NULL`. The check `segments_upper_ns_positive` is
   replaced by:

   ```sql
   CONSTRAINT segments_bounds_nonempty CHECK (upper_ns > lower_ns)
   ```

   The `no_segment_overlap` exclusion constraint stays as it is. Its
   `COALESCE(upper_ns, …)` has no effect once `upper_ns` is `NOT NULL`.
5. **One overlap rule.** The within-batch check and the check against stored rows both
   compare `timerange.NsBounds` results as half-open intervals
   ([ADR-0039](0039-timerange-stored-as-client-string-with-half-open-bounds.md)).
   `TimeRange.Overlaps` can stay for other callers, and a property test must show that
   it agrees with the bounds comparison.
6. **What the constraints guarantee.** Together, the check and the exclusion constraint
   guarantee that no stored row is empty or inverted, and that no two stored rows on a
   flow intersect as integer ranges. They do not detect a range that was converted
   incorrectly. Only the tests for `timerange.NsBounds` cover that.
7. **Migration.** `000005` is edited in place, not followed by a new migration, because
   OpenTAMS is an alpha. `golang-migrate` records only the version number, so a database
   already at version 5 does not run the edited file again. Operators of databases
   created from `v0.1.0-alpha.0` must recompute the bounds, remove any empty or
   open-ended rows, and then apply the schema change in rule 4 by hand.

### Consequences

* Good, because the TAMS example of an instantaneous data segment, `[0:0]`, is accepted,
  and a segment that ends before `0:0` is no longer rejected.
* Good, because the exclusion constraint cannot be bypassed by an empty range.
* Good, because the batch outcome no longer depends on how the client splits its
  requests.
* Good, because a client error never becomes a 500.
* Bad, because a client written against another TAMS implementation can send `[5:0_` or
  `(0:0_10:0)` and receive a failure only from OpenTAMS. This is recorded in
  [`../conformance.md`](../conformance.md).
* Bad, because a `v0.1.0-alpha.0` database stays on the old constraint until an operator
  changes it by hand. Version 5 means two different schemas until then.
* Bad, because the non-overlap rule still exists in two places, the service path and the
  database, as ADR-0015 already noted.

## More Information

* Supersedes [ADR-0015](0015-gist-exclusion-constraint.md) when accepted.
* Batch rejection on overlap is unchanged: [ADR-0024](0024-whole-batch-reject-on-segment-overlap.md).
* Per-segment failure shape: [ADR-0023](0023-rfc9457-problem-details-for-per-segment-failures.md).
* Requirements: `REQ-BEH-14`, `REQ-BEH-15` in [`../requirements.md`](../requirements.md).
* Design: [`internal-service-segment`](../design/internal-service-segment/functional-design/functional-design.md)
  BR-SEG-02 and BR-SEG-14,
  [`internal-metastore`](../design/internal-metastore/functional-design/business-rules.md)
  BR-META-05 and the schema.
* Migration: [`../../migrations/000005_objects_storage_id_reaping.up.sql`](../../migrations/000005_objects_storage_id_reaping.up.sql).
