# OpenTAMS Scale-Test Runbook

Operational, copy-paste steps for running the scale-test harness against a
~500M-segment Postgres (RDS / Cloud SQL). The *design* lives in
`docs/scale-test-plan.md`; this file is the **how-to-run**.

The three binaries (`scaletest-loader`, `scaletest-bench`, `scaletest-loadgen`)
all re-derive row addresses from the same seeded generator, so the
`**--preset` / `--segments` / `--seed` triple MUST be identical** across load,
bench, and loadgen. Pick it once and reuse it everywhere.

```
PRESET=6
SEGMENTS=500000000
SEED=42
```

---

## 1. Build the binaries (build host, matching target arch)

```bash
go build -o scaletest-loader  ./tools/scaletest/loader
go build -o scaletest-bench   ./tools/scaletest/bench
go build -o scaletest-loadgen ./tools/scaletest/loadgen
```

Copy `scaletest-bench` to the host that can reach the DB directly, and
`scaletest-loadgen` to the load-generation host (mirror production topology:
load-gen, API, DB on separate hosts — plan §7).

## 2. DB connection env (read by all three tools via `dbenv`)

```bash
export DB_HOST=<endpoint> DB_PORT=5432 \
       DB_USER=<user> DB_PASSWORD=<pw> \
       DB_NAME=<db> DB_SSLMODE=require
```

## 3. Load the dataset

Preview the plan (no DB writes):

```bash
scaletest-loader plan --preset=$PRESET --segments=$SEGMENTS --seed=$SEED
```

Load. The GiST `no_segment_overlap` exclusion constraint is created **after**
the bulk `COPY`, so the long tail of the load is the index build, not the copy:

```bash
scaletest-loader load --preset=$PRESET --segments=$SEGMENTS --seed=$SEED \
  --workers=8 --reset
```

`--reset` truncates first. Run inside `screen`/`tmux` — a 500M load + GiST build
is multi-hour.

### Confirming the load actually finished

The loader can print "done" while Postgres is still building the GiST index in
the background. Confirm completion before doing anything else:

```sql
-- 1. No index build still running:
SELECT pid, phase, blocks_done, blocks_total
FROM pg_stat_progress_create_index;          -- expect 0 rows

-- 2. No invalid indexes (a failed/aborted build leaves one behind):
SELECT indexrelid::regclass AS index, indrelid::regclass AS table
FROM pg_index WHERE NOT indisvalid;          -- expect 0 rows

-- 3. The exclusion constraint exists:
SELECT conname FROM pg_constraint WHERE conname = 'no_segment_overlap';
```

---

## 4. Pre-snapshot validation (REQUIRED before snapshotting)

A snapshot freezes whatever state the DB is in — including stale planner
statistics and any half-built index. Do **all** of the following first, so the
baseline you restore from is query-plan-correct.

### 4a. Refresh statistics into the snapshot

```sql
ANALYZE;
```

Run `ANALYZE` *before* the snapshot so every restored instance starts with fresh
stats (a freshly restored RDS instance does not re-analyze for you).

### 4b. Row-count + integrity sanity

```sql
SELECT
  (SELECT count(*) FROM segments) AS segments,
  (SELECT count(*) FROM objects)  AS objects,
  (SELECT count(*) FROM flows)    AS flows;

-- ref_count must equal the actual number of referencing segments
-- (sample a few objects; a full join over 500M is itself a heavy scan):
SELECT o.id, o.ref_count, c.actual
FROM objects o
JOIN LATERAL (
  SELECT count(*) AS actual FROM segments s WHERE s.object_id = o.id
) c ON true
ORDER BY o.id
LIMIT 20;                                    -- ref_count == actual for each
```

### 4c. Pick a *deep* flow for plan checks

The scale risk lives on deep flows (~150k segments each). Grab one — this
`GROUP BY` is a one-off full scan, acceptable as a pre-snapshot check:

```sql
SELECT flow_id FROM segments
GROUP BY flow_id ORDER BY count(*) DESC LIMIT 1 \gset
-- now :flow_id holds a deep flow's UUID for the EXPLAINs below
```

### 4d. EXPLAIN the query shapes the harness will run

Verify the planner uses the indexes, **not** a sequential scan over 500M rows.
These are the exact shapes from `internal/metastore/segments.go`.

**list-by-flow** (read path; the `ListSegments` cursor query) — must use
`segments_flow_lower`:

```sql
EXPLAIN (ANALYZE, BUFFERS)
SELECT s.id, s.flow_id, s.object_id, s.timerange, s.lower_ns, s.upper_ns
FROM segments s
WHERE s.flow_id = :'flow_id'
ORDER BY s.lower_ns ASC, s.id ASC
LIMIT 100;
-- EXPECT: Index Scan using segments_flow_lower  (NOT Seq Scan)
```

**list-by-timerange** (read path with the time window filter) — same index,
with the `lower_ns`/`upper_ns` range predicate applied:

```sql
EXPLAIN (ANALYZE, BUFFERS)
SELECT s.id, s.flow_id, s.object_id, s.timerange, s.lower_ns, s.upper_ns
FROM segments s
WHERE s.flow_id = :'flow_id'
  AND (s.lower_ns < 9000000000000)
  AND (s.upper_ns IS NULL OR s.upper_ns > 1000000000000)
ORDER BY s.lower_ns ASC, s.id ASC
LIMIT 100;
-- EXPECT: Index Scan using segments_flow_lower with an Index Cond on flow_id+lower_ns
```

**overlap probe** (the insert path's `no_segment_overlap` cost) — confirms the
GiST exclusion index is usable for overlap lookups, which is what every insert
pays:

```sql
EXPLAIN (ANALYZE, BUFFERS)
SELECT 1 FROM segments
WHERE flow_id = :'flow_id'
  AND int8range(lower_ns, COALESCE(upper_ns, 9223372036854775807))
      && int8range(0, 9223372036854775807);
-- EXPECT: Index Scan using no_segment_overlap  (the GiST index), NOT Seq Scan
```

**delete-by-timerange** (delete path) — must use the same b-tree index:

```sql
EXPLAIN
DELETE FROM segments
WHERE flow_id = :'flow_id'
  AND lower_ns < 9000000000000
  AND (upper_ns IS NULL OR upper_ns > 1000000000000);
-- EXPECT: Index Scan using segments_flow_lower  (do NOT run with ANALYZE — it deletes)
```

> If any of these shows a **Seq Scan** or a wildly wrong row estimate, stop:
> re-run `ANALYZE`, confirm the index is valid (§3), and fix the plan before
> snapshotting. A snapshot taken with bad plans bakes the problem into every run.

### 4e. Record table / index sizes (baseline metadata)

```sql
SELECT relname,
       pg_size_pretty(pg_total_relation_size(relid))   AS total,
       pg_size_pretty(pg_relation_size(relid))          AS heap,
       pg_size_pretty(pg_indexes_size(relid))           AS indexes
FROM pg_catalog.pg_statio_user_tables
WHERE relname IN ('segments','objects','flows')
ORDER BY pg_total_relation_size(relid) DESC;
```

Capture this output — it's the size baseline you compare variant runs against.

---

## 5. Snapshot

You're on RDS, so this is a storage snapshot (not `pg_dump` — a logical dump of
500M rows is impractical). Run it **only after §4 passes**.

```bash
SNAP=opentams-${SEGMENTS}-baseline-$(date +%Y%m%d)

aws rds create-db-snapshot \
  --db-instance-identifier <instance-id> \
  --db-snapshot-identifier "$SNAP"

aws rds wait db-snapshot-available --db-snapshot-identifier "$SNAP"
```

Cloud SQL equivalent: `gcloud sql backups create` then
`gcloud sql instances clone` for the restore.

---

## 6. Restore rules (load-once → restore-per-run)

RDS restores into a **new instance** (new endpoint), never in-place:

```bash
RUN_INST=opentams-run-$(date +%Y%m%d-%H%M)

aws rds restore-db-instance-from-db-snapshot \
  --db-instance-identifier "$RUN_INST" \
  --db-snapshot-identifier "$SNAP" \
  --db-instance-class db.r7g.4xlarge \
  --storage-type gp3

aws rds wait db-instance-available --db-instance-identifier "$RUN_INST"
# update DB_HOST (and the API's DB config) to the new endpoint before running
```

**Rules — these matter for trustworthy numbers:**

1. **Warm the gp3 volume first.** A snapshot-restored gp3 volume lazy-loads
  blocks from S3 on first touch; the first read of any block is slow and *not*
   representative. Before measuring, force a full hydrate:
   The harness `--warmup` window only primes what it happens to touch; it does
   **not** substitute for this full warm pass.
2. **Read-only buckets need no restore.** `list-by-flow`, `list-by-timerange`,
  and `page-deep-flow` don't mutate — run all of them against one restored
   instance back-to-back.
3. **Batch the destructive runs, then restore once.** Delete buckets,
  `--profile=destructive`, and `cascade-delete-deep` mutate the dataset. Run
   them as a group and restore from the baseline snapshot afterward — not
   between every run (a 1TB restore is tens of minutes + a second instance's
   cost).
4. **Tear down throwaway instances:**
  ```bash
   aws rds delete-db-instance --db-instance-identifier "$RUN_INST" \
     --skip-final-snapshot
  ```

---

## 7. DB-direct tier — `scaletest-bench`

```bash
scaletest-bench list-buckets       # show bucket names + which are opt-in

# default run = the 8 segment buckets (read/write/delete); excludes opt-in extras:
scaletest-bench run --preset=$PRESET --segments=$SEGMENTS --seed=$SEED \
  --samples=2000 --warmup=200 \
  --report=bench-baseline-$(date +%F).json
```

Opt-in wider-scale buckets (run explicitly; `cascade-delete-deep` is destructive →
restore after):

```bash
scaletest-bench run … --bucket=page-deep-flow        # read-only
scaletest-bench run … --bucket=cascade-delete-deep   # destructive
```

Markdown summary prints to stderr; JSON goes to `--report`.

## 8. HTTP tier — `scaletest-loadgen`

Start the API against the restored, warmed instance (`APP_ENV=development` for
dev-auth), then:

```bash
# steady 60/40, non-destructive, with DB-side pg_stat_* diff:
scaletest-loadgen run --base-url=http://<api>:8080 \
  --preset=$PRESET --segments=$SEGMENTS --seed=$SEED \
  --rps=500 --burst-rps=1500 --duration=15m --warmup=30s \
  --capture-db --report=loadgen-steady-$(date +%F).json
```

Wider-scale scenarios:

```bash
# overload ramp — finds the breaking-point RPS:
scaletest-loadgen ramp --base-url=http://<api>:8080 \
  --preset=$PRESET --segments=$SEGMENTS --seed=$SEED \
  --ramp-from=200 --ramp-step=200 --ramp-max=5000 --step-dur=30s \
  --report=ramp-$(date +%F).json

# idempotency lock contention (BOTH flags required):
scaletest-loadgen run --base-url=http://<api>:8080 \
  --preset=$PRESET --segments=$SEGMENTS --seed=$SEED \
  --profile=idem-contention --idem-keys=1 --duration=5m

# JWT overhead A/B: run twice, same load —
#   (A) API in dev-auth mode (token=dummy)
#   (B) API with the JWT provider; pass the real token via file:
scaletest-loadgen run --base-url=http://<api>:8080 --token-file=jwt.txt …

# destructive (deletes) — restore the snapshot afterward:
scaletest-loadgen run --base-url=http://<api>:8080 --profile=destructive …
```

---

## 9. Reports — generate & compare

Each run writes a JSON `report.Report` and prints a markdown summary. Sections:
per-bucket latency (p50/p95/p99/max, throughput, err%); `load`
(attempted/sent/completed RPS, shed%, max in-flight) for loadgen; a `ramp` table
for `ramp`; `db.stats` (pg_stat_* before/after diff) when `--capture-db` is set.

**Compare against budgets — manual.** The harness does *not* yet flag SLO
breaches (plan §11.4 is aspirational). Eyeball the p99 columns against REQ-PERF
(`docs/design/*/nfr-requirements/`):


| Bucket                   | Budget           |
| ------------------------ | ---------------- |
| writes (insert/register) | p99 < 200 ms     |
| reads (list per page)    | p99 < 50 ms      |
| delete (≤10k)            | < 500 ms         |
| bulk register            | ≥ 120 segments/s |


**Compare across runs / across DBs — manual.** Name reports by target+date and
diff the bucket arrays:

```bash
jq '.buckets[] | {name, p99: .latency.p99_ms, thr: .throughput_per_sec}' \
   bench-baseline-2026-06-05.json

# RDS vs Cloud SQL, or baseline vs a phase-2 variant:
diff <(jq -S '.buckets' rds.json) <(jq -S '.buckets' cloudsql.json)
```

Each JSON embeds the tool commit, dataset triple, and host/DB metadata, so every
report is self-describing.

---

## Quick checklist

- Same `--preset/--segments/--seed` everywhere
- Load finished — `pg_stat_progress_create_index` empty, no invalid indexes
- `ANALYZE` run; row counts + ref_count sane
- EXPLAIN: list-by-flow, list-by-timerange, overlap probe, delete all use indexes (no Seq Scan)
- Sizes recorded
- Snapshot created **after** the above
- Restore → full warm pass before measuring
- Read buckets first; destructive runs batched, restore once after
- Throwaway instances deleted

