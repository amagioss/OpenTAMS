package main

import (
	"context"
	"time"

	"github.com/amagioss/opentams/tools/scaletest/report"
)

// rampConfig parameterises an overload ramp: the offered rate climbs
// from `from` by `step` up to `maxRPS`, holding each level for one step
// (the runner decides the duration). The ramp stops at the first level
// where shed rate or aggregate error rate crosses its (positive)
// threshold — that level is the breaking point.
type rampConfig struct {
	from          float64
	step          float64
	maxRPS        float64
	shedThreshold float64
	errThreshold  float64
}

// stepRunner runs one fixed-rate step and returns its load counters and
// per-bucket results. The CLI supplies a runner backed by the HTTP
// engine; tests supply a fake.
type stepRunner func(ctx context.Context, rps float64) (engineStats, []report.BucketResult)

// runRamp executes the ramp, returning the per-step summary and the
// breaking-point RPS (0 if the run reached maxRPS without crossing a
// threshold). A positive step is required by the caller.
func runRamp(ctx context.Context, cfg rampConfig, run stepRunner) report.RampResult {
	var res report.RampResult
	for rps := cfg.from; rps <= cfg.maxRPS; rps += cfg.step {
		if ctx.Err() != nil {
			break
		}
		stats, buckets := run(ctx, rps)
		step := summarizeStep(rps, stats, buckets)
		res.Steps = append(res.Steps, step)
		if broke(step, cfg) {
			res.BreakingPointRPS = rps
			break
		}
	}
	return res
}

// broke reports whether a step crossed a configured (positive) threshold.
func broke(s report.RampStep, cfg rampConfig) bool {
	if cfg.shedThreshold > 0 && s.ShedRate >= cfg.shedThreshold {
		return true
	}
	if cfg.errThreshold > 0 && s.ErrorRate >= cfg.errThreshold {
		return true
	}
	return false
}

func summarizeStep(rps float64, stats engineStats, buckets []report.BucketResult) report.RampStep {
	ls := report.NewLoadStats(stats.attempted, stats.sent(), stats.completed, stats.shed,
		stats.maxInflight, stats.measureDur, rps, 0)

	var samples, errs int
	var maxP99 float64
	for _, b := range buckets {
		samples += b.Samples
		errs += b.Errors
		if b.Latency.P99 > maxP99 {
			maxP99 = b.Latency.P99
		}
	}
	var errRate float64
	if total := samples + errs; total > 0 {
		errRate = float64(errs) / float64(total)
	}
	return report.RampStep{
		TargetRPS:    rps,
		AttemptedRPS: ls.AttemptedRPS,
		CompletedRPS: ls.CompletedRPS,
		ShedRate:     ls.ShedRate,
		ErrorRate:    errRate,
		MaxP99Ms:     maxP99,
	}
}

// stepDuration is unused by the orchestrator (the runner owns timing) but
// documents the intended per-step hold; kept as a named default for the
// CLI.
const defaultStepDuration = 30 * time.Second
