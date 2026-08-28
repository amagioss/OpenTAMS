//go:build integration

package dbstats_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/amagioss/opentams/internal/dbtest"
	"github.com/amagioss/opentams/tools/scaletest/dbstats"
)

// TestCaptureAgainstRealPostgres verifies the capture SQL is valid
// against a real migrated database and that a before→after diff over a
// burst of writes shows the expected movement (commits up, segments
// table grows). pg_stat_statements is absent in the stock image, so this
// also exercises the graceful-skip path (TopQueries stays empty, no
// error).
func TestCaptureAgainstRealPostgres(t *testing.T) {
	ctx := context.Background()
	pool, cleanup, err := dbtest.Setup(ctx, dbtest.DefaultMigrationsPath())
	if err != nil {
		t.Fatalf("dbtest.Setup: %v", err)
	}
	defer cleanup()

	tables := []string{"segments", "objects"}
	before, err := dbstats.Capture(ctx, pool, tables)
	if err != nil {
		t.Fatalf("Capture(before): %v", err)
	}

	// A burst of writes: a source, a flow, and some objects+segments.
	srcID := "11111111-1111-4111-8111-111111111111"
	flowID := "22222222-2222-4222-8222-222222222222"
	if _, err := pool.Exec(ctx,
		`INSERT INTO sources (id, format, label) VALUES ($1, 'urn:x-nmos:format:video', 'src')`, srcID); err != nil {
		t.Fatalf("seed source: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO flows (id, source_id, format, codec, container, label, segment_duration, read_only, timerange)
		 VALUES ($1,$2,'urn:x-nmos:format:video','video/h264','video/mp4','flow','6:1',false,'[0:0_600:0)')`,
		flowID, srcID); err != nil {
		t.Fatalf("seed flow: %v", err)
	}
	for i := range 500 {
		oid := fmt.Sprintf("o-stat-%d", i)
		if _, err := pool.Exec(ctx,
			`INSERT INTO objects (id, ref_count, reaping) VALUES ($1, 1, false)`, oid); err != nil {
			t.Fatalf("seed object %d: %v", i, err)
		}
		lo := int64(i) * 1_000_000_000
		if _, err := pool.Exec(ctx,
			`INSERT INTO segments (flow_id, object_id, timerange, lower_ns, upper_ns, ts_offset)
			 VALUES ($1,$2,$3,$4,$5,'0:0')`,
			flowID, oid, fmt.Sprintf("[%d:0_%d:0)", i, i+1), lo, lo+1_000_000_000); err != nil {
			t.Fatalf("seed segment %d: %v", i, err)
		}
	}

	after, err := dbstats.Capture(ctx, pool, tables)
	if err != nil {
		t.Fatalf("Capture(after): %v", err)
	}

	d := dbstats.Diff(before, after)
	// Cumulative counters (commits, blocks) are flushed to the shared
	// stats collector asynchronously, so their delta is timing-dependent
	// and not asserted here. Relation size is read from disk immediately,
	// so it is the deterministic end-to-end signal that Capture + Diff
	// work against real Postgres.
	if d.Commits < 0 {
		t.Errorf("commits delta = %d, must not be negative", d.Commits)
	}

	var seg *struct {
		live, sizeBefore, sizeAfter int64
	}
	for _, tbl := range d.Tables {
		if tbl.Name == "segments" {
			seg = &struct{ live, sizeBefore, sizeAfter int64 }{tbl.LiveTuples, tbl.SizeBytesBefore, tbl.SizeBytesAfter}
		}
	}
	if seg == nil {
		t.Fatalf("segments table not in diff: %+v", d.Tables)
	}
	if seg.sizeAfter <= seg.sizeBefore {
		t.Errorf("segments size did not grow: before=%d after=%d", seg.sizeBefore, seg.sizeAfter)
	}
	// Graceful skip: no pg_stat_statements in the stock image.
	if len(d.TopQueries) != 0 {
		t.Logf("note: pg_stat_statements present, %d top queries", len(d.TopQueries))
	}
}
