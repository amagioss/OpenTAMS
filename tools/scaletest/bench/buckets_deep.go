package main

import (
	"context"
	"errors"
	"time"

	"github.com/amagioss/opentams/internal/metastore"
	"github.com/amagioss/opentams/tools/scaletest/report"
)

// deepBuckets are the wider API-scale scenarios that target deep flows.
// They are opt-in (not in the default run) because page-deep-flow walks
// many pages and cascade-delete-deep is heavily destructive — each call
// removes an entire deep flow and cascades over its ~150k segments.
func deepBuckets() []bucketDef {
	return []bucketDef{
		{
			name: "page-deep-flow", op: report.OpRead,
			desc: "Cursor-paginate a deep flow for up to --samples pages; per-page latency vs depth",
			run:  runPageDeepFlow,
		},
		{
			name: "cascade-delete-deep", op: report.OpDelete,
			desc: "DELETE a whole deep flow; times the FK cascade over its segments (destructive)",
			run:  runCascadeDeleteDeep,
		},
	}
}

// runPageDeepFlow walks one deep flow page-by-page via the cursor,
// recording each page request's latency. Deep pages exercise the cursor
// seek cost as the offset grows. It walks up to e.samples pages or until
// the flow is exhausted, whichever comes first. Read-only.
func runPageDeepFlow(ctx context.Context, e *benchEnv) (report.BucketResult, error) {
	if _, err := e.deepDepth(); err != nil {
		return report.BucketResult{}, err
	}
	flow := e.gen.Flow(0).ID // flow 0 is a deep flow

	lats := make([]time.Duration, 0, e.samples)
	var errCount int
	cursor := ""
	start := time.Now()
	for page := 0; page < e.samples; page++ {
		if ctx.Err() != nil {
			break
		}
		t0 := time.Now()
		res, err := e.store.ListSegments(ctx, metastore.ListQuery{
			FlowID: flow, Limit: 100, Page: cursor,
		})
		d := time.Since(t0)
		if err != nil {
			errCount++
			break
		}
		lats = append(lats, d)
		if res.NextCursor == "" || len(res.Items) == 0 {
			break // walked to the end
		}
		cursor = res.NextCursor
	}
	return report.NewBucketResult("page-deep-flow", report.OpRead, lats, errCount, time.Since(start)), nil
}

// runCascadeDeleteDeep deletes whole deep flows via raw SQL, timing the
// FK cascade that removes each flow's segments. It deletes up to
// min(e.samples, DeepFlowCount) distinct deep flows. This measures the
// database cascade cost specifically; it does NOT decrement objects'
// ref_count (that is the service-layer delete path) — orphaned objects
// are left for the GC worker. Destructive: restore the snapshot after.
func runCascadeDeleteDeep(ctx context.Context, e *benchEnv) (report.BucketResult, error) {
	if e.plan.DeepFlowCount == 0 {
		return report.BucketResult{}, errors.New("dataset has no deep flows")
	}
	if e.pool == nil {
		return report.BucketResult{}, errors.New("cascade-delete-deep requires a raw pool")
	}
	n := min(int64(e.samples), e.plan.DeepFlowCount)

	lats := make([]time.Duration, 0, n)
	var errCount int
	start := time.Now()
	for i := range n {
		if ctx.Err() != nil {
			break
		}
		flowID := e.gen.Flow(i).ID
		t0 := time.Now()
		_, err := e.pool.Exec(ctx, `DELETE FROM flows WHERE id = $1`, flowID)
		d := time.Since(t0)
		if err != nil {
			errCount++
			continue
		}
		lats = append(lats, d)
	}
	return report.NewBucketResult("cascade-delete-deep", report.OpDelete, lats, errCount, time.Since(start)), nil
}
