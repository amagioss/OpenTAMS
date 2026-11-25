---
unit: M16 internal/gc
stage: Functional Design
status: Draft (not implemented)
---

# Business Rules — object garbage collection

Each rule has an identifier `BR-GC-NN`. The code must obey each rule. The terms (allocation, claim, sweep, cycle, batch, and more) have the meanings in [`../README.md`](../README.md#terms). The SQL for each rule is in [`business-logic-model.md`](business-logic-model.md).

In this document, "the configured storage ID" is the value of the environment variable `OPENTAMS_OBJECT_STORE_ID`. Its default is `default` (`cmd/opentams/serve.go:168-171`). The API server and the GC must read the same value.

## Allocation record at `POST /flows/{flowId}/storage`

### BR-GC-01: Each issued upload URL gets an allocation row
When the server returns an upload URL for an object ID, it also writes one row in `allocations` for that ID. The row has these values:

| Column | Value |
|---|---|
| `id` | The object ID |
| `flow_id` | The flow ID of the request path |
| `storage_id` | The configured storage ID |
| `expires_at` | Database time `now()` plus `OBJECT_STORE_PRESIGN_EXPIRY` |
| `created_at` | Database time `now()` |
| `deleting` | `false` |
| `claimed_at` | NULL |

Use the database clock for `expires_at`. The sweep compares `expires_at` with the database clock. Thus a clock difference between the application and the database has no effect.

### BR-GC-02: Mode `limit` writes new rows
In mode `{"limit": N}`, the server generates N new UUIDv4 values (`uuid.New().String()`, as today). It writes N new allocation rows.

### BR-GC-03: Mode `object_ids` uses one decision table for each ID
In mode `{"object_ids": [...]}`, the server uses this table for each supplied ID X. Examine the rows in the sequence of the table. The first row that matches gives the result.

| # | Condition | Result |
|---|---|---|
| 1 | An object row for X exists and has `deleting = true` | Reject the request: 400 `object-deleting` |
| 2 | An object row for X exists and has `deleting = false` | Reject the request: 400 `object-exists` |
| 3 | An allocation row for X exists and has `deleting = true` | Reject the request: 400 `object-deleting` |
| 4 | An allocation row for X exists for a different flow | Reject the request: 400 `object-wrong-flow` |
| 5 | An allocation row for X exists for the same flow | Set `expires_at` to `now()` plus `OBJECT_STORE_PRESIGN_EXPIRY`. Return a new upload URL. |
| 6 | No row for X | Write a new allocation row (BR-GC-01) |

Row 5 is "re-request rule (a)". It lets a client get a new URL after an upload error. Row 2 replaces the check that reads `segments` today (`internal/metastore/segments.go:791-800`). An object row is the correct source, because a released object has no segment but still has a row.

### BR-GC-04: One request is all or nothing
The server writes all allocation rows of one request in one transaction. If one ID gets a rejection, the transaction stops, no row changes, and the response is the error of that ID. This keeps the existing rule "no partial allocation" (NFR-SVC-STG-R2).

### BR-GC-05: Generate the URLs before the transaction
The server generates all upload URLs before it starts the transaction. If a URL generation gives an error, the server does not start the transaction and returns the error. URL generation is a local signature. It makes no network call.

### BR-GC-06: Lock the IDs in ascending byte order
Inside the transaction, the server processes the IDs in ascending byte order (BR-GC-23). The response lists the objects in the sequence of the request.

### BR-GC-07: Flow checks do not change
A missing flow gives 404 and a read-only flow gives 403, as today. The flow check does not lock the flow row. If a flow delete occurs at the same time, an allocation row for the deleted flow can stay. Sweep 2 deletes it after it expires.

### BR-GC-08: Allocation rows have no foreign key to `flows`
A flow delete does not delete allocation rows. Rationale: with `ON DELETE CASCADE`, uploaded bytes lose their only record. With `RESTRICT`, a flow delete is blocked until the allocations expire. Known effect: a flow that a client creates again with the same ID can use the allocation rows of the old flow. Upstream flow IDs are client UUIDs, so the same ID means the same logical flow.

### BR-GC-09: Service information and presign time
- `GET /service` returns `min_presigned_url_timeout` with the same value as `min_object_timeout`. Both values come from `OBJECT_STORE_PRESIGN_EXPIRY`. The generated field `MinPresignedUrlTimeout` already exists (`gen/api/opentams.gen.go:1686`).
- The process does not start if `OBJECT_STORE_PRESIGN_EXPIRY` is less than 300 s. Upstream requires `min_object_timeout` of `300:0` or more (upstream `service.json`). This rule applies to `serve` and to `gc`.

## Allocation enforcement at segment registration

### BR-GC-10: The shape rule classifies each segment
The classification does not change (ADR-0026, BR-META-07). A segment with no `get_urls` is controlled. A segment with one or more `get_urls` is BYOS.

### BR-GC-11: Group the segments by object ID
In `InsertSegments`, after the overlap checks (steps 1 and 2 of today), group all segments of the request by `object_id`. For each group, n is the number of segments in the group. A group is controlled if one or more of its segments is controlled. Else the group is BYOS. Process the groups in ascending byte order of `object_id` (BR-GC-23). A controlled group uses BR-GC-12 and BR-GC-13. A BYOS group uses BR-GC-14. The result of a group applies to each segment of the group.

### BR-GC-12: Decision table for each controlled group
For each controlled group with object ID X and count n, use this table. The first row that matches gives the result.

| # | Condition | Result |
|---|---|---|
| 1 | An object row for X exists and has `deleting = false` | Add n to `ref_count`. Accept the segments. This is reuse. It is correct also if the object row belongs to a different flow. |
| 2 | An object row for X exists and has `deleting = true` | Reject each segment of the group: `object-deleting` |
| 3 | No object row. An allocation row for X has `deleting = false` and the same `flow_id` as the request | Delete the allocation row. Write an object row with `ref_count = n`, `storage_id` from the allocation row, and `deleting = false`. Accept the segments. |
| 4 | No object row. An allocation row for X has `deleting = true` | Reject each segment of the group: `object-deleting` |
| 5 | No object row. An allocation row for X has a different `flow_id` | Reject each segment of the group: `object-wrong-flow` |
| 6 | No object row and no allocation row | Reject each segment of the group: `object-not-allocated` |

Row 3 also applies after `expires_at`, while `deleting = false`. Thus a late registration is accepted until sweep 2 claims the allocation.

### BR-GC-13: Concurrency in the decision table
- Lock the object row with `SELECT … FOR UPDATE` before you use it.
- If there is no object row, lock the allocation row with `SELECT … FOR UPDATE`.
- If there is no allocation row at this point, read the object row one more time. A different transaction can have used the allocation and committed. If the object row exists, use row 1 or row 2. If it does not exist, use row 6.
- Write the new object row of row 3 with `INSERT … ON CONFLICT (id) DO NOTHING`. If the insert writes no row, use rows 1 and 2 one more time.

### BR-GC-14: BYOS segments
- A BYOS group has no allocation check (ADR-0041).
- Before the object upsert, if an allocation row for the ID exists, lock it with `SELECT … FOR SHARE`. If it has `deleting = true`, reject each segment of the group with `object-deleting`. Rationale: sweep 2 is about to delete the bucket key of that ID.
- The upsert adds n to `ref_count`. It keeps its predicate `WHERE objects.deleting = false`. If the upsert returns no row, reject each segment of the group with `object-deleting`.
- The BYOS path never deletes an allocation row. Sweep 2 deletes it later as a leftover allocation (BR-GC-42).

### BR-GC-15: A rejection applies to one segment, not to the request
A segment with a rejection from BR-GC-12 or BR-GC-14 is not written. The other segments of the request continue. The response is 200 with `failed_segments`, as for other per-segment errors today (`internal/httpx/handlers/segments.go:316-345`). Each entry has the problem type, the title, and a detail text (BR-GC-70 to BR-GC-74). An entry has no `status` field (`gen/api/opentams.gen.go:1266`). The store returns the rejections in `InsertResult.RejectedIndices` and `InsertResult.RejectReasons` (`internal/metastore/segments.go:57-69`).

If the store accepts no segment of the request, it rolls back the transaction and returns the rejections. Then the flow row does not change, and `segments_updated` keeps its value. Rationale: a rejected group writes no row. A commit changes only `segments_updated`, and the retention rules use that value (REQ-RET-02).

The object timerange check (step 3.5 of `InsertSegments`) examines only the accepted segments. A rejected ID has no stored segment, so the check of a rejected segment finds nothing.

### BR-GC-16: Errors for the full request do not change
These errors still stop the full request before BR-GC-12, with no change to the database:

| Error | Status |
|---|---|
| Flow not found | 404 |
| Flow is read-only | 403 |
| Segment overlap (ADR-0024) | 422 `segment-overlap` |

The 409 response with type `object-reaping` (`internal/httpx/handlers/segments.go:302-305`) is removed. Its case is now a per-segment `object-deleting` entry.

### BR-GC-17: The object row gets `storage_id` from the allocation
A new controlled object row gets `storage_id` from its allocation row. It does not get it from `InsertBatch.ControlledStorageID`. `ControlledStorageID` stays in use only for the error `ErrControlledStorageNotConfigured`.

## Segment and flow deletes

### BR-GC-20: `DeleteFlow` locks the flow and counts each segment
`DeleteFlow` does these steps in one transaction:

1. Lock the flow row with `SELECT read_only FROM flows WHERE id = $1 FOR UPDATE`. If there is no row, return "flow not found". If `read_only` is true, return `apperror.ErrReadOnly` with the detail "flow is read-only", and change nothing. The handler gives 403 `read-only`.
2. Delete all segments of the flow with `DELETE … RETURNING object_id`. Count the rows for each `object_id`.
3. Lock the affected object rows in ascending byte order (BR-GC-23). Decrease each `ref_count` by its count.
4. Delete the flow row.

This corrects four errors of the current code (`internal/metastore/flows.go:433-468`):

| Error | Cause | Effect |
|---|---|---|
| B1 | The decrease is 1 for each distinct object, but each segment added 1 | `ref_count` stays too high. The bytes never get deleted. |
| B2 | No flow lock. A timerange delete at the same time can delete a segment that `DeleteFlow` also counts. | `ref_count` becomes too low. Sweep 1 can delete bytes that a different flow uses. |
| B8 | No flow lock. A segment insert after the first read is deleted by the cascade with no decrease. | `ref_count` stays too high. |
| B9 | No read-only check | The delete removes a read-only flow. REQ-BEH-11 and BR-META-15 require 403. |

Insert and timerange delete already lock the flow row (`internal/metastore/segments.go:134-148`, BR-META-15). `DeleteFlow` now uses the same lock.

### BR-GC-21: The timerange delete uses the sorted lock sequence
`DeleteSegmentsByTimerange` keeps its behaviour. Step 3 (the decrease) changes. Lock the object rows in ascending byte order (BR-GC-23). Then decrease each `ref_count` with one statement.

### BR-GC-22: No request path deletes bytes
No API request calls the object store to delete bytes (ADR-0020). A row at `ref_count = 0` stays for sweep 1.

### BR-GC-23: Lock rows in ascending byte order of the ID
Each transaction that changes two or more rows of `objects` or of `allocations` locks them in ascending byte order of `id` before it changes them. This prevents deadlocks between two transactions that share rows. This rule applies to:

- `POST /flows/{flowId}/storage` (allocation rows).
- Segment registration (object rows and allocation rows).
- `DeleteFlow` and `DeleteSegmentsByTimerange` (object rows).
- The row delete of each sweep (BR-GC-36, BR-GC-44).

Byte order is the only permitted order. In Go, sort with `slices.Sort`. In SQL, use `ORDER BY id COLLATE "C"`. Do not lock with `ORDER BY id` alone. The default collation of a database can be different from byte order (for example `en_US.UTF-8` sorts `a1` before `B1`, and byte order sorts `B1` before `a1`). Two different orders give a deadlock (SQLSTATE `40P01`).

The sweep claims and the retry reclaims are exempt. They use `FOR UPDATE SKIP LOCKED`, which never waits for a lock. Thus they cannot be part of a deadlock. They keep `ORDER BY id`, so that PostgreSQL can use the partial indexes.

## Sweep 1: released objects

### BR-GC-30: Candidates of sweep 1
A candidate is an object row with all these conditions:

- `ref_count = 0`
- `deleting = false`
- `storage_id IS NULL`, or `storage_id` equal to the configured storage ID

Sweep 1 selects up to `GC_BATCH_SIZE` candidates with `ORDER BY id` and `FOR UPDATE SKIP LOCKED` (exempt from the byte-order rule, BR-GC-23).

### BR-GC-31: The claim flag
`objects.deleting = true` means "the GC deletes this object now". Segment registration does not use such a row (BR-GC-12 row 2, BR-GC-14). Only the GC sets the flag. Only the GC deletes a row that has the flag. The claim also sets `claimed_at` to the database time `now()` (BR-GC-65). A row with `deleting = true` always has a `claimed_at` value.

### BR-GC-32: The claim transaction
In one transaction, sweep 1 does these steps:

1. Select the candidates (BR-GC-30).
2. Do the segment-count check (BR-GC-33).
3. Delete the BYOS candidates (BR-GC-34).
4. Set `deleting = true` and `claimed_at = now()` on the controlled candidates that passed the check.
5. Commit.

### BR-GC-33: Segment-count check before the claim
For each candidate, count the segment rows that refer to it. If the count is more than 0, the stored `ref_count` is wrong (drift). For such a candidate:

- Set `ref_count` to the count.
- Do not claim it.
- Increment `opentams_gc_refcount_drift_total`.
- Write a WARN log line with `object_id`, `stored_ref_count` (0), and `actual_ref_count`.

Rationale: the GC must not trust a counter immediately before a delete that it cannot undo. A drift indicates a code error. The metric makes it visible.

### BR-GC-34: A BYOS candidate is a row-only delete
A candidate with `storage_id IS NULL` is a BYOS object. Sweep 1 deletes its row in the claim transaction. It makes no object-store call. Outcome: `byos_row_only`.

### BR-GC-35: Delete the bytes of the claimed objects
After the commit, sweep 1 calls `DeleteObjects` once for the claimed IDs, with the object-store client of the configured storage ID. The key of an object is its ID. For each ID:

- A success, also for a key that does not exist, goes to the row delete (BR-GC-36).
- An error leaves the row with `deleting = true` (BR-GC-63).

### BR-GC-36: Delete the rows of the deleted objects
In one transaction, sweep 1 locks the object rows of the successful IDs in ascending byte order (BR-GC-23), and then deletes them with `DELETE FROM objects WHERE id = ANY($1) AND deleting = true`. The foreign key from `segments.object_id` gives an error if a segment still refers to a row. Such an error is a batch-level error (BR-GC-62). Outcome: `deleted`.

### BR-GC-37: A row with an unknown storage ID stays
An object row with a `storage_id` that is not NULL and not the configured storage ID is not a candidate. The GC does not delete its bytes, because it has no client for that storage. The gauge `opentams_gc_backlog{sweep="released",state="unknown_storage"}` shows the count of such rows at `ref_count = 0`.

## Sweep 2: expired allocations

### BR-GC-40: Candidates of sweep 2
A candidate is an allocation row with all these conditions:

- `deleting = false`
- `expires_at < now() - GC_ALLOCATION_GRACE`
- `storage_id` equal to the configured storage ID

Sweep 2 selects up to `GC_BATCH_SIZE` candidates in ascending `(expires_at, id)` sequence with `FOR UPDATE SKIP LOCKED`.

### BR-GC-41: The claim transaction
In one transaction, sweep 2 does these steps:

1. Select the candidates (BR-GC-40).
2. Do the object-row check (BR-GC-42).
3. Set `deleting = true` and `claimed_at = now()` on the candidates that passed the check.
4. Commit.

### BR-GC-42: Object-row check before the claim
If an object row exists for the ID of a candidate, with any `ref_count` and any `storage_id`, then:

- Delete only the allocation row, in the claim transaction.
- Make no object-store call.
- Outcome: `registered_leftover`.

Rationale:
- With `ref_count > 0`, a segment uses the bytes.
- With `ref_count = 0`, the bytes belong to sweep 1 and its claim flag.
- A BYOS object row for an allocated ID is the "mixed object" case. Upstream says that `get_urls` adds uncontrolled URLs. Thus the bucket bytes are the controlled copy, and they must stay.

A leftover allocation occurs when a client registers an allocated ID with `get_urls`. The BYOS path does not delete the allocation row (BR-GC-14).

### BR-GC-43: Delete the bytes of the claimed allocations
After the commit, sweep 2 calls `DeleteObjects` once for the claimed IDs. The results go to BR-GC-44 and BR-GC-63, as in sweep 1.

### BR-GC-44: Delete the rows of the deleted allocations
In one transaction, sweep 2 locks the allocation rows of the successful IDs in ascending byte order (BR-GC-23), and then deletes them with `DELETE FROM allocations WHERE id = ANY($1) AND deleting = true`. Outcome: `deleted`.

### BR-GC-45: A row with an unknown storage ID stays
An allocation row with a different `storage_id` is not a candidate. The gauge `opentams_gc_backlog{sweep="allocation",state="unknown_storage"}` shows the count of such expired rows.

### BR-GC-46: Registration after a sweep 2 claim
Segment registration locks the allocation row (BR-GC-13, BR-GC-14). If the claim committed first, registration reads `deleting = true` and rejects the segment with `object-deleting`. If registration locked the row first, the sweep 2 claim does not get the row (`SKIP LOCKED`), and the next cycle finds an object row and uses BR-GC-42.

## Schema and upgrade

### BR-GC-50: Migration 000006 renames the flag and adds `allocations`
Migration `000006_allocations_and_deleting` does these changes in one file:

- `ALTER TABLE objects RENAME COLUMN reaping TO deleting`. PostgreSQL changes the predicates of the partial indexes `objects_gc_claim_idx` and `objects_gc_retry_idx` automatically. The index names do not change.
- Add the column `claimed_at TIMESTAMPTZ` to `objects`. Set it to `now()` on each row with `deleting = true` (BR-GC-31).
- Create the table `allocations` and its two partial indexes.
- Create the index `segments_object_id_idx` (BR-GC-51).

Migration 000005 does not change. It shipped in `v0.1.0-alpha.1`, and AGENTS.md does not permit a change to a released migration.

Known deviation from ADR-0017: a rename in one step breaks the old binary. The Helm migration job (`pre-install,pre-upgrade`) applies 000006 before the new pods start. Until the rollout replaces them, old API pods use `reaping` in the registration upsert. Each segment registration on an old pod then gets a 500 error. The upgrade procedure is:

1. Do the upgrade at a time with low write traffic. BR-GC-10 to BR-GC-12 also reject uploads that started before the upgrade (they have no allocation row).
2. If no registration error is permitted, set the API replica count to 0 before the upgrade.
3. Do `helm upgrade`. The migration blocks writes to `segments` while it builds the index (BR-GC-51).
4. Make sure that all API pods have the new version.
5. If the migration stops with an error, use runbook R7 ([`../../../runbooks/gc.md`](../../../runbooks/gc.md)).

ADR-0041 records this deviation.

### BR-GC-51: Migration 000006 adds the segment index
The last statement of migration 000006 is:

```sql
CREATE INDEX IF NOT EXISTS segments_object_id_idx ON segments (object_id);
```

Sweep 1 uses this index for the segment count (BR-GC-33). PostgreSQL uses it for the foreign-key check when the GC deletes an `objects` row.

The statement does not use `CONCURRENTLY`. These are the results:

- The file is one transaction. If one statement fails, PostgreSQL rolls back all statements of the file. The database stays at the schema of version 5.
- While the index builds, writes to `segments` wait (segment registration, flow delete, segment delete). Reads of `segments` continue.
- The rename holds a lock on `objects` until the end of the transaction. API reads do not use `objects`.
- The build time increases with the row count of `segments`.

*Decision: one migration file with a plain `CREATE INDEX`. Rationale: one file, one transaction, and no index that PostgreSQL marks INVALID after a failure. Rejected: `CREATE INDEX CONCURRENTLY` in a separate migration 000007. It does not block writes, but it adds a second file and a recovery procedure for an INVALID index.*

### BR-GC-52: The minimum schema version is 6
`metastore.ExpectedSchemaVersion` changes from 5 to 6. A new binary does not start on a database at version 5 (`ErrSchemaTooOld`).

### BR-GC-53: Breaking changes for the release notes
The release notes must list these changes:

1. The problem type `object-id-exists` is now `object-exists`.
2. The response 409 `object-reaping` is removed. The case is now a `failed_segments` entry `object-deleting` in a 200 response.
3. A controlled segment with an object ID that has no allocation is rejected with `object-not-allocated`.
4. `OBJECT_STORE_PRESIGN_EXPIRY` less than 300 s stops the process at start.
5. The column `objects.reaping` is now `objects.deleting`. The upgrade procedure in BR-GC-50 applies.
6. The GC deletes media bytes. Before this change, no bytes were deleted.
7. `DELETE /flows/{flowId}` on a read-only flow returns 403 `read-only`. Before this change, it deleted the flow.

Before the upgrade, an old pod can cache a 409 `object-reaping` response. During the idempotency TTL, a retry of that request gets the same cached response again. This is correct idempotency behaviour (the same key gives the same response).

## Worker cycle, failures, and shutdown

### BR-GC-60: Two loops, one for each sweep
`opentams gc` starts two loops: one for sweep 1 and one for sweep 2. Each loop repeats one cycle:

1. The retry phase (BR-GC-64).
2. The claim phase (BR-GC-61).
3. Update the gauges of the sweep.
4. Write the INFO line `gc cycle completed`.
5. Stay idle for `GC_POLL_INTERVAL`, or stop on shutdown.

The cycle has one exit point. Steps 3 and 4 occur also after a batch-level error and after a stop. Exception: if `ctx` is done, the cycle does not update the gauges, because each query fails. The field `ended_by` of the summary line is:

- `shutdown` if `stop` is closed or `ctx` is done.
- `batch_error` after a batch-level error, in the other cases.
- `empty` in all other cases.

### BR-GC-61: The claim phase is a drain loop
The claim phase does batches one after the other. It stops when the select of a batch returns fewer rows than `GC_BATCH_SIZE`, or when a batch-level error occurs. Each successful batch, also an empty one, sets `opentams_gc_last_success_timestamp_seconds{sweep}` to the current time.

### BR-GC-62: A batch-level error stops the cycle
These errors are batch-level errors:

- A database error in a claim transaction, a select, or a row delete.
- An object-store result with `ErrAuth` for the call or for one key.
- An error for each ID of one `DeleteObjects` call.

On a batch-level error, the sweep:

- Writes one ERROR log line `gc batch error` with `sweep`, `stage`, and `error`.
- Increments `opentams_gc_errors_total{sweep,stage}` by 1. Exception: if the error comes from the per-ID results of a call (the second case for one key, and the third case), BR-GC-63 already counted each ID with `stage="s3_delete"`. Then the sweep does not increment the counter again.
- Stops the current cycle.
- Stays idle for `GC_POLL_INTERVAL`.

Rationale: more requests do not help a dependency that is not available. Also, more claims during an object-store outage put more rows in the state `deleting = true`. A client cannot use such rows (`object-deleting`).

### BR-GC-63: An error for one object keeps the claim
If the object-store result for one ID is an error, and the call is not a batch-level error, then:

- The row keeps `deleting = true`.
- The sweep increments `opentams_gc_errors_total{sweep,stage="s3_delete"}`.
- The sweep writes a WARN log line with `object_id` and the error.
- The retry phase of a later cycle tries the ID again, after the claim lease (BR-GC-64, BR-GC-65).

There is no attempt limit. The gauge `opentams_gc_backlog{sweep,state="retrying"}` shows the rows that wait.

### BR-GC-64: The retry phase
At the start of each cycle, before the claim phase, the sweep takes back the rows that an earlier claim left with `deleting = true`. A row is taken back only if it has all these conditions:

- `deleting = true`, and `storage_id` equal to the configured storage ID.
- `claimed_at` is older than `claimLease` (BR-GC-65), by the database clock.
- `claimed_at` is earlier than the start of the current cycle.

The sweep selects up to `GC_BATCH_SIZE` such rows with `FOR UPDATE SKIP LOCKED`, sets `claimed_at = now()` on them, and commits. This is a new claim of the rows (a reclaim). Then the sweep does BR-GC-35 and BR-GC-36 (sweep 1) or BR-GC-43 and BR-GC-44 (sweep 2) for them. It repeats until a reclaim returns fewer rows than `GC_BATCH_SIZE`, or until `stop` is closed.

The third condition gives each row a maximum of one attempt in each cycle: a reclaimed row has a `claimed_at` later than the start of the cycle. The start of the cycle comes from the clock of the worker. A clock difference to the database has no unsafe effect, because the second condition uses only the database clock.

A batch-level error in the retry phase stops the cycle (BR-GC-62). Then the claim phase does not start.

### BR-GC-65: The claim lease
Two or more GC processes can operate at the same time. `FOR UPDATE SKIP LOCKED` gives each claim different rows. Each claim and each reclaim sets `claimed_at = now()`. A claim is valid for `claimLease` (5 minutes, a Go constant in `internal/gc`). While the claim is valid, no other GC process takes the row (BR-GC-64).

Rationale: without a lease, two GC processes can act on one claimed row. This occurs with two or more replicas, during a rolling update of one replica, and with `gc --once` beside the Deployment. Then this sequence deletes new bytes:

1. Process A claims X and calls `DeleteObjects`. The call is slow.
2. Process B deletes the key X and the row X.
3. A client allocates X again, uploads new bytes, and registers X.
4. The delayed delete of process A removes the new bytes. A segment refers to a missing key.

The lease prevents step 2.

Process A must not call the object store after its lease can have ended. Before each `DeleteObjects` call, the sweep measures the time since the claim (or reclaim) committed. If this time is more than `claimLease / 2`, the sweep does not call the object store for that batch. It writes the WARN line `gc batch skipped` and leaves the rows. A later cycle takes them back after the lease. `claimLease / 2` plus `s3Timeout` is less than `claimLease`. Thus each call of process A ends before a different process can take the rows.

The row delete (BR-GC-36, BR-GC-44) has the condition `deleting = true`. If a different process removed a row first, the delete changes 0 rows for that row. The metrics count only the rows that a delete removed.

### BR-GC-66: An interruption at any point loses no data

| Point of the interruption | Effect | Recovery |
|---|---|---|
| In the claim transaction | Rollback. No change. | The next cycle selects the rows again. |
| After the claim, before or during the object-store call | Rows have `deleting = true`. Some keys can be deleted. | After the claim lease, the retry phase deletes the keys and the rows. |
| After the object-store call, before the row delete commits | Rows have `deleting = true`. The keys are deleted. | After the claim lease, the retry phase deletes the keys again (success) and deletes the rows. |
| In the row-delete transaction | Rollback. Same as the row above. | Same as the row above. |

### BR-GC-67: Shutdown
On SIGTERM or SIGINT, the process does these steps:

1. `/readyz` returns 503.
2. Each loop claims no new batch.
3. The current batch continues for a maximum of `SERVER_GRACEFUL_SHUTDOWN_PERIOD` (default 30 s).
4. After this time, the process cancels the context of the batch. BR-GC-66 makes this safe.
5. The admin listener stops after the two loops stop.
6. The process stops with exit code 0.

### BR-GC-68: Timeouts
Each database statement of the GC has a context timeout of 30 s. Each `DeleteObjects` call has a context timeout of 60 s. The claim lease is 5 minutes (BR-GC-65). These values are Go constants in `internal/gc`. They are not settings. A timeout is a batch-level error if it occurs in a database statement. It is a per-ID error (`ErrTransient`) if it occurs in the object-store call (`internal/objectstore/objectstore.go:306-327`).

### BR-GC-69: Mode `--once`
`opentams gc --once` does one cycle of each sweep, at the same time, and then stops. It opens no admin listener. It returns exit code 0 if neither cycle stopped because of a batch-level error. It returns exit code 1 in the other case. A configuration error also gives exit code 1.

## Errors given to clients

All types have the base `https://github.com/amagioss/opentams/problems/`. All have status 400. The detail text names the `object_id`. It never names a flow other than the flow of the request path.

### BR-GC-70: `object-exists`
Title "Object Exists". `POST /flows/{flowId}/storage` with an `object_ids` entry that has an object row with `deleting = false` (BR-GC-03 row 2). This type replaces `object-id-exists`. The Go constant changes from `apperror.ErrObjectIDExists` to `apperror.ErrObjectExists`. Detail example: `object_id "obj-1" already exists`.

### BR-GC-71: `object-not-allocated`
Title "Object Not Allocated". A controlled segment with an object ID that has no allocation row and no object row (BR-GC-12 row 6). Go constant `apperror.ErrObjectNotAllocated`. Detail example: `object_id "obj-1" was not allocated by POST /flows/{flowId}/storage, or its allocation expired and was deleted`.

### BR-GC-72: `object-wrong-flow`
Title "Object Allocated to Another Flow". An ID with an allocation row for a different flow (BR-GC-03 row 4, BR-GC-12 row 5). Go constant `apperror.ErrObjectWrongFlow`. Detail example: `object_id "obj-1" is allocated to a different flow`.

### BR-GC-73: `object-deleting`
Title "Object Being Deleted". An ID that the GC claimed (BR-GC-03 rows 1 and 3, BR-GC-12 rows 2 and 4, BR-GC-14). Go constant `apperror.ErrObjectDeleting`. It replaces `apperror.ErrObjectReaping` (409). Detail example: `object_id "obj-1" is being deleted; use a new object_id`.

### BR-GC-74: Removed error identifiers
These identifiers are removed: `apperror.ErrObjectReaping`, `apperror.ErrObjectIDExists`, `metastore.ErrSegmentObjectReaping`, the problem types `object-reaping` and `object-id-exists`, and the function path in `internal/httpx/handlers/segments.go:302-305` that returns 409 for them. The 409 response of `POST /flows/{flowId}/segments` stays for the idempotency conflict only.

The function `cachedPostResponse` (`internal/httpx/handlers/segments.go:609-614`) keeps its 409 case. A cached 409 body replays as stored (BR-GC-53). If the cached body is empty or not valid, the fallback uses the type `object-deleting` with status 409, not `object-reaping`. Only an old pod cached such a 409.
