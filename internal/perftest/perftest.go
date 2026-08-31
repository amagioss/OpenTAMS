// Package perftest provides shared helpers for OpenTAMS performance and
// concurrency integration tests. The concurrency tests run on the
// default `go test ./...` invocation (gated only by `testing.Short()`).
// The performance tests are additionally gated behind the `perf` build
// tag so they are excluded from normal CI runs and only executed by
// `go test -tags=perf -run Perf ./...` on a representative runner.
//
// Spec budgets are declared per test function; runners
// with known-noisy hardware MAY override individual budgets via the
// `OPENTAMS_PERF_<KEY>_MS` / `OPENTAMS_PERF_<KEY>_QPS` env vars (see
// EnvOverrideMS / EnvOverrideQPS). Release performance CI runs without
// overrides — those env vars exist for developer-laptop debugging only.
package perftest

import (
	"os"
	"slices"
	"strconv"
	"testing"
	"time"
)

// SkipIfShort calls t.Skip when -short is passed. Concurrency and perf
// tests do real work (testcontainer setup, large fixtures) and are not
// suitable for fast unit-test runs.
func SkipIfShort(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("perftest: skipped under -short")
	}
}

// P99 returns the 99th-percentile sample from `samples` using the
// nearest-rank method. Samples are sorted in place; callers wanting to
// preserve order must copy first. Empty input returns 0.
func P99(samples []time.Duration) time.Duration {
	if len(samples) == 0 {
		return 0
	}
	slices.Sort(samples)
	return quantileByPctSorted(samples, 99)
}

// quantileByPctSorted returns the pct-th percentile of an
// already-sorted slice using the nearest-rank method:
// idx = ceil(pct*N/100) - 1, clamped to [0, N-1]. Integer arithmetic
// avoids the float rounding hazard (e.g. 0.99*100 → 99.0000…1 → ceil 100).
// For N=100: pct=50→idx 49, pct=95→idx 94, pct=99→idx 98. Callers must
// sort first; empty input returns 0.
func quantileByPctSorted(sorted []time.Duration, pct int) time.Duration {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	idx := max((pct*n+99)/100-1, 0)
	if idx >= n {
		idx = n - 1
	}
	return sorted[idx]
}

// Stats is the per-measurement latency summary consumed by the
// scale-test report (tools/scaletest/report). Percentiles use the same
// nearest-rank method as P99, so Stats.P99 always equals P99 for the
// same samples (pinned by TestSummarizeMatchesP99).
type Stats struct {
	Count int
	Min   time.Duration
	P50   time.Duration
	P95   time.Duration
	P99   time.Duration
	Max   time.Duration
}

// Summarize computes the latency Stats for a set of samples. Unlike P99
// it does NOT mutate the caller's slice — it sorts a copy — so a bench
// loop can summarize and then reuse or further inspect its samples. The
// copy + single sort cost is negligible against the work being measured.
// Empty input returns the zero Stats.
func Summarize(samples []time.Duration) Stats {
	if len(samples) == 0 {
		return Stats{}
	}
	sorted := make([]time.Duration, len(samples))
	copy(sorted, samples)
	slices.Sort(sorted)
	return Stats{
		Count: len(sorted),
		Min:   sorted[0],
		P50:   quantileByPctSorted(sorted, 50),
		P95:   quantileByPctSorted(sorted, 95),
		P99:   quantileByPctSorted(sorted, 99),
		Max:   sorted[len(sorted)-1],
	}
}

// EnvOverrideMS returns the budget in time.Duration. When the env var
// is set to a positive integer, it overrides `defaultMs`; otherwise the
// default applies. Non-positive or unparsable values fall back to the
// default and are silently ignored — the spec budget is the default.
func EnvOverrideMS(envKey string, defaultMs int) time.Duration {
	v := os.Getenv(envKey)
	if v == "" {
		return time.Duration(defaultMs) * time.Millisecond
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return time.Duration(defaultMs) * time.Millisecond
	}
	return time.Duration(n) * time.Millisecond
}

// EnvOverrideQPS returns a throughput budget. Same semantics as
// EnvOverrideMS but for "items/sec" budgets.
func EnvOverrideQPS(envKey string, defaultQPS float64) float64 {
	v := os.Getenv(envKey)
	if v == "" {
		return defaultQPS
	}
	n, err := strconv.ParseFloat(v, 64)
	if err != nil || n <= 0 {
		return defaultQPS
	}
	return n
}
