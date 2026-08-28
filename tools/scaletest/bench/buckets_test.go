package main

import "testing"

// TestAppendRegionsDisjoint is the regression guard for the Option-B
// fix: the two deep-flow append buckets (insert-deep, register-bulk)
// must never write the same segment index within a single run, so a
// full multi-bucket run needs only one snapshot restore, not one per
// bucket.
//
// It simulates both buckets' per-call segment indices for a generous
// sample count on the same deep flow and asserts the index sets do not
// intersect.
func TestAppendRegionsDisjoint(t *testing.T) {
	const (
		depth   = int64(150_000) // a deep flow's loaded extent
		samples = 50_000         // far above any realistic latency-bench N
	)

	used := make(map[int64]string)

	// insert-deep: one index per call.
	deepBase := appendRegionBase(depth, regionInsertDeep)
	for i := int64(0); i < samples; i++ {
		used[deepBase+i] = "insert-deep"
	}

	// register-bulk: benchBulkSize contiguous indices per call.
	bulkBase := appendRegionBase(depth, regionRegisterBulk)
	for i := int64(0); i < samples; i++ {
		start := bulkBase + i*benchBulkSize
		for j := int64(0); j < benchBulkSize; j++ {
			idx := start + j
			if who, dup := used[idx]; dup {
				t.Fatalf("segIdx %d written by both %q and register-bulk", idx, who)
			}
			used[idx] = "register-bulk"
		}
	}
}

// TestAppendRegionsBelowOverflow checks the highest index any append
// bucket reaches stays well under the int64-nanosecond overflow bound
// (segIdx * chunkNs must fit in int64; chunkNs = 6e9 for 6s chunks).
func TestAppendRegionsBelowOverflow(t *testing.T) {
	const (
		depth   = int64(150_000)
		samples = int64(2_000_000) // the documented safe ceiling
		chunkNs = int64(6_000_000_000)
	)
	maxIdx := appendRegionBase(depth, regionRegisterBulk) + samples*benchBulkSize
	// maxIdx * chunkNs must not overflow int64 (max ~9.2e18).
	const int64Max = int64(9_223_372_036_854_775_807)
	if maxIdx > int64Max/chunkNs {
		t.Fatalf("max segIdx %d * chunkNs overflows int64", maxIdx)
	}
}
