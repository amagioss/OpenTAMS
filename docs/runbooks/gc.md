# Runbooks — object garbage collection (GC)

Status: draft. These runbooks apply after the GC change is released ([design](../design/internal-gc/README.md)). Validate them after the first production deployment (REQ-OBS-08).

Each runbook has the format of REQ-OBS-08: Symptoms, Dashboard view, Investigation steps, Recovery verification. The metric names and the alerts are in the [interface specification](../design/internal-gc/functional-design/interface-spec.md#4-prometheus-metrics).

In the commands, `<ns>` is the namespace, `<release>` is the Helm release name, and `<fullname>` is the full name of the chart (`opentams.fullname`, for example `opentams` for the release `opentams`). The GC Deployment is `<fullname>-gc`. The GC pods have the label `app.kubernetes.io/name=<name>-gc`. `<name>` is `nameOverride`, or the chart name `opentams` if `nameOverride` is not set. The image has no shell. Call the binary directly.

## R1. The GC does not operate (alert GCDown)

**Symptoms.** `opentams_gc_last_success_timestamp_seconds` does not increase. Released objects stay in the bucket.

**Dashboard view.** `time() - max by (sweep) (opentams_gc_last_success_timestamp_seconds)` for each sweep. `opentams_gc_errors_total` by `stage`.

**Investigation steps.**
1. Show the pods: `kubectl -n <ns> get pods -l app.kubernetes.io/name=<name>-gc`.
2. If no pod operates, examine `gc.enabled` and `gc.replicaCount` in the Helm values.
3. If a pod restarts, read its logs: `kubectl -n <ns> logs deploy/<fullname>-gc --previous`. A configuration error names the variable.
4. Read the current logs. Find lines with the message `gc batch error`. The field `stage` tells where the error occurs.
5. For `stage = claim`, `reclaim`, or `row_delete`, examine the database connection. Follow the REQ-OBS-08 runbook "Metadata store unreachable".
6. For `stage = s3_delete`, go to R2.

**Recovery verification.** The last-success time increases again for both sweeps. The log has `gc cycle completed` lines with `ended_by = empty`.

## R2. Retry rows do not clear (alert GCRetryStuck)

**Symptoms.** `opentams_gc_backlog{state="retrying"}` stays above 0 for 2 h or more.

**Dashboard view.** `max by (sweep) (opentams_gc_backlog{state="retrying"})`. `rate(opentams_gc_errors_total{stage="s3_delete"}[15m])`.

**Investigation steps.**
1. Find the log lines `gc object delete failed`. Record the `object_id` values and the error text.
2. If the error is `AccessDenied`, make sure that the credentials of the GC have the permission `s3:DeleteObject` on the bucket.
3. If the bucket has Object Lock or a retention rule, the object store does not delete the key. Remove the rule from the bucket, or accept that the row stays until the retention ends.
4. If the error is `NoSuchBucket`, make sure that `OBJECT_STORE_BUCKET` is the same for `serve` and `gc`.
5. Do not delete the rows by hand. A row with `deleting = true` and a key that still exists is the only record of those bytes.

**Recovery verification.** `opentams_gc_backlog{state="retrying"}` decreases to 0 within two cycles after the correction.

## R3. `ref_count` drift (alert GCRefcountDrift)

**Symptoms.** `opentams_gc_refcount_drift_total` increases.

**Dashboard view.** `increase(opentams_gc_refcount_drift_total[1h])`.

**Investigation steps.**
1. The GC already stopped the delete and corrected the count. No media is lost.
2. Find the log lines `gc refcount drift`. Record `object_id`, `stored_ref_count`, and `actual_ref_count`.
3. For each `object_id`, find the requests that changed its segments before the drift. Use the access log and the `request_id`.
4. Open a bug report with these data. A drift means that a code path changes segments and does not change `ref_count` correctly.
5. Do not change `ref_count` by hand.

**Recovery verification.** For each recorded ID, this query returns no row:

```sql
SELECT o.id, o.ref_count, count(s.id) AS actual
FROM objects o LEFT JOIN segments s ON s.object_id = o.id
WHERE o.id = ANY('{<id1>,<id2>}')
GROUP BY o.id, o.ref_count
HAVING o.ref_count <> count(s.id);
```

## R4. The backlog grows (alerts GCBacklogGrowing, GCAllocationLag)

**Symptoms.** `opentams_gc_backlog{state="pending"}` increases for hours, or `opentams_gc_oldest_overdue_allocation_seconds` is more than 3600.

**Dashboard view.** The pending backlog for each sweep. `rate(opentams_gc_items_total[15m])`. `histogram_quantile(0.99, rate(opentams_gc_batch_duration_seconds_bucket[15m]))`. The database CPU and the API p99.

**Investigation steps.**
1. If `opentams_gc_errors_total` increases at the same time, go to R1 or R2.
2. If the batch time is high, examine the database load and the object-store latency.
3. If the API p99 is normal, increase `gc.replicaCount` by 1.
4. If `gc.batchSize` is less than 1000, increase it. The maximum is 1000.
5. If the API p99 is high, do not add replicas. Do a capacity review of the database first.

**Recovery verification.** The pending backlog decreases for each sweep. The API p99 stays inside its budget.

## R5. Rows with an unknown storage ID (alert GCUnknownStorage)

**Symptoms.** `opentams_gc_backlog{state="unknown_storage"}` is more than 0.

**Dashboard view.** `max by (sweep) (opentams_gc_backlog{state="unknown_storage"})`.

**Investigation steps.**
1. Find the storage IDs:

   ```sql
   SELECT storage_id, count(*) FROM objects
   WHERE ref_count = 0 AND deleting = false AND storage_id IS NOT NULL
   GROUP BY storage_id;
   SELECT storage_id, count(*) FROM allocations GROUP BY storage_id;
   ```

2. Compare them with `OPENTAMS_OBJECT_STORE_ID` of the GC and of the API.
3. If the value changed, set the old value again on both, or move the bytes and the rows to the new storage ID in one planned operation.

**Recovery verification.** The gauge is 0 after the next cycle.

## R6. Stop the GC

**Symptoms.** Planned maintenance.

**Dashboard view.** Not applicable.

**Investigation steps (procedure).**
1. Stop the GC: `helm upgrade <release> … --set gc.replicaCount=0`, or `kubectl -n <ns> scale deploy/<fullname>-gc --replicas=0`.
2. Make sure that no GC pod operates.
3. Do the maintenance.
4. Start the GC again with the previous replica count.

**Recovery verification.** R1 shows a normal state.

## R7. Migration 000006 stopped with an error

**Symptoms.** The migration job stops with an error at version 6. `schema_migrations` shows `version = 6` and `dirty = true`. The new pods do not start (`ErrSchemaTooOld`, or a dirty schema).

**Dashboard view.** Not applicable.

**Investigation steps (procedure).**
1. Read the log of the migration job. Find the statement that failed and the cause.
2. Find the state of the schema with this query:

   ```sql
   SELECT column_name FROM information_schema.columns
   WHERE table_name = 'objects' AND column_name IN ('reaping', 'deleting')
   UNION ALL
   SELECT 'allocations' FROM information_schema.tables WHERE table_name = 'allocations';
   ```

3. If the query returns only `reaping`, PostgreSQL rolled back the migration. The file is one transaction (BR-GC-51). Go to step 6.
4. If the query returns `deleting` and `allocations`, the migration committed, and only the dirty flag stays (for example, the job stopped after the commit). Set the version: `opentams migrate force 6`. Then go to the recovery verification.
5. If the query returns a different result, stop. Do not use `migrate force`. Make a report of the result to the maintainers.
6. Correct the cause of step 1. An example is a statement timeout that is shorter than the index build.
7. Set the version back: `opentams migrate force 5`.
8. Apply the migration again: `opentams migrate up`.

**Recovery verification.** `schema_migrations` shows `version = 6` and `dirty = false`. The query of step 2 returns `deleting` and `allocations`.
