---
unit: M16 internal/gc
stage: Design index
status: Draft (not implemented)
---

# Object garbage collection (GC) — design index

This folder is the complete design for the object garbage collector (GC) of OpenTAMS. It is the input for the code change. A reader with no other project context must be able to write the code and the tests from this folder and the documents that it links.

Status: implemented. `internal/gc`, `internal/metastore`, and `cmd/opentams/gc.go` do the behaviour that this folder gives.

The text follows ASD-STE100 Simplified Technical English. Code, SQL, identifiers, commands, and quoted text keep their original form.

## Read the documents in this sequence

| Step | Document | What it gives you |
|---|---|---|
| 1 | [`nfr-requirements/nfr-requirements.md`](nfr-requirements/nfr-requirements.md) | The problem, the scope, the personas, and the FURPS requirements with acceptance criteria |
| 2 | [`functional-design/business-rules.md`](functional-design/business-rules.md) | Each rule `BR-GC-*` that the code must obey |
| 3 | [`functional-design/business-logic-model.md`](functional-design/business-logic-model.md) | The schema, the SQL, the algorithms, the package layout, and the list of changes outside `internal/gc` |
| 4 | [`functional-design/interface-spec.md`](functional-design/interface-spec.md) | The external contract: HTTP errors, CLI, health, metrics, logs, and configuration |
| 5 | [`functional-design/test-plan.md`](functional-design/test-plan.md) | Each test `TC-GC-*` with its sample input and its expected result |
| 6 | [`nfr-requirements/tech-stack-decisions.md`](nfr-requirements/tech-stack-decisions.md) | The libraries. The change adds no new dependency. |

Related decision records:

- [ADR-0041](../../adr/0041-track-storage-allocations-in-a-table.md): track `/storage` allocations in a table.
- [ADR-0037](../../adr/0037-object-garbage-collection.md): reclaim object storage from a separate worker.
- [ADR-0020](../../adr/0020-gc-owns-object-lifetime.md): only the GC deletes media bytes.
- [ADR-0019](../../adr/0019-lazy-object-registration.md): the `objects` row appears at segment registration.
- [ADR-0017](../../adr/0017-expand-contract-schema-evolution.md): expand-contract schema changes. This change has one known deviation (BR-GC-50).

Operator procedures: [`../../runbooks/gc.md`](../../runbooks/gc.md).

## Scope in one table

| In scope (this change) | Out of scope (later work) |
|---|---|
| Sweep 1: delete the bytes and the row of each released object (`ref_count = 0`) | Asynchronous delete (`202` and `/flow-delete-requests`) |
| Sweep 2: delete the bytes of each expired allocation that no segment registered | Retention (loop recording, flow retention, empty-flow clean-up) |
| The `allocations` table, and allocation enforcement at segment registration | Reconciliation of bucket keys that have no row |
| The `DeleteFlow` correction, and the sorted lock sequence | Separate physical keys, multiple backends, init objects |
| The `deleting` name for the claim flag | A deadline before deletion (`reap_after`) |
| The `opentams gc` worker, its admin listener, metrics, logs, and Helm resources | Mixed controlled and uncontrolled objects (`get_urls` on a controlled object) |

## Terms

These terms have one meaning in all documents of this folder.

| Term | Meaning |
|---|---|
| Allocation | A row in the `allocations` table. `POST /flows/{flowId}/storage` writes it. It records an object ID that the server issued an upload URL for, before a segment uses that ID. |
| Object row | A row in the `objects` table. Segment registration writes it (ADR-0019). |
| Controlled object | An object row with `storage_id` NOT NULL. OpenTAMS owns the bytes in its bucket. |
| BYOS object | An object row with `storage_id` NULL ("bring your own storage"). The client owns the media. OpenTAMS has only URLs. |
| Released object | An object row with `ref_count = 0`. No segment refers to it. |
| Claim | The GC sets `deleting = true` and `claimed_at = now()` on a row and commits. While the row exists, no client request can use that ID. After the GC deletes the row, a client can allocate the ID again. |
| Claim lease | The time (5 minutes) after `claimed_at` during which no other GC process takes a claimed row (BR-GC-65). |
| Sweep 1 | The GC process that deletes released objects. Metric label `sweep="released"`. |
| Sweep 2 | The GC process that deletes expired allocations. Metric label `sweep="allocation"`. |
| Batch | One set of up to `GC_BATCH_SIZE` rows that the GC claims and deletes together. |
| Cycle | One wake-up of one sweep. A cycle does the retry phase, then claims batches until the backlog is empty, then the sweep stays idle for `GC_POLL_INTERVAL`. |
| Retry row | A row with `deleting = true`. A previous batch claimed it, but the GC did not complete the delete. |
| Batch-level error | An error that applies to the full batch: a database error, an `ErrAuth` error from the object store, or an error for each object in one object-store call. |
| Comeback | A segment registration that uses an object ID again after its `ref_count` became 0, before the GC claims it. Upstream TAMS permits this (upstream API, segment modification by delete and register again). |
| Drift | The stored `ref_count` is not equal to the number of segment rows that refer to the object. |
| Leftover allocation | An allocation row for an ID that also has an object row. |
| Upstream | The BBC TAMS specification, `github.com/bbc/tams`. Upstream ADR numbers are written "upstream ADR-NNNN". They are different from the OpenTAMS ADR numbers. |
