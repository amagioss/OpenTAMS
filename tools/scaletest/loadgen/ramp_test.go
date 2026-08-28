package main

import (
	"context"
	"testing"
	"time"

	"github.com/amagioss/opentams/tools/scaletest/report"
)

// fakeStep returns rising shed once rps crosses `breakAt`, so the ramp's
// stop logic can be exercised without any HTTP.
func fakeStep(breakAt float64) stepRunner {
	return func(_ context.Context, rps float64) (engineStats, []report.BucketResult) {
		attempted := int64(rps)
		var shed int64
		if rps >= breakAt {
			shed = int64(rps * 0.1) // 10% shed past the break point
		}
		return engineStats{
			attempted:   attempted,
			shed:        shed,
			completed:   attempted - shed,
			maxInflight: 10,
			measureDur:  time.Second,
		}, nil
	}
}

func TestRampStopsAtBreakingPoint(t *testing.T) {
	cfg := rampConfig{from: 200, step: 200, maxRPS: 2000, shedThreshold: 0.05}
	res := runRamp(context.Background(), cfg, fakeStep(600))
	if res.BreakingPointRPS != 600 {
		t.Errorf("breaking point = %.0f, want 600", res.BreakingPointRPS)
	}
	// Steps 200, 400, 600 then stop.
	if len(res.Steps) != 3 {
		t.Fatalf("steps = %d, want 3", len(res.Steps))
	}
	if res.Steps[2].TargetRPS != 600 || res.Steps[2].ShedRate < 0.05 {
		t.Errorf("breaking step wrong: %+v", res.Steps[2])
	}
}

func TestRampNeverBreaksRunsToMax(t *testing.T) {
	// Threshold never crossed (fakeStep break point above max).
	cfg := rampConfig{from: 100, step: 100, maxRPS: 500, shedThreshold: 0.05}
	res := runRamp(context.Background(), cfg, fakeStep(10_000))
	if res.BreakingPointRPS != 0 {
		t.Errorf("breaking point = %.0f, want 0 (never broke)", res.BreakingPointRPS)
	}
	// 100,200,300,400,500 = 5 steps.
	if len(res.Steps) != 5 {
		t.Errorf("steps = %d, want 5", len(res.Steps))
	}
}

func TestRampErrorThresholdStops(t *testing.T) {
	// Error rate (not shed) triggers the stop.
	run := func(_ context.Context, rps float64) (engineStats, []report.BucketResult) {
		var buckets []report.BucketResult
		if rps >= 400 {
			buckets = []report.BucketResult{{Name: "x", Samples: 80, Errors: 20}} // 20% errors
		} else {
			buckets = []report.BucketResult{{Name: "x", Samples: 100, Errors: 0}}
		}
		return engineStats{attempted: int64(rps), completed: int64(rps), measureDur: time.Second}, buckets
	}
	cfg := rampConfig{from: 200, step: 200, maxRPS: 2000, errThreshold: 0.1}
	res := runRamp(context.Background(), cfg, run)
	if res.BreakingPointRPS != 400 {
		t.Errorf("breaking point = %.0f, want 400", res.BreakingPointRPS)
	}
}
