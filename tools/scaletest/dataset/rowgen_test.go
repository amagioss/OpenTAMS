package dataset

import (
	"testing"

	"github.com/google/uuid"
)

// smallPlan keeps tests in microseconds — enough to exercise the
// bimodal shape without overflowing into seconds.
func smallPlan(t *testing.T) Plan {
	t.Helper()
	p, err := Compose(Knobs{
		TargetSegments:      10_000,
		ChunkDurationSec:    6,
		DeepDepth:           1_000, // small "deep" so totals add up cleanly
		ShallowDepth:        10,
		DeepFlowCount:       9, // 9 × 1000 = 9000
		RenditionsPerSource: 6,
	})
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	return p
}

// TestGenDeterministicForSameSeed pins reproducibility: the dataset is
// the contract between the loader and every measurement run. Two Gens
// with the same seed MUST produce byte-identical rows at every index.
func TestGenDeterministicForSameSeed(t *testing.T) {
	p := smallPlan(t)
	a := NewGen(p, 42)
	b := NewGen(p, 42)

	if a.Source(0) != b.Source(0) {
		t.Errorf("Source(0) diverges with same seed")
	}
	if a.Flow(0) != b.Flow(0) {
		t.Errorf("Flow(0) diverges with same seed")
	}
	if a.Object(0) != b.Object(0) {
		t.Errorf("Object(0) diverges with same seed")
	}
	if a.Segment(0, 0) != b.Segment(0, 0) {
		t.Errorf("Segment(0,0) diverges with same seed")
	}

	// Different seed → different rows (the seed is doing real work).
	c := NewGen(p, 43)
	if a.Source(0) == c.Source(0) {
		t.Errorf("Source(0) identical across different seeds; seed has no effect")
	}
}

// TestGenIteratorCountsMatchPlan walks every iterator end-to-end and
// asserts the row count equals what Compose said. If these diverge, the
// loader will silently load wrong totals.
func TestGenIteratorCountsMatchPlan(t *testing.T) {
	p := smallPlan(t)
	g := NewGen(p, 1)

	if got := countSources(g); got != p.SourcesCount {
		t.Errorf("sources iterated = %d, plan = %d", got, p.SourcesCount)
	}
	if got := countFlows(g); got != p.TotalFlows {
		t.Errorf("flows iterated = %d, plan = %d", got, p.TotalFlows)
	}
	if got := countFlowCollection(g); got != p.FlowCollectionRows {
		t.Errorf("flow_collection iterated = %d, plan = %d", got, p.FlowCollectionRows)
	}
	if got := countObjects(g); got != p.ObjectsCount {
		t.Errorf("objects iterated = %d, plan = %d", got, p.ObjectsCount)
	}
	if got := countSegments(g); got != p.TotalSegments {
		t.Errorf("segments iterated = %d, plan = %d", got, p.TotalSegments)
	}
}

// TestGenSegmentsNonOverlapPerFlow is the contract the GiST EXCLUDE
// constraint will validate when the loader re-CREATEs it post-COPY.
// If generated segments overlap within a flow, the re-add will fail
// and we won't find out until the full 500M load.
func TestGenSegmentsNonOverlapPerFlow(t *testing.T) {
	p := smallPlan(t)
	g := NewGen(p, 7)

	for flowIdx := range p.TotalFlows {
		depth := g.FlowDepth(flowIdx)
		prevUpper := int64(0)
		for j := range depth {
			s := g.Segment(flowIdx, j)
			if s.LowerNs < prevUpper {
				t.Fatalf("flow %d seg %d overlaps: lower=%d < prevUpper=%d",
					flowIdx, j, s.LowerNs, prevUpper)
			}
			if s.UpperNs <= s.LowerNs {
				t.Fatalf("flow %d seg %d zero/neg range: [%d,%d)",
					flowIdx, j, s.LowerNs, s.UpperNs)
			}
			prevUpper = s.UpperNs
		}
	}
}

// TestGenFlowSourceFKConsistency verifies every Flow's SourceID points
// at a generated source. Loader uses these in COPY order; an FK miss
// would fail the source→flow COPY.
func TestGenFlowSourceFKConsistency(t *testing.T) {
	p := smallPlan(t)
	g := NewGen(p, 11)

	sourceIDs := make(map[uuid.UUID]struct{}, p.SourcesCount)
	for i := range p.SourcesCount {
		sourceIDs[g.Source(i).ID] = struct{}{}
	}
	for i := range p.TotalFlows {
		f := g.Flow(i)
		if _, ok := sourceIDs[f.SourceID]; !ok {
			t.Fatalf("flow %d.SourceID %s not in generated sources", i, f.SourceID)
		}
	}
}

// TestGenObjectIDsUniqueWithinSample spot-checks object id uniqueness
// over a sample (segments require distinct object_ids since each
// segment-row carries its own object reference at avg_refs ≈ 1).
func TestGenObjectIDsUniqueWithinSample(t *testing.T) {
	p := smallPlan(t)
	g := NewGen(p, 3)

	seen := make(map[string]struct{}, 1000)
	n := min(int64(1000), p.ObjectsCount)
	for i := range n {
		id := g.Object(i).ID
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate object id at index %d: %s", i, id)
		}
		seen[id] = struct{}{}
	}
}

// TestPartitionedSegmentIter_DisjointAndComplete is the correctness
// proof for parallel COPY: N workers must between them emit every
// segment exactly once, and no two workers may ever touch the same
// flow_id (otherwise the GiST EXCLUDE — partitioned per flow_id —
// would have correctness implications even though we drop it during
// load).
func TestPartitionedSegmentIter_DisjointAndComplete(t *testing.T) {
	p := smallPlan(t)
	g := NewGen(p, 7)

	const workers = 4
	flowIDsPerWorker := make([]map[uuid.UUID]struct{}, workers)
	rowCountPerWorker := make([]int64, workers)

	for w := range int64(workers) {
		seen := make(map[uuid.UUID]struct{})
		it := g.PartitionedSegments(w, workers)
		for it.Next() {
			seen[it.Row().FlowID] = struct{}{}
			rowCountPerWorker[w]++
		}
		flowIDsPerWorker[w] = seen
	}

	// Disjoint: no flow_id appears in more than one worker.
	owner := make(map[uuid.UUID]int64)
	for w, set := range flowIDsPerWorker {
		for id := range set {
			if prev, dup := owner[id]; dup {
				t.Fatalf("flow_id %s appears in workers %d and %d", id, prev, int64(w))
			}
			owner[id] = int64(w)
		}
	}

	// Complete: total row count across all workers equals the plan.
	var total int64
	for _, c := range rowCountPerWorker {
		total += c
	}
	if total != p.TotalSegments {
		t.Errorf("partitioned total = %d, plan TotalSegments = %d", total, p.TotalSegments)
	}

	// Complete: union of distinct flow_ids equals plan.TotalFlows.
	if int64(len(owner)) != p.TotalFlows {
		t.Errorf("distinct flow_ids = %d, plan TotalFlows = %d", len(owner), p.TotalFlows)
	}
}

// TestPartitionedSegmentIter_SingleWorkerEqualsSequential pins the
// degenerate case: with N=1 the partitioned iterator must emit the
// same row sequence as the plain SegmentIter — so the parallel path
// with --workers=1 reduces to the existing sequential behaviour.
func TestPartitionedSegmentIter_SingleWorkerEqualsSequential(t *testing.T) {
	p := smallPlan(t)
	g := NewGen(p, 3)

	var seq []SegmentRow
	for it := g.Segments(); it.Next(); {
		seq = append(seq, it.Row())
	}

	var par []SegmentRow
	for it := g.PartitionedSegments(0, 1); it.Next(); {
		par = append(par, it.Row())
	}

	if len(seq) != len(par) {
		t.Fatalf("len mismatch: sequential=%d partitioned=%d", len(seq), len(par))
	}
	for i := range seq {
		if seq[i] != par[i] {
			t.Errorf("row %d diverges: seq=%+v par=%+v", i, seq[i], par[i])
		}
	}
}

// TestPartitionedSegmentIter_Deterministic confirms the parallel path
// preserves reproducibility — same seed + same partition → same rows.
// Reruns of the loader must produce byte-identical datasets regardless
// of worker count.
func TestPartitionedSegmentIter_Deterministic(t *testing.T) {
	p := smallPlan(t)

	collect := func(g *Gen, workerID, totalWorkers int64) []SegmentRow {
		var out []SegmentRow
		for it := g.PartitionedSegments(workerID, totalWorkers); it.Next(); {
			out = append(out, it.Row())
		}
		return out
	}

	a := collect(NewGen(p, 42), 2, 4)
	b := collect(NewGen(p, 42), 2, 4)
	if len(a) != len(b) {
		t.Fatalf("len mismatch with same seed: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Errorf("row %d diverges across identical seeds", i)
		}
	}
}

// helpers ---------------------------------------------------------------

func countSources(g *Gen) int64 {
	var n int64
	for it := g.Sources(); it.Next(); {
		_ = it.Row()
		n++
	}
	return n
}
func countFlows(g *Gen) int64 {
	var n int64
	for it := g.Flows(); it.Next(); {
		_ = it.Row()
		n++
	}
	return n
}
func countFlowCollection(g *Gen) int64 {
	var n int64
	for it := g.FlowCollection(); it.Next(); {
		_ = it.Row()
		n++
	}
	return n
}
func countObjects(g *Gen) int64 {
	var n int64
	for it := g.Objects(); it.Next(); {
		_ = it.Row()
		n++
	}
	return n
}
func countSegments(g *Gen) int64 {
	var n int64
	for it := g.Segments(); it.Next(); {
		_ = it.Row()
		n++
	}
	return n
}
