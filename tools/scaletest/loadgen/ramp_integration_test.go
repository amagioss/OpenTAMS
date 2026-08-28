//go:build perf

// The ramp test asserts a load *characteristic* -- that shed rate rises
// as offered RPS climbs past capacity. On an idle machine the server
// never sheds and the assertion fails, so it is not a correctness gate:
// it belongs with the other wall-clock budgets under `perf`, which runs
// nightly. See CONTRIBUTING.md, "Tests".

package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/amagioss/opentams/tools/scaletest/dataset"
	"github.com/amagioss/opentams/tools/scaletest/report"
)

// TestRampEndToEndFindsBreakingPoint drives the real step runner (fresh
// collector per step + runEngine) against a server with a fixed per-
// request delay and a small in-flight bound, so offered load eventually
// exceeds capacity and the ramp records rising shed → a breaking point.
func TestRampEndToEndFindsBreakingPoint(t *testing.T) {
	const reqDelay = 5 * time.Millisecond
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(reqDelay)
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	plan := smallLoadPlan(t)
	gen := dataset.NewGen(plan, 42)
	m, err := newMix("steady")
	if err != nil {
		t.Fatalf("newMix: %v", err)
	}
	// Small in-flight bound → capacity ≈ maxInflight/reqDelay ≈ 4/5ms = 800/s.
	const maxInflight = 4
	client := newClient(srv.URL, "x", 5*time.Second, maxInflight)
	exec := newExecutor(client, gen, plan, m, newCollector(bucketSpecsFor(m)), 42, 10, 5, 1000)

	run := func(ctx context.Context, rps float64) (engineStats, []report.BucketResult) {
		coll := newCollector(bucketSpecsFor(m))
		exec.useCollector(coll)
		st := runEngine(ctx, engineConfig{
			warmup:      0,
			measure:     300 * time.Millisecond,
			maxInflight: maxInflight,
			jit:         jitter{baseline: rps},
		}, exec.runOne)
		return st, coll.results(st.measureDur)
	}

	res := runRamp(context.Background(), rampConfig{
		from: 400, step: 400, maxRPS: 2400, shedThreshold: 0.05,
	}, run)

	if len(res.Steps) == 0 {
		t.Fatal("ramp produced no steps")
	}
	// Shed must rise as offered load passes capacity.
	first := res.Steps[0].ShedRate
	last := res.Steps[len(res.Steps)-1].ShedRate
	if last <= first {
		t.Errorf("shed did not rise with RPS: first=%.3f last=%.3f", first, last)
	}
	// With capacity ~800/s and max 2400, a breaking point should be found.
	if res.BreakingPointRPS == 0 {
		t.Errorf("expected a breaking point below max; steps: %+v", res.Steps)
	}
}
