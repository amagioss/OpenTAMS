package main

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/amagioss/opentams/tools/scaletest/dataset"
)

// Disjoint append regions, mirroring the bench tier: each write op
// that appends new segments owns a stride-separated band beyond the
// loaded extent, so concurrent inserts/register/idempotent writes never
// collide on (flow_id, timerange). Per-op counters (not a global seq)
// keep each band's index growth proportional to that op's own call
// count, bounding the highest segIdx well under the int64-ns limit.
const (
	lgRegionStride     int64 = 200_000_000
	lgRegInsertDeep    int64 = 0
	lgRegRegisterBulk  int64 = 1
	lgRegIdempotent    int64 = 2
	lgRegInsertShallow int64 = 0 // shallow flows; disjoint from deep bands by flow_id
	lgListWindow       int64 = 100
)

func lgAppendBase(depth, region int64) int64 { return depth + region*lgRegionStride }

// executor turns a picked op into HTTP call(s) and records the result(s)
// into the collector. It addresses the loaded dataset by replaying the
// generator (same preset+seed as the loader), deriving fresh per-op
// indices from per-op atomic counters so concurrent workers never
// collide.
type executor struct {
	client       *Client
	gen          *dataset.Gen
	plan         dataset.Plan
	mix          *mix
	coll         *collector
	bulkSize     int
	storageLimit int
	idemKeys     int // size of the shared idempotency-key pool (1 = max contention)

	rngMu sync.Mutex
	rng   *rand.Rand
	seqs  [numOpKinds]atomic.Int64 // per-opKind counters; lock-free on the hot path
}

func newExecutor(c *Client, g *dataset.Gen, p dataset.Plan, m *mix, coll *collector, seed uint64, bulkSize, storageLimit, idemKeys int) *executor {
	return &executor{
		client:       c,
		gen:          g,
		plan:         p,
		mix:          m,
		coll:         coll,
		bulkSize:     bulkSize,
		storageLimit: storageLimit,
		idemKeys:     idemKeys,
		rng:          rand.New(rand.NewSource(int64(seed))), //nolint:gosec // workload selection, not security.
	}
}

// idemSlot maps a call index onto the shared idempotency-key pool. With
// keys=1 every call collapses to slot 0 (all requests collide on one
// idempotency_keys row → max lock contention); with a large key space
// slots are effectively unique (the default, non-contended behaviour).
func idemSlot(i int64, keys int) int64 {
	return i % int64(max(keys, 1))
}

// useCollector swaps the collector latencies are recorded into. Safe to
// call only between engine runs (no in-flight requests) — the ramp uses
// it to give each fixed-rate step its own fresh per-bucket accumulation.
func (x *executor) useCollector(c *collector) { x.coll = c }

func (x *executor) pick() opKind {
	x.rngMu.Lock()
	defer x.rngMu.Unlock()
	return x.mix.pick(x.rng)
}

// next returns the next 0-based index for an op's own counter.
func (x *executor) next(k opKind) int64 {
	return x.seqs[k].Add(1) - 1
}

func (x *executor) deepFlow(i int64) uuid.UUID {
	return x.gen.Flow(i % max(x.plan.DeepFlowCount, 1)).ID
}

func (x *executor) shallowFlow(i int64) uuid.UUID {
	idx := x.plan.DeepFlowCount + i%max(x.plan.ShallowFlowCount, 1)
	return x.gen.Flow(idx).ID
}

func trOne(segIdx, chunk int64) string {
	return fmt.Sprintf("[%d:0_%d:0)", segIdx*chunk, (segIdx+1)*chunk)
}

func trWindow(startSeg, window, chunk int64) string {
	return fmt.Sprintf("[%d:0_%d:0)", startSeg*chunk, (startSeg+window)*chunk)
}

func ok(status int, err error) bool {
	return err == nil && status >= 200 && status < 300
}

// runOne picks one operation and executes it, returning whether all of
// its HTTP call(s) received a response (no transport error) — the engine
// uses this for the completed counter. Latencies are recorded only when
// measuring (warmup calls still hit the server to prime it).
func (x *executor) runOne(ctx context.Context, measuring bool) bool {
	switch x.pick() {
	case opListByFlow:
		return x.doListByFlow(ctx, measuring)
	case opListByTimerange:
		return x.doListByTimerange(ctx, measuring)
	case opRegisterBulk:
		return x.doRegisterBulk(ctx, measuring)
	case opInsertDeep:
		return x.doInsertDeep(ctx, measuring)
	case opInsertShallow:
		return x.doInsertShallow(ctx, measuring)
	case opCreateStorage:
		return x.doCreateStorage(ctx, measuring)
	case opIdempotentRetry:
		return x.doIdempotent(ctx, measuring)
	case opDeleteByTimerange:
		return x.doDeleteByTimerange(ctx, measuring)
	case opDeleteByObject:
		return x.doDeleteByObject(ctx, measuring)
	default:
		return false
	}
}

// timed runs an HTTP call, times it, records to the named bucket when
// measuring, and reports whether the call received a response (err ==
// nil) — distinct from HTTP success (2xx), which decides the bucket
// sample-vs-error split.
func (x *executor) timed(ctx context.Context, measuring bool, bucket string, call func(context.Context) (int, error)) bool {
	t0 := time.Now()
	status, err := call(ctx)
	lat := time.Since(t0)
	if measuring {
		x.coll.record(bucket, lat, ok(status, err))
	}
	return err == nil
}

func (x *executor) chunk() int64 { return int64(x.plan.ChunkDurationSec) }

func (x *executor) doListByFlow(ctx context.Context, measuring bool) bool {
	flow := x.deepFlow(x.next(opListByFlow))
	return x.timed(ctx, measuring, bktListByFlow, func(ctx context.Context) (int, error) {
		return x.client.listSegments(ctx, flow, "", 100)
	})
}

func (x *executor) doListByTimerange(ctx context.Context, measuring bool) bool {
	i := x.next(opListByTimerange)
	flow := x.deepFlow(i)
	start := (i * lgListWindow) % max(x.plan.DeepDepth-lgListWindow, 1)
	tr := trWindow(start, lgListWindow, x.chunk())
	return x.timed(ctx, measuring, bktListByTimerange, func(ctx context.Context) (int, error) {
		return x.client.listSegments(ctx, flow, tr, 100)
	})
}

func (x *executor) doInsertDeep(ctx context.Context, measuring bool) bool {
	i := x.next(opInsertDeep)
	flow := x.deepFlow(i)
	segIdx := lgAppendBase(x.plan.DeepDepth, lgRegInsertDeep) + i
	seg := segmentPost{ObjectID: fmt.Sprintf("lg-deep-%d", i), Timerange: trOne(segIdx, x.chunk()), TSOffset: "0:0"}
	return x.timed(ctx, measuring, bktInsertDeep, func(ctx context.Context) (int, error) {
		return x.client.registerSegments(ctx, flow, []segmentPost{seg}, fmt.Sprintf("lg-deep-k-%d", i))
	})
}

func (x *executor) doInsertShallow(ctx context.Context, measuring bool) bool {
	i := x.next(opInsertShallow)
	flow := x.shallowFlow(i)
	segIdx := lgAppendBase(x.plan.ShallowDepth, lgRegInsertShallow) + i
	seg := segmentPost{ObjectID: fmt.Sprintf("lg-shallow-%d", i), Timerange: trOne(segIdx, x.chunk()), TSOffset: "0:0"}
	return x.timed(ctx, measuring, bktInsertShallow, func(ctx context.Context) (int, error) {
		return x.client.registerSegments(ctx, flow, []segmentPost{seg}, fmt.Sprintf("lg-shallow-k-%d", i))
	})
}

func (x *executor) doRegisterBulk(ctx context.Context, measuring bool) bool {
	i := x.next(opRegisterBulk)
	flow := x.deepFlow(i)
	base := lgAppendBase(x.plan.DeepDepth, lgRegRegisterBulk) + i*int64(x.bulkSize)
	segs := make([]segmentPost, x.bulkSize)
	for j := range x.bulkSize {
		segs[j] = segmentPost{
			ObjectID:  fmt.Sprintf("lg-bulk-%d-%d", i, j),
			Timerange: trOne(base+int64(j), x.chunk()),
			TSOffset:  "0:0",
		}
	}
	return x.timed(ctx, measuring, bktRegisterBulk, func(ctx context.Context) (int, error) {
		return x.client.registerSegments(ctx, flow, segs, fmt.Sprintf("lg-bulk-k-%d", i))
	})
}

func (x *executor) doCreateStorage(ctx context.Context, measuring bool) bool {
	flow := x.deepFlow(x.next(opCreateStorage))
	return x.timed(ctx, measuring, bktCreateStorage, func(ctx context.Context) (int, error) {
		return x.client.allocateStorage(ctx, flow, x.storageLimit)
	})
}

// doIdempotent issues the first POST then an immediate replay with the
// same key+body, recording the two into separate buckets so the
// dedupe-path cost is visible distinct from the initial write.
func (x *executor) doIdempotent(ctx context.Context, measuring bool) bool {
	i := x.next(opIdempotentRetry)
	// Flow, body, and key all derive from the shared slot so that calls
	// landing on the same slot are genuine duplicates (same request) —
	// they contend on one idempotency_keys row. With the default large
	// key pool, slot == i and each call is unique.
	slot := idemSlot(i, x.idemKeys)
	flow := x.deepFlow(slot)
	segIdx := lgAppendBase(x.plan.DeepDepth, lgRegIdempotent) + slot
	seg := segmentPost{ObjectID: fmt.Sprintf("lg-idem-%d", slot), Timerange: trOne(segIdx, x.chunk()), TSOffset: "0:0"}
	key := fmt.Sprintf("lg-idem-k-%d", slot)
	body := []segmentPost{seg}
	first := x.timed(ctx, measuring, bktIdempotentFirst, func(ctx context.Context) (int, error) {
		return x.client.registerSegments(ctx, flow, body, key)
	})
	dedupe := x.timed(ctx, measuring, bktIdempotentDedupe, func(ctx context.Context) (int, error) {
		return x.client.registerSegments(ctx, flow, body, key)
	})
	// The op "completed" only if both the first POST and the dedupe replay
	// got a response.
	return first && dedupe
}

func (x *executor) doDeleteByTimerange(ctx context.Context, measuring bool) bool {
	i := x.next(opDeleteByTimerange)
	flow := x.deepFlow(i)
	const win = 10
	start := (i * win) % max(x.plan.DeepDepth-win, 1)
	tr := trWindow(start, win, x.chunk())
	return x.timed(ctx, measuring, bktDeleteByTimerange, func(ctx context.Context) (int, error) {
		return x.client.deleteSegments(ctx, flow, tr, "")
	})
}

func (x *executor) doDeleteByObject(ctx context.Context, measuring bool) bool {
	i := x.next(opDeleteByObject)
	fi := i % max(x.plan.DeepFlowCount, 1)
	flow := x.gen.Flow(fi).ID
	// Top-down within the loaded extent, disjoint from delete-by-timerange.
	segIdx := x.plan.DeepDepth - 1 - i%x.plan.DeepDepth
	seg := x.gen.Segment(fi, segIdx)
	return x.timed(ctx, measuring, bktDeleteByObject, func(ctx context.Context) (int, error) {
		return x.client.deleteSegments(ctx, flow, seg.Timerange, seg.ObjectID)
	})
}
