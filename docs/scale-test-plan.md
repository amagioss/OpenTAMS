# OpenTAMS Scale Test Plan

Status: **Draft for review** · Scope: planning document (harness implementation follows separately)

## 1. Purpose & goals

Establish a repeatable way to battle-test and benchmark OpenTAMS for production.
Concretely:

1. **Benchmark for production** — measure latency, throughput, and resource cost
   against a production-equivalent topology.
2. **Simulate clients** — drive the API with a scriptable load generator modelling
   realistic ingest + playback.
3. **Hold ~500M segment rows** — validate behaviour at production data volume,
   where each immutable object reference materialises a segment row.
4. **Capture latency / CPU / resources** — per-operation percentiles plus host
   and database resource utilisation.
5. **Exercise the wider API server** — connection pooling, idempotency, auth,
   pagination, deletes, soak, and overload behaviour.
6. **Evaluate DB & design choices** — quantify the cost of the current schema
   decisions (GiST exclusion, `ref_count`, indexing, partitioning) at scale.

### What this test answers that existing tests do not

The current `perf`-tagged tests (`internal/metastore/segments_perf_test.go`)
validate the published budgets at **100k segments on a single flow**. The
budgets (REQ-PERF, see §6) were written against that fixture. This plan validates
that those budgets **still hold — or finds where they break — at ~500M rows in
the `segments` table**, across a realistic distribution of flows.

## 2. System under test

Postgres-backed metastore (`migrations/000001_init.up.sql`). Tables and the
relationships that drive the data model:

- `sources` → `flows` (FK `flows.source_id`)
- `flows` → `segments` (FK `segments.flow_id`, `ON DELETE CASCADE`)
- `objects` → `segments` (FK `segments.object_id`); `objects.ref_count` tracks
  how many segments reference an object.
- `segments` carries the **GiST `EXCLUDE` constraint `no_segment_overlap`** on
  `(flow_id WITH =, int8range(lower_ns, COALESCE(upper_ns, max)) WITH &&)` plus a
  btree `segments_flow_lower (flow_id, lower_ns)`.

### Identified scale risks

| # | Risk | Why it matters at 500M |
|---|---|---|
| R1 | **GiST `EXCLUDE` overlap check** | Every segment insert probes the **single global composite GiST index** over `(flow_id, int8range)` — `btree_gist` lets `flow_id =` live in the same tree as the range `&&`. Cost depends on (a) global tree depth (set by total table size — ~500M rows) and (b) how sharply the `flow_id =` predicate prunes the descent. GiST equality pruning is **approximate** (bounding-box based, less crisp than a btree prefix), so deep flows force more candidate inspection. Dominant write-path risk; deep flows + giant table compound it. |
| R2 | **`objects.ref_count` writes** | Each reference updates `ref_count`. Popular objects become hot rows → lock contention under concurrent writers. |
| R3 | **Global index size** | btree + GiST over 500M rows affect cache residency, planner choices, and read latency even for shallow per-flow queries. |
| R4 | **MVCC / autovacuum** | A 60/40 read/write mix at volume generates bloat; autovacuum must keep pace or read latency degrades. |

## 3. Data model & derivation

### 3.1 Target volume

| Quantity | Target | Notes |
|---|---|---|
| segments | **~500M** | Anchor metric. Rounded up from the workload-model figure of ~363M for headroom against the "at least half a billion" requirement. |
| objects | **~500M** | ≈ 1:1 with segments (avg refs ≈ 1). Content reuse is modelled during generation, not as shared `object_id` rows. |
| flows | **~1M** | Derived from the bimodal distribution below (not the uniform 7.26M of the original model). |
| sources | **~167k** | Derived, not free: `sources ≈ flows / renditions_per_essence` (≈ 6). See §3.5. |
| flow_collection | **~833k** | Member rows beyond the first flow per source (`flows − sources`). |

### 3.2 Workload model (origin of the figures)

The segment count derives from a content-generation model:

```
flows   = (15 days × 35/100) × (24×60) × 16 × 300(feeds) / 5(min per item) ≈ 7.26M items
objects = flows × (5 min × 60 / 6s chunk) = flows × 50                      ≈ 363M
segments ≈ objects                                                          ≈ 363M
```

`35/100` is the unique-content fraction (35% unique over the 15-day window); `16`
is the combined dimensions×variants factor; chunk size is 6s. The model yields
~363M; the plan targets **~500M** for headroom.

### 3.3 Distribution shape — bimodal, parameterised by chunk duration

Per-flow depth is **not** a free assumption — it is derived from **segment
chunk duration**. Real TAMS uses both **6 s** and **1 s** chunks; each
produces materially different per-flow depth from the same content duration.
The plan therefore runs **both as canonical scenarios** at the same ~500M
anchor, and chunk duration is a first-class loader knob.

Per-flow depth (derived from content duration):

| Class | Content duration | Depth @ 6 s chunks | Depth @ 1 s chunks |
|---|---|---|---|
| Deep (live, 24×7) | 15 days × 86,400 s | ~216,000 seg/flow | ~1,296,000 seg/flow |
| Shallow (VOD item) | 5 min × 60 s | ~50 seg/flow | ~300 seg/flow |

Bimodal compositions at the ~500M anchor:

| Scenario | Chunk | Deep flows × depth | Deep subtotal | Shallow flows × depth | Shallow subtotal | Total flows |
|---|---|---|---|---|---|---|
| **S-6s** (breadth) | 6 s | ~3,000 × 150k | ~450M | ~1,000,000 × 50 | ~50M | ~1.0M |
| **S-1s** (depth) | 1 s | ~300 × 1.3M | ~390M | ~370,000 × 300 | ~111M | ~370k |

Both scenarios isolate different stress modes against the **single global**
composite GiST index (§2 risk R1):

- **S-6s** stresses **index breadth and planner choices** — many flows, big
  tree, more diverse `flow_id =` keys.
- **S-1s** stresses **per-insert overlap-probe sharpness** — one flow holds up
  to ~1.3M tightly packed ranges in the *same* global tree, so the
  bounding-box-based `flow_id =` pruning has more candidate ranges to inspect.
  This is the realistic worst case for the GiST insert path.

Chunk duration is a **per-flow** loader knob, so mixed datasets are possible;
S-6s and S-1s are the two scenarios run by default.

Generation parameters: total segments, **per-flow chunk duration (1 s / 6 s)**,
deep/shallow split, per-class flow counts, renditions-per-essence (§3.5), and
a **seeded RNG**.

### 3.5 Source grouping — by essence

Sources are not a free knob; they are bounded by an FK invariant
(`flows.source_id UUID NOT NULL`) and the TAMS essence model:

- A **source represents one content essence**. The flows under it are different
  **renditions (resolutions/bitrates) of that same essence**, grouped via
  `flow_collection`.
- **Different essences do not share a source.** Video, audio, captions, etc. for
  the same program each get their own source — they are never grouped together.

Therefore the grouping factor is **renditions per essence (~6)**, not the full
dimensions×variants figure:

```
sources              ≈ flows / renditions_per_essence ≈ 1,000,000 / 6 ≈ 167,000
flow_collection rows ≈ flows − sources                                ≈ 833,000
```

`renditions_per_essence` is a loader knob (default 6); the loader populates
`flow_collection` for the grouped members.

### 3.4 Storage envelope (planning estimate)

~500M segment rows ≈ 150–250 GB table + sizeable GiST/btree indexes; ~500M
objects ≈ tens of GB. Plan for **~0.5 TB** of database storage per dataset. This
is why the methodology loads once and restores from a snapshot between runs (§7).

## 4. Harness architecture (Approach C)

Three independently deliverable components plus reporting.

```
                    +-------------------------+
                    |  Bulk data loader (Go)  |  generate (seeded) -> COPY -> snapshot
                    +-----------+-------------+
                                | restore per run
                                v
   +------------------+   +-----+------+   +--------------------------+
   | DB-direct tier   |   |  Postgres  |   | HTTP load tier (Go)      |
   | scaletest-bench  |-->|  (managed, |<--| custom client, 60/40 mix |
   | (Go binary; uses |   |  vanilla)  |   | concurrent ingest+play   |
   |  internal/       |   +------------+   +--------------------------+
   |  perftest as lib)|
   +------------------+
            \                                        /
             \                                      /
              v                                    v
            +--------------------------------------------+
            | Per-bucket reports + resource capture      |
            | (JSON/CSV + markdown; host & DB metrics)   |
            +--------------------------------------------+
```

- **Bulk data loader** (`tools/scaletest/loader/`, Go) — generates the bimodal
  dataset deterministically and bulk-loads via Postgres `COPY` (the seed pattern
  in `internal/metastore/segments_perf_test.go:seedNSegments`, scaled up).
  Honours FK ordering (`sources → flows → flow_collection → objects → segments`),
  groups rendition flows under shared essence-sources (§3.5), and keeps
  `ref_count` consistent. The GiST exclusion constraint is created after the bulk COPY for
  load speed, then validated; steady-state insert tests run with it enabled.
  After load it produces a **snapshot** (`pg_dump`/volume snapshot) for restore.
  The deterministic generator lives in a shared, pgx-free package
  (`tools/scaletest/dataset/`) so the bench and HTTP tiers replay the same
  `(plan, seed)` to derive any flow_id / object_id without lookup queries.
- **DB-direct tier** (`scaletest-bench`, `tools/scaletest/bench/`, Go binary) —
  a standalone binary (not `go test`) so a single precompiled artifact runs on
  the measurement host without a repo checkout or Go toolchain. It imports
  `internal/perftest` as a library for the nearest-rank latency summary and
  drives metastore methods through the pgx pool against the loaded dataset,
  isolating **schema/design cost** with no HTTP/app overhead. Subcommands:
  `list-buckets`, `plan` (both no-DB), and `run` (measures selected buckets and
  emits the per-bucket report). Buckets run sequentially — per-call latency
  isolation; concurrency/contention scenarios belong to the HTTP tier and D6.
- **HTTP load tier** (`scaletest-loadgen`, `tools/scaletest/loadgen/`, Go binary) —
  a custom load generator hitting the running API over HTTP. It reuses the shared
  `tools/scaletest/dataset` generator to address the loaded data (same replay
  trick as the bench — no lookup queries) and `tools/scaletest/report` for output.
  No generated HTTP client exists, so it hand-rolls requests against the real
  routes (base path `/tams/v1`): `GET/POST/DELETE …/flows/{id}/segments` (POST
  carries the required `X-Idempotency-Key`) and `POST …/flows/{id}/storage`.
  Subcommands: `endpoints` (print mix + plan, no network) and `run`.
  **Auth:** a bearer token is always sent; a dummy value passes the dev-auth
  provider, keeping JWT-validation cost out of D4 numbers (that overhead is a D6
  concern). The realistic JWT path is opt-in via `--token` against a server
  running the JWT provider.

  **Load model: open-loop / target RPS.** The generator offers requests at a
  configured baseline RPS regardless of server speed (a rate-limited scheduler
  feeding a bounded in-flight pool, `--max-inflight`). When the in-flight bound
  saturates, the would-be request is **shed** and counted — the open-loop
  overload signal. This is deliberately *not* closed-loop: a closed-loop
  generator throttles itself when the server slows, hiding the overload behaviour
  a scale test exists to find.

  **Jitter burst.** On top of the steady baseline the target rises to
  `--burst-rps` for `--burst-for` (default 45 s) every `--burst-every` (default
  **5 min**), then returns to baseline — exercising *steady-state capacity* and
  *recovery* (queue drain, p99 settle, autovacuum catch-up). Setting
  `--burst-rps=0` disables bursting.

  **Profiles.** `steady` (default, non-destructive): 60% read / 40% write / **0%
  deletes**, so reads never race shrinking data and steady-state numbers are
  repeatable. `destructive` (opt-in): adds delete ops. Default sub-mix —
  list-by-timerange 35%, list-by-flow 25%, register-bulk 14%, insert-deep 8%,
  insert-shallow 6%, create-storage-endpoint 6%, idempotent-retry 6%. Writes
  append in disjoint segment-index regions (mirroring the bench) so concurrent
  writers never collide. `idempotent-retry` issues the first POST and an
  immediate same-key replay, reported as **separate** `idempotent-first` and
  `idempotent-dedupe` buckets.

  **Load-control telemetry.** Beyond per-bucket latency, every loadgen report
  carries a `load` section: attempted / sent / completed RPS, shed count + rate,
  max observed in-flight, and the baseline/burst targets.

## 5. Workload buckets (reporting is split by these)

Per-bucket reporting is a first-class requirement: aggregate numbers hide the
cost of the operations that matter. Every report breaks results down by:

**Writes**
- deep-live-flow insert (append to a ~150k-segment flow)
- shallow-VOD insert
- register-segments bulk
- overlap-rejection path (insert violating the GiST exclusion — measure the
  rejection latency, not just success)
- idempotent-retry path (re-POST with the same `Idempotency-Key`)
- create-storage-endpoint (object-store allocation / URL issuance)

**Reads**
- list-by-flow
- list-segments-by-timerange

**Deletes**
- delete-by-flow
- delete-by-object (including `ref_count` decrement)

For each bucket: **p50 / p95 / p99 / max latency, throughput, error/rejection
rate**, and DB-side cost where measurable.

## 6. Metrics, resource capture & SLO mapping

### 6.1 Application latency
`internal/perftest/perftest.go` provides `Summarize`, returning a `Stats`
(`Min/P50/P95/P99/Max`, count) via the same nearest-rank method as the existing
`P99` (which it preserves for the current perf tests), retaining the
env-override budget mechanism. The bench imports it as a library.

### 6.2 Server-side
Scrape the existing Prometheus metrics during runs: the `opentams_segment_*`
counters and the HTTP latency histogram (added in commit `079c2c7`).

### 6.3 Database-side (implemented — D5b)
The harness captures the portable DB-side signal via the `tools/scaletest/dbstats`
package: a **before→after snapshot of the standard `pg_stat_*` views**, diffed
into the report's `db.stats` section. The views are identical on RDS and Cloud
SQL, so this needs only a connection — no host agent. Captured: cache-hit ratio,
WAL bytes, checkpoints, commits/rollbacks, deadlocks, temp files/bytes, and
per-table (`segments`, `objects`) live/dead tuples, autovacuum/analyze counts,
and size growth. `pg_stat_statements` top-queries-by-time are included **when the
extension is enabled** (it requires `shared_preload_libraries` + a reboot;
captured gracefully if present, skipped if not). Version differences
(`pg_stat_checkpointer` vs `pg_stat_bgwriter`, `pg_stat_wal`) are handled
best-effort. The bench captures this automatically (it holds a pool); loadgen
captures it on `--capture-db` (opens a side pool from `DB_*`).

### 6.4 Host metrics (deferred)
The original plan called for self-collecting OS metrics (CPU/mem/disk/network)
on both the API host and the DB host. Two reasons this is **deferred, may
revisit**:

1. **The DB host is unreachable.** Managed Postgres (RDS / Cloud SQL) gives no
   OS-level access, so a self-collected DB-host agent is impossible; those
   metrics live in the provider's dashboard (CloudWatch / Performance Insights),
   which is cloud-specific and out of scope for a cloud-agnostic tool. The
   in-DB `pg_stat_*` diff (§6.3) is the portable substitute and carries the
   bottleneck signal anyway.
2. **The harness host's saturation is already observable.** Whether the bench /
   loadgen host itself is the bottleneck is inferable from the `load` section
   (a gap between target `--rps` and `attempted_rps`, or rising shed) and from a
   live `top` on the box — so baking `/proc` (Linux-only) or a `gopsutil`
   dependency into the report is low ROI. If a real run shows the generator
   pegging out in a way `load` doesn't make obvious, process-host metrics are a
   cheap later add.

Prometheus correlation (the API's existing `opentams_*` counters + HTTP
histogram, §6.2) remains available to scrape externally.

### 6.5 SLO mapping (existing REQ-PERF budgets)
Each bucket is compared against the published budgets — now at 500M instead of
100k. A bucket exceeding its budget is flagged in the report.

| Operation | API budget | Metastore budget |
|---|---|---|
| Segment write (single POST) | p99 < 200 ms | insert p99 ≤ 80 ms |
| Segment write (bulk) | ≥ 120 segs/s | ≥ 200 segs/s |
| Segment read (GET page) | p99 < 50 ms | list p99 ≤ 30 ms |
| Delete (≤10k in 1 h) | p99 < 500 ms | ≤ 400 ms |
| 50 concurrent inserts / flow | — | ≤ 5 s |

### 6.6 Output
Machine-readable JSON plus a per-bucket markdown summary, via the shared
`tools/scaletest/report/` package — a serialization-only contract (no DB/HTTP
deps) so both the bench and the HTTP tier emit the identical `Report` shape and
one dashboard / diff tool renders either. Every run embeds **environment
metadata** (host, Postgres version + scale-relevant settings, dataset shape,
seed, and the harness commit SHA) so runs are comparable across machines, clouds,
and schema variants. HTTP-tier reports additionally carry a `load` section
(attempted/sent/completed RPS, shed count + rate, max in-flight, baseline/burst
targets); the DB-direct tier omits it. Both tiers attach the `db.stats` section
(§6.3) when DB capture is active.

## 7. Methodology

- **Baseline first** on a single vanilla Postgres node to isolate schema/design
  cost, then validate portability by running the **same harness** on managed
  vanilla Postgres on more than one cloud (e.g. RDS *and* Cloud SQL).
  Cloud-agnostic by design — no provider-specific engine features.
- **Topology: mirror production** — load generator, API server, and managed
  Postgres on separate hosts, so the production benchmark includes real network
  latency and managed-storage IOPS ceilings.
- **Load once → snapshot → restore per run.** Deterministic seeded data makes
  runs reproducible and keeps per-iteration cost bounded.
- **Warm-up** before each measurement window; report only the measurement window.
- **Steady-state vs post-burst reporting.** With the 5-minute jitter pattern,
  per-bucket reports distinguish two windows: **steady-state** (between bursts,
  sustained baseline) and **post-burst recovery** (the interval immediately
  after each spike — until p99 returns to within X% of the steady value).
  Recovery time is itself a reported metric.
- Full 500M runs are gated behind the `perf` build tag / explicit invocation and
  never execute in default CI (`go test ./...`).

## 8. Wider API-server scale tests (D6)

Beyond the segment hot path. The four below are **implemented**; soak and a
dedicated connection-pool test remain (notes at the end).

**Overload backpressure (`scaletest-loadgen ramp`).** Open-loop ramp: the
offered rate climbs (`--ramp-from` / `--ramp-step` / `--ramp-max`, holding each
level for `--step-dur`) until shed rate or aggregate error rate crosses a
threshold (`--shed-threshold` / `--err-threshold`). The report's `ramp` section
records each level's attempted/completed RPS, shed %, error %, and worst p99,
plus the **breaking-point RPS** — the offered load past which the server
degrades (429s / timeouts / shed).

**Idempotency lock contention (`--profile=idem-contention --idem-keys=1`).** A
loadgen profile dominated by idempotent-retry; `--idem-keys=K` collapses many
concurrent requests onto a shared pool of K idempotency keys (K=1 = all on one
`idempotency_keys` row, migrations 000002 / 000004). The `idempotent-dedupe`
bucket then shows the dedupe-path latency under row-lock contention.

**JWT validation overhead (A/B).** D4 runs against dev-auth, so JWT cost is
excluded from baseline numbers. To measure it, run loadgen **twice** against the
same restored dataset — once vs a server in dev-auth (`APP_ENV=development`,
`--token=dummy`) and once vs a server with the JWT provider (`APP_ENV` set to a
non-dev value + the `AUTH_*` issuer/audience config) using a real token via
`--token-file` (keeps the JWT out of process args). The per-bucket p99 delta is
the JWT + JWKS-fetch cost. The harness cannot mint the token — the server
validates against live JWKS, so the token must come from the configured OIDC
issuer.

**Deep pagination + cascade-delete (`scaletest-bench`, opt-in buckets).**
`page-deep-flow` cursor-paginates a deep flow to the end, measuring per-page
latency as the offset grows. `cascade-delete-deep` deletes whole deep flows via
raw SQL, timing the FK cascade over each flow's ~150k segments. Both are opt-in
(named explicitly, never in the default `run`): pagination is slow and the
cascade is heavily destructive (restore the snapshot after).

**Remaining (not yet built):** multi-hour soak (steady load for hours with
periodic `dbstats` snapshots → a time series of bloat / autovacuum / leak
signals — needs a periodic-capture mode) and a dedicated connection-pool
saturation test (partly emergent from the overload ramp). Track as follow-ups.

## 9. Design-choice evaluation (Phase 2 — deferred)

Documented now, built after the baseline harness proves the methodology. Each
variant requires its own ~500M dataset and is compared via the DB-direct tier:

1. GiST `EXCLUDE` constraint **on vs off** — the price of overlap protection at scale.
2. **Partitioning** — unpartitioned vs hash-partition `segments` by `flow_id`
   (the constraint is per-flow, so it becomes partition-local; bounds tree depth
   and index size).
3. **Index strategy** — necessity of `segments_flow_lower`, the JSONB GIN index
   (migration 000003), and covering indexes for list.
4. **`ref_count` model** — per-reference `UPDATE` vs batched/deferred; hot-row
   contention.
5. **`object_id TEXT` PK** — FK-lookup cost at 500M objects.
6. **`timerange TEXT` vs `int8range`** — the table stores both the text and
   `lower_ns/upper_ns`; measure the redundancy cost.
7. **`get_urls` JSONB TOAST** overhead at 500M rows.

## 10. Implementation deliverables (post-review)

| ID | Deliverable | Location | Status |
|---|---|---|---|
| D2 | Bulk data loader | `tools/scaletest/loader/`, `tools/scaletest/dataset/`, `tools/scaletest/dbenv/` | done |
| D3 | DB-direct scale bench (binary) + `perftest.Summarize` + report contract | `tools/scaletest/bench/`, `internal/perftest/perftest.go`, `tools/scaletest/report/` | done |
| D4 | HTTP load generator (open-loop + jitter, steady/destructive profiles) | `tools/scaletest/loadgen/` (reuses `dataset` + `report`) | done |
| D5 | Per-bucket reporting + DB resource capture (`pg_stat_*` diff) | `tools/scaletest/report/`, `tools/scaletest/dbstats/` (wired into bench + loadgen `--capture-db`) | done (host metrics deferred, §6.4) |
| D6 | Wider API-server scale tests (overload ramp, idempotency contention, JWT A/B, deep pagination + cascade) | `tools/scaletest/loadgen/` (ramp, idem-contention, `--token-file`), `tools/scaletest/bench/` (opt-in `page-deep-flow` / `cascade-delete-deep`) | done (soak + conn-pool test deferred, §8) |

The deterministic generator (`dataset`), the DB-env plumbing (`dbenv`), the
report contract (`report`), and the DB-stats capture (`dbstats`) are shared
packages the loader, bench, and loadgen import as needed — no code is duplicated
across the three binaries.

## 11. Verification (of the harness, once built)

1. Loader correctness at small N (`--segments=1M`): per-table row counts, bimodal
   distribution, FK integrity, `ref_count` consistency; GiST exclusion holds post-load.
2. DB-direct tier: build once (`go build -o scaletest-bench ./tools/scaletest/bench`),
   copy the binary to the measurement host, then
   `DB_HOST=… scaletest-bench run --preset=6 --segments=500000000 --seed=42 --bucket=…`
   emits the per-bucket p50/p95/p99/max report compared to budgets. `--preset`,
   `--segments`, and `--seed` MUST match the load so the bench addresses the
   right rows. Write/delete buckets mutate the dataset — restore the snapshot
   between measured runs (§7).
3. HTTP tier: run the API (dev-auth) against a restored snapshot, then build once
   (`go build -o scaletest-loadgen ./tools/scaletest/loadgen`) and from the
   load host run e.g.
   `scaletest-loadgen run --base-url=http://api:8080 --preset=6 --segments=500000000 --seed=42 --rps=500 --burst-rps=1500 --duration=15m`.
   Confirm the steady 60/40 mix, the per-bucket report (incl. `idempotent-first`
   / `idempotent-dedupe`), and the `load` section (attempted/sent/completed RPS,
   shed, max in-flight). `steady` is non-destructive; use `--profile=destructive`
   (and restore the snapshot afterward) to exercise deletes.
4. SLO check: reports flag any bucket exceeding REQ-PERF at 500M.
5. Portability: comparable reports on at least two managed Postgres targets.
6. Full 500M run never runs in default CI.

## Open items for reviewers

- Confirmation of managed Postgres instance class(es) to benchmark against.
- Whether the soak duration and overload thresholds (§8) should have explicit targets.
