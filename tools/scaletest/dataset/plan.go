// Package dataset is the deterministic, pgx-free data generator for the
// OpenTAMS scale-test harness. It owns the bimodal row plan (Compose,
// presets) and the seeded row generators (Gen and its iterators).
//
// Keeping this package free of any database dependency lets every
// consumer — the bulk loader (tools/scaletest/loader), the DB-direct
// bench (tools/scaletest/bench), and the HTTP load generator
// (tools/scaletest/loadgen) — replay byte-identical rows from the same
// (plan, seed) without touching Postgres. The bench and loadgen tiers
// derive flow_ids / object_ids this way to drive API calls against the
// pre-loaded dataset with no lookup queries.
package dataset

import (
	"fmt"
	"math"
)

// Knobs are the user-facing levers for the loader's bimodal data plan.
// All fields are required and validated by Compose — there is no
// implicit defaulting. The two canonical scenarios are produced by
// PresetS6s / PresetS1s; bespoke shapes are built by hand.
type Knobs struct {
	// TargetSegments is the desired row count for the segments table.
	// Actual TotalSegments in the returned Plan may differ by < 1% due
	// to integer rounding of flow counts.
	TargetSegments int64

	// ChunkDurationSec is the segment chunk duration in seconds. Only
	// 1 and 6 are accepted — they are the two canonical scenarios
	// pinned by docs/scale-test-plan.md §3.3.
	ChunkDurationSec int

	// DeepDepth is the segments-per-flow count for deep (live, 24×7)
	// flows. e.g. 150_000 at 6s chunks, 1_296_000 at 1s chunks.
	DeepDepth int64

	// ShallowDepth is the segments-per-flow count for shallow (VOD)
	// flows. e.g. 50 at 6s chunks, 300 at 1s chunks.
	ShallowDepth int64

	// DeepFlowCount is the number of deep flows in the dataset. The
	// shallow flow count is derived to land near TargetSegments. A
	// caller wanting a shallow-only dataset can pass 0; deep cannot
	// overshoot the target by more than 10%.
	DeepFlowCount int64

	// RenditionsPerSource is the average number of rendition flows
	// grouped under one essence-source (§3.5). Sources are derived as
	// ceil(TotalFlows / RenditionsPerSource).
	RenditionsPerSource int
}

// Plan is Compose's output: the exact row counts the loader will
// produce in each table, derived deterministically from the Knobs.
type Plan struct {
	Knobs

	DeepSegments       int64
	ShallowFlowCount   int64
	ShallowSegments    int64
	TotalSegments      int64
	TotalFlows         int64
	SourcesCount       int64
	FlowCollectionRows int64
	ObjectsCount       int64
}

// Compose validates the knobs and computes the per-table row counts.
// All downstream pieces (generator, COPY loader, reporting) trust the
// returned Plan, so all bounds enforcement lives here.
func Compose(k Knobs) (Plan, error) {
	if k.TargetSegments <= 0 {
		return Plan{}, fmt.Errorf("TargetSegments must be > 0, got %d", k.TargetSegments)
	}
	if k.ChunkDurationSec != 1 && k.ChunkDurationSec != 6 {
		return Plan{}, fmt.Errorf("ChunkDurationSec must be 1 or 6, got %d", k.ChunkDurationSec)
	}
	if k.DeepDepth <= 0 {
		return Plan{}, fmt.Errorf("DeepDepth must be > 0, got %d", k.DeepDepth)
	}
	if k.ShallowDepth <= 0 {
		return Plan{}, fmt.Errorf("ShallowDepth must be > 0, got %d", k.ShallowDepth)
	}
	if k.RenditionsPerSource <= 0 {
		return Plan{}, fmt.Errorf("RenditionsPerSource must be > 0, got %d", k.RenditionsPerSource)
	}
	if k.DeepFlowCount < 0 {
		return Plan{}, fmt.Errorf("DeepFlowCount must be >= 0, got %d", k.DeepFlowCount)
	}

	deepSegs := k.DeepFlowCount * k.DeepDepth
	if deepSegs > k.TargetSegments+k.TargetSegments/10 {
		return Plan{}, fmt.Errorf(
			"deep segments %d exceeds target %d by >10%% (DeepFlowCount=%d × DeepDepth=%d)",
			deepSegs, k.TargetSegments, k.DeepFlowCount, k.DeepDepth)
	}

	var shallowCount int64
	if shallowTarget := k.TargetSegments - deepSegs; shallowTarget > 0 {
		shallowCount = int64(math.Round(float64(shallowTarget) / float64(k.ShallowDepth)))
	}
	shallowSegs := shallowCount * k.ShallowDepth

	totalSegs := deepSegs + shallowSegs
	totalFlows := k.DeepFlowCount + shallowCount

	// Ceil(totalFlows / RenditionsPerSource); guard against zero.
	var sources int64
	if totalFlows > 0 {
		sources = (totalFlows + int64(k.RenditionsPerSource) - 1) / int64(k.RenditionsPerSource)
	}

	return Plan{
		Knobs:              k,
		DeepSegments:       deepSegs,
		ShallowFlowCount:   shallowCount,
		ShallowSegments:    shallowSegs,
		TotalSegments:      totalSegs,
		TotalFlows:         totalFlows,
		SourcesCount:       sources,
		FlowCollectionRows: totalFlows - sources,
		ObjectsCount:       totalSegs,
	}, nil
}

// Canonical depths and deep-fractions for the two chunk-duration
// presets; pinned by docs/scale-test-plan.md §3.3 and guarded by
// TestPresetsMatchChunkDefaults.
const (
	// 6-second chunks: 15 days × 24h × 3600s / 6s ≈ 216k upper bound;
	// 150k is the "deep" anchor in the doc (slightly below the full
	// live depth to keep the deep-flow count near 3000 feeds×dimensions
	// at 500M).
	c6DeepDepth    int64   = 150_000
	c6ShallowDepth int64   = 50 // 5-min VOD × 60s / 6s
	c6DeepFrac     float64 = 0.9

	// 1-second chunks: 15 days × 86400 / 1s = 1_296_000 (full live depth).
	c1DeepDepth    int64   = 1_296_000
	c1ShallowDepth int64   = 300 // 5-min VOD × 60s / 1s
	c1DeepFrac     float64 = 0.78
)

// Preset6 builds Knobs for the 6-second-chunk scenario (breadth) at
// the given target row count. Deep flow count scales with target so
// the bimodal shape is preserved at small N (validation runs) and at
// 500M alike.
func Preset6(target int64) Knobs {
	return Knobs{
		TargetSegments:      target,
		ChunkDurationSec:    6,
		DeepDepth:           c6DeepDepth,
		ShallowDepth:        c6ShallowDepth,
		DeepFlowCount:       int64(math.Round(float64(target) * c6DeepFrac / float64(c6DeepDepth))),
		RenditionsPerSource: 6,
	}
}

// Preset1 builds Knobs for the 1-second-chunk scenario (depth) at the
// given target row count.
func Preset1(target int64) Knobs {
	return Knobs{
		TargetSegments:      target,
		ChunkDurationSec:    1,
		DeepDepth:           c1DeepDepth,
		ShallowDepth:        c1ShallowDepth,
		DeepFlowCount:       int64(math.Round(float64(target) * c1DeepFrac / float64(c1DeepDepth))),
		RenditionsPerSource: 6,
	}
}
