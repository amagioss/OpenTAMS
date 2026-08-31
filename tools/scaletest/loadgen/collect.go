package main

import (
	"sync"
	"time"

	"github.com/amagioss/opentams/tools/scaletest/report"
)

// Bucket names. Most match the mix op names; idempotent-retry is the one
// op that records into two buckets (the first POST and the dedupe POST).
const (
	bktListByFlow        = "list-by-flow"
	bktListByTimerange   = "list-by-timerange"
	bktRegisterBulk      = "register-bulk"
	bktInsertDeep        = "insert-deep"
	bktInsertShallow     = "insert-shallow"
	bktCreateStorage     = "create-storage-endpoint"
	bktIdempotentFirst   = "idempotent-first"
	bktIdempotentDedupe  = "idempotent-dedupe"
	bktDeleteByTimerange = "delete-by-timerange"
	bktDeleteByObject    = "delete-by-object"
)

// bucketSpec is a report bucket the collector tracks: a name and its
// operation class.
type bucketSpec struct {
	name string
	op   report.Op
}

// bucketsForOp maps a mix op to the report bucket(s) it records into.
// Idempotent-retry is the only one-to-many mapping.
func bucketsForOp(k opKind) []bucketSpec {
	switch k {
	case opListByFlow:
		return []bucketSpec{{bktListByFlow, report.OpRead}}
	case opListByTimerange:
		return []bucketSpec{{bktListByTimerange, report.OpRead}}
	case opRegisterBulk:
		return []bucketSpec{{bktRegisterBulk, report.OpWrite}}
	case opInsertDeep:
		return []bucketSpec{{bktInsertDeep, report.OpWrite}}
	case opInsertShallow:
		return []bucketSpec{{bktInsertShallow, report.OpWrite}}
	case opCreateStorage:
		return []bucketSpec{{bktCreateStorage, report.OpWrite}}
	case opIdempotentRetry:
		return []bucketSpec{
			{bktIdempotentFirst, report.OpWrite},
			{bktIdempotentDedupe, report.OpWrite},
		}
	case opDeleteByTimerange:
		return []bucketSpec{{bktDeleteByTimerange, report.OpDelete}}
	case opDeleteByObject:
		return []bucketSpec{{bktDeleteByObject, report.OpDelete}}
	default:
		return nil
	}
}

// bucketSpecsFor returns the buckets to pre-register for a mix, in mix
// order, expanding one-to-many ops (idempotent-retry).
func bucketSpecsFor(m *mix) []bucketSpec {
	var specs []bucketSpec
	for _, op := range m.ops {
		specs = append(specs, bucketsForOp(op.kind)...)
	}
	return specs
}

// bucketAcc accumulates one bucket's samples under its own lock so
// concurrent engine workers don't contend on a single global lock.
type bucketAcc struct {
	spec bucketSpec
	mu   sync.Mutex
	lat  []time.Duration
	errs int
}

// collector gathers per-bucket latencies/errors during a run. Buckets
// are pre-registered (the mix is known up front), so the map is
// read-only during the run and only per-bucket locks are taken.
type collector struct {
	m     map[string]*bucketAcc
	order []string
}

func newCollector(specs []bucketSpec) *collector {
	c := &collector{m: make(map[string]*bucketAcc, len(specs))}
	for _, s := range specs {
		if _, exists := c.m[s.name]; exists {
			continue
		}
		c.m[s.name] = &bucketAcc{spec: s}
		c.order = append(c.order, s.name)
	}
	return c
}

// record adds one call's outcome to its bucket: a latency sample on
// success, an error tally otherwise. Unknown bucket names are ignored
// (defensive — the executor only records pre-registered buckets).
func (c *collector) record(name string, lat time.Duration, ok bool) {
	acc := c.m[name]
	if acc == nil {
		return
	}
	acc.mu.Lock()
	if ok {
		acc.lat = append(acc.lat, lat)
	} else {
		acc.errs++
	}
	acc.mu.Unlock()
}

// results builds the per-bucket report. In the concurrent mixed run all
// buckets span the same measurement window, so each bucket's elapsed is
// measureDur (throughput = successes per second of the run).
func (c *collector) results(measureDur time.Duration) []report.BucketResult {
	out := make([]report.BucketResult, 0, len(c.order))
	for _, name := range c.order {
		acc := c.m[name]
		acc.mu.Lock()
		out = append(out, report.NewBucketResult(acc.spec.name, acc.spec.op, acc.lat, acc.errs, measureDur))
		acc.mu.Unlock()
	}
	return out
}
