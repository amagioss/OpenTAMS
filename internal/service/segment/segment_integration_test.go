//go:build integration || perf

// Integration concurrency tests for the segments service slice. These
// run against a real Postgres testcontainer (the in-memory fakeMetaStore
// cannot exercise the read_only flip / serialisation paths). Gated by
// testing.Short() so the default `go test ./...` skips on developer
// laptops without Docker — full release CI runs them.

package segment_test

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"

	"github.com/amagioss/opentams/internal/dbtest"
	"github.com/amagioss/opentams/internal/domain"
	"github.com/amagioss/opentams/internal/metastore"
	"github.com/amagioss/opentams/internal/perftest"
	"github.com/amagioss/opentams/internal/service"
	"github.com/amagioss/opentams/internal/service/segment"
)

// integrationPool is the shared pgxpool boot in TestMain. nil when
// testing.Short() — concurrency tests skip in that case.
var integrationPool *pgxpool.Pool

func TestMain(m *testing.M) { os.Exit(runIntegrationMain(m)) }

func runIntegrationMain(m *testing.M) int {
	// flag.Parse is required before testing.Short — TestMain runs
	// before m.Run() does it for us.
	flag.Parse()
	// `-short` runs ONLY the in-memory unit tests; skip container boot.
	if testing.Short() {
		return m.Run()
	}
	pool, cleanup, err := dbtest.Setup(context.Background(), dbtest.DefaultMigrationsPath())
	if err != nil {
		// Don't fail the whole binary — developers without Docker would
		// otherwise see every unit test fail. Tests that need the pool
		// guard on integrationPool == nil and t.Skip themselves.
		fmt.Fprintf(os.Stderr, "service/segment integration: dbtest.Setup failed: %v (skipping integration suite)\n", err)
		return m.Run()
	}
	defer cleanup()
	integrationPool = pool
	return m.Run()
}

// requirePool returns the shared pool or skips the test when boot
// failed / -short was used.
func requirePool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if integrationPool == nil {
		t.Skip("integration: postgres testcontainer not available")
	}
	return integrationPool
}

// seedRealFlow inserts a sources/flows pair and returns the flow ID.
// Cleanup deletes the flow (cascading to segments) and any orphaned
// objects rows. Mirrors the metastore_test seedFlow helper.
func seedRealFlow(t *testing.T, readOnly bool) uuid.UUID {
	t.Helper()
	pool := integrationPool
	srcID := uuid.New()
	flID := uuid.New()
	ctx := context.Background()
	if _, err := pool.Exec(ctx,
		`INSERT INTO sources (id, format) VALUES ($1, $2)`,
		srcID, "urn:x-nmos:format:video"); err != nil {
		t.Fatalf("seed source: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO flows (id, source_id, format, read_only) VALUES ($1, $2, $3, $4)`,
		flID, srcID, "urn:x-nmos:format:video", readOnly); err != nil {
		t.Fatalf("seed flow: %v", err)
	}
	t.Cleanup(func() {
		var oids []string
		rows, err := pool.Query(ctx, `SELECT object_id FROM segments WHERE flow_id = $1`, flID)
		if err == nil {
			for rows.Next() {
				var oid string
				_ = rows.Scan(&oid)
				oids = append(oids, oid)
			}
			rows.Close()
		}
		_, _ = pool.Exec(ctx, `DELETE FROM flows WHERE id = $1`, flID)
		_, _ = pool.Exec(ctx, `DELETE FROM sources WHERE id = $1`, srcID)
		for _, oid := range oids {
			var n int64
			if err := pool.QueryRow(ctx,
				`SELECT COUNT(*) FROM segments WHERE object_id = $1`, oid).Scan(&n); err == nil && n == 0 {
				_, _ = pool.Exec(ctx, `DELETE FROM objects WHERE id = $1`, oid)
			}
		}
	})
	return flID
}

// newRealService wires the real metastore against the integration pool.
// Each call gets a fresh prometheus registry so metric collisions don't
// occur across tests.
func newRealService(t *testing.T) segment.Service {
	t.Helper()
	reg := prometheus.NewRegistry()
	m, err := service.NewAppServiceMetrics(reg)
	if err != nil {
		t.Fatalf("NewAppServiceMetrics: %v", err)
	}
	// ControlledStorageID must be set: the metastore refuses to persist a
	// controlled segment when InsertBatch.ControlledStorageID is empty, so
	// omitting it here fails every insert rather than exercising the
	// concurrency invariant under test. Production wires this from config
	// at cmd/opentams/serve.go:176.
	return segment.New(segment.Deps{
		Meta:                metastore.New(integrationPool),
		Logger:              zap.NewNop(),
		Metrics:             m,
		ControlledStorageID: "default",
	})
}

// =============================================================================
// SCN-SEG-CONC-01 — read_only flip during RegisterBatch
// =============================================================================
//
// Trace: NFR-SEG-REL-03. 25 RegisterBatch goroutines + 25 read_only
// togglers race on the same flow. Invariant: each call returns either
// AllAccepted (with its segments persisted) or ErrFlowReadOnly (with
// nothing persisted) — no half-commits.
func Test_SCN_SEG_CONC_01_ReadOnlyFlipDuringRegister(t *testing.T) {
	requirePool(t)
	flID := seedRealFlow(t, false)
	svc := newRealService(t)

	const inserters = 25
	const togglers = 25
	const togglesPerWorker = 8

	type outcome struct {
		idx int
		res domain.RegisterResult
		err error
	}
	out := make(chan outcome, inserters)

	var wg sync.WaitGroup
	for i := 0; i < inserters; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			lo := idx * 1000
			res, err := svc.RegisterBatch(context.Background(), domain.RegisterParams{
				FlowID: flID,
				Segments: []domain.Segment{
					{
						ObjectID:  fmt.Sprintf("o-segc1-%d", idx),
						Timerange: mustTR(t, fmt.Sprintf("[%d:0_%d:0)", lo, lo+10)),
					},
				},
			})
			out <- outcome{idx: idx, res: res, err: err}
		}(i)
	}
	for i := 0; i < togglers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for k := 0; k < togglesPerWorker; k++ {
				v := k%2 == 0
				if _, err := integrationPool.Exec(context.Background(),
					`UPDATE flows SET read_only = $1 WHERE id = $2`, v, flID); err != nil {
					t.Errorf("toggle: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(out)

	// Restore writeable for cleanup.
	if _, err := integrationPool.Exec(context.Background(),
		`UPDATE flows SET read_only = false WHERE id = $1`, flID); err != nil {
		t.Fatalf("restore writeable: %v", err)
	}

	successIDs := map[string]struct{}{}
	for o := range out {
		switch {
		case o.err == nil:
			if o.res.Outcome != domain.OutcomeAllAccepted {
				t.Errorf("inserter %d: nil err but Outcome=%v", o.idx, o.res.Outcome)
			}
			successIDs[fmt.Sprintf("o-segc1-%d", o.idx)] = struct{}{}
		case errors.Is(o.err, segment.ErrFlowReadOnly):
			// Expected when read_only=true was observed.
		default:
			t.Errorf("inserter %d: unexpected err = %v", o.idx, o.err)
		}
	}

	rows, err := integrationPool.Query(context.Background(),
		`SELECT object_id FROM segments WHERE flow_id = $1`, flID)
	if err != nil {
		t.Fatalf("post-state query: %v", err)
	}
	defer rows.Close()
	persisted := map[string]struct{}{}
	for rows.Next() {
		var oid string
		if err := rows.Scan(&oid); err != nil {
			t.Fatalf("scan: %v", err)
		}
		persisted[oid] = struct{}{}
	}
	if len(persisted) != len(successIDs) {
		t.Errorf("persisted=%d successes=%d (mismatch implies half-commit)",
			len(persisted), len(successIDs))
	}
	for id := range successIDs {
		if _, ok := persisted[id]; !ok {
			t.Errorf("nil-error register %q has no row (half-commit)", id)
		}
	}
}

// =============================================================================
// SCN-SEG-CONC-02 — 100 goroutines × 10 disjoint segments
// =============================================================================
//
// Trace: NFR-SEG-PERF-02, BR-SEG-03. All return AllAccepted, total
// segment count = 1000, wall-clock ≤ 5s.
func Test_SCN_SEG_CONC_02_DisjointConcurrentRegister(t *testing.T) {
	requirePool(t)
	flID := seedRealFlow(t, false)
	svc := newRealService(t)

	const G = 100
	const PerG = 10
	var wg sync.WaitGroup
	errs := make(chan error, G)

	t0 := time.Now()
	for i := 0; i < G; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			segs := make([]domain.Segment, PerG)
			base := idx * 1000
			for j := 0; j < PerG; j++ {
				lo := base + j*10
				segs[j] = domain.Segment{
					ObjectID:  fmt.Sprintf("o-segc2-%d-%d", idx, j),
					Timerange: mustTR(t, fmt.Sprintf("[%d:0_%d:0)", lo, lo+10)),
				}
			}
			res, err := svc.RegisterBatch(context.Background(), domain.RegisterParams{
				FlowID: flID, Segments: segs,
			})
			if err != nil {
				errs <- fmt.Errorf("g=%d: %w", idx, err)
				return
			}
			if res.Outcome != domain.OutcomeAllAccepted {
				errs <- fmt.Errorf("g=%d: Outcome=%v want AllAccepted", idx, res.Outcome)
				return
			}
			errs <- nil
		}(i)
	}
	wg.Wait()
	elapsed := time.Since(t0)
	close(errs)

	for err := range errs {
		if err != nil {
			t.Error(err)
		}
	}

	var count int64
	if err := integrationPool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM segments WHERE flow_id = $1`, flID).Scan(&count); err != nil {
		t.Fatalf("count query: %v", err)
	}
	if count != G*PerG {
		t.Errorf("segments count = %d, want %d", count, G*PerG)
	}

	budget := perftest.EnvOverrideMS("OPENTAMS_PERF_SEG_CONC_DISJOINT_MS", 5000)
	if elapsed > budget {
		t.Errorf("elapsed = %v, want ≤ %v", elapsed, budget)
	}
}

// =============================================================================
// SCN-SEG-CONC-03 — 50 goroutines colliding on the same range
// =============================================================================
//
// Trace: REQ-SEG-03, BR-SEG-03. Exactly one wins (AllAccepted); the
// other 49 return AllRejected with segment-overlap reasons.
func Test_SCN_SEG_CONC_03_CollidingConcurrentRegister(t *testing.T) {
	requirePool(t)
	flID := seedRealFlow(t, false)
	svc := newRealService(t)

	const G = 50
	type outcome struct {
		res domain.RegisterResult
		err error
	}
	out := make(chan outcome, G)

	var wg sync.WaitGroup
	for i := 0; i < G; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			res, err := svc.RegisterBatch(context.Background(), domain.RegisterParams{
				FlowID: flID,
				Segments: []domain.Segment{
					{
						ObjectID:  fmt.Sprintf("o-segc3-%d", idx),
						Timerange: mustTR(t, "[0:0_10:0)"),
					},
				},
			})
			out <- outcome{res: res, err: err}
		}(i)
	}
	wg.Wait()
	close(out)

	var accepted, overlap atomic.Int32
	for o := range out {
		switch {
		case o.err == nil && o.res.Outcome == domain.OutcomeAllAccepted:
			accepted.Add(1)
		case errors.Is(o.err, metastore.ErrSegmentOverlap):
			overlap.Add(1)
		default:
			t.Errorf("unexpected outcome=%v err=%v", o.res.Outcome, o.err)
		}
	}
	if accepted.Load() != 1 {
		t.Errorf("accepted = %d, want exactly 1", accepted.Load())
	}
	if accepted.Load()+overlap.Load() != G {
		t.Errorf("accepted+overlap = %d, want %d", accepted.Load()+overlap.Load(), G)
	}
	var count int64
	if err := integrationPool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM segments WHERE flow_id = $1`, flID).Scan(&count); err != nil {
		t.Fatalf("count query: %v", err)
	}
	if count != 1 {
		t.Errorf("segments count = %d, want 1", count)
	}
}
