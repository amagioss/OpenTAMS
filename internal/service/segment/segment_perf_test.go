//go:build perf

// Performance scenarios for the service-segment slice. Gated by `perf`
// build tag — `go test -tags=perf -run Perf ./internal/service/segment/...`.
// Each test function declares its own budget in its name and header
// comment (PERF-01..04). Reuses integration TestMain (segment_integration_test.go)
// for the Postgres testcontainer + integrationPool. Env overrides per
// internal/perftest.

package segment_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/amagioss/opentams/internal/domain"
	"github.com/amagioss/opentams/internal/perftest"
)

const (
	segPerfFlowSegments = 100_000
	segPerfSamples      = 1000
)

// seedSegPerfRows bulk-inserts `n` non-overlapping segments at one-second
// intervals starting `startSec`. Same approach as the metastore perf
// helper — raw COPY-style chunked INSERT to avoid the overlap-check cost
// of the service path during fixture setup.
func seedSegPerfRows(t *testing.T, flID uuid.UUID, startSec, n int) {
	t.Helper()
	tx, err := integrationPool.Begin(context.Background())
	if err != nil {
		t.Fatalf("begin seed tx: %v", err)
	}
	defer tx.Rollback(context.Background()) //nolint:errcheck // commit path returns; rollback on the post-commit defer is a no-op whose error is uninteresting.

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
			oid := fmt.Sprintf("o-segperf-%d", s)
			off := i * 5
			if i > 0 {
				sb.WriteString(",")
			}
			fmt.Fprintf(&sb, "($%d::uuid, $%d, $%d, $%d, $%d, '0:0')", off+1, off+2, off+3, off+4, off+5)
			args = append(args, flID, oid, tr, lo, hi)
		}
		if _, err := tx.Exec(context.Background(), sb.String(), args...); err != nil {
			t.Fatalf("seed segs chunk base=%d: %v", base, err)
		}
		var ob strings.Builder
		ob.WriteString(`INSERT INTO objects (id, ref_count, reaping) VALUES `)
		oargs := make([]any, 0, size)
		for i := 0; i < size; i++ {
			s := startSec + base + i
			if i > 0 {
				ob.WriteString(",")
			}
			fmt.Fprintf(&ob, "($%d, 1, false)", i+1)
			oargs = append(oargs, fmt.Sprintf("o-segperf-%d", s))
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
// SCN-SEG-PERF-01 — RegisterBatch single-segment p99 ≤ 200ms
// =============================================================================
func Test_SCN_SEG_PERF_01_RegisterSinglePerf(t *testing.T) {
	perftest.SkipIfShort(t)
	requirePool(t)
	flID := seedRealFlow(t, false)
	seedSegPerfRows(t, flID, 0, segPerfFlowSegments)
	svc := newRealService(t)

	samples := make([]time.Duration, segPerfSamples)
	for i := 0; i < segPerfSamples; i++ {
		s := segPerfFlowSegments + i
		seg := domain.Segment{
			ObjectID:  fmt.Sprintf("o-segpf01-%d", i),
			Timerange: mustTR(t, fmt.Sprintf("[%d:0_%d:0)", s, s+1)),
		}
		t0 := time.Now()
		_, err := svc.RegisterBatch(context.Background(), domain.RegisterParams{
			FlowID: flID, Segments: []domain.Segment{seg},
		})
		samples[i] = time.Since(t0)
		if err != nil {
			t.Fatalf("RegisterBatch[%d]: %v", i, err)
		}
	}
	got := perftest.P99(samples)
	budget := perftest.EnvOverrideMS("OPENTAMS_PERF_SEG_REGISTER_P99_MS", 200)
	if got > budget {
		t.Errorf("p99 = %v, want ≤ %v", got, budget)
	}
}

// =============================================================================
// SCN-SEG-PERF-02 — Bulk RegisterBatch ≥ 120 segs/sec
// =============================================================================
func Test_SCN_SEG_PERF_02_BulkThroughputPerf(t *testing.T) {
	perftest.SkipIfShort(t)
	requirePool(t)
	flID := seedRealFlow(t, false)
	svc := newRealService(t)

	const batches = 50
	const batchSize = 100
	total := batches * batchSize

	t0 := time.Now()
	for b := 0; b < batches; b++ {
		segs := make([]domain.Segment, batchSize)
		for i := 0; i < batchSize; i++ {
			s := b*batchSize + i
			segs[i] = domain.Segment{
				ObjectID:  fmt.Sprintf("o-segpf02-%d-%d", b, i),
				Timerange: mustTR(t, fmt.Sprintf("[%d:0_%d:0)", s, s+1)),
			}
		}
		if _, err := svc.RegisterBatch(context.Background(), domain.RegisterParams{
			FlowID: flID, Segments: segs,
		}); err != nil {
			t.Fatalf("batch %d: %v", b, err)
		}
	}
	elapsed := time.Since(t0)
	rate := float64(total) / elapsed.Seconds()
	want := perftest.EnvOverrideQPS("OPENTAMS_PERF_SEG_BULK_QPS", 120)
	if rate < want {
		t.Errorf("rate = %.1f segs/sec, want ≥ %.1f (elapsed=%v)", rate, want, elapsed)
	}
}

// =============================================================================
// SCN-SEG-PERF-03 — List 100-segment page p99 ≤ 50ms
// =============================================================================
func Test_SCN_SEG_PERF_03_ListPagePerf(t *testing.T) {
	perftest.SkipIfShort(t)
	requirePool(t)
	flID := seedRealFlow(t, false)
	seedSegPerfRows(t, flID, 0, segPerfFlowSegments)
	svc := newRealService(t)

	samples := make([]time.Duration, segPerfSamples)
	for i := 0; i < segPerfSamples; i++ {
		t0 := time.Now()
		_, err := svc.List(context.Background(), domain.ListParams{
			FlowID: flID, Limit: 100,
		})
		samples[i] = time.Since(t0)
		if err != nil {
			t.Fatalf("List[%d]: %v", i, err)
		}
	}
	got := perftest.P99(samples)
	budget := perftest.EnvOverrideMS("OPENTAMS_PERF_SEG_LIST_P99_MS", 50)
	if got > budget {
		t.Errorf("p99 = %v, want ≤ %v", got, budget)
	}
}

// =============================================================================
// SCN-SEG-PERF-04 — Delete 10k segments ≤ 500ms
// =============================================================================
func Test_SCN_SEG_PERF_04_Delete10kPerf(t *testing.T) {
	perftest.SkipIfShort(t)
	requirePool(t)
	flID := seedRealFlow(t, false)
	seedSegPerfRows(t, flID, 0, 10_000)
	svc := newRealService(t)

	t0 := time.Now()
	res, err := svc.Delete(context.Background(), domain.DeleteParams{
		FlowID: flID, Timerange: mustTR(t, "[0:0_10000:0)"),
	})
	elapsed := time.Since(t0)
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if res.DeletedCount != 10_000 {
		t.Errorf("DeletedCount = %d, want 10000", res.DeletedCount)
	}
	budget := perftest.EnvOverrideMS("OPENTAMS_PERF_SEG_DELETE_MS", 500)
	if elapsed > budget {
		t.Errorf("elapsed = %v, want ≤ %v", elapsed, budget)
	}
}
