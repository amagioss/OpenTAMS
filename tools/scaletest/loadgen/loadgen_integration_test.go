package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/amagioss/opentams/tools/scaletest/dataset"
	"github.com/amagioss/opentams/tools/scaletest/report"
)

// cannedServer returns 2xx for every TAMS endpoint the load generator
// drives, counting requests by method so the test can confirm the mix
// actually exercised reads and writes.
func cannedServer(t *testing.T) (*httptest.Server, *map[string]*atomic.Int64) {
	t.Helper()
	counts := map[string]*atomic.Int64{
		http.MethodGet:    {},
		http.MethodPost:   {},
		http.MethodDelete: {},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c := counts[r.Method]; c != nil {
			c.Add(1)
		}
		switch {
		case r.Method == http.MethodGet:
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/storage"):
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &counts
}

func smallLoadPlan(t *testing.T) dataset.Plan {
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
		t.Fatalf("compose: %v", err)
	}
	return p
}

func TestLoadgenSteadyEndToEnd(t *testing.T) {
	srv, counts := cannedServer(t)
	plan := smallLoadPlan(t)
	gen := dataset.NewGen(plan, 42)

	m, err := newMix("steady")
	if err != nil {
		t.Fatalf("newMix: %v", err)
	}
	coll := newCollector(bucketSpecsFor(m))
	client := newClient(srv.URL, "dummy", 5*time.Second, 200)
	exec := newExecutor(client, gen, plan, m, coll, 42, 10, 5, 1000)

	cfg := engineConfig{
		warmup:      50 * time.Millisecond,
		measure:     400 * time.Millisecond,
		maxInflight: 200,
		jit:         jitter{baseline: 2000},
	}
	st := runEngine(context.Background(), cfg, exec.runOne)

	// Load telemetry populated.
	if st.attempted == 0 || st.completed == 0 {
		t.Fatalf("no load: attempted=%d completed=%d", st.attempted, st.completed)
	}
	if st.maxInflight == 0 {
		t.Error("maxInflight not tracked")
	}

	res := coll.results(st.measureDur)
	by := map[string]report.BucketResult{}
	var totalSamples, totalErrors int
	for _, r := range res {
		by[r.Name] = r
		totalSamples += r.Samples
		totalErrors += r.Errors
	}

	if totalErrors != 0 {
		t.Errorf("expected 0 errors against canned 2xx server, got %d", totalErrors)
	}
	if totalSamples == 0 {
		t.Fatal("no samples recorded")
	}
	// High-weight read buckets must have samples.
	if by[bktListByTimerange].Samples == 0 || by[bktListByFlow].Samples == 0 {
		t.Errorf("read buckets empty: tr=%d fl=%d", by[bktListByTimerange].Samples, by[bktListByFlow].Samples)
	}
	// Idempotent split: both buckets exist and each pick records to both,
	// so their sample counts match.
	first, fok := by[bktIdempotentFirst]
	dedupe, dok := by[bktIdempotentDedupe]
	if !fok || !dok {
		t.Fatalf("missing idempotent buckets: first=%v dedupe=%v", fok, dok)
	}
	if first.Samples != dedupe.Samples {
		t.Errorf("idempotent first/dedupe sample mismatch: %d vs %d", first.Samples, dedupe.Samples)
	}
	// Steady profile must not produce delete buckets.
	if _, ok := by[bktDeleteByTimerange]; ok {
		t.Error("steady run produced delete-by-timerange bucket")
	}
	// The mix actually drove both reads and writes.
	if (*counts)[http.MethodGet].Load() == 0 {
		t.Error("no GET requests issued")
	}
	if (*counts)[http.MethodPost].Load() == 0 {
		t.Error("no POST requests issued")
	}

	// Report assembles with a Load section + markdown.
	load := report.NewLoadStats(st.attempted, st.sent(), st.completed, st.shed, st.maxInflight, st.measureDur, 2000, 0)
	rep := &report.Report{Tool: "scaletest-loadgen", Load: &load, Buckets: res}
	md := rep.Markdown()
	if !strings.Contains(md, "load:") || !strings.Contains(md, "idempotent-dedupe") {
		t.Errorf("markdown missing load/idempotent rows:\n%s", md)
	}
}

func TestLoadgenDestructiveProducesDeletes(t *testing.T) {
	srv, _ := cannedServer(t)
	plan := smallLoadPlan(t)
	gen := dataset.NewGen(plan, 42)
	m, err := newMix("destructive")
	if err != nil {
		t.Fatalf("newMix: %v", err)
	}
	coll := newCollector(bucketSpecsFor(m))
	exec := newExecutor(newClient(srv.URL, "x", 5*time.Second, 200), gen, plan, m, coll, 42, 10, 5, 1000)

	st := runEngine(context.Background(), engineConfig{
		warmup: 0, measure: 400 * time.Millisecond, maxInflight: 200, jit: jitter{baseline: 2000},
	}, exec.runOne)

	by := map[string]report.BucketResult{}
	for _, r := range coll.results(st.measureDur) {
		by[r.Name] = r
	}
	if _, ok := by[bktDeleteByTimerange]; !ok {
		t.Error("destructive run missing delete-by-timerange bucket")
	}
	if by[bktDeleteByObject].Op != report.OpDelete {
		t.Errorf("delete-by-object op = %s, want delete", by[bktDeleteByObject].Op)
	}
}
