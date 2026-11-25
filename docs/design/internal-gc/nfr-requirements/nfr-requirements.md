---
unit: M16 internal/gc
stage: NFR Requirements
status: Draft (not implemented)
---

# Requirements — object garbage collection (FURPS)

This document uses the FURPS structure: Functionality, Usability, Reliability, Performance, and Serviceability. Each requirement has an identifier. Section 8 gives the acceptance criteria. The test plan ([`../functional-design/test-plan.md`](../functional-design/test-plan.md)) has one or more tests for each criterion.

A decision note has this form: *Decision. Rationale. Rejected.*

## 1. Problem statement and scope

OpenTAMS does not delete media bytes at this time. A segment delete decreases `objects.ref_count`, and the row stays at `ref_count = 0` (`internal/metastore/flows.go:428-449`, `internal/metastore/segments.go:505-577`). The function `objectstore.DeleteObjects` exists, but no code calls it (`internal/objectstore/objectstore.go:265`). `opentams gc` stops with `errGCNotImplemented` (`cmd/opentams/gc.go`). Thus the bucket grows without limit.

Bytes that a client uploads through `POST /flows/{flowId}/storage` and never registers have no row at all. No process can find these bytes from the database.

This change adds:

1. A record of each upload URL that the server issues (the `allocations` table).
2. A worker, `opentams gc`, with two sweeps. Sweep 1 deletes released objects. Sweep 2 deletes expired allocations that no segment used.
3. The corrections that make the worker safe: a correct `DeleteFlow`, a sorted lock sequence, and guards before each irreversible delete.

The out-of-scope list is in section 9.

## 2. Personas

| Persona | Need |
|---|---|
| Client developer | Uploads media and registers segments. Needs clear, stable errors when an object ID is not usable. |
| Operator (DevOps) | Deploys and monitors OpenTAMS. Needs health endpoints, metrics, alerts, configuration, and runbooks. |
| Support engineer | Answers "where did my media go?". Needs a log record of each deletion. |
| Maintainer | Changes the code later. Needs rules, tests, and decision records. |

## 3. Functionality

The functional rules are in [`../functional-design/business-rules.md`](../functional-design/business-rules.md). This section gives the summary and the main decisions.

| Area | Rules |
|---|---|
| Allocation record at `POST /flows/{flowId}/storage`, and service information | BR-GC-01 to BR-GC-09 |
| Allocation enforcement at segment registration | BR-GC-10 to BR-GC-17 |
| Segment and flow deletes | BR-GC-20 to BR-GC-23 |
| Sweep 1 (released objects) | BR-GC-30 to BR-GC-37 |
| Sweep 2 (expired allocations) | BR-GC-40 to BR-GC-46 |
| Schema and upgrade | BR-GC-50 to BR-GC-53 |
| Worker cycle, failures, and shutdown | BR-GC-60 to BR-GC-69 |
| Errors given to clients | BR-GC-70 to BR-GC-74 |

*Decision: keep a separate `allocations` table. Do not write an `objects` row at `/storage`. Rationale: an `objects` row at `ref_count = 0` means "released". If `/storage` writes the row, the row also starts at 0, and sweep 1 can delete it before the upload. ADR-0019 keeps the `objects` row lazy for this reason and for two other races (ADR-0041). Rejected: an `objects` row at allocation. Rejected: no record (the bytes leak).*

*Decision: registration deletes the allocation row in the same transaction ("use-once"). Rationale: an allocation row then means "not registered yet". Rejected: keep the row and delete it later.*

*Decision: delete a released object at the next cycle. Do not wait for a deadline. Rationale: upstream gives no minimum time. A deadline only moves the risk later. Rejected: a `reap_after` deadline. It stays a possible later improvement.*

*Decision: a controlled object ID that has no allocation row and no object row is not accepted at registration. Rationale: without this rule, a segment can refer to bytes that the GC does not know about. Rejected: a migration period with an enforcement flag.*

*Decision: the GC changes only rows and bucket keys that OpenTAMS owns. A BYOS object row is deleted without an object-store call. Rationale: OpenTAMS has no credentials for client storage and does not own it.*

## 4. Usability

- **NFR-GC-U1**: Each client error that this change adds has a stable problem type, an HTTP status, a title, and a detail text. The list is in BR-GC-70 to BR-GC-74 and in the interface specification.
- **NFR-GC-U2**: All problem types for object IDs use the prefix `object-`. The existing type `object-id-exists` changes to `object-exists`. The release notes must name this change as breaking.
- **NFR-GC-U3**: A detail text names the `object_id`. A detail text never names a different flow. Rationale: a future per-flow permission model must not show which flow owns an object.
- **NFR-GC-U4**: `opentams gc --help` describes the two sweeps, the long-running mode, the `--once` mode, and each environment variable that the command reads.
- **NFR-GC-U5**: The configuration keeps the existing style: environment variables only, no command-line flags except `--once`.
- **NFR-GC-U6** (audit): The GC writes one log line at level INFO for each row that it deletes. The line has the fields `object_id`, `storage_id`, `sweep`, and `outcome`. There is no setting to stop this line.

*Decision: one INFO line for each deleted object, always on. Rationale: a delete cannot be undone, and a log line is the cheapest searchable proof. REQ-SEC-12 uses logs for audit. Rejected: a per-batch line with all IDs (log pipelines cut long lines). Rejected: an audit table (more writes, and a dedicated audit log is Phase 2 work). Rejected: a setting to stop the line.*

## 5. Reliability

- **NFR-GC-R1**: The GC never deletes bytes that a segment refers to. Three guards apply:
  - The claim flag `deleting` (BR-GC-31, BR-GC-12).
  - A segment-count check before each sweep 1 claim (BR-GC-33).
  - An object-row check before each sweep 2 claim (BR-GC-42).
- **NFR-GC-R2**: If the process stops at any point of a batch, no data is lost. The rows stay `deleting = true`, and the next cycle completes the delete (BR-GC-66).
- **NFR-GC-R3**: An object-store delete for a key that does not exist is a success. Thus each delete can be done again.
- **NFR-GC-R4**: A batch-level error stops the current cycle. The sweep starts again after `GC_POLL_INTERVAL` (BR-GC-62).
- **NFR-GC-R5**: Each database statement of the GC has a timeout of 30 s. Each object-store call has a timeout of 60 s. These values are code constants.
- **NFR-GC-R6**: `/healthz` returns 200 while the process operates. It does not examine the database or the object store. `/readyz` returns 200 only if the database and the object store are available, and the process did not receive SIGTERM.
- **NFR-GC-R7**: On SIGTERM, the GC claims no new batch. It lets the current batch complete for a maximum of `SERVER_GRACEFUL_SHUTDOWN_PERIOD`, and then it cancels the batch.
- **NFR-GC-R8**: Two or more GC replicas can operate at the same time. `FOR UPDATE SKIP LOCKED` divides the work. A duplicate delete of the same row is harmless (BR-GC-65).
- **NFR-GC-R9**: All transactions that change object rows lock them in ascending `object_id` sequence. This prevents deadlocks (SQLSTATE `40P01`) between requests that share objects (BR-GC-23).

*Decision: retry a failed object one time in each cycle, with no attempt limit. Rationale: a permanent error must stay visible, and a metric with an alert shows it. A hot loop must not occur. Rejected: an attempt counter (one more column, and the row still needs a person to act).*

*Decision: no lease column for retry rows. Rationale: when two replicas retry the same row, the second object-store delete is a success, and the second row delete changes 0 rows. Rejected: a `deleting_since` column (one more column and one more setting, for a harmless duplicate).*

## 6. Performance

- **NFR-GC-P1**: Each sweep operates as a drain loop. It claims batches one after the other until a batch has fewer rows than `GC_BATCH_SIZE`. Then it stays idle for `GC_POLL_INTERVAL`.
- **NFR-GC-P2**: `GC_BATCH_SIZE` has the default 1000 and the range 1 to 1000. 1000 is the maximum number of keys in one S3 `DeleteObjects` call. Thus one batch makes one object-store call.
- **NFR-GC-P3**: `GC_POLL_INTERVAL` has the default 15m. It is the maximum time between the release of an object and the start of its delete when the GC is idle.
- **NFR-GC-P4**: Sweep 1 and sweep 2 operate in two separate loops. A large backlog in one sweep does not delay the other sweep.
- **NFR-GC-P5**: The change must not move the API outside the budgets of `docs/scale-test-plan.md` §6.5. The scale cases S1, S3, S4, and S5 measure this. A result outside a budget goes to the maintainers for a decision. It does not change the design automatically.
- **NFR-GC-P6**: A new index `segments_object_id_idx` on `segments (object_id)` makes the segment-count check and the foreign-key check of an `objects` delete use an index scan.

*Decision: a drain loop with no pause between batches. Rationale: one batch per cycle deletes too few objects (about 67 objects each minute at 1000 for each 15 minutes). Rejected: a pause setting between batches. The batch size and the replica count already control the load. A pause can be added later.*

*Decision: poll interval 15m and batch size 1000, both configurable. Rationale: the idle cost is small at each value. 15m keeps the "GC is down" alert at about 40 minutes. Rejected: 30m (slower alert, and no reliable gain).*

## 7. Serviceability

- **NFR-GC-S1**: `opentams gc` opens an admin HTTP listener on `SERVER_PORT` with `/healthz`, `/readyz`, and `/metrics`. It does not serve the TAMS API.
- **NFR-GC-S2**: The metrics are in the interface specification. Prometheus collects them through a ServiceMonitor endpoint for the GC pods.
- **NFR-GC-S3**: The configuration is in the interface specification. Each new setting has a default and a validation rule. The process does not start if a value is not valid.
- **NFR-GC-S4**: The Helm chart deploys the GC as a Deployment by default (`gc.enabled: true`). The chart has the values `gc.replicaCount`, `gc.pollInterval`, `gc.batchSize`, `gc.allocationGrace`, and `gc.resources`.
- **NFR-GC-S5**: The runbooks R1 to R6 in [`../../../runbooks/gc.md`](../../../runbooks/gc.md) use the format of REQ-OBS-08. Each documented alert links to one runbook.
- **NFR-GC-S6**: `opentams gc --once` does one cycle of each sweep and stops. It returns exit code 0 if no cycle stopped because of a batch-level error. It returns exit code 1 in the other case.
- **NFR-GC-S7**: The release notes of this change must contain the upgrade procedure in BR-GC-50 and the list of breaking changes in BR-GC-53.

*Decision: an admin listener, not a push gateway. Rationale: the GC is a long-running process. Prometheus uses pull. Kubernetes probes need an HTTP endpoint because the image has no shell. Long-running workers in Kubernetes (controllers, kube-proxy, Envoy, Fluent Bit) use this pattern. Rejected: Pushgateway (it is for short jobs, and it hides a dead process). Rejected: OTLP push (it needs a collector in each deployment).*

## 8. Acceptance criteria

Each criterion is concrete and has one or more tests in the test plan.

| ID | Criterion | Tests |
|---|---|---|
| AC-GC-01 | `POST /flows/{flowId}/storage` with `{"limit": 2}` returns 201 with 2 objects, and the database has 2 rows in `allocations` with that `flow_id` and `expires_at = now() + OBJECT_STORE_PRESIGN_EXPIRY` (tolerance 5 s). | TC-GC-001, TC-GC-002 |
| AC-GC-02 | `POST /flows/{flowId}/storage` with an `object_ids` entry that has an object row returns 400 with type `…/object-exists`. | TC-GC-005 |
| AC-GC-03 | A second request for the same `object_ids` entry on the same flow returns 201 and moves `expires_at` later. On a different flow it returns 400 `…/object-wrong-flow`. | TC-GC-006, TC-GC-007 |
| AC-GC-04 | A segment registration with a controlled `object_id` that has no allocation row and no object row returns 200 with one entry in `failed_segments` of type `…/object-not-allocated`. | TC-GC-020 |
| AC-GC-05 | A segment registration that uses an allocation deletes the allocation row and writes an object row with `ref_count` equal to the number of segments in the request that use the ID. | TC-GC-021, TC-GC-024 |
| AC-GC-06 | A segment registration with an ID that the GC claimed returns `failed_segments` with type `…/object-deleting`. No response has status 409 with type `object-reaping`. | TC-GC-026, TC-GC-027 |
| AC-GC-07 | `DELETE /flows/{flowId}` decreases `ref_count` by the number of segments of the flow, also if one object is in two segments of the flow. | TC-GC-040 |
| AC-GC-08 | A flow delete and a timerange delete on the same flow at the same time give a correct `ref_count`. | TC-GC-041, TC-GC-042 |
| AC-GC-09 | After a cycle, a released controlled object has no row and no bucket key. One INFO log line records it. | TC-GC-060, TC-GC-140 |
| AC-GC-10 | Sweep 1 does not delete an object whose `ref_count` is 0 while a segment refers to it. It sets `ref_count` to the real count and increments `opentams_gc_refcount_drift_total`. | TC-GC-063 |
| AC-GC-11 | Sweep 2 deletes the bucket key of an allocation after `expires_at + GC_ALLOCATION_GRACE`, and not before. | TC-GC-080, TC-GC-081 |
| AC-GC-12 | Sweep 2 never deletes bytes for an ID that has an object row. It deletes only the allocation row. | TC-GC-083 |
| AC-GC-13 | An object-store error for each key of a batch stops the cycle, and the rows stay `deleting = true`. The next cycle deletes them. | TC-GC-100, TC-GC-101 |
| AC-GC-14 | `opentams gc --once` returns exit code 0 after a cycle with no batch-level error, and exit code 1 after a cycle with a batch-level error. | TC-GC-120, TC-GC-121 |
| AC-GC-15 | `GET /service` returns `min_presigned_url_timeout` with the same value as `min_object_timeout`. | TC-GC-012 |
| AC-GC-16 | The process does not start if `OBJECT_STORE_PRESIGN_EXPIRY` is less than 300 s, if `GC_BATCH_SIZE` is outside 1 to 1000, if `GC_POLL_INTERVAL` is 0 or less, or if `GC_ALLOCATION_GRACE` is less than 0. | TC-GC-150 to TC-GC-154 |
| AC-GC-17 | After migration 000006, the column `objects.deleting` exists, `objects.reaping` does not exist, and the partial indexes use `deleting`. | TC-GC-170 |

## 9. Deferred items and open questions

These items are not in this change. The last column names the document that records each item.

| Item | Why deferred | Where it continues |
|---|---|---|
| Asynchronous delete (`202`, `/flow-delete-requests`, hidden flow during purge) | It is a separate feature. It uses the GC Deployment later. Until then, a very large delete can reach the 60 s write timeout (`internal/server/server.go:40`). | `docs/conformance.md`, flow delete requests |
| Retention (`segment_retention_offset`, `flow_retention_offset`, empty flows) | It follows upstream ADR-0043 and upstream application note 0019 as a client of the delete API. | `docs/requirements.md` §4.12 |
| Reconciliation of bucket keys with no row | Such bytes cost storage. They do not cause data loss. | REQ-REL-13, REQ-REL-14 |
| A deadline before deletion (`reap_after`) | Upstream gives no minimum time. It can be added later with an additive migration. | ADR-0037 |
| Mixed objects (`get_urls` on a controlled object) | The shape rule (ADR-0026) classifies such a segment as BYOS. Sweep 2 keeps the bucket bytes of such an ID (BR-GC-42). | ADR-0041 |
| Re-request rule for `object_ids` | Rule (a) is the lenient reading of upstream. A change to "reject all" is breaking after v1.0.0. Examine it again before v1.0.0. | ADR-0041 |
| Grace time for presigned URLs | URLs are signed with exactly the advertised time. The difference is milliseconds. No change. | — |
