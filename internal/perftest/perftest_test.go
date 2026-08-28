package perftest

import (
	"testing"
	"time"
)

func TestP99(t *testing.T) {
	// 100 samples 1..100ms; p99 = 99ms (nearest-rank index = ceil(0.99*100)-1 = 98).
	xs := make([]time.Duration, 100)
	for i := range xs {
		xs[i] = time.Duration(i+1) * time.Millisecond
	}
	got := P99(xs)
	if got != 99*time.Millisecond {
		t.Errorf("P99 = %v, want 99ms", got)
	}
}

func TestP99Empty(t *testing.T) {
	if P99(nil) != 0 {
		t.Error("P99(nil) != 0")
	}
}

func TestEnvOverrideMSDefault(t *testing.T) {
	t.Setenv("OPENTAMS_PERF_TEST_MS", "")
	got := EnvOverrideMS("OPENTAMS_PERF_TEST_MS", 50)
	if got != 50*time.Millisecond {
		t.Errorf("got %v, want 50ms", got)
	}
}

func TestEnvOverrideMSSet(t *testing.T) {
	t.Setenv("OPENTAMS_PERF_TEST_MS", "200")
	got := EnvOverrideMS("OPENTAMS_PERF_TEST_MS", 50)
	if got != 200*time.Millisecond {
		t.Errorf("got %v, want 200ms", got)
	}
}

func TestEnvOverrideMSBogus(t *testing.T) {
	t.Setenv("OPENTAMS_PERF_TEST_MS", "junk")
	got := EnvOverrideMS("OPENTAMS_PERF_TEST_MS", 50)
	if got != 50*time.Millisecond {
		t.Errorf("bogus override fell through, got %v", got)
	}
}

func TestSummarize(t *testing.T) {
	// 100 samples 1..100ms. Nearest-rank percentiles match the existing
	// P99 convention: idx = ceil(pct*N/100)-1.
	xs := make([]time.Duration, 100)
	for i := range xs {
		xs[i] = time.Duration(i+1) * time.Millisecond
	}
	s := Summarize(xs)
	if s.Count != 100 {
		t.Errorf("Count = %d, want 100", s.Count)
	}
	if s.Min != 1*time.Millisecond {
		t.Errorf("Min = %v, want 1ms", s.Min)
	}
	if s.P50 != 50*time.Millisecond {
		t.Errorf("P50 = %v, want 50ms", s.P50)
	}
	if s.P95 != 95*time.Millisecond {
		t.Errorf("P95 = %v, want 95ms", s.P95)
	}
	if s.P99 != 99*time.Millisecond {
		t.Errorf("P99 = %v, want 99ms", s.P99)
	}
	if s.Max != 100*time.Millisecond {
		t.Errorf("Max = %v, want 100ms", s.Max)
	}
}

// TestSummarizeMatchesP99 pins that Summarize.P99 equals the standalone
// P99 for the same input — the two must never diverge, since existing
// perf tests assert on P99 and bench reports assert on Summarize.
func TestSummarizeMatchesP99(t *testing.T) {
	xs := []time.Duration{
		7, 3, 99, 1, 42, 88, 5, 61, 23, 100,
	}
	for i := range xs {
		xs[i] *= time.Millisecond
	}
	cp := make([]time.Duration, len(xs))
	copy(cp, xs)
	if got, want := Summarize(xs).P99, P99(cp); got != want {
		t.Errorf("Summarize.P99 = %v, standalone P99 = %v", got, want)
	}
}

func TestSummarizeEmpty(t *testing.T) {
	s := Summarize(nil)
	if s != (Stats{}) {
		t.Errorf("Summarize(nil) = %+v, want zero Stats", s)
	}
}

func TestSummarizeSingle(t *testing.T) {
	s := Summarize([]time.Duration{42 * time.Millisecond})
	if s.Count != 1 || s.Min != 42*time.Millisecond || s.P50 != 42*time.Millisecond ||
		s.P95 != 42*time.Millisecond || s.P99 != 42*time.Millisecond || s.Max != 42*time.Millisecond {
		t.Errorf("single-sample stats wrong: %+v", s)
	}
}

// TestSummarizeDoesNotMutateInput is the contract that lets a caller
// reuse its sample slice after summarizing — unlike the in-place P99.
func TestSummarizeDoesNotMutateInput(t *testing.T) {
	xs := []time.Duration{5, 1, 3, 2, 4}
	for i := range xs {
		xs[i] *= time.Millisecond
	}
	before := make([]time.Duration, len(xs))
	copy(before, xs)
	_ = Summarize(xs)
	for i := range xs {
		if xs[i] != before[i] {
			t.Fatalf("Summarize mutated input at %d: got %v, was %v", i, xs[i], before[i])
		}
	}
}
