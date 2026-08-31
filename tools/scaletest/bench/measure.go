package main

import (
	"context"
	"time"
)

// measurement is the raw output of one bucket's measurement loop: the
// per-call latencies of the successful calls, the count of failed calls,
// and the wall-clock elapsed across the measured (non-warmup) phase.
// report.NewBucketResult turns this into the serializable BucketResult.
type measurement struct {
	latencies []time.Duration
	errors    int
	elapsed   time.Duration
}

// measure runs op `warmup` times to prime caches/plans (discarded), then
// `samples` more times with per-call timing. op receives a monotonically
// increasing call index spanning both phases (0..warmup-1 are warmup,
// warmup..warmup+samples-1 are measured), so a bucket can target distinct
// rows per call without warmup and measured calls colliding.
//
// Only successful calls contribute a latency sample; failures increment
// the error count. The loop checks ctx between calls and returns early
// if it is cancelled (^C / SIGTERM), so a long run can be interrupted
// without losing the samples already collected.
func measure(ctx context.Context, samples, warmup int, op func(ctx context.Context, i int) error) measurement {
	for i := range warmup {
		if ctx.Err() != nil {
			return measurement{}
		}
		_ = op(ctx, i)
	}

	m := measurement{latencies: make([]time.Duration, 0, samples)}
	start := time.Now()
	for i := range samples {
		if ctx.Err() != nil {
			break
		}
		idx := warmup + i
		t0 := time.Now()
		err := op(ctx, idx)
		d := time.Since(t0)
		if err != nil {
			m.errors++
			continue
		}
		m.latencies = append(m.latencies, d)
	}
	m.elapsed = time.Since(start)
	return m
}
