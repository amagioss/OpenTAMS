package main

import (
	"sync"
	"testing"
	"time"

	"github.com/amagioss/opentams/tools/scaletest/report"
)

func TestCollectorRecordsPerBucketConcurrent(t *testing.T) {
	specs := []bucketSpec{
		{name: "list-by-flow", op: report.OpRead},
		{name: "insert-deep", op: report.OpWrite},
	}
	c := newCollector(specs)

	var wg sync.WaitGroup
	// 100 reads (all success) + 50 writes (40 success, 10 error), concurrently.
	for range 100 {
		wg.Add(1)
		go func() { defer wg.Done(); c.record("list-by-flow", time.Millisecond, true) }()
	}
	for i := range 50 {
		wg.Add(1)
		go func(i int) { defer wg.Done(); c.record("insert-deep", 2*time.Millisecond, i >= 10) }(i)
	}
	wg.Wait()

	res := c.results(time.Second)
	by := map[string]report.BucketResult{}
	for _, r := range res {
		by[r.Name] = r
	}
	if by["list-by-flow"].Samples != 100 || by["list-by-flow"].Errors != 0 {
		t.Errorf("list-by-flow = %+v, want 100 samples / 0 errors", by["list-by-flow"])
	}
	if by["insert-deep"].Samples != 40 || by["insert-deep"].Errors != 10 {
		t.Errorf("insert-deep = %+v, want 40 samples / 10 errors", by["insert-deep"])
	}
	// Order is preserved (registration order).
	if res[0].Name != "list-by-flow" || res[1].Name != "insert-deep" {
		t.Errorf("result order = %s,%s; want registration order", res[0].Name, res[1].Name)
	}
}

func TestBucketSpecsExpandIdempotentSteady(t *testing.T) {
	m, err := newMix("steady")
	if err != nil {
		t.Fatalf("newMix: %v", err)
	}
	specs := bucketSpecsFor(m)
	names := map[string]report.Op{}
	for _, s := range specs {
		names[s.name] = s.op
	}
	if _, ok := names["idempotent-retry"]; ok {
		t.Error("idempotent-retry should expand into first/dedupe, not appear directly")
	}
	if _, ok := names["idempotent-first"]; !ok {
		t.Error("missing idempotent-first bucket")
	}
	if _, ok := names["idempotent-dedupe"]; !ok {
		t.Error("missing idempotent-dedupe bucket")
	}
	for _, del := range []string{"delete-by-timerange", "delete-by-object"} {
		if _, ok := names[del]; ok {
			t.Errorf("steady profile must not register delete bucket %q", del)
		}
	}
}

func TestBucketSpecsDestructiveHasDeletes(t *testing.T) {
	m, err := newMix("destructive")
	if err != nil {
		t.Fatalf("newMix: %v", err)
	}
	specs := bucketSpecsFor(m)
	var hasDel bool
	for _, s := range specs {
		if s.name == "delete-by-timerange" || s.name == "delete-by-object" {
			hasDel = true
			if s.op != report.OpDelete {
				t.Errorf("%s op = %s, want delete", s.name, s.op)
			}
		}
	}
	if !hasDel {
		t.Error("destructive specs missing delete buckets")
	}
}
