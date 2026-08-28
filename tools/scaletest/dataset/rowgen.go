package dataset

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"

	"github.com/google/uuid"
)

// Row types — kept pgx-free so this package is unit-testable without a
// database. The COPY adapter in dbload.go wraps the iterators below.

// SourceRow is one row of the `sources` table.
type SourceRow struct {
	ID          uuid.UUID
	Format      string
	Label       string
	Description string
}

// FlowRow is one row of the `flows` table. Nullable columns are populated
// with deterministic non-null values for loader simplicity — the loader's
// job is volume, not modelling every nil permutation.
type FlowRow struct {
	ID              uuid.UUID
	SourceID        uuid.UUID
	Format          string
	Codec           string
	Container       string
	Label           string
	SegmentDuration string // rational "n:d" seconds, e.g. "6:1" or "1:1"
	ReadOnly        bool
	Timerange       string // "[0:0_NNNN:0)" aggregate across the flow's segments
}

// FlowCollectionRow groups rendition members under a parent (essence) flow.
type FlowCollectionRow struct {
	FlowID    uuid.UUID // parent
	ItemID    uuid.UUID // member flow id
	SortOrder int32
}

// ObjectRow is one row of the `objects` table.
type ObjectRow struct {
	ID       string
	RefCount int32
	Reaping  bool
}

// SegmentRow is one row of the `segments` table. The `id` (BIGSERIAL),
// `created_at`, and JSONB columns are left to defaults in COPY.
type SegmentRow struct {
	FlowID    uuid.UUID
	ObjectID  string
	Timerange string
	LowerNs   int64
	UpperNs   int64
	TsOffset  string
}

// Gen holds the plan and seed. All getters are pure functions of (plan,
// seed, index): no internal mutable state, so callers can shard and
// parallelise the load without coordination.
type Gen struct {
	plan    Plan
	seed    uint64
	chunkNs int64
}

// NewGen constructs a generator. Determinism is per-seed.
func NewGen(p Plan, seed uint64) *Gen {
	return &Gen{
		plan:    p,
		seed:    seed,
		chunkNs: int64(p.ChunkDurationSec) * 1_000_000_000,
	}
}

// Plan returns the row plan this generator was built from. Exposed so
// consumers in other packages (the loader's COPY orchestration, the
// bench tier's bucket sizing) can read per-table totals without
// reaching into Gen's internals.
func (g *Gen) Plan() Plan { return g.plan }

// Seed returns the RNG seed this generator was built from. Bench and
// loadgen tiers record it in their reports so a run is reproducible.
func (g *Gen) Seed() uint64 { return g.seed }

// FlowDepth is the segments-per-flow count for the flow at index i.
// Deep flows are laid out first (indices [0, DeepFlowCount)), shallow
// after. This makes per-flow indexing arithmetic instead of lookup.
func (g *Gen) FlowDepth(flowIdx int64) int64 {
	if flowIdx < g.plan.DeepFlowCount {
		return g.plan.DeepDepth
	}
	return g.plan.ShallowDepth
}

// Source returns the i-th source row deterministically (0 <= i < SourcesCount).
func (g *Gen) Source(i int64) SourceRow {
	return SourceRow{
		ID:          detUUID(g.seed, "source", i),
		Format:      "urn:x-nmos:format:video",
		Label:       fmt.Sprintf("src-%d", i),
		Description: "scaletest-generated source",
	}
}

// Flow returns the i-th flow (0 <= i < TotalFlows). Flow→source mapping
// is i / RenditionsPerSource; the first flow of each source is its parent.
func (g *Gen) Flow(i int64) FlowRow {
	srcIdx := i / int64(g.plan.RenditionsPerSource)
	depth := g.FlowDepth(i)
	endSec := depth * int64(g.plan.ChunkDurationSec)
	return FlowRow{
		ID:              detUUID(g.seed, "flow", i),
		SourceID:        detUUID(g.seed, "source", srcIdx),
		Format:          "urn:x-nmos:format:video",
		Codec:           "video/h264",
		Container:       "video/mp4",
		Label:           fmt.Sprintf("flow-%d", i),
		SegmentDuration: fmt.Sprintf("%d:1", g.plan.ChunkDurationSec),
		ReadOnly:        false,
		Timerange:       fmt.Sprintf("[0:0_%d:0)", endSec),
	}
}

// Object returns the k-th object (0 <= k < ObjectsCount). k is the global
// segment index — each segment row references object_id[k] via Segment.
func (g *Gen) Object(k int64) ObjectRow {
	return ObjectRow{
		ID:       detObjectID(g.seed, k),
		RefCount: 1, // avg_refs ≈ 1 per docs/scale-test-plan.md §3.1
		Reaping:  false,
	}
}

// Segment returns the j-th segment of flow flowIdx. Ranges are
// non-overlapping by construction within a flow: [j*chunkNs, (j+1)*chunkNs).
func (g *Gen) Segment(flowIdx, segIdx int64) SegmentRow {
	chunk := int64(g.plan.ChunkDurationSec)
	startSec := segIdx * chunk
	endSec := (segIdx + 1) * chunk
	return SegmentRow{
		FlowID:    detUUID(g.seed, "flow", flowIdx),
		ObjectID:  detObjectID(g.seed, g.globalSegIdx(flowIdx, segIdx)),
		Timerange: fmt.Sprintf("[%d:0_%d:0)", startSec, endSec),
		LowerNs:   segIdx * g.chunkNs,
		UpperNs:   (segIdx + 1) * g.chunkNs,
		TsOffset:  "0:0",
	}
}

// globalSegIdx linearises (flowIdx, segIdx) into a single 0-based offset
// across all flows in plan order. Each global index corresponds to one
// object row (1:1 segments↔objects per the plan's avg_refs ≈ 1).
func (g *Gen) globalSegIdx(flowIdx, segIdx int64) int64 {
	if flowIdx < g.plan.DeepFlowCount {
		return flowIdx*g.plan.DeepDepth + segIdx
	}
	deepTotal := g.plan.DeepFlowCount * g.plan.DeepDepth
	shallowFlowOffset := flowIdx - g.plan.DeepFlowCount
	return deepTotal + shallowFlowOffset*g.plan.ShallowDepth + segIdx
}

// Iterators -------------------------------------------------------------
//
// Each iterator is a simple index walker; callers use the standard
// for it := g.Xxx(); it.Next(); { _ = it.Row() } pattern. The dbload
// COPY adapter wraps these to satisfy pgx.CopyFromSource.

type SourceIter struct {
	g   *Gen
	i   int64
	cur SourceRow
}

func (g *Gen) Sources() *SourceIter { return &SourceIter{g: g, i: -1} }
func (it *SourceIter) Next() bool {
	it.i++
	if it.i >= it.g.plan.SourcesCount {
		return false
	}
	it.cur = it.g.Source(it.i)
	return true
}
func (it *SourceIter) Row() SourceRow { return it.cur }

type FlowIter struct {
	g   *Gen
	i   int64
	cur FlowRow
}

func (g *Gen) Flows() *FlowIter { return &FlowIter{g: g, i: -1} }
func (it *FlowIter) Next() bool {
	it.i++
	if it.i >= it.g.plan.TotalFlows {
		return false
	}
	it.cur = it.g.Flow(it.i)
	return true
}
func (it *FlowIter) Row() FlowRow { return it.cur }

// FlowCollectionIter emits one row per (parent, member) pair within each
// source, skipping the parent itself. Iteration walks source-by-source.
type FlowCollectionIter struct {
	g          *Gen
	srcIdx     int64 // current source
	memberPos  int64 // 1..(flowsInSource-1); 0 is the parent, no row emitted
	cur        FlowCollectionRow
	rendsPer   int64
	totalFlows int64
}

func (g *Gen) FlowCollection() *FlowCollectionIter {
	return &FlowCollectionIter{
		g:          g,
		srcIdx:     0,
		memberPos:  0, // first Next advances to 1
		rendsPer:   int64(g.plan.RenditionsPerSource),
		totalFlows: g.plan.TotalFlows,
	}
}

func (it *FlowCollectionIter) Next() bool {
	for it.srcIdx < it.g.plan.SourcesCount {
		flowsInSrc := it.rendsPer
		if remain := it.totalFlows - it.srcIdx*it.rendsPer; remain < flowsInSrc {
			flowsInSrc = remain
		}
		it.memberPos++
		if it.memberPos < flowsInSrc {
			parent := it.srcIdx * it.rendsPer
			member := parent + it.memberPos
			it.cur = FlowCollectionRow{
				FlowID:    detUUID(it.g.seed, "flow", parent),
				ItemID:    detUUID(it.g.seed, "flow", member),
				SortOrder: int32(it.memberPos), //nolint:gosec // memberPos bounded by rendsPer (≤ a few thousand in practice)
			}
			return true
		}
		// exhausted this source; move on
		it.srcIdx++
		it.memberPos = 0
	}
	return false
}
func (it *FlowCollectionIter) Row() FlowCollectionRow { return it.cur }

type ObjectIter struct {
	g   *Gen
	i   int64
	cur ObjectRow
}

func (g *Gen) Objects() *ObjectIter { return &ObjectIter{g: g, i: -1} }
func (it *ObjectIter) Next() bool {
	it.i++
	if it.i >= it.g.plan.ObjectsCount {
		return false
	}
	it.cur = it.g.Object(it.i)
	return true
}
func (it *ObjectIter) Row() ObjectRow { return it.cur }

// SegmentIter walks every flow's segments in turn.
type SegmentIter struct {
	g       *Gen
	flowIdx int64
	segIdx  int64
	depth   int64
	cur     SegmentRow
}

func (g *Gen) Segments() *SegmentIter {
	return &SegmentIter{g: g, flowIdx: 0, segIdx: -1, depth: 0}
}

func (it *SegmentIter) Next() bool {
	for it.flowIdx < it.g.plan.TotalFlows {
		if it.segIdx == -1 {
			it.depth = it.g.FlowDepth(it.flowIdx)
		}
		it.segIdx++
		if it.segIdx < it.depth {
			it.cur = it.g.Segment(it.flowIdx, it.segIdx)
			return true
		}
		it.flowIdx++
		it.segIdx = -1
	}
	return false
}
func (it *SegmentIter) Row() SegmentRow { return it.cur }

// PartitionedSegmentIter emits every segment for the flows owned by one
// worker in a hash-by-flow-index partitioning scheme: worker w of N
// owns flow indices {w, w+N, w+2N, ...}. Union of N workers covers
// every flow exactly once, with no two workers ever touching the same
// flow_id — which is what makes parallel COPY into `segments` safe
// (the GiST EXCLUDE constraint is per-flow_id).
type PartitionedSegmentIter struct {
	g            *Gen
	totalWorkers int64
	flowIdx      int64
	segIdx       int64
	depth        int64
	cur          SegmentRow
}

// PartitionedSegments returns the iterator for one worker. Callers run
// N concurrent goroutines, each with a fresh iterator. workerID must
// be in [0, totalWorkers) and totalWorkers must be >= 1; out-of-range
// values produce an empty iterator (Next() returns false immediately)
// rather than panicking — easier to compose with shutdown logic.
func (g *Gen) PartitionedSegments(workerID, totalWorkers int64) *PartitionedSegmentIter {
	if totalWorkers < 1 {
		totalWorkers = 1
	}
	if workerID < 0 || workerID >= totalWorkers {
		// Start past the end so Next() yields nothing.
		workerID = g.plan.TotalFlows
	}
	return &PartitionedSegmentIter{
		g:            g,
		totalWorkers: totalWorkers,
		flowIdx:      workerID,
		segIdx:       -1,
	}
}

func (it *PartitionedSegmentIter) Next() bool {
	for it.flowIdx < it.g.plan.TotalFlows {
		if it.segIdx == -1 {
			it.depth = it.g.FlowDepth(it.flowIdx)
		}
		it.segIdx++
		if it.segIdx < it.depth {
			it.cur = it.g.Segment(it.flowIdx, it.segIdx)
			return true
		}
		it.flowIdx += it.totalWorkers
		it.segIdx = -1
	}
	return false
}
func (it *PartitionedSegmentIter) Row() SegmentRow { return it.cur }

// Deterministic ID helpers ---------------------------------------------

// detUUID derives a UUIDv4 deterministically from (seed, domain, idx).
// SHA-256 ensures collision resistance across domains so the same idx
// in different domains never collides.
func detUUID(seed uint64, domain string, idx int64) uuid.UUID {
	h := sha256.New()
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], seed)
	_, _ = h.Write(buf[:])
	_, _ = h.Write([]byte(domain))
	binary.BigEndian.PutUint64(buf[:], uint64(idx)) //nolint:gosec // idx ≥ 0 by construction; reinterpret bytes only.
	_, _ = h.Write(buf[:])
	var u uuid.UUID
	copy(u[:], h.Sum(nil)[:16])
	// RFC 4122: set version (v4) and variant.
	u[6] = (u[6] & 0x0f) | 0x40
	u[8] = (u[8] & 0x3f) | 0x80
	return u
}

// detObjectID returns the deterministic TEXT object id "o-<hex32>".
// 128 bits of entropy keep the birthday-collision probability negligible
// at 500M objects (~10^−22). `objects.id` is a PRIMARY KEY: an 8-byte
// (64-bit) prefix yields ~0.7% collision probability at 500M, which
// would abort the load with a unique-violation. 16 bytes is the
// minimum safe width.
func detObjectID(seed uint64, idx int64) string {
	h := sha256.New()
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], seed)
	_, _ = h.Write(buf[:])
	_, _ = h.Write([]byte("object"))
	binary.BigEndian.PutUint64(buf[:], uint64(idx)) //nolint:gosec // idx ≥ 0 by construction.
	_, _ = h.Write(buf[:])
	sum := h.Sum(nil)
	return fmt.Sprintf("o-%x", sum[:16])
}
