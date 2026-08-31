package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestJitterSchedule(t *testing.T) {
	j := jitter{baseline: 500, burst: 1500, every: 5 * time.Minute, dur: 45 * time.Second}
	cases := []struct {
		elapsed time.Duration
		want    float64
	}{
		{0, 1500},                              // burst window starts at each period boundary
		{30 * time.Second, 1500},               // still inside the 45s burst
		{50 * time.Second, 500},                // past burst, baseline
		{5 * time.Minute, 1500},                // next period boundary → burst again
		{5*time.Minute + 50*time.Second, 500},  // baseline again
		{5*time.Minute + 10*time.Second, 1500}, // inside the next burst
	}
	for _, c := range cases {
		if got := j.rpsAt(c.elapsed); got != c.want {
			t.Errorf("rpsAt(%v) = %v, want %v", c.elapsed, got, c.want)
		}
	}
}

func TestJitterDisabledReturnsBaseline(t *testing.T) {
	j := jitter{baseline: 300, burst: 0, every: 0, dur: 0}
	for _, e := range []time.Duration{0, time.Second, time.Minute} {
		if got := j.rpsAt(e); got != 300 {
			t.Errorf("rpsAt(%v) = %v, want 300 (jitter disabled)", e, got)
		}
	}
}

func TestEngineDispatchesAtRate(t *testing.T) {
	var calls atomic.Int64
	cfg := engineConfig{
		warmup:      0,
		measure:     300 * time.Millisecond,
		maxInflight: 200,
		jit:         jitter{baseline: 200},
	}
	st := runEngine(context.Background(), cfg, func(_ context.Context, _ bool) bool {
		calls.Add(1)
		return true
	})
	// ~200 rps × 0.3s ≈ 60 attempts. Generous tolerance for real-time scheduling.
	if st.attempted < 30 || st.attempted > 100 {
		t.Errorf("attempted = %d, want ~60", st.attempted)
	}
	if st.shed != 0 {
		t.Errorf("shed = %d, want 0 (fast op, ample inflight)", st.shed)
	}
	if st.sent() != st.attempted {
		t.Errorf("sent %d != attempted %d with no shed", st.sent(), st.attempted)
	}
	if st.completed < st.attempted-2 {
		t.Errorf("completed = %d, want ~attempted %d", st.completed, st.attempted)
	}
}

func TestEngineShedsWhenInflightSaturated(t *testing.T) {
	cfg := engineConfig{
		warmup:      0,
		measure:     300 * time.Millisecond,
		maxInflight: 1,
		jit:         jitter{baseline: 1000},
	}
	st := runEngine(context.Background(), cfg, func(ctx context.Context, _ bool) bool {
		// Slow op: holds the single in-flight slot, forcing sheds.
		select {
		case <-time.After(40 * time.Millisecond):
		case <-ctx.Done():
		}
		return true
	})
	if st.shed == 0 {
		t.Errorf("expected sheds with maxInflight=1 and a slow op, got 0")
	}
	if st.maxInflight != 1 {
		t.Errorf("maxInflight = %d, want 1", st.maxInflight)
	}
}

func TestEngineCompletedCountsOnlyResponded(t *testing.T) {
	// An op that never gets a response (returns false) must leave
	// completed at 0 while still counting as attempted/sent — so
	// sent − completed surfaces transport failures under overload.
	cfg := engineConfig{
		warmup:      0,
		measure:     200 * time.Millisecond,
		maxInflight: 200,
		jit:         jitter{baseline: 200},
	}
	st := runEngine(context.Background(), cfg, func(_ context.Context, _ bool) bool {
		return false // simulates a transport failure / timeout
	})
	if st.attempted == 0 {
		t.Fatal("expected some attempts")
	}
	if st.completed != 0 {
		t.Errorf("completed = %d, want 0 when no op responds", st.completed)
	}
	if st.sent() == 0 {
		t.Errorf("sent = 0, want > 0 (dispatches still happened)")
	}
}

func TestEngineWarmupNotCounted(t *testing.T) {
	var calls atomic.Int64
	cfg := engineConfig{
		warmup:      200 * time.Millisecond,
		measure:     200 * time.Millisecond,
		maxInflight: 200,
		jit:         jitter{baseline: 200},
	}
	st := runEngine(context.Background(), cfg, func(_ context.Context, measuring bool) bool {
		calls.Add(1)
		_ = measuring
		return true
	})
	// Total op calls span warmup+measure; counted attempts only the measure window.
	if total := calls.Load(); total <= st.attempted {
		t.Errorf("total calls %d should exceed counted attempts %d (warmup excluded)", total, st.attempted)
	}
}
