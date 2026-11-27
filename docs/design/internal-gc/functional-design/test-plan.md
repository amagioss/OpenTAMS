---
unit: M16 internal/gc
stage: Functional Design
status: Complete
---

# Test Plan — object garbage collection

## 0. Rules for these tests

- Write each test before its code. The test must first give an error for the correct reason (AGENTS.md, CONTRIBUTING.md). For a bug correction (TC-GC-040 to TC-GC-043), first run the test against the current code and record that it shows the bug.
- Put the label of the test in a comment above the test function, for example `// TC-GC-041: DeleteFlow and a timerange delete at the same time`. The labels are local to this design (docs/README.md, "Identifier conventions").
- Level "unit" uses no infrastructure. Use hand-made fakes with function fields, as the existing tests do (NFR-SVC-STG-M2).
- Level "integration" uses the build tag `integration` and (where the test names MinIO) a MinIO container. Move `setupMinIO` from `internal/objectstore/objectstore_perf_test.go` to a shared helper `internal/objtest` with the build tags `integration || perf`. Do not copy it.
- PostgreSQL for integration tests:
  - In `internal/metastore`, use the shared `testPool` of `store_test.go` (one container for the package). Each test of sections 4 and 5 calls `resetGCTables(t)` first. Reason: the sweep methods read the full table, and other tests of the package leave committed rows (for example `segments_test.go:415` leaves `oB` and `oC` at `ref_count = 0`). The tests of the package do not use `t.Parallel`.
  - In other packages, use `internal/dbtest.Setup`. Exception: `cmd/opentams` already has a PostgreSQL container in its `TestMain`. Its tests use that container and `freshMigrateDB`, which gives each test a new database and sets `DB_NAME` for `config.Load`.
  - Before each `InsertSegments` call with a controlled segment that a test does not name as "no allocation", call `seedAllocation` (logic model section 13).
- Collation database: TC-GC-036, TC-GC-043, and TC-GC-071 operate on a second database in the same container. Make it with `CREATE DATABASE gc_icu TEMPLATE template0 LOCALE_PROVIDER icu ICU_LOCALE 'en-US'`, and apply the migrations. The test first makes sure that `SELECT 'a1' < 'B1'` returns true in that database. If it returns false, the test fails, because it cannot show the error. Reason: `postgres:16-alpine` sorts the default collation in byte order, and a production database (for example `en_US.UTF-8`) does not.
- The database fixtures use plain SQL `INSERT` statements. Unless a test gives other values, use:

| Fixture | Value |
|---|---|
| Flow F | `11111111-1111-1111-1111-111111111111`, codec `video/h264`, not read-only |
| Flow G | `22222222-2222-2222-2222-222222222222` |
| Configured storage ID | `default` |
| `objects.storage_id` | `'default'` (controlled). A test that needs a BYOS object gives `storage_id NULL`. |
| `objects.claimed_at` | `now()` if the test gives `deleting = true`. Else NULL. |
| Allocation row | `storage_id 'default'`, `expires_at = now() + interval '1 hour'`, `deleting = false`, `claimed_at` NULL |
| `OBJECT_STORE_PRESIGN_EXPIRY` | `1h` |
| `GC_ALLOCATION_GRACE` | `5m` |
| `GC_BATCH_SIZE` | `1000` |
| Timestamp tolerance | 5 s |

- For the worker unit tests, `internal/gc` has the unexported fields `dbTimeout`, `s3Timeout`, `claimLease`, and `now` on `Worker`. `New` sets them from the constants and `time.Now`. A test in the same package can set the timeouts to 50 ms and `now` to a fake clock.
- The tests of sections 7 and 8 call `runGC` (logic model section 10) with the environment of that section and a `zaptest/observer` logger. They examine the log lines through the observer.
- The minimum completeness rule: each acceptance criterion `AC-GC-*` has one or more tests, each error type has a test, and each reliability rule has a test.

## 1. API and service tests: allocation (`POST /flows/{flowId}/storage`)

**TC-GC-001: Mode `limit` writes one allocation row for each ID.** Integration, `internal/metastore`.
- Given: flow F. No allocation rows.
- When: `AllocateObjects(ctx, AllocateParams{FlowID: F, StorageID: "default", TTL: time.Hour, IDs: ["b-id", "a-id"], Requested: false})`.
- Then: the function returns nil. `allocations` has 2 rows, `a-id` and `b-id`. Each row has `flow_id = F`, `storage_id = 'default'`, `deleting = false`, and `expires_at` = `now() + 1h` (tolerance 5 s).
- Rules: BR-GC-01, BR-GC-02.

**TC-GC-002: The service sends the IDs and settings to the store.** Unit, `internal/service/storage`.
- Given: a fake store that records its arguments, a fake object store that returns `https://u/<id>`, storage ID `default`, presign expiry 1h.
- When: `AllocateStorage(ctx, F, AllocateRequest{Limit: ptr(2)})`.
- Then: `GenerateUploadURL` is called 2 times before `AllocateObjects`. `AllocateObjects` is called once with `Requested = false`, `TTL = 1h`, `StorageID = "default"`, and `IDs` equal to the 2 IDs of the response, in the same sequence.
- Rules: BR-GC-01, BR-GC-05.

**TC-GC-003: Mode `object_ids` with new IDs keeps the request sequence.** Unit, `internal/service/storage`.
- Given: a fake store that accepts all IDs.
- When: `AllocateStorage(ctx, F, AllocateRequest{ObjectIDs: ["z", "a"]})`.
- Then: the response objects are `z`, then `a`. `AllocateObjects` gets `IDs = ["z", "a"]` and `Requested = true`.
- Rules: BR-GC-06.

**TC-GC-004: Mode `object_ids` writes new rows.** Integration, `internal/metastore`.
- Given: flow F. No rows for `x1`.
- When: `AllocateObjects` with `IDs ["x1"]`, `Requested: true`.
- Then: one allocation row `x1` with `flow_id = F`.
- Rules: BR-GC-03 row 6.

**TC-GC-005: An ID with an object row gives `object-exists`.** Integration, `internal/metastore`, and unit, `internal/service/storage`.
- Given: `INSERT INTO objects (id, ref_count, storage_id, deleting) VALUES ('x1', 1, 'default', false)`.
- When: `AllocateObjects` with `IDs ["x1"]`, `Requested: true`.
- Then: the error wraps `metastore.ErrObjectExists` and contains `"x1"`. No allocation row for `x1`. In the service test, the fake store returns this error, and `AllocateStorage` returns `*apperror.AppError` with code `object-exists` and status 400.
- Variant: the object row has `ref_count = 0`. The result is the same.
- Rules: BR-GC-03 row 2, BR-GC-70.

**TC-GC-006: A second request on the same flow gives a new expiry.** Integration, `internal/metastore`.
- Given: allocation row `x1`, flow F, `expires_at = now() + interval '10 minutes'`.
- When: `AllocateObjects` with flow F, `IDs ["x1"]`, `Requested: true`, TTL 1h.
- Then: nil. `expires_at` = `now() + 1h` (tolerance 5 s). `flow_id` and `created_at` do not change.
- Rules: BR-GC-03 row 5.

**TC-GC-007: An allocation for a different flow gives `object-wrong-flow`.** Integration, `internal/metastore`.
- Given: allocation row `x1` with `flow_id = G`.
- When: `AllocateObjects` with flow F, `IDs ["x1"]`, `Requested: true`.
- Then: the error wraps `metastore.ErrObjectWrongFlow`. The detail does not contain G. The row keeps `flow_id = G` and its `expires_at`.
- Rules: BR-GC-03 row 4, BR-GC-72, NFR-GC-U3.

**TC-GC-008: An object row with `deleting = true` gives `object-deleting`.** Integration, `internal/metastore`.
- Given: object row `x1` with `ref_count = 0`, `deleting = true`.
- When: `AllocateObjects` with `IDs ["x1"]`, `Requested: true`.
- Then: the error wraps `metastore.ErrObjectDeleting`.
- Rules: BR-GC-03 row 1, BR-GC-73.

**TC-GC-009: An allocation row with `deleting = true` gives `object-deleting`.** Integration, `internal/metastore`.
- Given: allocation row `x1`, flow F, `deleting = true`.
- When: `AllocateObjects` with flow F, `IDs ["x1"]`, `Requested: true`.
- Then: the error wraps `metastore.ErrObjectDeleting`. The row keeps `deleting = true`.
- Rules: BR-GC-03 row 3.

**TC-GC-010: One rejected ID rejects the full request.** Integration, `internal/metastore`.
- Given: object row `b1`.
- When: `AllocateObjects` with `IDs ["a1", "b1"]`, `Requested: true`.
- Then: the error wraps `ErrObjectExists`. No allocation row for `a1`.
- Rules: BR-GC-04.

**TC-GC-011: A URL generation error stops the request before the database.** Unit, `internal/service/storage`.
- Given: a fake object store that returns an error for the second ID.
- When: `AllocateStorage` with `Limit 2`.
- Then: the error is returned. `AllocateObjects` is not called.
- Rules: BR-GC-05.

**TC-GC-012: `GET /service` gives `min_presigned_url_timeout`.** Unit, `internal/httpx/handlers`.
- Given: `ObjectStorePresignExpiry = 1h`.
- When: `GetService`.
- Then: `min_object_timeout = "3600:0"` and `min_presigned_url_timeout = "3600:0"`.
- Rules: BR-GC-09.

**TC-GC-013: Two requests at the same time for the same ID on two flows.** Integration, `internal/metastore`.
- Given: no row for `x1`.
- When: two goroutines start at a barrier. One calls `AllocateObjects` for flow F, the other for flow G, both with `IDs ["x1"]`, `Requested: true`.
- Then: one call returns nil and the other returns `ErrObjectWrongFlow`. One allocation row exists. No error has SQLSTATE `40P01`.
- Rules: BR-GC-03, BR-GC-06.

**TC-GC-014: The handler renders the allocation errors as problem details.** Unit, `internal/httpx/handlers`.
- Given: a fake storage service that returns `apperror.New(apperror.ErrObjectWrongFlow, "object_id \"x1\" is allocated to a different flow")`.
- When: `POST /tams/v1/flows/<F>/storage` with body `{"object_ids":["x1"]}` through the router with the error middleware.
- Then: status 400, `Content-Type: application/problem+json`, `type` = `https://github.com/amagioss/opentams/problems/object-wrong-flow`, `title` = `Object Allocated to Another Flow`. The handler writes a WARN log line, not ERROR.
- Rules: BR-GC-72, section 4.3 of the logic model.

**TC-GC-015: Missing flow and read-only flow keep their status.** Unit, `internal/service/storage`.
- Given: a fake store with no flow F. Second case: flow F with `read_only = true`.
- When: `AllocateStorage` with `Limit 1`.
- Then: first case not found (404). Second case `read-only` (403). `AllocateObjects` is not called.
- Rules: BR-GC-07.

## 2. Behaviour tests: segment registration (`InsertSegments`)

In these tests, a "controlled segment" has no `get_urls`, and a "BYOS segment" has one `get_urls` entry `{"url": "https://cdn.example/x", "label": "cdn"}`. Each segment has a timerange that does not overlap the others, for example `[0:0_2:0)`, `[2:0_4:0)`.

**TC-GC-020: A controlled ID with no allocation is rejected, and the other segments continue.** Integration, `internal/metastore`.
- Given: allocation `a1` for flow F. No row for `zz`.
- When: `InsertSegments` for flow F with segment s1 (`a1`, `[0:0_2:0)`) and segment s2 (`zz`, `[2:0_4:0)`).
- Then:
  - `AcceptedIndices = [0]`. `RejectedIndices = [1]`. `RejectReasons[0].Code = apperror.ErrObjectNotAllocated`. The detail contains `zz`.
  - The database has segment s1 and no segment s2.
  - There is no object row `zz`.
- Rules: BR-GC-12 row 6, BR-GC-15, BR-GC-71.

**TC-GC-016: The service maps a store rejection.** Unit, `internal/service/segment`.
- Given: a fake store that returns `InsertResult{AcceptedIndices: [0], RejectedIndices: [1], RejectReasons: [{Code: apperror.ErrObjectNotAllocated, Detail: "d"}]}` for a batch of two segments.
- When: `RegisterBatch`.
- Then: `OutcomePartial`. `Failed[0].Type` = `https://github.com/amagioss/opentams/problems/object-not-allocated`, `Failed[0].Title` = `Object Not Allocated`, `Failed[0].Reason` = `d`.
- Rules: BR-GC-15, BR-GC-71.

**TC-GC-021: Registration uses the allocation.** Integration, `internal/metastore`.
- Given: allocation `a1`, flow F, `storage_id 'default'`.
- When: `InsertSegments` for flow F with one controlled segment on `a1`.
- Then: accepted. Object row `a1` with `ref_count = 1`, `storage_id = 'default'`, `deleting = false`. No allocation row `a1`.
- Rules: BR-GC-12 row 3.

**TC-GC-022: The object row gets `storage_id` from the allocation.** Integration, `internal/metastore`.
- Given: allocation `a1` with `storage_id 'store-b'`. `InsertBatch.ControlledStorageID = "default"`.
- When: `InsertSegments` with one controlled segment on `a1`.
- Then: object row `a1` has `storage_id = 'store-b'`.
- Rules: BR-GC-17.

**TC-GC-023: A late registration is accepted.** Integration, `internal/metastore`.
- Given: allocation `a1`, flow F, `expires_at = now() - interval '2 hours'`, `deleting = false`.
- When: `InsertSegments` with one controlled segment on `a1`.
- Then: accepted, as in TC-GC-021.
- Rules: BR-GC-12 row 3.

**TC-GC-024: Two segments with the same ID give `ref_count = 2`.** Integration, `internal/metastore`.
- Given: allocation `a1`, flow F.
- When: `InsertSegments` with two controlled segments on `a1` (`[0:0_2:0)` and `[2:0_4:0)`).
- Then: both accepted. `ref_count = 2`. No allocation row.
- Rules: BR-GC-11, BR-GC-12 row 3.

**TC-GC-025: Reuse on a different flow needs no allocation.** Integration, `internal/metastore`.
- Given: object row `a1` with `ref_count = 1` (from flow F). Flow G exists.
- When: `InsertSegments` for flow G with one controlled segment on `a1`.
- Then: accepted. `ref_count = 2`.
- Rules: BR-GC-12 row 1.

**TC-GC-026: A claimed object gives `object-deleting`.** Integration, `internal/metastore`.
- Given: object row `a1` with `ref_count = 0` and `deleting = true`.
- When: `InsertSegments` for flow F with one controlled segment on `a1`.
- Then: rejected with type `…/object-deleting`. The object row does not change. No segment row.
- Rules: BR-GC-12 row 2, BR-GC-73.

**TC-GC-027: No response has status 409 for an object.** Unit, `internal/httpx/handlers`.
- Given: a fake segment service that returns `RegisterResult{Outcome: OutcomeAllRejected, Failed: [one entry with Type ".../object-deleting", Status 400]}`.
- When: `POST /tams/v1/flows/<F>/segments` with a valid idempotency key.
- Then: status 200, body `failed_segments[0].error.type` ends with `object-deleting`. The entry has no `status` field. A search of the package for `object-reaping` finds nothing. Also, `cachedPostResponse` with a cached 409 and an empty body gives type `…/object-deleting` and status 409.
- Rules: BR-GC-16, BR-GC-74.

**TC-GC-028: A claimed allocation gives `object-deleting`.** Integration, `internal/metastore`.
- Given: allocation `a1`, flow F, `deleting = true`.
- When: `InsertSegments` for flow F with one controlled segment on `a1`.
- Then: rejected, `…/object-deleting`. The allocation row does not change.
- Rules: BR-GC-12 row 4.

**TC-GC-029: An allocation for a different flow gives `object-wrong-flow`.** Integration, `internal/metastore`.
- Given: allocation `a1`, flow G.
- When: `InsertSegments` for flow F with one controlled segment on `a1`.
- Then: rejected, `…/object-wrong-flow`. The detail does not contain G. The allocation row does not change.
- Rules: BR-GC-12 row 5.

**TC-GC-030: A comeback before the claim is accepted.** Integration, `internal/metastore`.
- Given: object row `a1` with `ref_count = 0` and `deleting = false`.
- When: `InsertSegments` for flow F with one controlled segment on `a1`.
- Then: accepted. `ref_count = 1`.
- Rules: BR-GC-12 row 1.

**TC-GC-031: A BYOS segment needs no allocation.** Integration, `internal/metastore`.
- Given: no row for `b1`.
- When: `InsertSegments` with one BYOS segment on `b1`.
- Then: accepted. Object row `b1` with `ref_count = 1`, `storage_id IS NULL`.
- Rules: BR-GC-14.

**TC-GC-032: A BYOS segment on a claimed allocation gives `object-deleting`.** Integration, `internal/metastore`.
- Given: allocation `b1`, flow F, `deleting = true`.
- When: `InsertSegments` with one BYOS segment on `b1`.
- Then: rejected, `…/object-deleting`. No object row `b1`.
- Rules: BR-GC-14.

**TC-GC-033: A BYOS segment does not delete the allocation.** Integration, `internal/metastore`.
- Given: allocation `b1`, flow F, `deleting = false`.
- When: `InsertSegments` with one BYOS segment on `b1`.
- Then: accepted. Object row `b1` with `storage_id IS NULL`. The allocation row `b1` still exists.
- Rules: BR-GC-14.

**TC-GC-034: Two first registrations of one ID at the same time.** Integration, `internal/metastore`.
- Given: allocation `a1`, flow F. Flow G exists.
- When: at a barrier, goroutine 1 calls `InsertSegments` for flow F on `a1`, and goroutine 2 calls `InsertSegments` for flow G on `a1`.
- Then: repeat 50 times with new IDs. In each repetition, one of these results occurs:
  - Both are accepted and `ref_count = 2` (F first).
  - F is accepted and G gets `object-wrong-flow` (G read the allocation first).
  In both cases, `ref_count` equals the number of segment rows for the ID, and no allocation row stays.
- Rules: BR-GC-13.

**TC-GC-035: Registration and the sweep 2 claim at the same time.** Integration, `internal/metastore`.
- Given: allocation `a1`, flow F, `expires_at = now() - interval '1 hour'`.
- Case A: a raw transaction does `UPDATE allocations SET deleting = true WHERE id = 'a1'` and commits. Then `InsertSegments` on `a1`. Then: rejected, `object-deleting`.
- Case B: a raw transaction locks the row with `SELECT … FOR UPDATE` and holds it. `ClaimExpiredAllocations` returns `Selected = 0`. Then the raw transaction commits with no change, `InsertSegments` on `a1` is accepted, and a second `ClaimExpiredAllocations` returns nothing for `a1` (no allocation row).
- Rules: BR-GC-46.

**TC-GC-036: Sorted locks prevent deadlocks in registration.** Integration, `internal/metastore`, collation database.
- Given: object rows `B1` and `a1` with `ref_count = 0`, `deleting = false`, and no segments. Flows F and G.
- When: 200 repetitions. In each, two goroutines start at a barrier. Goroutine 1 registers on flow F two segments, on `a1` and then on `B1`. Goroutine 2 registers on flow G two segments, on `B1` and then on `a1`. Each repetition uses new timeranges.
- Then: no error with SQLSTATE `40P01`. After all repetitions, `ref_count` of each object equals its segment count.
- Rules: BR-GC-11, BR-GC-23.

**TC-GC-037: An overlap still rejects the full request.** Integration, `internal/metastore`.
- Given: allocation `a1`, flow F, and an existing segment on F at `[0:0_2:0)`.
- When: `InsertSegments` on F with one controlled segment on `a1` at `[1:0_3:0)`.
- Then: error `ErrSegmentOverlap`. The allocation row `a1` still exists. No object row `a1`.
- Rules: BR-GC-16.

**TC-GC-038: A group with a controlled and a BYOS segment is controlled.** Integration, `internal/metastore`.
- Given: no row for `m1`.
- When: `InsertSegments` with one controlled segment and one BYOS segment, both on `m1`.
- Then: both rejected with `object-not-allocated`.
- Rules: BR-GC-11, BR-GC-12.

**TC-GC-039: A request with no accepted segment changes nothing.** Integration, `internal/metastore`.
- Given: flow F with `segments_updated = '2020-01-01T00:00:00Z'` (set with SQL). No row for `zz`.
- When: `InsertSegments` for flow F with one controlled segment on `zz`.
- Then: nil error. `RejectedIndices = [0]`, `AcceptedIndices = []`. `segments_updated` of F is `2020-01-01T00:00:00Z`. No object row `zz`. No segment row.
- Rules: BR-GC-15.

## 3. Behaviour tests: deletes

**TC-GC-040: `DeleteFlow` decreases by the number of segments (B1).** Integration, `internal/metastore`.
- Given: object `a1` with `ref_count = 3`. Flow F has 2 segments on `a1`. Flow G has 1 segment on `a1`.
- When: `DeleteFlow(F)`.
- Then: `ref_count = 1`. Against the current code, the result is 2. Record this before the correction.
- Rules: BR-GC-20.

**TC-GC-041: `DeleteFlow` and a timerange delete at the same time (B2).** Integration, `internal/metastore`.
- Given: object `a1` with `ref_count = 2`. Flow F has segment s1 on `a1` at `[0:0_2:0)`. Flow G has segment s2 on `a1`.
- When:
  1. Raw transaction L: `BEGIN; SELECT 1 FROM objects WHERE id = 'a1' FOR UPDATE;`.
  2. Goroutine 1: `DeleteSegmentsByTimerange(F, "[0:0_2:0)")`.
  3. Wait until `pg_stat_activity` shows goroutine 1 with `wait_event_type = 'Lock'`.
  4. Goroutine 2: `DeleteFlow(F)`.
  5. Wait 500 ms, or until goroutine 2 is also blocked.
  6. `ROLLBACK` of L. Wait for both goroutines.
- Then: no error from either goroutine. Flow F does not exist. `ref_count = 1`. Against the current code, the result is 0. Record this before the correction.
- Rules: BR-GC-20.

**TC-GC-042: `DeleteFlow` and a segment insert at the same time (B8).** Integration, `internal/metastore`.
- Given: object `c1` with `ref_count = 1` from flow F. Object `b1` with `ref_count = 1` from flow G.
- When:
  1. Raw transaction L locks object `c1` with `FOR UPDATE`.
  2. Goroutine 1: `DeleteFlow(F)`.
  3. Wait until goroutine 1 is blocked or holds the flow lock.
  4. Goroutine 2: `InsertSegments(F, one controlled segment on b1)`.
  5. Release L. Wait for both.
- Then: `ref_count` of `b1` equals the number of segment rows on `b1`. With the correction, goroutine 2 waits for the flow lock and then gets "flow not found", and `ref_count(b1) = 1`. Against the current code, `ref_count(b1) = 2` with 1 segment row.
- Rules: BR-GC-20.

**TC-GC-043: Sorted locks prevent deadlocks in deletes.** Integration, `internal/metastore`, collation database.
- Given: in each repetition i, new flows F and G, each with segments on new objects `B<i>` and `a<i>` (F: `a<i>` then `B<i>`, G: `B<i>` then `a<i>`).
- When: 100 repetitions of `DeleteFlow(F)` and `DeleteFlow(G)` at a barrier.
- Then: no error with SQLSTATE `40P01`. Each object has `ref_count = 0`.
- Rules: BR-GC-23.

**TC-GC-044: The timerange delete decreases by the count.** Integration, `internal/metastore`.
- Given: object `a1` with `ref_count = 3`. Flow F has segments on `a1` at `[0:0_2:0)` and `[2:0_4:0)`.
- When: `DeleteSegmentsByTimerange(F, "[0:0_4:0)")`.
- Then: `ref_count = 1`.
- Rules: BR-GC-21.

**TC-GC-045: `DeleteFlow` of a missing flow.** Integration, `internal/metastore`.
- When: `DeleteFlow` of a random UUID.
- Then: `*apperror.AppError` with code `not-found`. No other change.
- Rules: BR-GC-20.

**TC-GC-046: `DeleteFlow` of a flow with no segments.** Integration, `internal/metastore`.
- Given: flow F with 0 segments.
- When: `DeleteFlow(F)`.
- Then: nil. Flow F does not exist.
- Rules: BR-GC-20.

**TC-GC-047: `DeleteFlow` of a read-only flow (B9).** Integration, `internal/metastore`.
- Given: flow F with `read_only = true` and one segment on `a1`. Object `a1` with `ref_count = 1`.
- When: `DeleteFlow(F)`.
- Then: `*apperror.AppError` with code `read-only`. Flow F, its segment, and `ref_count = 1` do not change. Against the current code, the flow is deleted. Record this before the correction.
- Rules: BR-GC-20, AC-GC-19.

**TC-GC-048: `DELETE /flows/{flowId}` of a read-only flow gives 403.** Unit, `internal/httpx` (router with the error middleware, fake flow service).
- Given: the fake `DeleteFlow` returns `apperror.New(apperror.ErrReadOnly, "flow is read-only")`.
- When: `DELETE /tams/v1/flows/<F>`.
- Then: status 403. The body `type` ends with `read-only`.
- Rules: BR-GC-20, AC-GC-19.

## 4. Behaviour tests: sweep 1 store methods

**TC-GC-060: A released controlled object is claimed and deleted.** Integration, `internal/metastore`, with MinIO for the e2e part in TC-GC-140.
- Given: object `r1` with `ref_count = 0`, `storage_id 'default'`, `deleting = false`.
- When: `ClaimReleasedObjects(ctx, "default", 1000)`, then `DeleteClaimedObjects(ctx, ["r1"])`.
- Then: the claim returns `Selected = 1`, `Claimed = ["r1"]`. After the claim, `deleting = true`, and `claimed_at` is within 5 s of the database `now()`. The delete returns `["r1"]`, and there is no object row `r1`.
- Rules: BR-GC-30, BR-GC-31, BR-GC-32, BR-GC-36.

**TC-GC-061: A released BYOS object is a row-only delete.** Integration, `internal/metastore`.
- Given: object `y1` with `ref_count = 0`, `storage_id NULL`.
- When: `ClaimReleasedObjects(ctx, "default", 1000)`.
- Then: `RowOnly = ["y1"]`, `Claimed = []`. No object row `y1`.
- Rules: BR-GC-34.

**TC-GC-062: An object in use is not a candidate.** Integration, `internal/metastore`.
- Given: object `u1` with `ref_count = 1` and one segment.
- When: `ClaimReleasedObjects`.
- Then: `Selected = 0`. The row does not change.
- Rules: BR-GC-30.

**TC-GC-063: Drift is repaired, not deleted.** Integration, `internal/metastore`.
- Given: object `d1` with `ref_count = 0`, `storage_id 'default'`. One segment row on `d1` (insert it with SQL).
- When: `ClaimReleasedObjects`.
- Then: `Drift = [{ObjectID: "d1", Actual: 1}]`, `Claimed = []`. Row `d1` has `ref_count = 1`, `deleting = false`.
- Rules: BR-GC-33, AC-GC-10.

**TC-GC-064: An unknown storage ID is not a candidate.** Integration, `internal/metastore`.
- Given: object `o1` with `ref_count = 0`, `storage_id 'other'`.
- When: `ClaimReleasedObjects(ctx, "default", 1000)` and `ReleasedBacklog(ctx, "default")`.
- Then: `Selected = 0`. `UnknownStorage = 1`. The row does not change.
- Rules: BR-GC-37.

**TC-GC-065: A claimed row is not a candidate again, and the lease protects it.** Integration, `internal/metastore`.
- Given: object `r1` with `ref_count = 0`, `deleting = true`, `claimed_at = now()`.
- When:
  1. `ClaimReleasedObjects(ctx, "default", 1000)`.
  2. `ReclaimRetryObjects(ctx, "default", 5*time.Minute, time.Now().Add(time.Minute), 1000)`.
  3. With SQL, set `claimed_at = now() - interval '10 minutes'`. Then step 2 again.
- Then: step 1 gives `Selected = 0`. Step 2 gives `[]` (a different process holds the lease). Step 3 gives `["r1"]`, and `claimed_at` of `r1` is within 5 s of the database `now()`.
- Rules: BR-GC-30, BR-GC-64, BR-GC-65, AC-GC-18.

**TC-GC-066: Two claims at the same time get different rows.** Integration, `internal/metastore`.
- Given: objects `r1` and `r2` with `ref_count = 0`.
- When: two transactions at a barrier call `ClaimReleasedObjects` with limit 1.
- Then: the two `Claimed` lists are `["r1"]` and `["r2"]` in some sequence.
- Rules: NFR-GC-R8.

**TC-GC-067: The reclaim gives one attempt in each cycle.** Integration, `internal/metastore`.
- Given: objects `k1`, `k2`, `k3` with `deleting = true`, `claimed_at = now() - interval '10 minutes'`. T is `time.Now()` at the start of the test.
- When: `ReclaimRetryObjects(ctx, "default", 5*time.Minute, T, 2)` three times.
- Then: `["k1", "k2"]`, then `["k3"]`, then `[]`. After the third call, each row has a `claimed_at` later than T.
- Short-lease case: with a lease of 5 minutes, the lease alone stops a second attempt in the same cycle, and the test cannot show a missing cycle-start condition. Thus a second test uses a lease of 1 ms. Given row `k1` as above, the first reclaim with cycle start T gives `["k1"]`. A second reclaim with the same T, after the lease expired, gives `[]`. A reclaim with a later cycle start gives `["k1"]`.
- Rules: BR-GC-64.

**TC-GC-068: The row delete removes only claimed rows.** Integration, `internal/metastore`.
- Given: object `r1` with `ref_count = 0`, `deleting = false`.
- When: `DeleteClaimedObjects(ctx, ["r1"])`.
- Then: `[]`. The row still exists.
- Rules: BR-GC-36.

**TC-GC-069: The foreign key stops a row delete with a segment.** Integration, `internal/metastore`.
- Given: object `r1` with `deleting = true`, and one segment row on `r1` (insert with SQL).
- When: `DeleteClaimedObjects(ctx, ["r1"])`.
- Then: an error with SQLSTATE `23503`. The row still exists.
- Rules: BR-GC-36.

**TC-GC-071: The row delete and a registration do not deadlock.** Integration, `internal/metastore`, collation database.
- Given: in each repetition i, objects `B<i>` and `a<i>` with `ref_count = 0`, `deleting = true`, and object `Z<i>` with `ref_count = 1`. In byte order, `B<i>` < `Z<i>` < `a<i>`. In the collation database, `a<i>` < `B<i>` < `Z<i>`.
- When: 5 repetitions. In each, a raw transaction locks row `Z<i>`. Goroutine 1 calls `InsertSegments` on flow F with one controlled segment on each of the three IDs. It locks `B<i>` and waits for `Z<i>`. The test waits until one session waits for a lock. Goroutine 2 calls `DeleteClaimedObjects(ctx, ["a<i>", "B<i>"])`. The test waits until two sessions wait for a lock. Then it releases the raw lock.
- Then: no error with SQLSTATE `40P01`. Goroutine 1 accepts the segment on `Z<i>` and gives `object-deleting` for `a<i>` and `B<i>`. Goroutine 2 removes `B<i>` and `a<i>`.
- Reason for the raw lock: in a mutation test, a version with two IDs and a barrier passed also when the row delete locked in collation order. The raw lock makes the overlap certain. In byte order, the row delete waits for `B<i>` and holds no lock. In collation order, it locks `a<i>` first, and the two transactions wait for each other.
- Rules: BR-GC-23, BR-GC-36.

## 5. Behaviour tests: sweep 2 store methods

**TC-GC-080: An allocation after `expires_at + grace` is claimed.** Integration, `internal/metastore`.
- Given: allocation `e1` with `expires_at = now() - interval '5 minutes' - interval '1 second'`.
- When: `ClaimExpiredAllocations(ctx, "default", 5*time.Minute, 1000)`, then `DeleteClaimedAllocations(ctx, ["e1"])`.
- Then: `Claimed = ["e1"]`. After the claim, `claimed_at` of `e1` is within 5 s of the database `now()`. The delete returns `["e1"]`, and no allocation row `e1` exists.
- Rules: BR-GC-40, BR-GC-41, BR-GC-44, AC-GC-11.

**TC-GC-081: An allocation before `expires_at + grace` is not claimed.** Integration, `internal/metastore`.
- Given: allocation `e2` with `expires_at = now() - interval '4 minutes'`.
- When: `ClaimExpiredAllocations` with grace 5m.
- Then: `Selected = 0`.
- Rules: BR-GC-40.

**TC-GC-082: Grace 0.** Integration, `internal/metastore`.
- Given: allocation `e3` with `expires_at = now() - interval '1 second'`.
- When: `ClaimExpiredAllocations` with grace 0.
- Then: `Claimed = ["e3"]`.
- Rules: BR-GC-40.

**TC-GC-083: An allocation with an object row is a leftover.** Integration, `internal/metastore`.
- Given: allocation `l1`, expired. Object row `l1` with `ref_count = 1`.
- When: `ClaimExpiredAllocations`.
- Then: `RowOnly = ["l1"]`, `Claimed = []`. No allocation row `l1`. The object row does not change.
- Rules: BR-GC-42, AC-GC-12.

**TC-GC-084: A leftover with an object at `ref_count = 0`.** Integration, `internal/metastore`.
- Given: allocation `l2`, expired. Object row `l2` with `ref_count = 0`, `deleting = false`.
- When: `ClaimExpiredAllocations`.
- Then: `RowOnly = ["l2"]`. The object row does not change (sweep 1 owns it).
- Rules: BR-GC-42.

**TC-GC-085: A leftover with a BYOS object row.** Integration, `internal/metastore`.
- Given: allocation `l3`, expired. Object row `l3` with `storage_id NULL`, `ref_count = 1`.
- When: `ClaimExpiredAllocations`.
- Then: `RowOnly = ["l3"]`, `Claimed = []`.
- Rules: BR-GC-42.

**TC-GC-086: A claimed allocation is not a candidate again, and the lease protects it.** Integration, `internal/metastore`.
- Given: allocation `e4`, expired, `deleting = true`, `claimed_at = now()`.
- When: `ClaimExpiredAllocations`. Then `ReclaimRetryAllocations(ctx, "default", 5*time.Minute, time.Now().Add(time.Minute), 1000)`. Then set `claimed_at = now() - interval '10 minutes'` with SQL, and do the reclaim again.
- Then: `Selected = 0`. The first reclaim gives `[]`. The second reclaim gives `["e4"]`.
- Short-lease case: the same test as the short-lease case of TC-GC-067, on `allocations` and `ReclaimRetryAllocations`.
- Rules: BR-GC-40, BR-GC-64, BR-GC-65.

**TC-GC-087: An allocation with an unknown storage ID.** Integration, `internal/metastore`.
- Given: allocation `e5`, expired, `storage_id 'other'`.
- When: `ClaimExpiredAllocations(ctx, "default", 5m, 1000)` and `AllocationBacklog(ctx, "default", 5m)`.
- Then: `Selected = 0`. `UnknownStorage = 1`.
- Rules: BR-GC-45.

**TC-GC-088: The backlog values of sweep 2.** Integration, `internal/metastore`.
- Given: allocation `p1` with `expires_at = now() - interval '15 minutes'`. Allocation `p2` with `expires_at = now() + interval '1 hour'`. Allocation `p3` with `deleting = true`.
- When: `AllocationBacklog(ctx, "default", 5*time.Minute)`.
- Then: `Pending = 1`, `Retrying = 1`, `Rows = 3`, `OldestOverdue` = 600 (tolerance 5).
- Rules: BR-GC-60, interface spec section 4.

## 6. Reliability tests: worker (`internal/gc`, unit, fakes)

The fake `Store` records each call. The fake `Deleter` returns results from a script.

**TC-GC-100: An error for each ID stops the cycle.** Unit.
- Given: claim returns `Selected 2, Claimed ["a", "b"]`. The deleter returns `ErrTransient` for `a` and `b`.
- When: one cycle of sweep 1.
- Then: `CycleResult{Failed: 2, BatchError: true}`. `DeleteClaimedObjects` is not called. The claim is called one time only. `opentams_gc_errors_total{sweep="released",stage="s3_delete"}` = 2 (one for each ID, no more). One ERROR line `gc batch error` with `stage = s3_delete`.
- Rules: BR-GC-62, AC-GC-13.

**TC-GC-101: The next cycle completes the delete.** Unit.
- Given: the state after TC-GC-100. `ReclaimRetryObjects` returns `["a", "b"]`. The deleter returns success.
- When: the next cycle.
- Then: `ReclaimRetryObjects` is called before `ClaimReleasedObjects`. `DeleteClaimedObjects(["a", "b"])` is called. `Deleted = 2`.
- Rules: BR-GC-64, AC-GC-13.

**TC-GC-102: An error for one ID does not stop the cycle.** Unit.
- Given: claim `["a", "b", "c"]`. The deleter fails `b` with `ErrTransient`.
- When: one cycle.
- Then: `DeleteClaimedObjects(["a", "c"])`. `Failed = 1`, `BatchError = false`. One WARN line names `b`.
- Rules: BR-GC-63.

**TC-GC-103: `ErrAuth` for the call stops the cycle.** Unit.
- Given: the deleter returns `(nil, ErrAuth)`.
- When: one cycle.
- Then: `BatchError = true`. `DeleteClaimedObjects` is not called. One ERROR line with `stage = s3_delete`. `errors_total{stage="s3_delete"} = 1`.
- Rules: BR-GC-62.

**TC-GC-104: `ErrAuth` for one key deletes the others first.** Unit.
- Given: claim `["a", "b"]`. The deleter returns success for `a` and `ErrAuth` for `b`.
- When: one cycle.
- Then: `DeleteClaimedObjects(["a"])` is called. Then `BatchError = true`. `errors_total{stage="s3_delete"} = 1` (for `b` only). One ERROR line `gc batch error`.
- Rules: BR-GC-62, logic model 9.2.

**TC-GC-105: A database error in the claim.** Unit.
- Given: `ClaimReleasedObjects` returns an error.
- When: one cycle.
- Then: `BatchError = true`. `errors_total{stage="claim"} = 1`.
- Rules: BR-GC-62.

**TC-GC-106: A database error in the row delete.** Unit.
- Given: deleter success. `DeleteClaimedObjects` returns an error.
- When: one cycle.
- Then: `BatchError = true`. `errors_total{stage="row_delete"} = 1`.
- Rules: BR-GC-62.

**TC-GC-107: The drain rule.** Unit.
- Given: `BatchSize = 3`. The claim returns `Selected` 3, 3, 1 in sequence.
- When: one cycle.
- Then: the claim is called 3 times. Second case: `Selected` 3, then 0. The claim is called 2 times.
- Rules: BR-GC-61.

**TC-GC-108: The retry phase repeats the reclaim with one cycle start.** Unit.
- Given: `BatchSize = 2`. The fake clock gives T at the start of the cycle. `ReclaimRetryObjects` returns `["a", "b"]`, then `["c"]`.
- When: one cycle.
- Then: `ReclaimRetryObjects` is called two times, each time with `cycleStart = T` and `lease = claimLease`. Each ID goes to the deleter one time.
- Rules: BR-GC-64.

**TC-GC-109: A batch error in the retry phase skips the claim phase.** Unit.
- Given: `ReclaimRetryObjects` returns `["a"]`. The deleter returns `(nil, ErrAuth)`.
- When: one cycle.
- Then: `ClaimReleasedObjects` is not called.
- Rules: BR-GC-64.

**TC-GC-110: The two sweeps are independent.** Unit.
- Given: the sweep 1 claim blocks on a channel. The sweep 2 store returns `Selected 0`.
- When: `Run` starts.
- Then: the sweep 2 cycle completes (its gauges are set) while sweep 1 is blocked.
- Rules: BR-GC-60, NFR-GC-P4.

**TC-GC-111: An empty batch is a success.** Unit.
- Given: the claim returns `Selected 0`.
- When: one cycle. The fake clock gives T.
- Then: `opentams_gc_last_success_timestamp_seconds{sweep="released"}` = T.
- Rules: BR-GC-61.

**TC-GC-112: Shutdown lets the current batch complete.** Unit.
- Given: the deleter blocks until the test closes a channel. The claim returns `Selected = BatchSize` (more work).
- When: `stop` is closed while the deleter blocks. Then the deleter channel is closed.
- Then: `DeleteClaimedObjects` is called for that batch. The claim is not called again. `Run` returns nil.
- Rules: BR-GC-67.

**TC-GC-113: The hard limit cancels the batch.** Unit.
- Given: the deleter blocks until its context is done.
- When: `stop` is closed and `ctx` is cancelled.
- Then: `Run` returns nil. `DeleteClaimedObjects` is not called. The rows stay claimed (BR-GC-66).
- Rules: BR-GC-66, BR-GC-67.

**TC-GC-114: Only removed rows are counted.** Unit.
- Given: deleter success for `["a", "b"]`. `DeleteClaimedObjects` returns `["a"]` (a different replica removed `b`).
- When: one cycle.
- Then: `items_total{sweep="released",outcome="deleted"} = 1`. One INFO `gc object removed` line, for `a`.
- Rules: BR-GC-65.

**TC-GC-115: `New` validates its configuration.** Unit.
- Given: these configurations, one at a time: `BatchSize` 0, `BatchSize` 1001, `PollInterval` 0, `AllocationGrace` -1s, `StorageID` "".
- When: `New`.
- Then: each one returns an error. `BatchSize` 1 and 1000 return no error.
- Rules: section 2 of the logic model.

**TC-GC-116: The database timeout.** Unit.
- Given: `dbTimeout = 50 ms`. The claim blocks until its context is done.
- When: one cycle.
- Then: the cycle returns within 1 s with `BatchError = true`.
- Rules: BR-GC-68.

**TC-GC-119: The object-store timeout.** Unit.
- Given: `s3Timeout = 50 ms`. The claim returns `Claimed ["a"]`. The deleter blocks until its context is done, and then returns `ErrTransient` for `a`.
- When: one cycle.
- Then: the cycle returns within 1 s. The context that the deleter got had a deadline. `Failed = 1`, `BatchError = true`.
- Rules: BR-GC-68, NFR-GC-R5.

**TC-GC-117: Sweep 2 leftovers and its outcome.** Unit.
- Given: `ClaimExpiredAllocations` returns `RowOnly ["l1"]`, `Claimed []`.
- When: one cycle of sweep 2.
- Then: the deleter is not called. `items_total{sweep="allocation",outcome="registered_leftover"} = 1`. One INFO line with `outcome = registered_leftover`.
- Rules: BR-GC-42.

**TC-GC-118: Sweep 1 drift is recorded.** Unit.
- Given: the claim returns `Drift [{d1, 2}]`.
- When: one cycle.
- Then: `opentams_gc_refcount_drift_total = 1`. One WARN `gc refcount drift` line with `object_id d1`, `stored_ref_count 0`, `actual_ref_count 2`.
- Rules: BR-GC-33.

**TC-GC-096: A batch older than half the lease is not sent.** Unit.
- Given: the claim returns `Claimed ["a"]`. The fake clock gives T after the claim, and `T + claimLease/2 + 1s` at the lease check.
- When: one cycle.
- Then: the deleter is not called. `DeleteClaimedObjects` is not called. One WARN line `gc batch skipped`. `BatchError = false`, `Failed = 0`.
- Rules: BR-GC-65, AC-GC-18.

**TC-GC-194: `RunOnce` stops claiming when `stop` closes.** Unit.
- Given: `BatchSize = 1`. The first claim of sweep 1 returns `Selected 1`, `Claimed ["a"]`. Later claims return nothing. The deleter closes `stop` and then deletes `a`.
- When: `RunOnce(ctx, stop)`.
- Then: `ClaimReleasedObjects` is called one time. The `gc cycle completed` line of sweep 1 has `ended_by = shutdown`. `Released.Deleted = 1`, `Released.BatchError = false`.
- Rules: BR-GC-67.

## 7. CLI and process tests (`cmd/opentams`)

**TC-GC-120: `gc --once` returns 0 after a normal cycle.** Integration, PostgreSQL and MinIO.
- Given: a migrated database. Object `r1` with `ref_count = 0`, `storage_id 'default'`. Key `r1` in the bucket.
- When: the command `gc --once` runs in the test process (call the cobra command).
- Then: exit code 0. No object row `r1`. No key `r1`.
- Rules: BR-GC-69, AC-GC-14.

**TC-GC-121: `gc --once` returns 1 after a batch error.** Integration, PostgreSQL.
- Given: object `r1` as in TC-GC-120. `OBJECT_STORE_ENDPOINT` points to a closed port.
- When: `gc --once`.
- Then: exit code 1. Row `r1` has `deleting = true`.
- Rules: BR-GC-69, AC-GC-14.

**TC-GC-122: `gc --once` opens no listener.** Integration.
- Given: `SERVER_PORT` set to a free port P.
- When: `gc --once` runs.
- Then: during the run, a TCP connection to P is refused.
- Rules: BR-GC-69.

**TC-GC-123: The help text.** Unit.
- When: `opentams gc --help`.
- Then: the text contains `--once`, `GC_POLL_INTERVAL`, `GC_BATCH_SIZE`, `GC_ALLOCATION_GRACE`, and `OPENTAMS_OBJECT_STORE_ID`. It does not contain `CronJob`, `sidecar`, or `flow delete requests`.
- Rules: section 10 of the logic model.

**TC-GC-124: A configuration error.** Unit.
- Given: `GC_BATCH_SIZE=0`.
- When: `gc --once`.
- Then: exit code 1. The error text contains `GC_BATCH_SIZE`.
- Rules: NFR-GC-S3.

**TC-GC-125: Admin endpoints and shutdown.** Integration, PostgreSQL and MinIO.
- Given: `gc` (no `--once`) on a free port P.
- When: the test calls `GET /healthz`, `GET /readyz`, and `GET /metrics`. Then the test sends SIGTERM (or closes the stop channel through the command function).
- Then: 200, 200, and a body that contains `opentams_gc_last_success_timestamp_seconds`. After the signal, `GET /readyz` returns 503 until the listener stops. The command returns exit code 0.
- Rules: BR-GC-67, NFR-GC-R6, NFR-GC-S1.

**TC-GC-126: `/readyz` with the database down.** Integration, PostgreSQL and MinIO.
- Given: `gc` (no `--once`) on a free port P, started with a database that operates.
- When: the test stops the PostgreSQL container. Then it calls `GET /readyz` and `GET /healthz`.
- Then: `/readyz` returns 503 within 10 s. `/healthz` returns 200.
- Rules: NFR-GC-R6.

**TC-GC-127: The admin listener ignores the TLS settings.** Integration, PostgreSQL and MinIO.
- Given: `SERVER_TLS_CERT_FILE=/nonexistent/cert.pem` and `SERVER_TLS_KEY_FILE=/nonexistent/key.pem`.
- When: `gc` (no `--once`) on a free port P. The test calls `GET http://127.0.0.1:P/healthz`.
- Then: 200 over plain HTTP.
- Rules: logic model section 10.

## 8. End-to-end tests (integration, PostgreSQL and MinIO, real handlers)

Use the HTTP handlers through the router with authentication in development mode, the real metastore, and the real object store against MinIO. Upload the bytes with an HTTP `PUT` to the returned URL.

**TC-GC-140: The full lifecycle of one object.**
1. `PUT /flows/F`.
2. `POST /flows/F/storage {"limit": 1}`. Record the object ID `o`.
3. `PUT` 16 bytes to the URL.
4. `POST /flows/F/segments [{"object_id": o, "timerange": "[0:0_2:0)"}]`.
5. `DELETE /flows/F`.
6. Start `gc --once`.
- Then: the step 4 response is 201. After step 4, there is no allocation row and the object row has `ref_count = 1`. After step 6, there is no object row, `HeadObject(o)` gives "not found", and the log has one `gc object removed` line with `object_id = o`, `outcome = deleted`.
- Rules: AC-GC-09.

**TC-GC-141: An upload with no registration.**
- Steps: `POST …/storage {"limit": 1}`, `PUT` bytes, then `UPDATE allocations SET expires_at = now() - interval '1 hour'`, then `gc --once`.
- Then: no allocation row. No key.
- Rules: BR-GC-40 to BR-GC-44.

**TC-GC-142: A BYOS registration of an allocated ID keeps the bytes.**
- Steps: `POST …/storage {"limit": 1}`, record the ID `o`, `PUT` bytes. `POST …/segments [{"object_id": o, "timerange": "[0:0_2:0)", "get_urls": [{"url": "https://cdn.example/x", "label": "cdn"}]}]`. With SQL, set `expires_at` of the allocation in the past. `gc --once`.
- Then: the registration is 201. The object row `o` has `storage_id NULL`. After `gc --once`: no allocation row, the key still exists, and the object row does not change.
- Rules: BR-GC-14, BR-GC-42.

**TC-GC-143: A shared object stays until its last reference goes.**
- Steps: register object `o` on flow F, then on flow G (reuse). `DELETE /flows/F`, `gc --once`. Then `DELETE /flows/G`, `gc --once`.
- Then: after the first `gc --once`, the key exists and `ref_count = 1`. After the second, no key and no row.
- Rules: BR-GC-30, NFR-GC-R1.

**TC-GC-144: Upstream segment modification (comeback) before the cycle.**
- Steps: register object `o` on flow F at `[0:0_2:0)`. `DELETE /flows/F/segments?timerange=[0:0_2:0)`. Register `o` again on F at `[0:0_2:0)`. `gc --once`.
- Then: the second registration is 201. The key exists. `ref_count = 1`.
- Rules: BR-GC-12 row 1.

## 9. Configuration tests (`internal/config`, unit)

**TC-GC-150: Presign time below 300 s.** `OBJECT_STORE_PRESIGN_EXPIRY=299s` gives an error that contains `OBJECT_STORE_PRESIGN_EXPIRY`. `300s` gives no error. Rules: BR-GC-09.

**TC-GC-151: Batch size range.** `GC_BATCH_SIZE` 0 and 1001 give an error that contains `GC_BATCH_SIZE`. 1 and 1000 give no error. Rules: NFR-GC-P2.

**TC-GC-152: Poll interval.** `GC_POLL_INTERVAL=0s` gives an error. `1s` gives no error. Rules: NFR-GC-P3.

**TC-GC-153: Allocation grace.** `GC_ALLOCATION_GRACE=-1s` gives an error. `0s` gives no error. Rules: section 11 of the logic model.

**TC-GC-154: Defaults.** With no GC variables: `GCPollInterval = 15m`, `GCBatchSize = 1000`, `GCAllocationGrace = 5m`, `ObjectStoreID = "default"`. Rules: section 11.

**TC-GC-155: The storage ID setting.** `OPENTAMS_OBJECT_STORE_ID=store-b` gives `ObjectStoreID = "store-b"`. Rules: section 11.

## 10. Serviceability tests (`internal/gc`, unit)

**TC-GC-160: Metric names.** Given a registry from `pkg/metrics.New(WithNamespace("opentams"))` and `NewMetrics`, run one cycle, then gather. Then each name of interface spec section 4 exists with the correct type and label names. The cycle is necessary because a metric with labels has no series, and thus does not appear in a gather, until the code sets a value for one label set.

**TC-GC-161: Gauges after a cycle.** Given `ReleasedBacklog` returns `{Pending 5, Retrying 2, UnknownStorage 1}`. After one cycle, `opentams_gc_backlog{sweep="released",state="pending"} = 5`, `retrying = 2`, `unknown_storage = 1`.

**TC-GC-162: The audit line.** For each outcome (`deleted`, `byos_row_only`, `registered_leftover`), one cycle with one ID gives one INFO line `gc object removed` with the fields `object_id`, `storage_id`, `sweep`, `outcome` (use `zaptest/observer`).

**TC-GC-163: The cycle summary.** After one cycle that stops with a batch error, the INFO line `gc cycle completed` has `ended_by = batch_error`, and the gauges are updated. After a normal cycle, `ended_by = empty`. After a shutdown, `ended_by = shutdown`. If `ctx` is cancelled during a batch and the batch returns an error, `ended_by = shutdown`, `BatchError = false`, and `ReleasedBacklog` is not called. If `stop` closes between two reclaims of the retry phase, the second reclaim does not occur.

**TC-GC-164: A gauge error does not stop the loop.** Given `ReleasedBacklog` returns an error. Then `errors_total{stage="gauge"} = 1`, one WARN line, and `BatchError = false`.

## 11. Migration tests (`migrations`)

**TC-GC-170: Schema after migration 6.** Integration. After `dbtest.Setup`:
- `information_schema.columns` has `objects.deleting` and has no `objects.reaping`.
- `pg_indexes.indexdef` of `objects_gc_claim_idx` contains `deleting = false`, and of `objects_gc_retry_idx` contains `deleting = true`.
- The table `allocations` has the columns of section 3.1 of the logic model.
- `allocations_gc_claim_idx`, `allocations_gc_retry_idx`, and `segments_object_id_idx` exist.

Rules: AC-GC-17.

**TC-GC-171: Down to version 5.** Integration. After `migrate down 1`: no table `allocations`, column `objects.reaping` exists, no index `segments_object_id_idx`.

**TC-GC-172: Migration 000006 is one transaction.** Unit. The up file contains `CREATE INDEX IF NOT EXISTS segments_object_id_idx ON segments (object_id);`. The up file and the down file do not contain `CONCURRENTLY`, `BEGIN`, or `COMMIT` (match without case). The down file contains `DROP INDEX IF EXISTS segments_object_id_idx`.

**TC-GC-173: Migration 000006 contents.** Unit, the same style as TC-MIG-05. The up file contains `ALTER TABLE objects RENAME COLUMN reaping TO deleting`, `CREATE TABLE IF NOT EXISTS allocations`, `allocations_gc_claim_idx`, and `allocations_gc_retry_idx`. The down file contains `DROP TABLE IF EXISTS allocations` and `RENAME COLUMN deleting TO reaping`. TC-MIG-05 does not change.

**TC-GC-174: The rename keeps the values.** Integration. Migrate to 5. Write an object row with `reaping = true`. Migrate to 6. The row has `deleting = true`.

**TC-GC-175: The minimum schema version.** Integration. Migrate to 5. `VerifySchema` returns `ErrSchemaTooOld`. `ExpectedSchemaVersion = 6`.

## 12. Helm tests (`helm template` with assertions in a Go test or a shell test in CI)

Each render and each lint uses `--values ci/default-values.yaml`. A bare `helm lint` fails the schema of the chart by design (`.github/workflows/contract.yml:13-16`).

**TC-GC-180: Default render.** The render gives a Deployment `<fullname>-gc` with `args: ["gc"]`, the probes `/healthz` and `/readyz`, and env `GC_POLL_INTERVAL=15m`, `GC_BATCH_SIZE=1000`, `GC_ALLOCATION_GRACE=5m`. Its metadata labels come from `opentams.gcLabels`, and its pod labels from `opentams.gcSelectorLabels`. No GC object has the label `app.kubernetes.io/name: <name>`. The selector of the API Service does not match the GC pod labels.

**TC-GC-181: Disabled.** `--set gc.enabled=false` gives no resource with the name suffix `-gc`.

**TC-GC-182: ServiceMonitor.** `--set serviceMonitor.enabled=true` gives a ServiceMonitor `<fullname>-gc` that selects the GC Service.

**TC-GC-183: Schema.** `helm lint --values ci/default-values.yaml --set gc.batchSize=1001` gives an error. `gc.batchSize=1000` gives no error.

**TC-GC-184: The API ServiceMonitor does not select GC objects.** `--set serviceMonitor.enabled=true`. The selector of the ServiceMonitor `<fullname>` does not match the labels of the Service `<fullname>-gc`. The selector of the ServiceMonitor `<fullname>-gc` matches the Service `<fullname>-gc` and does not match the Service `<fullname>`.

**TC-GC-185: Scrape annotations by default.** The default render gives the Service `<fullname>-gc` the annotations `prometheus.io/scrape: "true"`, `prometheus.io/port` equal to `server.port`, and `prometheus.io/path: /metrics`. With `--set serviceMonitor.enabled=true`, it has no `prometheus.io/*` annotation.

**TC-GC-186: TLS does not reach the GC pod.** `--set server.tls.enabled=true,server.tls.existingSecret=tls`. The Deployment `<fullname>-gc` has no TLS volume and no TLS volume mount. Its probes have no `scheme: HTTPS`.

## 13. Performance tests (`docs/scale-test-plan.md`)

| ID | Scale case | Measured value | Pass rule |
|---|---|---|---|
| TC-GC-190 | S1: registration with the allocation step, the new index, and sorted locks, compared with `main` | Insert p99, bulk segments each second | Inside the budgets of §6.5. A result outside a budget goes to the maintainers. No automatic design change. |
| TC-GC-191 | S3: sweep 1 at 500M segments | Objects each second for each replica. API p99 while the GC operates. | API p99 inside §6.5. The backlog does not grow in a 1 h mixed run. |
| TC-GC-192 | S4: sweep 2 and allocation churn | Dead tuples and autovacuum of `allocations` | Table size stays limited during the run |
| TC-GC-193 | S5: `DeleteFlow` after the correction | Delete p99 | Inside the delete budget of §6.5 |

## 14. Traceability: acceptance criteria to tests

| Criterion | Tests |
|---|---|
| AC-GC-01 | TC-GC-001, TC-GC-002 |
| AC-GC-02 | TC-GC-005 |
| AC-GC-03 | TC-GC-006, TC-GC-007 |
| AC-GC-04 | TC-GC-020 |
| AC-GC-05 | TC-GC-021, TC-GC-024 |
| AC-GC-06 | TC-GC-026, TC-GC-027 |
| AC-GC-07 | TC-GC-040 |
| AC-GC-08 | TC-GC-041, TC-GC-042 |
| AC-GC-09 | TC-GC-060, TC-GC-140 |
| AC-GC-10 | TC-GC-063 |
| AC-GC-11 | TC-GC-080, TC-GC-081, TC-GC-141 |
| AC-GC-12 | TC-GC-083 |
| AC-GC-13 | TC-GC-100, TC-GC-101 |
| AC-GC-14 | TC-GC-120, TC-GC-121 |
| AC-GC-15 | TC-GC-012 |
| AC-GC-16 | TC-GC-150 to TC-GC-154 |
| AC-GC-17 | TC-GC-170 |
| AC-GC-18 | TC-GC-065, TC-GC-096 |
| AC-GC-19 | TC-GC-047, TC-GC-048 |
