package report

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func msSamples(n int) []time.Duration {
	xs := make([]time.Duration, n)
	for i := range xs {
		xs[i] = time.Duration(i+1) * time.Millisecond
	}
	return xs
}

func TestNewBucketResultMath(t *testing.T) {
	// 100 successful samples 1..100ms, no errors, 1s wall clock.
	b := NewBucketResult("list-by-flow", OpRead, msSamples(100), 0, time.Second)

	if b.Name != "list-by-flow" || b.Op != OpRead {
		t.Errorf("name/op wrong: %q %q", b.Name, b.Op)
	}
	if b.Samples != 100 {
		t.Errorf("Samples = %d, want 100", b.Samples)
	}
	if b.Errors != 0 || b.ErrorRate != 0 {
		t.Errorf("errors = %d rate = %v, want 0/0", b.Errors, b.ErrorRate)
	}
	if b.ThroughputPerSec != 100 {
		t.Errorf("throughput = %v, want 100", b.ThroughputPerSec)
	}
	if b.Latency.P50 != 50 || b.Latency.P95 != 95 || b.Latency.P99 != 99 ||
		b.Latency.Max != 100 || b.Latency.Min != 1 {
		t.Errorf("latency ms wrong: %+v", b.Latency)
	}
}

func TestNewBucketResultErrorRate(t *testing.T) {
	// 90 successes + 10 errors over 1s. Throughput counts only successes;
	// error rate is errors / total attempts.
	b := NewBucketResult("insert-deep", OpWrite, msSamples(90), 10, time.Second)
	if b.ThroughputPerSec != 90 {
		t.Errorf("throughput = %v, want 90", b.ThroughputPerSec)
	}
	if b.Errors != 10 {
		t.Errorf("errors = %d, want 10", b.Errors)
	}
	if got := b.ErrorRate; got < 0.099 || got > 0.101 {
		t.Errorf("error rate = %v, want ~0.1", got)
	}
}

func TestNewBucketResultEmptyNoPanic(t *testing.T) {
	b := NewBucketResult("empty", OpRead, nil, 0, 0)
	if b.Samples != 0 || b.ThroughputPerSec != 0 || b.ErrorRate != 0 {
		t.Errorf("empty bucket not zero: %+v", b)
	}
	if b.Latency != (LatencyMs{}) {
		t.Errorf("empty latency = %+v, want zero", b.Latency)
	}
}

func TestNewBucketResultErrorsOnlyRate(t *testing.T) {
	// All attempts failed: 0 successes, 5 errors → error rate 1.0, no div0.
	b := NewBucketResult("overlap-rejection", OpWrite, nil, 5, time.Second)
	if b.ErrorRate != 1.0 {
		t.Errorf("error rate = %v, want 1.0", b.ErrorRate)
	}
	if b.ThroughputPerSec != 0 {
		t.Errorf("throughput = %v, want 0", b.ThroughputPerSec)
	}
}

func TestReportJSONRoundTrip(t *testing.T) {
	r := &Report{
		RunID:      "run-1",
		Tool:       "scaletest-bench",
		ToolCommit: "abc123",
		StartedAt:  time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC),
		DB:         DBInfo{Version: "PostgreSQL 18", Settings: map[string]string{"work_mem": "32MB"}},
		Dataset:    DatasetInfo{Preset: "6", Seed: 42, TotalSegments: 500_000_000, TotalFlows: 1_003_000},
		Buckets:    []BucketResult{NewBucketResult("list-by-flow", OpRead, msSamples(10), 0, time.Second)},
	}
	var buf bytes.Buffer
	if err := r.WriteJSON(&buf); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	var got Report
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.RunID != r.RunID || got.Dataset.Seed != 42 || got.Dataset.TotalSegments != 500_000_000 {
		t.Errorf("round-trip lost fields: %+v", got)
	}
	if len(got.Buckets) != 1 || got.Buckets[0].Name != "list-by-flow" {
		t.Errorf("buckets lost: %+v", got.Buckets)
	}
	if got.DB.Settings["work_mem"] != "32MB" {
		t.Errorf("settings lost: %+v", got.DB.Settings)
	}
}

func TestNewLoadStatsMath(t *testing.T) {
	// 10s measurement: 5000 attempted, 4800 sent, 4700 completed, 200 shed,
	// peak 64 in-flight, baseline 500 / burst 1500 RPS.
	ls := NewLoadStats(5000, 4800, 4700, 200, 64, 10*time.Second, 500, 1500)
	if ls.AttemptedRPS != 500 {
		t.Errorf("AttemptedRPS = %v, want 500", ls.AttemptedRPS)
	}
	if ls.SentRPS != 480 {
		t.Errorf("SentRPS = %v, want 480", ls.SentRPS)
	}
	if ls.CompletedRPS != 470 {
		t.Errorf("CompletedRPS = %v, want 470", ls.CompletedRPS)
	}
	if ls.Shed != 200 {
		t.Errorf("Shed = %d, want 200", ls.Shed)
	}
	if got := ls.ShedRate; got < 0.0399 || got > 0.0401 {
		t.Errorf("ShedRate = %v, want ~0.04", got)
	}
	if ls.MaxInflight != 64 {
		t.Errorf("MaxInflight = %d, want 64", ls.MaxInflight)
	}
	if ls.TargetRPSBaseline != 500 || ls.TargetRPSBurst != 1500 {
		t.Errorf("targets = %v/%v, want 500/1500", ls.TargetRPSBaseline, ls.TargetRPSBurst)
	}
}

func TestNewLoadStatsZeroNoPanic(t *testing.T) {
	ls := NewLoadStats(0, 0, 0, 0, 0, 0, 0, 0)
	if ls.AttemptedRPS != 0 || ls.ShedRate != 0 {
		t.Errorf("zero load stats not zero: %+v", ls)
	}
}

func TestReportLoadStatsOmittedWhenNil(t *testing.T) {
	// A bench-style report (no Load section) must not emit a "load" key.
	r := &Report{Tool: "scaletest-bench"}
	var buf bytes.Buffer
	if err := r.WriteJSON(&buf); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	if strings.Contains(buf.String(), "\"load\"") {
		t.Errorf("nil Load should be omitted from JSON:\n%s", buf.String())
	}
}

func TestReportLoadStatsRoundTrip(t *testing.T) {
	ls := NewLoadStats(100, 90, 88, 10, 12, time.Second, 100, 300)
	r := &Report{Tool: "scaletest-loadgen", Load: &ls}
	var buf bytes.Buffer
	if err := r.WriteJSON(&buf); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	var got Report
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Load == nil || got.Load.MaxInflight != 12 || got.Load.Shed != 10 {
		t.Errorf("load round-trip lost data: %+v", got.Load)
	}
}

func TestDBStatsOmittedWhenNil(t *testing.T) {
	r := &Report{Tool: "scaletest-bench", DB: DBInfo{Version: "PostgreSQL 18"}}
	var buf bytes.Buffer
	if err := r.WriteJSON(&buf); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	if strings.Contains(buf.String(), "\"stats\"") {
		t.Errorf("nil DB.Stats should be omitted:\n%s", buf.String())
	}
}

func TestDBStatsRoundTripAndMarkdown(t *testing.T) {
	r := &Report{
		Tool: "scaletest-bench",
		DB: DBInfo{
			Version: "PostgreSQL 18",
			Stats: &DBStatsDelta{
				Commits:       1000,
				BlksHit:       9500,
				BlksRead:      500,
				CacheHitRatio: 0.95,
				WALBytes:      1 << 30,
				Checkpoints:   2,
				Deadlocks:     0,
				Tables: []TableStatsDelta{
					{Name: "segments", LiveTuples: 500, DeadTuples: 50, Autovacuums: 1,
						SizeBytesBefore: 1000, SizeBytesAfter: 2000},
				},
				TopQueries: []QueryStat{
					{Query: "INSERT INTO segments ...", Calls: 1000, TotalTimeMs: 1234.5, MeanTimeMs: 1.23},
				},
			},
		},
	}
	var buf bytes.Buffer
	if err := r.WriteJSON(&buf); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	var got Report
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.DB.Stats == nil || got.DB.Stats.CacheHitRatio != 0.95 || len(got.DB.Stats.Tables) != 1 {
		t.Errorf("DB.Stats round-trip lost data: %+v", got.DB.Stats)
	}
	if got.DB.Stats.Tables[0].Name != "segments" {
		t.Errorf("table stats lost: %+v", got.DB.Stats.Tables)
	}
	md := r.Markdown()
	for _, want := range []string{"cache hit", "segments", "WAL"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown missing %q\n%s", want, md)
		}
	}
}

func TestRampOmittedWhenNil(t *testing.T) {
	r := &Report{Tool: "scaletest-loadgen"}
	var buf bytes.Buffer
	if err := r.WriteJSON(&buf); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	if strings.Contains(buf.String(), "\"ramp\"") {
		t.Errorf("nil Ramp should be omitted:\n%s", buf.String())
	}
}

func TestRampRoundTripAndMarkdown(t *testing.T) {
	r := &Report{
		Tool: "scaletest-loadgen",
		Ramp: &RampResult{
			BreakingPointRPS: 800,
			Steps: []RampStep{
				{TargetRPS: 200, AttemptedRPS: 199, CompletedRPS: 199, ShedRate: 0, ErrorRate: 0, MaxP99Ms: 12},
				{TargetRPS: 800, AttemptedRPS: 760, CompletedRPS: 700, ShedRate: 0.06, ErrorRate: 0.08, MaxP99Ms: 410},
			},
		},
	}
	var buf bytes.Buffer
	if err := r.WriteJSON(&buf); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	var got Report
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Ramp == nil || got.Ramp.BreakingPointRPS != 800 || len(got.Ramp.Steps) != 2 {
		t.Errorf("ramp round-trip lost data: %+v", got.Ramp)
	}
	md := r.Markdown()
	for _, want := range []string{"ramp", "breaking point", "shed"} {
		if !strings.Contains(strings.ToLower(md), want) {
			t.Errorf("ramp markdown missing %q\n%s", want, md)
		}
	}
}

func TestReportMarkdownContainsBuckets(t *testing.T) {
	r := &Report{
		Tool:    "scaletest-bench",
		Dataset: DatasetInfo{Preset: "6", Seed: 42, TotalSegments: 500_000_000},
		Buckets: []BucketResult{
			NewBucketResult("list-by-flow", OpRead, msSamples(100), 0, time.Second),
			NewBucketResult("insert-deep", OpWrite, msSamples(50), 2, 2*time.Second),
		},
	}
	md := r.Markdown()
	for _, want := range []string{"list-by-flow", "insert-deep", "p99", "throughput"} {
		if !strings.Contains(strings.ToLower(md), strings.ToLower(want)) {
			t.Errorf("markdown missing %q\n---\n%s", want, md)
		}
	}
}
