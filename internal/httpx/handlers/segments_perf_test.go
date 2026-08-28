//go:build perf

// Performance scenarios for the httpx/handlers segments slice. Gated
// behind the `perf` build tag — `go test -tags=perf -run Perf
// ./internal/httpx/handlers/...`. Each test function declares its own
// budget in its name and header comment (PERF-01..04).
//
// Harness wiring:
//
//	real metastore (Postgres testcontainer via internal/dbtest)
//	   ↓
//	real segment.Service
//	   ↓
//	handlers.Handler ← fakeObjectStore (URL projection only;
//	                                     bytes never move on this path
//	                                     per INV-SEG-06)
//
// The handler is invoked at its StrictServerInterface method boundary —
// the same conversion + service + JSON encode path the Gin adapter
// exercises in production. Auth + middleware (≤5ms per the NFR-PERF-01
// budget) are intentionally skipped here; the metastore + URL projection
// dominate the budget by an order of magnitude.

package handlers_test

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"

	"github.com/amagioss/opentams/gen/api"
	"github.com/amagioss/opentams/internal/dbtest"
	"github.com/amagioss/opentams/internal/httpx/handlers"
	"github.com/amagioss/opentams/internal/metastore"
	"github.com/amagioss/opentams/internal/perftest"
	"github.com/amagioss/opentams/internal/service"
	"github.com/amagioss/opentams/internal/service/segment"
)

const (
	httpPerfFlowSegments = 100_000
	httpPerfSamples      = 1000
)

var httpPerfPool *pgxpool.Pool

// TestMain only exists in `perf` builds (file is excluded otherwise),
// so default `go test ./internal/httpx/handlers/...` is unaffected.
func TestMain(m *testing.M) { os.Exit(runHTTPPerfMain(m)) }

func runHTTPPerfMain(m *testing.M) int {
	flag.Parse()
	if testing.Short() {
		return m.Run()
	}
	pool, cleanup, err := dbtest.Setup(context.Background(), dbtest.DefaultMigrationsPath())
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"httpx/handlers perf: dbtest.Setup failed: %v (perf tests will skip)\n", err)
		return m.Run()
	}
	defer cleanup()
	httpPerfPool = pool
	return m.Run()
}

func requireHTTPPerfPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if httpPerfPool == nil {
		t.Skip("perf: postgres testcontainer not available")
	}
	return httpPerfPool
}

// seedHTTPPerfFlow inserts a writable flow + source row.
func seedHTTPPerfFlow(t *testing.T) uuid.UUID {
	t.Helper()
	srcID := uuid.New()
	flID := uuid.New()
	ctx := context.Background()
	if _, err := httpPerfPool.Exec(ctx,
		`INSERT INTO sources (id, format) VALUES ($1, $2)`,
		srcID, "urn:x-nmos:format:video"); err != nil {
		t.Fatalf("seed source: %v", err)
	}
	if _, err := httpPerfPool.Exec(ctx,
		`INSERT INTO flows (id, source_id, format, read_only) VALUES ($1, $2, $3, false)`,
		flID, srcID, "urn:x-nmos:format:video"); err != nil {
		t.Fatalf("seed flow: %v", err)
	}
	t.Cleanup(func() {
		_, _ = httpPerfPool.Exec(ctx, `DELETE FROM flows WHERE id = $1`, flID)
		_, _ = httpPerfPool.Exec(ctx, `DELETE FROM sources WHERE id = $1`, srcID)
		_, _ = httpPerfPool.Exec(ctx,
			`DELETE FROM objects WHERE id LIKE 'o-httpperf-%' AND ref_count = 0`)
	})
	return flID
}

// seedHTTPPerfSegments bulk-inserts non-overlapping segments at one-second
// intervals starting `startSec`. Object rows are seeded too so the
// LEFT JOIN in ListSegments resolves Controlled correctly.
func seedHTTPPerfSegments(t *testing.T, flID uuid.UUID, startSec, n int) {
	t.Helper()
	tx, err := httpPerfPool.Begin(context.Background())
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
			oid := fmt.Sprintf("o-httpperf-%d", s)
			off := i * 5
			if i > 0 {
				sb.WriteString(",")
			}
			fmt.Fprintf(&sb, "($%d::uuid, $%d, $%d, $%d, $%d, '0:0')", off+1, off+2, off+3, off+4, off+5)
			args = append(args, flID, oid, tr, lo, hi)
		}
		if _, err := tx.Exec(context.Background(), sb.String(), args...); err != nil {
			t.Fatalf("seed segs base=%d: %v", base, err)
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
			oargs = append(oargs, fmt.Sprintf("o-httpperf-%d", s))
		}
		if _, err := tx.Exec(context.Background(), ob.String(), oargs...); err != nil {
			t.Fatalf("seed objects base=%d: %v", base, err)
		}
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatalf("commit seed tx: %v", err)
	}
}

// newPerfHandler wires a real handlers.Handler against the integration
// pool, with the local fakeObjectStore for URL projection.
func newPerfHandler(t *testing.T) *handlers.Handler {
	t.Helper()
	reg := prometheus.NewRegistry()
	m, err := service.NewAppServiceMetrics(reg)
	if err != nil {
		t.Fatalf("NewAppServiceMetrics: %v", err)
	}
	ms := metastore.New(httpPerfPool)
	svc := segment.New(segment.Deps{Meta: ms, Logger: zap.NewNop(), Metrics: m})
	objs := newFakeObjectStore()
	return handlers.New(nil, testConfig(), nil, nil, svc, nil, freshIdem(), nil, objs)
}

// =============================================================================
// SCN-HTTP-PERF-01 — GET p99 ≤ 50ms for 100-segment page
// =============================================================================
func Test_SCN_HTTP_PERF_01_GetP99(t *testing.T) {
	perftest.SkipIfShort(t)
	requireHTTPPerfPool(t)
	flID := seedHTTPPerfFlow(t)
	seedHTTPPerfSegments(t, flID, 0, httpPerfFlowSegments)
	h := newPerfHandler(t)
	limit := 100

	samples := make([]time.Duration, httpPerfSamples)
	for i := 0; i < httpPerfSamples; i++ {
		t0 := time.Now()
		_, err := h.GetFlowSegments(context.Background(), api.GetFlowSegmentsRequestObject{
			FlowId: flID.String(),
			Params: api.GetFlowSegmentsParams{Limit: &limit},
		})
		samples[i] = time.Since(t0)
		if err != nil {
			t.Fatalf("GetFlowSegments[%d]: %v", i, err)
		}
	}
	got := perftest.P99(samples)
	budget := perftest.EnvOverrideMS("OPENTAMS_PERF_HTTP_GET_P99_MS", 50)
	if got > budget {
		t.Errorf("p99 = %v, want ≤ %v", got, budget)
	}
}

// =============================================================================
// SCN-HTTP-PERF-02 — POST single p99 ≤ 200ms
// =============================================================================
func Test_SCN_HTTP_PERF_02_PostSingleP99(t *testing.T) {
	perftest.SkipIfShort(t)
	requireHTTPPerfPool(t)
	flID := seedHTTPPerfFlow(t)
	seedHTTPPerfSegments(t, flID, 0, httpPerfFlowSegments)
	h := newPerfHandler(t)

	samples := make([]time.Duration, httpPerfSamples)
	for i := 0; i < httpPerfSamples; i++ {
		s := httpPerfFlowSegments + i
		body := postBodySingle(t,
			fmt.Sprintf("o-httppf02-%d", i),
			fmt.Sprintf("[%d:0_%d:0)", s, s+1))
		t0 := time.Now()
		_, err := h.PostFlowSegments(context.Background(), api.PostFlowSegmentsRequestObject{
			FlowId: flID.String(),
			Params: api.PostFlowSegmentsParams{XIdempotencyKey: fmt.Sprintf("k-pf02-%d", i)},
			Body:   body,
		})
		samples[i] = time.Since(t0)
		if err != nil {
			t.Fatalf("PostFlowSegments[%d]: %v", i, err)
		}
	}
	got := perftest.P99(samples)
	budget := perftest.EnvOverrideMS("OPENTAMS_PERF_HTTP_POST_SINGLE_P99_MS", 200)
	if got > budget {
		t.Errorf("p99 = %v, want ≤ %v", got, budget)
	}
}

// =============================================================================
// SCN-HTTP-PERF-03 — POST bulk throughput ≥ 120 segs/sec
// =============================================================================
func Test_SCN_HTTP_PERF_03_BulkThroughput(t *testing.T) {
	perftest.SkipIfShort(t)
	requireHTTPPerfPool(t)
	flID := seedHTTPPerfFlow(t)
	h := newPerfHandler(t)

	const batches = 50
	const batchSize = 100
	total := batches * batchSize

	t0 := time.Now()
	for b := 0; b < batches; b++ {
		segs := make([]postSeg, batchSize)
		for i := 0; i < batchSize; i++ {
			s := b*batchSize + i
			segs[i] = postSeg{
				ObjectID:  fmt.Sprintf("o-httppf03-%d-%d", b, i),
				Timerange: fmt.Sprintf("[%d:0_%d:0)", s, s+1),
			}
		}
		body := postBodyArray(t, segs)
		_, err := h.PostFlowSegments(context.Background(), api.PostFlowSegmentsRequestObject{
			FlowId: flID.String(),
			Params: api.PostFlowSegmentsParams{XIdempotencyKey: fmt.Sprintf("k-pf03-%d", b)},
			Body:   body,
		})
		if err != nil {
			t.Fatalf("batch %d: %v", b, err)
		}
	}
	elapsed := time.Since(t0)
	rate := float64(total) / elapsed.Seconds()
	want := perftest.EnvOverrideQPS("OPENTAMS_PERF_HTTP_BULK_QPS", 120)
	if rate < want {
		t.Errorf("rate = %.1f segs/sec, want ≥ %.1f (elapsed=%v)", rate, want, elapsed)
	}
}

// =============================================================================
// SCN-HTTP-PERF-04 — DELETE 10k p99 ≤ 500ms
// =============================================================================
func Test_SCN_HTTP_PERF_04_Delete10kP99(t *testing.T) {
	perftest.SkipIfShort(t)
	requireHTTPPerfPool(t)
	flID := seedHTTPPerfFlow(t)
	seedHTTPPerfSegments(t, flID, 0, 10_000)
	h := newPerfHandler(t)

	tr := "[0:0_10000:0)"
	t0 := time.Now()
	_, err := h.DeleteFlowSegments(context.Background(), api.DeleteFlowSegmentsRequestObject{
		FlowId: flID.String(),
		Params: api.DeleteFlowSegmentsParams{Timerange: &tr},
	})
	elapsed := time.Since(t0)
	if err != nil {
		t.Fatalf("DeleteFlowSegments: %v", err)
	}
	budget := perftest.EnvOverrideMS("OPENTAMS_PERF_HTTP_DELETE_MS", 500)
	if elapsed > budget {
		t.Errorf("elapsed = %v, want ≤ %v", elapsed, budget)
	}
}
