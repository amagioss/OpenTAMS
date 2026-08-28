//go:build integration

package main

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/amagioss/opentams/internal/dbtest"
	"github.com/amagioss/opentams/internal/metastore"
	"github.com/amagioss/opentams/tools/scaletest/dataset"
)

// smallBenchPlan is a tiny bimodal dataset the bench buckets can address:
// 2 deep flows × 30 segments + 8 shallow flows × 5 segments. Big enough
// to exercise every bucket's addressing math, small enough to seed via
// per-row INSERTs in well under a second.
func smallBenchPlan(t *testing.T) dataset.Plan {
	t.Helper()
	p, err := dataset.Compose(dataset.Knobs{
		TargetSegments:      100,
		ChunkDurationSec:    6,
		DeepDepth:           30,
		ShallowDepth:        5,
		DeepFlowCount:       2,
		RenditionsPerSource: 6,
	})
	if err != nil {
		t.Fatalf("compose small plan: %v", err)
	}
	return p
}

// seedSmall loads the full small dataset (sources → flows → objects →
// segments) so the read/overlap/delete buckets have existing rows to
// address. Uses the same generator the buckets replay, so flow_ids and
// object_ids line up exactly.
func seedSmall(t *testing.T, pool *pgxpool.Pool, g *dataset.Gen) {
	t.Helper()
	ctx := context.Background()

	for it := g.Sources(); it.Next(); {
		s := it.Row()
		if _, err := pool.Exec(ctx,
			`INSERT INTO sources (id, format, label, description) VALUES ($1,$2,$3,$4)`,
			s.ID, s.Format, s.Label, s.Description); err != nil {
			t.Fatalf("seed source: %v", err)
		}
	}
	for it := g.Flows(); it.Next(); {
		f := it.Row()
		if _, err := pool.Exec(ctx,
			`INSERT INTO flows (id, source_id, format, codec, container, label, segment_duration, read_only, timerange)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
			f.ID, f.SourceID, f.Format, f.Codec, f.Container, f.Label, f.SegmentDuration, f.ReadOnly, f.Timerange,
		); err != nil {
			t.Fatalf("seed flow: %v", err)
		}
	}
	for it := g.Segments(); it.Next(); {
		s := it.Row()
		if _, err := pool.Exec(ctx,
			`INSERT INTO objects (id, ref_count, reaping) VALUES ($1, 1, false) ON CONFLICT (id) DO NOTHING`,
			s.ObjectID); err != nil {
			t.Fatalf("seed object: %v", err)
		}
		if _, err := pool.Exec(ctx,
			`INSERT INTO segments (flow_id, object_id, timerange, lower_ns, upper_ns, ts_offset)
			 VALUES ($1,$2,$3,$4,$5,$6)`,
			s.FlowID, s.ObjectID, s.Timerange, s.LowerNs, s.UpperNs, s.TsOffset,
		); err != nil {
			t.Fatalf("seed segment: %v", err)
		}
	}
}

// TestBucketsAgainstSmallDataset runs every registered bucket against a
// migrated Postgres seeded with a small dataset. It is the bench's
// end-to-end correctness gate: a SQL typo, wrong column, or addressing
// bug in any bucket fails here rather than during a multi-hour 500M run.
func TestBucketsAgainstSmallDataset(t *testing.T) {
	ctx := context.Background()
	pool, cleanup, err := dbtest.Setup(ctx, dbtest.DefaultMigrationsPath())
	if err != nil {
		t.Fatalf("dbtest.Setup: %v", err)
	}
	defer cleanup()

	plan := smallBenchPlan(t)
	g := dataset.NewGen(plan, 42)
	seedSmall(t, pool, g)

	const samples, warmup = 3, 1
	env := &benchEnv{
		store:   metastore.New(pool),
		gen:     g,
		plan:    plan,
		samples: samples,
		warmup:  warmup,
	}

	for _, b := range defaultBuckets() {
		res, rerr := b.run(ctx, env)
		if rerr != nil {
			t.Errorf("bucket %q: run error: %v", b.name, rerr)
			continue
		}
		if res.Name != b.name || res.Op != b.op {
			t.Errorf("bucket %q: result name/op = %q/%q", b.name, res.Name, res.Op)
		}
		if res.Samples != samples {
			t.Errorf("bucket %q: samples = %d, want %d", b.name, res.Samples, samples)
		}
		if res.Errors != 0 {
			t.Errorf("bucket %q: errors = %d, want 0", b.name, res.Errors)
		}
	}
}

// TestDeepBucketsAgainstSmallDataset exercises the opt-in deep-flow buckets
// (page-deep-flow, cascade-delete-deep) against a seeded database:
// page-deep-flow walks a deep flow (read-only), then cascade-delete-deep
// removes the deep flows via the FK cascade. Run in that order because
// the cascade is destructive.
func TestDeepBucketsAgainstSmallDataset(t *testing.T) {
	ctx := context.Background()
	pool, cleanup, err := dbtest.Setup(ctx, dbtest.DefaultMigrationsPath())
	if err != nil {
		t.Fatalf("dbtest.Setup: %v", err)
	}
	defer cleanup()

	plan := smallBenchPlan(t)
	g := dataset.NewGen(plan, 42)
	seedSmall(t, pool, g)

	env := &benchEnv{
		store:   metastore.New(pool),
		pool:    pool,
		gen:     g,
		plan:    plan,
		samples: 10,
		warmup:  0,
	}

	deep := deepBuckets()
	var page, cascade *bucketDef
	for i := range deep {
		switch deep[i].name {
		case "page-deep-flow":
			page = &deep[i]
		case "cascade-delete-deep":
			cascade = &deep[i]
		}
	}
	if page == nil || cascade == nil {
		t.Fatal("deepBuckets missing page-deep-flow or cascade-delete-deep")
	}

	// page-deep-flow: deep flow has DeepDepth segments; with limit 100 and
	// depth 30 that's a single page, so >= 1 sample, no errors.
	pres, perr := page.run(ctx, env)
	if perr != nil {
		t.Fatalf("page-deep-flow: %v", perr)
	}
	if pres.Samples < 1 || pres.Errors != 0 {
		t.Errorf("page-deep-flow = %d samples / %d errors, want >=1 / 0", pres.Samples, pres.Errors)
	}

	// cascade-delete-deep: deletes min(samples, DeepFlowCount) deep flows.
	cres, cerr := cascade.run(ctx, env)
	if cerr != nil {
		t.Fatalf("cascade-delete-deep: %v", cerr)
	}
	if got, want := cres.Samples, int(plan.DeepFlowCount); got != want || cres.Errors != 0 {
		t.Errorf("cascade-delete-deep = %d samples / %d errors, want %d / 0", got, cres.Errors, want)
	}

	// The deep flows are gone — confirm the cascade removed their segments.
	var remaining int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM segments WHERE flow_id = $1`, g.Flow(0).ID).Scan(&remaining); err != nil {
		t.Fatalf("count segments: %v", err)
	}
	if remaining != 0 {
		t.Errorf("flow 0 still has %d segments after cascade delete", remaining)
	}
}
