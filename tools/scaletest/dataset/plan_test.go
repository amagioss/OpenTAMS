package dataset

import (
	"math"
	"testing"
)

// TestComposePreset6At500M pins the canonical 6s-chunk (breadth) composition
// from docs/scale-test-plan.md §3.3: ~3,000 deep × 150k + ~1,000,000 shallow
// × 50 = ~500M segments, with sources = ceil(flows/6) and flow_collection
// rows = flows − sources.
func TestComposePreset6At500M(t *testing.T) {
	const target int64 = 500_000_000
	plan, err := Compose(Preset6(target))
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}

	if got, want := plan.ChunkDurationSec, 6; got != want {
		t.Errorf("ChunkDurationSec = %d, want %d", got, want)
	}
	if got, want := plan.DeepDepth, int64(150_000); got != want {
		t.Errorf("DeepDepth = %d, want %d", got, want)
	}
	if got, want := plan.ShallowDepth, int64(50); got != want {
		t.Errorf("ShallowDepth = %d, want %d", got, want)
	}
	if got, want := plan.DeepFlowCount, int64(3_000); got != want {
		t.Errorf("DeepFlowCount = %d, want %d", got, want)
	}
	if got, want := plan.DeepSegments, int64(450_000_000); got != want {
		t.Errorf("DeepSegments = %d, want %d", got, want)
	}
	if got, want := plan.ShallowFlowCount, int64(1_000_000); got != want {
		t.Errorf("ShallowFlowCount = %d, want %d", got, want)
	}
	if got, want := plan.ShallowSegments, int64(50_000_000); got != want {
		t.Errorf("ShallowSegments = %d, want %d", got, want)
	}
	if got, want := plan.TotalSegments, int64(500_000_000); got != want {
		t.Errorf("TotalSegments = %d, want %d", got, want)
	}
	if got, want := plan.TotalFlows, int64(1_003_000); got != want {
		t.Errorf("TotalFlows = %d, want %d", got, want)
	}
	// sources = ceil(1_003_000 / 6) = 167_167
	if got, want := plan.SourcesCount, int64(167_167); got != want {
		t.Errorf("SourcesCount = %d, want %d", got, want)
	}
	if got, want := plan.FlowCollectionRows, plan.TotalFlows-plan.SourcesCount; got != want {
		t.Errorf("FlowCollectionRows = %d, want %d (flows-sources)", got, want)
	}
	if got, want := plan.ObjectsCount, plan.TotalSegments; got != want {
		t.Errorf("ObjectsCount = %d, want %d (1:1 with segments)", got, want)
	}
}

// TestComposePreset1At500M pins the canonical 1s-chunk (depth) composition
// from docs/scale-test-plan.md §3.3: ~300 deep × 1.296M + ~371k shallow ×
// 300 ≈ ~500M segments.
func TestComposePreset1At500M(t *testing.T) {
	const target int64 = 500_000_000
	plan, err := Compose(Preset1(target))
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}

	if got, want := plan.ChunkDurationSec, 1; got != want {
		t.Errorf("ChunkDurationSec = %d, want %d", got, want)
	}
	if got, want := plan.DeepDepth, int64(1_296_000); got != want {
		t.Errorf("DeepDepth = %d, want %d", got, want)
	}
	if got, want := plan.ShallowDepth, int64(300); got != want {
		t.Errorf("ShallowDepth = %d, want %d", got, want)
	}
	// 500M × 0.78 / 1_296_000 ≈ 300.93 → 301
	if got := plan.DeepFlowCount; got < 295 || got > 305 {
		t.Errorf("DeepFlowCount = %d, want ~300 (within [295,305])", got)
	}
	// total close to target (within 1%)
	if drift := math.Abs(float64(plan.TotalSegments-target)) / float64(target); drift > 0.01 {
		t.Errorf("TotalSegments = %d, target = %d, drift = %.4f (want < 1%%)",
			plan.TotalSegments, target, drift)
	}
	// internal consistency
	if plan.DeepSegments+plan.ShallowSegments != plan.TotalSegments {
		t.Errorf("deep+shallow segments mismatch: %d + %d != %d",
			plan.DeepSegments, plan.ShallowSegments, plan.TotalSegments)
	}
	if plan.DeepFlowCount+plan.ShallowFlowCount != plan.TotalFlows {
		t.Errorf("deep+shallow flow counts mismatch")
	}
	if plan.ObjectsCount != plan.TotalSegments {
		t.Errorf("ObjectsCount = %d, want = TotalSegments = %d", plan.ObjectsCount, plan.TotalSegments)
	}
}

// TestComposePreset6At1M is the small-N validation case: the bimodal shape
// must scale down proportionally so the loader can be exercised end-to-end
// on a cheap dataset before the 500M run.
func TestComposePreset6At1M(t *testing.T) {
	const target int64 = 1_000_000
	plan, err := Compose(Preset6(target))
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}

	// 1M × 0.9 / 150_000 = 6.0
	if got, want := plan.DeepFlowCount, int64(6); got != want {
		t.Errorf("DeepFlowCount = %d, want %d", got, want)
	}
	if got, want := plan.DeepSegments, int64(900_000); got != want {
		t.Errorf("DeepSegments = %d, want %d", got, want)
	}
	if got, want := plan.ShallowFlowCount, int64(2_000); got != want {
		t.Errorf("ShallowFlowCount = %d, want %d", got, want)
	}
	if got, want := plan.TotalSegments, int64(1_000_000); got != want {
		t.Errorf("TotalSegments = %d, want %d", got, want)
	}
	// At 1M, total flows = 2006, sources = ceil(2006/6) = 335
	if got, want := plan.SourcesCount, int64(335); got != want {
		t.Errorf("SourcesCount = %d, want %d", got, want)
	}
}

// TestComposeRejectsBadKnobs checks that obviously-wrong knobs fail loudly
// rather than producing silently wrong row counts. Compose is the only
// place this validation lives — every downstream piece trusts the Plan.
func TestComposeRejectsBadKnobs(t *testing.T) {
	tests := []struct {
		name string
		k    Knobs
	}{
		{"zero target", Knobs{TargetSegments: 0, ChunkDurationSec: 6, DeepDepth: 150_000, ShallowDepth: 50, DeepFlowCount: 3000, RenditionsPerSource: 6}},
		{"negative target", Knobs{TargetSegments: -1, ChunkDurationSec: 6, DeepDepth: 150_000, ShallowDepth: 50, DeepFlowCount: 3000, RenditionsPerSource: 6}},
		{"invalid chunk", Knobs{TargetSegments: 1_000_000, ChunkDurationSec: 3, DeepDepth: 150_000, ShallowDepth: 50, DeepFlowCount: 6, RenditionsPerSource: 6}},
		{"zero deep depth", Knobs{TargetSegments: 1_000_000, ChunkDurationSec: 6, DeepDepth: 0, ShallowDepth: 50, DeepFlowCount: 6, RenditionsPerSource: 6}},
		{"zero shallow depth", Knobs{TargetSegments: 1_000_000, ChunkDurationSec: 6, DeepDepth: 150_000, ShallowDepth: 0, DeepFlowCount: 6, RenditionsPerSource: 6}},
		{"zero renditions", Knobs{TargetSegments: 1_000_000, ChunkDurationSec: 6, DeepDepth: 150_000, ShallowDepth: 50, DeepFlowCount: 6, RenditionsPerSource: 0}},
		{"negative deep flow count", Knobs{TargetSegments: 1_000_000, ChunkDurationSec: 6, DeepDepth: 150_000, ShallowDepth: 50, DeepFlowCount: -1, RenditionsPerSource: 6}},
		// Deep alone overshoots the target by > 10% — likely user error,
		// not a legitimate "deep-only" mode.
		{"deep overshoots target", Knobs{TargetSegments: 100_000, ChunkDurationSec: 6, DeepDepth: 150_000, ShallowDepth: 50, DeepFlowCount: 10, RenditionsPerSource: 6}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Compose(tc.k); err == nil {
				t.Errorf("Compose accepted invalid knobs; want error")
			}
		})
	}
}

// TestPresetsMatchChunkDefaults guards against silent regressions in the
// canonical depths/renditions used by the two presets. These numbers are
// referenced by §3.3 of the plan; changing them changes the test contract.
func TestPresetsMatchChunkDefaults(t *testing.T) {
	p6 := Preset6(500_000_000)
	if p6.ChunkDurationSec != 6 || p6.DeepDepth != 150_000 || p6.ShallowDepth != 50 || p6.RenditionsPerSource != 6 {
		t.Errorf("Preset6 defaults drifted: %+v", p6)
	}
	p1 := Preset1(500_000_000)
	if p1.ChunkDurationSec != 1 || p1.DeepDepth != 1_296_000 || p1.ShallowDepth != 300 || p1.RenditionsPerSource != 6 {
		t.Errorf("Preset1 defaults drifted: %+v", p1)
	}
}
