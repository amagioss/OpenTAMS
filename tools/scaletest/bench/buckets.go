package main

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/amagioss/opentams/internal/metastore"
	"github.com/amagioss/opentams/tools/scaletest/dataset"
	"github.com/amagioss/opentams/tools/scaletest/report"
)

// benchEnv is the shared state every bucket run receives: the metastore
// under test, the deterministic generator replaying the loaded dataset
// (same preset + seed used by the loader), and the sample/warmup counts.
//
// Buckets derive flow_ids / object_ids from gen — never by querying the
// DB — so a bucket can address any of the loaded ~1M flows or ~500M
// segments by index with zero lookup overhead.
type benchEnv struct {
	store   *metastore.PostgresStore
	pool    *pgxpool.Pool // for buckets that need raw SQL (cascade-delete)
	gen     *dataset.Gen
	plan    dataset.Plan
	samples int
	warmup  int
}

// bucketDef is one measurable workload. run executes the measurement
// loop (via measure) and returns the serializable result. Errors from
// run are setup/fatal errors (e.g. could not reach the DB); per-call
// failures are counted inside the BucketResult, not returned here.
type bucketDef struct {
	name string
	op   report.Op
	desc string
	run  func(ctx context.Context, e *benchEnv) (report.BucketResult, error)
}

// defaultBuckets is the safe set run when no --bucket is given: the D3
// segment buckets (read/append/overlap, plus bounded deletes). It
// excludes the opt-in deep-flow extras, which are either slow (page-deep-flow walks
// thousands of pages) or heavily destructive (cascade-delete-deep removes
// whole flows) and so must be opted into explicitly.
func defaultBuckets() []bucketDef {
	return segmentBuckets()
}

// allBuckets is every selectable bucket (defaults + deep-flow extras), used for
// list-buckets display and for resolving explicit --bucket names.
func allBuckets() []bucketDef {
	return append(segmentBuckets(), deepBuckets()...)
}

// registry is the full, listable set.
func registry() []bucketDef { return allBuckets() }

// findBuckets returns the buckets whose names appear in `names`, in
// registry order. An empty `names` selects the default (safe) set — the
// deep-flow extras must be named explicitly. Unknown names are returned so the
// caller can report them.
func findBuckets(names []string) (selected []bucketDef, unknown []string) {
	if len(names) == 0 {
		return defaultBuckets(), nil
	}
	all := allBuckets()
	want := make(map[string]struct{}, len(names))
	for _, n := range names {
		want[n] = struct{}{}
	}
	for _, b := range all {
		if _, ok := want[b.name]; ok {
			selected = append(selected, b)
			delete(want, b.name)
		}
	}
	for n := range want {
		unknown = append(unknown, n)
	}
	return selected, unknown
}
