package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/amagioss/opentams/internal/domain"
	"github.com/amagioss/opentams/internal/metastore"
	"github.com/amagioss/opentams/internal/timerange"
	"github.com/amagioss/opentams/tools/scaletest/report"
)

// Tunables for the multi-segment buckets. Kept modest so a bucket's
// total dataset mutation stays bounded (see destructiveness note below).
const (
	benchControlledStorageID = "default" // stamped on controlled inserts
	benchBulkSize            = 100       // segments per register-bulk batch
	benchDeleteWindow        = 10        // segments removed per delete-by-timerange call

	// benchRegionStride separates the append regions of the write buckets
	// so a single multi-bucket run is collision-free: each append-style
	// bucket owns a stride-wide band of segment indices beyond the loaded
	// extent (see appendRegionBase), so insert-deep and register-bulk
	// never write the same (flow_id, timerange) within one run.
	//
	// Disjointness holds while a bucket's per-run consumption stays below
	// one stride: insert-deep uses `samples` indices, register-bulk uses
	// `samples * benchBulkSize`. So a run is safe up to
	// samples ≈ benchRegionStride/benchBulkSize = 2,000,000 — far beyond
	// what a latency bench needs. The stride also keeps the highest
	// segIdx well under the int64-ns overflow bound (segIdx * chunkNs):
	// region 1 tops out near 2e8 → lower_ns ≈ 1.2e18 ≪ 9.2e18.
	benchRegionStride int64 = 200_000_000

	regionInsertDeep    int64 = 0 // deep-flow append band
	regionRegisterBulk  int64 = 1 // deep-flow append band, disjoint from insert-deep
	regionInsertShallow int64 = 0 // shallow-flow band; disjoint from deep bands by flow_id
)

// appendRegionBase is the first segment index of a write bucket's append
// region: beyond the flow's loaded extent (`depth`), offset into a
// stride-separated band by `region`. Per-call offsets are added by the
// caller within [0, benchRegionStride).
func appendRegionBase(depth, region int64) int64 {
	return depth + region*benchRegionStride
}

// segmentBuckets returns the metastore-tier (DB-direct) workload buckets.
//
// Tier boundary: these drive metastore methods straight through pgx. The
// idempotency-retry and create-storage-endpoint buckets from the plan
// live ABOVE the metastore (idempotency_keys + HTTP handlers) and belong
// to the D4 HTTP tier, not here.
//
// Destructiveness: read buckets (list-*) and overlap-rejection do NOT
// mutate the dataset. insert-* and register-bulk APPEND new rows beyond
// each target flow's loaded extent; delete-* REMOVE loaded rows. The
// append buckets write into disjoint segment-index regions (see
// benchRegionStride) so they never collide WITHIN a single run, and
// buckets run reads → writes → deletes so reads always see the pristine
// loaded state. Mutation is still bounded by --samples and drifts the
// dataset across runs — restore the snapshot between measured runs, per
// docs/scale-test-plan.md §7 (load-once → snapshot → restore-per-run).
func segmentBuckets() []bucketDef {
	return []bucketDef{
		{
			name: "list-by-flow", op: report.OpRead,
			desc: "ListSegments first page (Limit=100) on a deep flow",
			run:  runListByFlow,
		},
		{
			name: "list-by-timerange", op: report.OpRead,
			desc: "ListSegments with a timerange window on a deep flow",
			run:  runListByTimerange,
		},
		{
			name: "insert-deep", op: report.OpWrite,
			desc: "InsertSegments single row appended to a deep (150k-seg) flow",
			run:  runInsertDeep,
		},
		{
			name: "insert-shallow", op: report.OpWrite,
			desc: "InsertSegments single row appended to a shallow (50-seg) flow",
			run:  runInsertShallow,
		},
		{
			name: "register-bulk", op: report.OpWrite,
			desc: fmt.Sprintf("InsertSegments batch of %d appended to a deep flow", benchBulkSize),
			run:  runRegisterBulk,
		},
		{
			name: "overlap-rejection", op: report.OpWrite,
			desc: "InsertSegments that overlaps an existing row (expects rejection)",
			run:  runOverlapRejection,
		},
		{
			name: "delete-by-timerange", op: report.OpDelete,
			desc: fmt.Sprintf("DeleteSegmentsByTimerange of a %d-seg window on a deep flow", benchDeleteWindow),
			run:  runDeleteByTimerange,
		},
		{
			name: "delete-by-object", op: report.OpDelete,
			desc: "DeleteSegmentsByTimerange filtered to one object_id on a deep flow",
			run:  runDeleteByObject,
		},
	}
}

// deepDepth validates the dataset has deep flows and returns their loaded
// segment depth. Buckets compute the per-call flow index themselves
// (i % DeepFlowCount) to rotate across deep flows.
func (e *benchEnv) deepDepth() (int64, error) {
	if e.plan.DeepFlowCount == 0 {
		return 0, errors.New("dataset has no deep flows (DeepFlowCount=0)")
	}
	return e.plan.DeepDepth, nil
}

// shallowDepth is the shallow-flow counterpart of deepDepth. Buckets
// rotate via DeepFlowCount + i % ShallowFlowCount.
func (e *benchEnv) shallowDepth() (int64, error) {
	if e.plan.ShallowFlowCount == 0 {
		return 0, errors.New("dataset has no shallow flows (ShallowFlowCount=0)")
	}
	return e.plan.ShallowDepth, nil
}

// segmentAt seconds [s, e) of one chunk at the given absolute segment
// index, as a parsed TimeRange. Mirrors the generator's chunk layout
// (segIdx*chunk .. (segIdx+1)*chunk seconds).
func (e *benchEnv) chunkRange(segIdx int64) (timerange.TimeRange, error) {
	chunk := int64(e.plan.ChunkDurationSec)
	return timerange.Parse(fmt.Sprintf("[%d:0_%d:0)", segIdx*chunk, (segIdx+1)*chunk))
}

// appendInsert builds an InsertBatch that appends one controlled segment
// at segIdx (which the caller chooses beyond the flow's loaded extent so
// it cannot overlap), with a bench-unique object_id that cannot collide
// with any loaded object.
func (e *benchEnv) appendInsert(flowIdx, segIdx int64, objectID string) (metastore.InsertBatch, error) {
	tr, err := e.chunkRange(segIdx)
	if err != nil {
		return metastore.InsertBatch{}, err
	}
	return metastore.InsertBatch{
		FlowID:              e.gen.Flow(flowIdx).ID,
		ControlledStorageID: benchControlledStorageID,
		Segments:            []domain.Segment{{ObjectID: objectID, Timerange: tr}},
	}, nil
}

func runInsertDeep(ctx context.Context, e *benchEnv) (report.BucketResult, error) {
	depth, err := e.deepDepth()
	if err != nil {
		return report.BucketResult{}, err
	}
	base := appendRegionBase(depth, regionInsertDeep)
	m := measure(ctx, e.samples, e.warmup, func(ctx context.Context, i int) error {
		fi := int64(i) % e.plan.DeepFlowCount
		batch, berr := e.appendInsert(fi, base+int64(i), fmt.Sprintf("bench-ins-deep-%d", i))
		if berr != nil {
			return berr
		}
		_, ierr := e.store.InsertSegments(ctx, batch)
		return ierr
	})
	return report.NewBucketResult("insert-deep", report.OpWrite, m.latencies, m.errors, m.elapsed), nil
}

func runInsertShallow(ctx context.Context, e *benchEnv) (report.BucketResult, error) {
	depth, err := e.shallowDepth()
	if err != nil {
		return report.BucketResult{}, err
	}
	base := appendRegionBase(depth, regionInsertShallow)
	m := measure(ctx, e.samples, e.warmup, func(ctx context.Context, i int) error {
		fi := e.plan.DeepFlowCount + int64(i)%e.plan.ShallowFlowCount
		batch, berr := e.appendInsert(fi, base+int64(i), fmt.Sprintf("bench-ins-shallow-%d", i))
		if berr != nil {
			return berr
		}
		_, ierr := e.store.InsertSegments(ctx, batch)
		return ierr
	})
	return report.NewBucketResult("insert-shallow", report.OpWrite, m.latencies, m.errors, m.elapsed), nil
}

func runRegisterBulk(ctx context.Context, e *benchEnv) (report.BucketResult, error) {
	depth, err := e.deepDepth()
	if err != nil {
		return report.BucketResult{}, err
	}
	region := appendRegionBase(depth, regionRegisterBulk)
	m := measure(ctx, e.samples, e.warmup, func(ctx context.Context, i int) error {
		fi := int64(i) % e.plan.DeepFlowCount
		base := region + int64(i)*benchBulkSize
		segs := make([]domain.Segment, benchBulkSize)
		for j := range benchBulkSize {
			tr, terr := e.chunkRange(base + int64(j))
			if terr != nil {
				return terr
			}
			segs[j] = domain.Segment{ObjectID: fmt.Sprintf("bench-bulk-%d-%d", i, j), Timerange: tr}
		}
		_, ierr := e.store.InsertSegments(ctx, metastore.InsertBatch{
			FlowID:              e.gen.Flow(fi).ID,
			ControlledStorageID: benchControlledStorageID,
			Segments:            segs,
		})
		return ierr
	})
	return report.NewBucketResult("register-bulk", report.OpWrite, m.latencies, m.errors, m.elapsed), nil
}

func runOverlapRejection(ctx context.Context, e *benchEnv) (report.BucketResult, error) {
	depth, err := e.deepDepth()
	if err != nil {
		return report.BucketResult{}, err
	}
	m := measure(ctx, e.samples, e.warmup, func(ctx context.Context, i int) error {
		fi := int64(i) % e.plan.DeepFlowCount
		// Overlap an EXISTING loaded segment within [0, depth).
		existing := int64(i) % depth
		tr, terr := e.chunkRange(existing)
		if terr != nil {
			return terr
		}
		_, ierr := e.store.InsertSegments(ctx, metastore.InsertBatch{
			FlowID:              e.gen.Flow(fi).ID,
			ControlledStorageID: benchControlledStorageID,
			Segments:            []domain.Segment{{ObjectID: fmt.Sprintf("bench-ovl-%d", i), Timerange: tr}},
		})
		// The rejection IS the measured path: ErrSegmentOverlap is success.
		if errors.Is(ierr, metastore.ErrSegmentOverlap) {
			return nil
		}
		if ierr != nil {
			return ierr
		}
		return fmt.Errorf("expected overlap rejection on flow %d seg %d, insert succeeded", fi, existing)
	})
	return report.NewBucketResult("overlap-rejection", report.OpWrite, m.latencies, m.errors, m.elapsed), nil
}

func runListByFlow(ctx context.Context, e *benchEnv) (report.BucketResult, error) {
	if _, err := e.deepDepth(); err != nil {
		return report.BucketResult{}, err
	}
	m := measure(ctx, e.samples, e.warmup, func(ctx context.Context, i int) error {
		fi := int64(i) % e.plan.DeepFlowCount
		_, lerr := e.store.ListSegments(ctx, metastore.ListQuery{
			FlowID: e.gen.Flow(fi).ID,
			Limit:  100,
		})
		return lerr
	})
	return report.NewBucketResult("list-by-flow", report.OpRead, m.latencies, m.errors, m.elapsed), nil
}

func runListByTimerange(ctx context.Context, e *benchEnv) (report.BucketResult, error) {
	depth, err := e.deepDepth()
	if err != nil {
		return report.BucketResult{}, err
	}
	m := measure(ctx, e.samples, e.warmup, func(ctx context.Context, i int) error {
		fi := int64(i) % e.plan.DeepFlowCount
		// A 100-chunk window at a rotating offset inside the loaded extent.
		const window = 100
		start := (int64(i) * window) % max(depth-window, 1)
		tr, terr := windowRange(e, start, window)
		if terr != nil {
			return terr
		}
		_, lerr := e.store.ListSegments(ctx, metastore.ListQuery{
			FlowID:    e.gen.Flow(fi).ID,
			Timerange: &tr,
			Limit:     100,
		})
		return lerr
	})
	return report.NewBucketResult("list-by-timerange", report.OpRead, m.latencies, m.errors, m.elapsed), nil
}

func runDeleteByTimerange(ctx context.Context, e *benchEnv) (report.BucketResult, error) {
	depth, err := e.deepDepth()
	if err != nil {
		return report.BucketResult{}, err
	}
	m := measure(ctx, e.samples, e.warmup, func(ctx context.Context, i int) error {
		fi := int64(i) % e.plan.DeepFlowCount
		// Distinct window per call so repeated deletes hit fresh rows.
		start := (int64(i) * benchDeleteWindow) % max(depth-benchDeleteWindow, 1)
		tr, terr := windowRange(e, start, benchDeleteWindow)
		if terr != nil {
			return terr
		}
		_, derr := e.store.DeleteSegmentsByTimerange(ctx, metastore.DeleteQuery{
			FlowID:    e.gen.Flow(fi).ID,
			Timerange: tr,
		})
		return derr
	})
	return report.NewBucketResult("delete-by-timerange", report.OpDelete, m.latencies, m.errors, m.elapsed), nil
}

func runDeleteByObject(ctx context.Context, e *benchEnv) (report.BucketResult, error) {
	depth, err := e.deepDepth()
	if err != nil {
		return report.BucketResult{}, err
	}
	m := measure(ctx, e.samples, e.warmup, func(ctx context.Context, i int) error {
		fi := int64(i) % e.plan.DeepFlowCount
		// Address loaded rows from the TOP of the extent, counting down,
		// so this bucket targets a region disjoint from
		// delete-by-timerange's bottom-up windows. In a full run that
		// bucket runs first; without this, the rows here would already be
		// gone and we'd measure no-op (0-row) deletes. Disjoint while
		// samples*(deleteWindow+1) < depth (~13k samples at 6s depth).
		segIdx := depth - 1 - int64(i)%depth
		seg := e.gen.Segment(fi, segIdx) // exact loaded row → real object_id + timerange
		tr, terr := timerange.Parse(seg.Timerange)
		if terr != nil {
			return terr
		}
		oid := seg.ObjectID
		_, derr := e.store.DeleteSegmentsByTimerange(ctx, metastore.DeleteQuery{
			FlowID:    e.gen.Flow(fi).ID,
			Timerange: tr,
			ObjectID:  &oid,
		})
		return derr
	})
	return report.NewBucketResult("delete-by-object", report.OpDelete, m.latencies, m.errors, m.elapsed), nil
}

// windowRange builds a [start, start+window)-chunk TimeRange in seconds.
func windowRange(e *benchEnv, startSeg, window int64) (timerange.TimeRange, error) {
	chunk := int64(e.plan.ChunkDurationSec)
	return timerange.Parse(fmt.Sprintf("[%d:0_%d:0)", startSeg*chunk, (startSeg+window)*chunk))
}
