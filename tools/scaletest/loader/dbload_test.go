//go:build integration

package main

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/amagioss/opentams/internal/dbtest"
	"github.com/amagioss/opentams/tools/scaletest/dataset"
)

// allowedSegmentsSurvivors lists the constraints and indexes that
// SHOULD remain on `segments` after dropSegmentsConstraintsAndIndex
// runs. Anything else surviving the drop step would silently slow the
// bulk COPY (fires per-row over 500M rows). TestDropLeavesOnlyAllowed
// Survivors enforces this list; each entry needs a one-line
// justification — keep the list short.
//
// Format:
//
//	"constraint:<contype>:<name>"   (NOT NULL constraints are filtered out;
//	                                they're free during COPY)
//	"index:<name>"
var allowedSegmentsSurvivors = []string{
	"constraint:c:segments_upper_ns_positive", // CHECK; ~5ns/row, negligible vs FK/btree
	"constraint:p:segments_pkey",              // PK; can never drop
	"index:segments_pkey",                     // PK's backing index
}

// TestLoadEndToEnd_Small spins a Postgres testcontainer with the full
// migration tree, runs Load with a small plan, and verifies every
// invariant the larger 500M run depends on:
//
//   - row counts in each table match the Plan,
//   - the GiST EXCLUDE constraint exists after re-add (ie the generated
//     ranges actually don't overlap; ADD CONSTRAINT validates the data),
//   - segments → flows → sources FK chain is satisfied (no orphan rows),
//   - objects.ref_count is set to 1 (avg_refs ≈ 1 per the plan).
//
// Gated behind testing.Short() because container startup is ~10 s; the
// test runs under `make integration` (and locally without -short).
func TestLoadEndToEnd_Small(t *testing.T) {
	if testing.Short() {
		t.Skip("loader integration test: needs Postgres testcontainer")
	}
	ctx := context.Background()
	pool, cleanup, err := dbtest.Setup(ctx, dbtest.DefaultMigrationsPath())
	if err != nil {
		t.Fatalf("dbtest.Setup: %v", err)
	}
	defer cleanup()

	plan, err := dataset.Compose(dataset.Knobs{
		TargetSegments:      10_000,
		ChunkDurationSec:    6,
		DeepDepth:           1_000,
		ShallowDepth:        10,
		DeepFlowCount:       9,
		RenditionsPerSource: 6,
	})
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}

	// Use a chunk size that forces multiple chunks per table — the
	// chunked path is the production path; the test must exercise it,
	// not the fall-through "all in one chunk" case. Workers=4 also
	// exercises the parallel segments-COPY path (PartitionedSegmentIter
	// + errgroup + atomic progress aggregation).
	var progressMu sync.Mutex
	var progressCalls int
	progressByTable := map[string]int{}
	opts := LoadOptions{
		ChunkSize: 250, // 40 chunks for 10k segments, 4 for 1k flows, etc.
		Workers:   4,   // parallel segments-COPY across 4 workers
		ProgressFn: func(table string, _ /*done*/, _ /*total*/ int64) {
			progressMu.Lock()
			defer progressMu.Unlock()
			progressCalls++
			progressByTable[table]++
		},
	}
	if err := Load(ctx, pool, plan, 42, opts); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if progressCalls < 5 {
		t.Errorf("ProgressFn called %d times; expected multiple chunks per table", progressCalls)
	}
	// segments alone (10k rows across 4 workers at chunk 250) must
	// still emit ≥ 40 chunks total across workers.
	if progressByTable["segments"] < 40 {
		t.Errorf("segments progress calls = %d, want >= 40", progressByTable["segments"])
	}

	// Row counts match the plan exactly.
	checks := []struct {
		table string
		want  int64
	}{
		{"sources", plan.SourcesCount},
		{"flows", plan.TotalFlows},
		{"flow_collection", plan.FlowCollectionRows},
		{"objects", plan.ObjectsCount},
		{"segments", plan.TotalSegments},
	}
	for _, c := range checks {
		var n int64
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM "+c.table).Scan(&n); err != nil {
			t.Errorf("count %s: %v", c.table, err)
			continue
		}
		if n != c.want {
			t.Errorf("count(%s) = %d, want %d", c.table, n, c.want)
		}
	}

	// Every constraint and index dropped during the bulk-load path
	// must be re-created by rebuildSegmentsConstraintsAndIndex. Each
	// guards a real invariant — GiST: non-overlap, FKs: referential
	// integrity, btree: query path. A missing one means the rebuild
	// silently regressed (or VALIDATE found a real integrity failure).
	for _, c := range []string{
		"no_segment_overlap",
		"segments_flow_id_fkey",
		"segments_object_id_fkey",
	} {
		var conname string
		err := pool.QueryRow(ctx, `
			SELECT conname FROM pg_constraint
			 WHERE conrelid = 'segments'::regclass
			   AND conname  = $1
		`, c).Scan(&conname)
		if err != nil {
			t.Errorf("constraint %s missing after Load: %v", c, err)
		}
	}
	var idxname string
	if err := pool.QueryRow(ctx, `
		SELECT indexname FROM pg_indexes
		 WHERE tablename = 'segments'
		   AND indexname = 'segments_flow_lower'
	`).Scan(&idxname); err != nil {
		t.Errorf("segments_flow_lower index missing after Load: %v", err)
	}

	// FK integrity — every segment.flow_id must exist in flows.id; every
	// flow.source_id must exist in sources.id. The FK is declared, so
	// these counts come from anti-joins.
	var orphanSegs, orphanFlows int64
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM segments s
		 WHERE NOT EXISTS (SELECT 1 FROM flows f WHERE f.id = s.flow_id)
	`).Scan(&orphanSegs); err != nil {
		t.Errorf("orphan segments query: %v", err)
	}
	if orphanSegs != 0 {
		t.Errorf("orphan segments (flow_id with no flow): %d", orphanSegs)
	}
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM flows f
		 WHERE NOT EXISTS (SELECT 1 FROM sources sc WHERE sc.id = f.source_id)
	`).Scan(&orphanFlows); err != nil {
		t.Errorf("orphan flows query: %v", err)
	}
	if orphanFlows != 0 {
		t.Errorf("orphan flows (source_id with no source): %d", orphanFlows)
	}

	// ref_count is what the plan assumes (1 per object, since segments ≈
	// objects 1:1). If we ever model shared objects, this will need to
	// change in lockstep with the plan.
	var minRefCount, maxRefCount int32
	if err := pool.QueryRow(ctx,
		`SELECT min(ref_count), max(ref_count) FROM objects`).
		Scan(&minRefCount, &maxRefCount); err != nil {
		t.Errorf("ref_count agg: %v", err)
	}
	if minRefCount != 1 || maxRefCount != 1 {
		t.Errorf("ref_count out of [1,1]: min=%d max=%d", minRefCount, maxRefCount)
	}
}

// TestReset_TruncatesLoaderTables proves Reset clears every table the
// loader writes (so a follow-up Load succeeds with no duplicate-PK
// errors) and leaves the migrate state alone (so `migrate version`
// still reports the schema is at HEAD).
func TestReset_TruncatesLoaderTables(t *testing.T) {
	if testing.Short() {
		t.Skip("loader integration test: needs Postgres testcontainer")
	}
	ctx := context.Background()
	pool, cleanup, err := dbtest.Setup(ctx, dbtest.DefaultMigrationsPath())
	if err != nil {
		t.Fatalf("dbtest.Setup: %v", err)
	}
	defer cleanup()

	plan, err := dataset.Compose(dataset.Knobs{
		TargetSegments:      1_000,
		ChunkDurationSec:    6,
		DeepDepth:           100,
		ShallowDepth:        10,
		DeepFlowCount:       5,
		RenditionsPerSource: 6,
	})
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}

	// Phase 1: load and confirm tables are populated.
	if err := Load(ctx, pool, plan, 7, LoadOptions{ChunkSize: 100}); err != nil {
		t.Fatalf("first Load: %v", err)
	}
	var segs int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM segments`).Scan(&segs); err != nil {
		t.Fatalf("count segments: %v", err)
	}
	if segs != plan.TotalSegments {
		t.Fatalf("pre-Reset segments = %d, want %d", segs, plan.TotalSegments)
	}

	// Phase 2: Reset and confirm every loader table is empty.
	if err := Reset(ctx, pool); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	for _, tbl := range []string{
		"sources", "flows", "flow_collection",
		"objects", "segments", "source_tags", "flow_tags",
	} {
		var n int64
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM "+tbl).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", tbl, err)
		}
		if n != 0 {
			t.Errorf("after Reset, count(%s) = %d, want 0", tbl, n)
		}
	}

	// Phase 3: schema_migrations untouched (Reset must not lie to migrate).
	var migrationVersion int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM schema_migrations`).Scan(&migrationVersion); err != nil {
		t.Fatalf("schema_migrations: %v", err)
	}
	if migrationVersion == 0 {
		t.Error("Reset truncated schema_migrations; it must be preserved")
	}

	// Phase 4: a follow-up Load works (no duplicate-PK errors from
	// leftover rows). This is the actual operational scenario the
	// --reset flag exists to support.
	if err := Load(ctx, pool, plan, 7, LoadOptions{ChunkSize: 100}); err != nil {
		t.Fatalf("Load after Reset: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM segments`).Scan(&segs); err != nil {
		t.Fatalf("count segments after reload: %v", err)
	}
	if segs != plan.TotalSegments {
		t.Errorf("post-reload segments = %d, want %d", segs, plan.TotalSegments)
	}
}

// TestLoaderRebuildMatchesMigrations is the drift-detection guard for
// the loader's hand-written DDL in rebuildSegmentsConstraintsAndIndex.
// It spins two testcontainers — one with just migrations, one with
// migrations + Load — and asserts the segments-table constraint and
// index definitions are byte-identical between them. A future
// migration that changes a constraint definition will fail this test
// immediately, naming the offending object; without this guard, the
// drift would silently land production with a stale schema in every
// scale-test measurement.
//
// What it catches:
//   - rebuild DDL drifts from migrations (different ON DELETE, etc.)
//   - a constraint is dropped but not re-added (post-Load missing it)
//
// What it does NOT catch (intentional — covered by TestReset / other tests):
//   - a constraint is in migrations but not in the loader's drop list
//     (both containers would have it identically; that hazard surfaces
//     instead on a re-run via TestReset_TruncatesLoaderTables's
//     "Load after Reset works" phase, which would fail with
//     "constraint already exists" if rebuild tried to add a name the
//     drop list missed).
func TestLoaderRebuildMatchesMigrations(t *testing.T) {
	if testing.Short() {
		t.Skip("loader integration test: needs Postgres testcontainer")
	}
	ctx := context.Background()

	// Container A: migrate-up only. This is the canonical schema.
	poolA, cleanupA, err := dbtest.Setup(ctx, dbtest.DefaultMigrationsPath())
	if err != nil {
		t.Fatalf("dbtest.Setup A: %v", err)
	}
	defer cleanupA()
	ddlA, err := captureSegmentsDDL(ctx, poolA)
	if err != nil {
		t.Fatalf("captureSegmentsDDL A: %v", err)
	}

	// Container B: migrate-up + Load. After Load, the segments-table
	// DDL must match what migrate-up produces in isolation.
	poolB, cleanupB, err := dbtest.Setup(ctx, dbtest.DefaultMigrationsPath())
	if err != nil {
		t.Fatalf("dbtest.Setup B: %v", err)
	}
	defer cleanupB()

	plan, err := dataset.Compose(dataset.Knobs{
		TargetSegments:      1_000,
		ChunkDurationSec:    6,
		DeepDepth:           100,
		ShallowDepth:        10,
		DeepFlowCount:       5,
		RenditionsPerSource: 6,
	})
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}
	if err := Load(ctx, poolB, plan, 1, LoadOptions{ChunkSize: 100}); err != nil {
		t.Fatalf("Load B: %v", err)
	}
	ddlB, err := captureSegmentsDDL(ctx, poolB)
	if err != nil {
		t.Fatalf("captureSegmentsDDL B: %v", err)
	}

	if joined := strings.Join(ddlA, "\n"); joined != strings.Join(ddlB, "\n") {
		t.Errorf(
			"segments-table DDL drifts between migrate-only and post-Load\n"+
				"--- migrate-only (container A) ---\n%s\n"+
				"--- post-Load (container B) ---\n%s\n",
			joined,
			strings.Join(ddlB, "\n"),
		)
	}
}

// TestDropLeavesOnlyAllowedSurvivors guards the maintenance contract:
// every constraint or index on `segments` must either be dropped by
// dropSegmentsConstraintsAndIndex (perf-impacting; fires per row
// during COPY) or be in allowedSegmentsSurvivors (free during COPY).
//
// A future migration adding a constraint/index without thinking about
// this — e.g. a UNIQUE, a partial index, a CHECK that touches JSONB —
// would silently survive the drop step, slow the bulk COPY by
// minutes-to-hours at 500M, and not be caught by any other test
// (TestLoaderRebuildMatchesMigrations passes because the schema is
// identical pre- and post-Load; TestLoadEndToEnd_Small only checks
// the named constraints it knows about).
//
// When this test fails, the dev adding the migration has two choices:
//  1. Add the new object to dropSegmentsConstraintsAndIndex (and its
//     rebuild side); or
//  2. Add it to allowedSegmentsSurvivors with a one-line justification.
func TestDropLeavesOnlyAllowedSurvivors(t *testing.T) {
	if testing.Short() {
		t.Skip("loader integration test: needs Postgres testcontainer")
	}
	ctx := context.Background()
	pool, cleanup, err := dbtest.Setup(ctx, dbtest.DefaultMigrationsPath())
	if err != nil {
		t.Fatalf("dbtest.Setup: %v", err)
	}
	defer cleanup()

	if err := dropSegmentsConstraintsAndIndex(ctx, pool); err != nil {
		t.Fatalf("dropSegmentsConstraintsAndIndex: %v", err)
	}

	survivors, err := captureSegmentsSurvivors(ctx, pool)
	if err != nil {
		t.Fatalf("captureSegmentsSurvivors: %v", err)
	}

	want := slices.Clone(allowedSegmentsSurvivors)
	slices.Sort(want)

	if !slices.Equal(survivors, want) {
		t.Errorf(
			"post-drop survivors on `segments` don't match allowedSegmentsSurvivors.\n\n"+
				"actual survivors (NOT NULL filtered):\n  %s\n\n"+
				"allowlist:\n  %s\n\n"+
				"if the new entry impacts COPY perf, add it to dropSegmentsConstraintsAndIndex\n"+
				"(and its counterpart in rebuildSegmentsConstraintsAndIndex);\n"+
				"otherwise add it to allowedSegmentsSurvivors with a one-line justification.",
			strings.Join(survivors, "\n  "),
			strings.Join(want, "\n  "),
		)
	}
}

// captureSegmentsSurvivors returns the sorted set of constraints and
// indexes remaining on `segments`. NOT NULL constraints (contype='n',
// PG 18+) are filtered out because they're free during COPY and would
// just add noise to the allowlist.
func captureSegmentsSurvivors(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	var out []string

	rows, err := pool.Query(ctx, `
		SELECT 'constraint:' || contype::text || ':' || conname
		  FROM pg_constraint
		 WHERE conrelid = 'segments'::regclass
		   AND contype  != 'n'
	`)
	if err != nil {
		return nil, fmt.Errorf("query constraints: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, fmt.Errorf("scan constraint: %w", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iter constraints: %w", err)
	}

	idxRows, err := pool.Query(ctx, `
		SELECT 'index:' || indexname
		  FROM pg_indexes
		 WHERE tablename = 'segments'
	`)
	if err != nil {
		return nil, fmt.Errorf("query indexes: %w", err)
	}
	defer idxRows.Close()
	for idxRows.Next() {
		var s string
		if err := idxRows.Scan(&s); err != nil {
			return nil, fmt.Errorf("scan index: %w", err)
		}
		out = append(out, s)
	}
	if err := idxRows.Err(); err != nil {
		return nil, fmt.Errorf("iter indexes: %w", err)
	}

	slices.Sort(out)
	return out, nil
}

// captureSegmentsDDL returns sorted "<name>: <definition>" lines for
// every constraint and index on the `segments` table. The format is
// stable across PG versions and across the two containers, so a
// byte-level string compare is sufficient to detect any drift.
//
// We deliberately include the PK and any underlying index of an
// exclusion constraint — those are part of the schema contract too.
func captureSegmentsDDL(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	var out []string

	rows, err := pool.Query(ctx, `
		SELECT 'constraint:' || conname || ' := ' || pg_get_constraintdef(oid)
		  FROM pg_constraint
		 WHERE conrelid = 'segments'::regclass
		 ORDER BY conname
	`)
	if err != nil {
		return nil, fmt.Errorf("query constraints: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, fmt.Errorf("scan constraint: %w", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iter constraints: %w", err)
	}

	idxRows, err := pool.Query(ctx, `
		SELECT 'index:' || indexname || ' := ' || indexdef
		  FROM pg_indexes
		 WHERE tablename = 'segments'
		 ORDER BY indexname
	`)
	if err != nil {
		return nil, fmt.Errorf("query indexes: %w", err)
	}
	defer idxRows.Close()
	for idxRows.Next() {
		var s string
		if err := idxRows.Scan(&s); err != nil {
			return nil, fmt.Errorf("scan index: %w", err)
		}
		out = append(out, s)
	}
	if err := idxRows.Err(); err != nil {
		return nil, fmt.Errorf("iter indexes: %w", err)
	}

	return out, nil
}
