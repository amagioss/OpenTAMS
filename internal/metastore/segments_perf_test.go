//go:build perf

// Performance scenarios for the metastore-segments slice. Gated behind
// the `perf` build tag so they run only on representative hardware via
// `go test -tags=perf -run Perf ./internal/metastore/...`. Each test
// function declares its own budget in its name and header comment
// (PERF-01..04); budgets may be overridden by `OPENTAMS_PERF_*` env vars per
// internal/perftest. Fixtures use the shared testcontainer pool from
// store_test.go (TestMain).

package metastore_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/amagioss/opentams/internal/domain"
	"github.com/amagioss/opentams/internal/metastore"
	"github.com/amagioss/opentams/internal/perftest"
)

const (
	perfFlowSegments = 100_000 // 100k existing segments in the seeded flow
	perfSamples      = 1000    // 1000 sequential calls per latency measurement
)

// seedNSegments inserts `n` non-overlapping segments at
// [start_s+i:0_start_s+i+1:0) for i ∈ [0..n). Uses raw COPY for speed
// — InsertSegments would multiply seed time by ~3x via overlap checks.
func seedNSegments(t *testing.T, flID string, startSec, n int) {
	t.Helper()
	pool := metastore.SharedTestPool()
	tx, err := pool.Begin(context.Background())
	if err != nil {
		t.Fatalf("begin seed tx: %v", err)
	}
	defer tx.Rollback(context.Background()) //nolint:errcheck // commit path returns; rollback on the post-commit defer is a no-op whose error is uninteresting.

	// Per-row INSERTs in chunks; pgx COPY would be faster but the helper
	// is one-shot per perf test (~100k rows in ~30s).
	const chunk = 1000
	for base := 0; base < n; base += chunk {
		size := chunk
		if base+size > n {
			size = n - base
		}
		var sb strings.Builder
		sb.WriteString(`INSERT INTO segments (flow_id, object_id, timerange, lower_ns, upper_ns, ts_offset) VALUES `)
		args := make([]any, 0, size*5)
		for i := 0; i < size; i++ {
			s := startSec + base + i
			lo := int64(s) * 1_000_000_000
			hi := lo + 1_000_000_000
			tr := fmt.Sprintf("[%d:0_%d:0)", s, s+1)
			oid := fmt.Sprintf("o-perf-%d", s)
			off := i * 5
			if i > 0 {
				sb.WriteString(",")
			}
			fmt.Fprintf(&sb, "($%d::uuid, $%d, $%d, $%d, $%d, '0:0')", off+1, off+2, off+3, off+4, off+5)
			args = append(args, flID, oid, tr, lo, hi)
		}
		if _, err := tx.Exec(context.Background(), sb.String(), args...); err != nil {
			t.Fatalf("seed chunk base=%d: %v", base, err)
		}
		// Mirror the FK objects rows for ref-count integrity.
		// ON CONFLICT increments not needed — each seg uses a distinct
		// object_id.
		var ob strings.Builder
		ob.WriteString(`INSERT INTO objects (id, ref_count, reaping) VALUES `)
		oargs := make([]any, 0, size)
		for i := 0; i < size; i++ {
			s := startSec + base + i
			if i > 0 {
				ob.WriteString(",")
			}
			fmt.Fprintf(&ob, "($%d, 1, false)", i+1)
			oargs = append(oargs, fmt.Sprintf("o-perf-%d", s))
		}
		if _, err := tx.Exec(context.Background(), ob.String(), oargs...); err != nil {
			t.Fatalf("seed objects chunk base=%d: %v", base, err)
		}
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatalf("commit seed tx: %v", err)
	}
}

// =============================================================================
// SCN-META-PERF-01 — Insert single-segment p99 ≤ 80ms
// =============================================================================
func Test_SCN_META_PERF_01_InsertSinglePerf(t *testing.T) {
	perftest.SkipIfShort(t)
	flUUID := seedFlow(t, false)
	flID := flUUID.String()
	seedNSegments(t, flID, 0, perfFlowSegments)
	store := newStore(t)

	samples := make([]time.Duration, perfSamples)
	for i := 0; i < perfSamples; i++ {
		s := perfFlowSegments + i
		seg := makeSegment(t, fmt.Sprintf("o-pf01-%d", i),
			fmt.Sprintf("[%d:0_%d:0)", s, s+1))
		t0 := time.Now()
		_, err := store.InsertSegments(context.Background(), metastore.InsertBatch{
			FlowID: flUUID, Segments: []domain.Segment{seg}, ControlledStorageID: "default",
		})
		samples[i] = time.Since(t0)
		if err != nil {
			t.Fatalf("InsertSegments[%d]: %v", i, err)
		}
	}
	got := perftest.P99(samples)
	budget := perftest.EnvOverrideMS("OPENTAMS_PERF_META_INSERT_P99_MS", 80)
	if got > budget {
		t.Errorf("p99 = %v, want ≤ %v", got, budget)
	}
}

// =============================================================================
// SCN-META-PERF-02 — Bulk 100-segment throughput ≥ 200 segs/sec
// =============================================================================
func Test_SCN_META_PERF_02_BulkThroughputPerf(t *testing.T) {
	perftest.SkipIfShort(t)
	flUUID := seedFlow(t, false)
	store := newStore(t)

	const batches = 50
	const batchSize = 100
	total := batches * batchSize

	t0 := time.Now()
	for b := 0; b < batches; b++ {
		segs := make([]domain.Segment, batchSize)
		for i := 0; i < batchSize; i++ {
			s := b*batchSize + i
			segs[i] = makeSegment(t, fmt.Sprintf("o-pf02-%d-%d", b, i),
				fmt.Sprintf("[%d:0_%d:0)", s, s+1))
		}
		if _, err := store.InsertSegments(context.Background(), metastore.InsertBatch{
			FlowID: flUUID, Segments: segs, ControlledStorageID: "default",
		}); err != nil {
			t.Fatalf("batch %d: %v", b, err)
		}
	}
	elapsed := time.Since(t0)
	rate := float64(total) / elapsed.Seconds()
	want := perftest.EnvOverrideQPS("OPENTAMS_PERF_META_BULK_QPS", 200)
	if rate < want {
		t.Errorf("rate = %.1f segs/sec, want ≥ %.1f (elapsed=%v)", rate, want, elapsed)
	}
}

// =============================================================================
// SCN-META-PERF-03 — List 100-segment page p99 ≤ 30ms
// =============================================================================
func Test_SCN_META_PERF_03_ListPagePerf(t *testing.T) {
	perftest.SkipIfShort(t)
	flUUID := seedFlow(t, false)
	flID := flUUID.String()
	seedNSegments(t, flID, 0, perfFlowSegments)
	store := newStore(t)

	samples := make([]time.Duration, perfSamples)
	for i := 0; i < perfSamples; i++ {
		t0 := time.Now()
		_, err := store.ListSegments(context.Background(), metastore.ListQuery{
			FlowID: flUUID, Limit: 100,
		})
		samples[i] = time.Since(t0)
		if err != nil {
			t.Fatalf("ListSegments[%d]: %v", i, err)
		}
	}
	got := perftest.P99(samples)
	budget := perftest.EnvOverrideMS("OPENTAMS_PERF_META_LIST_P99_MS", 30)
	if got > budget {
		t.Errorf("p99 = %v, want ≤ %v", got, budget)
	}
}

// =============================================================================
// SCN-META-PERF-04 — Delete 10k rows p99 ≤ 400ms
// =============================================================================
func Test_SCN_META_PERF_04_Delete10kPerf(t *testing.T) {
	perftest.SkipIfShort(t)
	flUUID := seedFlow(t, false)
	flID := flUUID.String()
	// 100k segments — first 10k seconds (3600 entries, but we want 10k
	// inside the delete range): seed 10k inside [0:0_10000:0) and
	// 90k outside.
	seedNSegments(t, flID, 0, 10_000)
	seedNSegments(t, flID, 100_000, 90_000)
	store := newStore(t)

	t0 := time.Now()
	res, err := store.DeleteSegmentsByTimerange(context.Background(), metastore.DeleteQuery{
		FlowID: flUUID, Timerange: mustTR(t, "[0:0_10000:0)"),
	})
	elapsed := time.Since(t0)
	if err != nil {
		t.Fatalf("DeleteSegmentsByTimerange: %v", err)
	}
	if res.DeletedCount != 10_000 {
		t.Errorf("DeletedCount = %d, want 10000", res.DeletedCount)
	}
	budget := perftest.EnvOverrideMS("OPENTAMS_PERF_META_DELETE_P99_MS", 400)
	if elapsed > budget {
		t.Errorf("elapsed = %v, want ≤ %v", elapsed, budget)
	}
}
