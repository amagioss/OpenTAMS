package main

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// jitter describes the periodic burst pattern layered on the baseline
// offered rate (docs/scale-test-plan.md §4): for `dur` out of every
// `every`, the target rises from `baseline` to `burst` RPS. A zero
// every/dur/burst disables bursting (steady baseline).
type jitter struct {
	baseline float64
	burst    float64
	every    time.Duration
	dur      time.Duration
}

// rpsAt returns the target RPS at the given elapsed time. The burst
// occupies the first `dur` of each `every`-length period.
func (j jitter) rpsAt(elapsed time.Duration) float64 {
	if j.every <= 0 || j.dur <= 0 || j.burst <= 0 {
		return j.baseline
	}
	if elapsed%j.every < j.dur {
		return j.burst
	}
	return j.baseline
}

type engineConfig struct {
	warmup      time.Duration
	measure     time.Duration
	maxInflight int
	jit         jitter
}

// engineStats are the raw load-control counters from a run, gathered
// only over the measurement window. report.NewLoadStats turns these
// into rates.
type engineStats struct {
	attempted   int64
	shed        int64
	completed   int64
	maxInflight int64
	measureDur  time.Duration
}

func (s engineStats) sent() int64 { return s.attempted - s.shed }

// runEngine drives the open-loop workload: it dispatches operations at
// the jitter-controlled target RPS, bounding concurrency to
// cfg.maxInflight. When the in-flight cap is saturated, the would-be
// dispatch is SHED (counted, not sent) — the open-loop overload signal.
// runOne executes exactly one operation; the engine passes whether the
// current window is the measured one so the executor records latencies
// only then, and runOne returns whether the operation's HTTP call(s) all
// received a response (no transport error). Counters are tallied only
// during the measurement window: `completed` counts operations that
// responded, so `sent − completed` reveals dispatches that timed out or
// failed at the transport under overload (distinct from `sent`).
func runEngine(ctx context.Context, cfg engineConfig, runOne func(ctx context.Context, measuring bool) bool) engineStats {
	maxIF := max(cfg.maxInflight, 1)
	sem := make(chan struct{}, maxIF)

	var attempted, shed, completed, curInflight, maxInflightSeen int64
	var wg sync.WaitGroup

	start := time.Now()
	warmupEnd := start.Add(cfg.warmup)
	measureEnd := warmupEnd.Add(cfg.measure)

	// next is the scheduled instant of the next dispatch; the interval is
	// recomputed each iteration because the target RPS changes with jitter.
	next := start
	for {
		now := time.Now()
		if !now.Before(measureEnd) || ctx.Err() != nil {
			break
		}
		rps := cfg.jit.rpsAt(now.Sub(start))
		if rps <= 0 {
			rps = 1
		}
		interval := time.Duration(float64(time.Second) / rps)
		next = next.Add(interval)
		if d := time.Until(next); d > 0 {
			select {
			case <-time.After(d):
			case <-ctx.Done():
			}
		}
		if ctx.Err() != nil {
			// Cancelled during the sleep — stop without dispatching again,
			// so a ^C'd run records no spurious cancelled-context call.
			break
		}

		measuring := !time.Now().Before(warmupEnd)
		if measuring {
			atomic.AddInt64(&attempted, 1)
		}

		select {
		case sem <- struct{}{}:
			cur := atomic.AddInt64(&curInflight, 1)
			storeMax(&maxInflightSeen, cur)
			wg.Add(1)
			go func(measuring bool) {
				defer wg.Done()
				defer func() {
					// Decrement before releasing the slot. The other order
					// leaves a window where the slot is free but the goroutine
					// is still counted: the dispatch loop can take that slot
					// and increment, reading curInflight above maxInflight and
					// latching an over-reported peak in storeMax. Admission is
					// unaffected either way -- the semaphore bounds real
					// concurrency -- but the reported figure ends up in scale
					// test results, so it has to be right.
					atomic.AddInt64(&curInflight, -1)
					<-sem
				}()
				responded := runOne(ctx, measuring)
				if measuring && responded {
					atomic.AddInt64(&completed, 1)
				}
			}(measuring)
		default:
			if measuring {
				atomic.AddInt64(&shed, 1)
			}
		}
	}

	wg.Wait()
	return engineStats{
		attempted:   atomic.LoadInt64(&attempted),
		shed:        atomic.LoadInt64(&shed),
		completed:   atomic.LoadInt64(&completed),
		maxInflight: atomic.LoadInt64(&maxInflightSeen),
		measureDur:  cfg.measure,
	}
}

// storeMax atomically raises *dst to v if v is larger.
func storeMax(dst *int64, v int64) {
	for {
		old := atomic.LoadInt64(dst)
		if v <= old || atomic.CompareAndSwapInt64(dst, old, v) {
			return
		}
	}
}
