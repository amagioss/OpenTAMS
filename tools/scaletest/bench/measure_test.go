package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

func TestMeasureCountsAndCallTotal(t *testing.T) {
	var calls atomic.Int64
	m := measure(context.Background(), 10, 3, func(_ context.Context, _ int) error {
		calls.Add(1)
		return nil
	})
	if got := len(m.latencies); got != 10 {
		t.Errorf("latencies = %d, want 10 (measured only)", got)
	}
	if m.errors != 0 {
		t.Errorf("errors = %d, want 0", m.errors)
	}
	// Warmup iterations also invoke op but are not recorded.
	if got := calls.Load(); got != 13 {
		t.Errorf("op called %d times, want 13 (warmup 3 + measured 10)", got)
	}
}

func TestMeasureRecordsErrorsNotLatency(t *testing.T) {
	// Fail every measured call with an even index; warmup = 0.
	m := measure(context.Background(), 10, 0, func(_ context.Context, i int) error {
		if i%2 == 0 {
			return errors.New("boom")
		}
		return nil
	})
	if m.errors != 5 {
		t.Errorf("errors = %d, want 5", m.errors)
	}
	if got := len(m.latencies); got != 5 {
		t.Errorf("latencies = %d, want 5 (only successes timed)", got)
	}
}

func TestMeasureWarmupIndicesPrecedeMeasured(t *testing.T) {
	// op must see warmup indices 0..2 then measured 3..5 — so buckets
	// that target distinct data per call never collide warmup vs measured.
	var seen []int
	measure(context.Background(), 3, 3, func(_ context.Context, i int) error {
		seen = append(seen, i)
		return nil
	})
	want := []int{0, 1, 2, 3, 4, 5}
	if len(seen) != len(want) {
		t.Fatalf("indices = %v, want %v", seen, want)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("indices = %v, want %v", seen, want)
		}
	}
}

func TestMeasureStopsOnCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m := measure(ctx, 1000, 0, func(_ context.Context, _ int) error {
		return nil
	})
	if len(m.latencies) != 0 {
		t.Errorf("latencies = %d, want 0 on pre-cancelled ctx", len(m.latencies))
	}
}
