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

### BR-GC-06: Lock the IDs in ascending sequence
Inside the transaction, the server processes the IDs in ascending string sequence. The response lists the objects in the sequence of the request. This obeys BR-GC-23.

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

### BR-GC-11: Group the controlled segments by object ID
In `InsertSegments`, after the overlap checks (steps 1 and 2 of today), group the controlled segments of the request by `object_id`. For each group, n is the number of segments in the group. Process the groups in ascending `object_id` sequence.

### BR-GC-12: Decision table for each controlled object ID
For each controlled object ID X with count n, use this table. The first row that matches gives the result.

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
- A BYOS segment has no allocation check (ADR-0041).
- Before the object upsert, if an allocation row for the ID exists, lock it with `SELECT … FOR SHARE`. If it has `deleting = true`, reject the segment with `object-deleting`. Rationale: sweep 2 is about to delete the bucket key of that ID.
- The upsert keeps its form and its predicate `WHERE objects.deleting = false`. If the upsert returns no row, reject the segment with `object-deleting`.
- The BYOS path never deletes an allocation row. Sweep 2 deletes it later as a leftover allocation (BR-GC-42).
- Group the BYOS segments by object ID and add n, as in BR-GC-11.

### BR-GC-15: A rejection applies to one segment, not to the request
A segment with a rejection from BR-GC-12 or BR-GC-14 is not written. The other segments of the request continue. The response is 200 with `failed_segments`, as for other per-segment errors today (`internal/httpx/handlers/segments.go:316-345`). Each entry has the problem type, the title, status 400, and a detail text (BR-GC-70 to BR-GC-74). The store returns the rejections in `InsertResult.RejectedIndices` and `InsertResult.RejectReasons` (`internal/metastore/segments.go:57-69`).

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

1. Lock the flow row with `SELECT 1 FROM flows WHERE id = $1 FOR UPDATE`. If there is no row, return "flow not found".
2. Delete all segments of the flow with `DELETE … RETURNING object_id`. Count the rows for each `object_id`.
3. Lock the affected object rows in ascending `id` sequence. Decrease each `ref_count` by its count.
4. Delete the flow row.

This corrects three errors of the current code (`internal/metastore/flows.go:433-468`):

| Error | Cause | Effect |
|---|---|---|
| B1 | The decrease is 1 for each distinct object, but each segment added 1 | `ref_count` stays too high. The bytes never get deleted. |
| B2 | No flow lock. A timerange delete at the same time can delete a segment that `DeleteFlow` also counts. | `ref_count` becomes too low. Sweep 1 can delete bytes that a different flow uses. |
| B8 | No flow lock. A segment insert after the first read is deleted by the cascade with no decrease. | `ref_count` stays too high. |

Insert and timerange delete already lock the flow row (`internal/metastore/segments.go:134-148`, BR-META-15). `DeleteFlow` now uses the same lock.

### BR-GC-21: The timerange delete uses the sorted lock sequence
`DeleteSegmentsByTimerange` keeps its behaviour. Step 3 (the decrease) changes. Lock the object rows in ascending `id` sequence. Then decrease each `ref_count` with one statement.

### BR-GC-22: No request path deletes bytes
No API request calls the object store to delete bytes (ADR-0020). A row at `ref_count = 0` stays for sweep 1.

### BR-GC-23: Lock rows in ascending ID sequence
Each transaction that changes two or more rows of `objects` or two or more rows of `allocations` locks them in ascending `id` sequence before it changes them. This prevents deadlocks between two requests that share objects. This rule applies to:

- `POST /flows/{flowId}/storage` (allocation rows).
- Segment registration (object rows and allocation rows).
- `DeleteFlow` and `DeleteSegmentsByTimerange` (object rows).

The sweep claims are exempt. Their select uses `FOR UPDATE SKIP LOCKED`, which never waits for a lock. Thus a sweep claim cannot be part of a deadlock, and its row sequence is free.

## Sweep 1: released objects

### BR-GC-30: Candidates of sweep 1
A candidate is an object row with all these conditions:

- `ref_count = 0`
- `deleting = false`
- `storage_id IS NULL`, or `storage_id` equal to the configured storage ID

Sweep 1 selects up to `GC_BATCH_SIZE` candidates in ascending `id` sequence with `FOR UPDATE SKIP LOCKED`.

### BR-GC-31: The claim flag
`objects.deleting = true` means "the GC deletes this object now". Segment registration does not use such a row (BR-GC-12 row 2, BR-GC-14). Only the GC sets the flag. Only the GC deletes a row that has the flag.

### BR-GC-32: The claim transaction
In one transaction, sweep 1 does these steps:

1. Select the candidates (BR-GC-30).
2. Do the segment-count check (BR-GC-33).
3. Delete the BYOS candidates (BR-GC-34).
4. Set `deleting = true` on the controlled candidates that passed the check.
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
Sweep 1 deletes the object rows of the successful IDs with `DELETE FROM objects WHERE id = ANY($1) AND deleting = true`. The foreign key from `segments.object_id` gives an error if a segment still refers to a row. Such an error is a batch-level error (BR-GC-62). Outcome: `deleted`.

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
3. Set `deleting = true` on the candidates that passed the check.
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

This check also prevents data loss in a rolling upgrade: a new pod writes the allocation, and an old pod registers the segment and does not delete the allocation row.

### BR-GC-43: Delete the bytes of the claimed allocations
After the commit, sweep 2 calls `DeleteObjects` once for the claimed IDs. The results go to BR-GC-44 and BR-GC-63, as in sweep 1.

### BR-GC-44: Delete the rows of the deleted allocations
Sweep 2 deletes the allocation rows of the successful IDs with `DELETE FROM allocations WHERE id = ANY($1) AND deleting = true`. Outcome: `deleted`.

### BR-GC-45: A row with an unknown storage ID stays
An allocation row with a different `storage_id` is not a candidate. The gauge `opentams_gc_backlog{sweep="allocation",state="unknown_storage"}` shows the count of such expired rows.

### BR-GC-46: Registration after a sweep 2 claim
Segment registration locks the allocation row (BR-GC-13, BR-GC-14). If the claim committed first, registration reads `deleting = true` and rejects the segment with `object-deleting`. If registration locked the row first, the sweep 2 claim does not get the row (`SKIP LOCKED`), and the next cycle finds an object row and uses BR-GC-42.

## Schema and upgrade

### BR-GC-50: Migration 000006 renames the flag and adds `allocations`
Migration `000006_allocations_and_deleting` does these changes in one file:

- `ALTER TABLE objects RENAME COLUMN reaping TO deleting`. PostgreSQL changes the predicates of the partial indexes `objects_gc_claim_idx` and `objects_gc_retry_idx` automatically. The index names do not change.
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

*Decision: one migration file with a plain `CREATE INDEX`. Rationale: one file, one transaction, and no index that PostgreSQL marks INVALID after a failure. On a database with 431,000 segment rows, a sorted scan of `segments.object_id` took 0.9 s. Thus the writes wait for a few seconds. Rejected: `CREATE INDEX CONCURRENTLY` in a separate migration 000007. It does not block writes, but it adds a second file and a recovery procedure for an INVALID index.*

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

## Worker cycle, failures, and shutdown

### BR-GC-60: Two loops, one for each sweep
`opentams gc` starts two loops: one for sweep 1 and one for sweep 2. Each loop repeats one cycle:

1. The retry phase (BR-GC-64).
2. The claim phase (BR-GC-61).
3. Update the gauges of the sweep.
4. Stay idle for `GC_POLL_INTERVAL`, or stop on shutdown.

### BR-GC-61: The claim phase is a drain loop
The claim phase does batches one after the other. It stops when the select of a batch returns fewer rows than `GC_BATCH_SIZE`, or when a batch-level error occurs. Each successful batch, also an empty one, sets `opentams_gc_last_success_timestamp_seconds{sweep}` to the current time.

### BR-GC-62: A batch-level error stops the cycle
These errors are batch-level errors:

- A database error in a claim transaction, a select, or a row delete.
- An object-store result with `ErrAuth` for the call or for one key.
- An error for each ID of one `DeleteObjects` call.

On a batch-level error, the sweep:

- Writes an ERROR log line.
- Increments `opentams_gc_errors_total{sweep,stage}`.
- Stops the current cycle.
- Stays idle for `GC_POLL_INTERVAL`.

Rationale: more requests do not help a dependency that is not available. Also, more claims during an object-store outage put more rows in the state `deleting = true`. A client cannot use such rows (`object-deleting`).

### BR-GC-63: An error for one object keeps the claim
If the object-store result for one ID is an error, and the call is not a batch-level error, then:

- The row keeps `deleting = true`.
- The sweep increments `opentams_gc_errors_total{sweep,stage="s3_delete"}`.
- The sweep writes a WARN log line with `object_id` and the error.
- The retry phase tries the ID again in the next cycle.

There is no attempt limit. The gauge `opentams_gc_backlog{sweep,state="retrying"}` shows the rows that wait.

### BR-GC-64: The retry phase
At the start of each cycle, before the claim phase, the sweep reads all rows with `deleting = true` and a known storage ID. It reads them in ascending `id` sequence, in pages of `GC_BATCH_SIZE`, with a keyset (`id > last_id`). It does not lock them. For each page, it does BR-GC-35 and BR-GC-36 (sweep 1) or BR-GC-43 and BR-GC-44 (sweep 2). Each retry row gets one attempt in each cycle. A batch-level error in the retry phase stops the cycle (BR-GC-62). Then the claim phase does not start.

### BR-GC-65: Two or more replicas
Two or more GC replicas can operate at the same time. `FOR UPDATE SKIP LOCKED` gives each claim different rows. The retry phase of two replicas can read the same retry row. This is harmless:

- The second object-store delete is a success.
- The second row delete changes 0 rows.
- The metrics count only the rows that a delete actually removed.

### BR-GC-66: An interruption at any point loses no data

| Point of the interruption | Effect | Recovery |
|---|---|---|
| In the claim transaction | Rollback. No change. | The next cycle selects the rows again. |
| After the claim, before or during the object-store call | Rows have `deleting = true`. Some keys can be deleted. | The retry phase deletes the keys and the rows. |
| After the object-store call, before the row delete commits | Rows have `deleting = true`. The keys are deleted. | The retry phase deletes the keys again (success) and deletes the rows. |
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
Each database statement of the GC has a context timeout of 30 s. Each `DeleteObjects` call has a context timeout of 60 s. These values are Go constants in `internal/gc`. They are not settings. A timeout is a batch-level error if it occurs in a database statement. It is a per-ID error (`ErrTransient`) if it occurs in the object-store call (`internal/objectstore/objectstore.go:306-327`).

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
