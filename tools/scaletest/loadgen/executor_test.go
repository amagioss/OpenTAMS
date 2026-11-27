package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/amagioss/opentams/tools/scaletest/dataset"
	"github.com/amagioss/opentams/tools/scaletest/report"
)

func TestIdemSlotSharedKeys(t *testing.T) {
	// keys=1 → every call collapses to slot 0 (max contention on one
	// idempotency_keys row).
	for i := int64(0); i < 100; i++ {
		if got := idemSlot(i, 1); got != 0 {
			t.Fatalf("idemSlot(%d, 1) = %d, want 0", i, got)
		}
	}
	// keys=4 → calls spread over exactly 4 slots, deterministically.
	if idemSlot(5, 4) != 1 || idemSlot(8, 4) != 0 || idemSlot(7, 4) != 3 {
		t.Errorf("idemSlot mod-4 wrong: %d %d %d", idemSlot(5, 4), idemSlot(8, 4), idemSlot(7, 4))
	}
	// keys <= 0 is treated as 1 (no divide-by-zero).
	if idemSlot(9, 0) != 0 {
		t.Errorf("idemSlot(9, 0) = %d, want 0", idemSlot(9, 0))
	}
	// Large key space → effectively unique (slot == i), restoring the
	// default non-contended behaviour.
	if idemSlot(42, 1_000_000) != 42 {
		t.Errorf("idemSlot(42, 1e6) = %d, want 42", idemSlot(42, 1_000_000))
	}
}

// loggedReq is one request the scripted server received, in arrival order.
type loggedReq struct {
	method string
	path   string
	body   []byte
}

// scriptedServer records every request in order. It answers POST
// …/storage with storageStatus and POST …/segments with segmentsStatus
// and segmentsBody.
type scriptedServer struct {
	mu   sync.Mutex
	reqs []loggedReq
}

func newScriptedServer(t *testing.T, storageStatus, segmentsStatus int, segmentsBody string) (*httptest.Server, *scriptedServer) {
	t.Helper()
	s := &scriptedServer{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.reqs = append(s.reqs, loggedReq{method: r.Method, path: r.URL.Path, body: body})
		s.mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/storage"):
			w.WriteHeader(storageStatus)
		case strings.HasSuffix(r.URL.Path, "/segments"):
			if segmentsBody != "" {
				w.Header().Set("Content-Type", "application/json")
			}
			w.WriteHeader(segmentsStatus)
			_, _ = io.WriteString(w, segmentsBody)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, s
}

func (s *scriptedServer) requests() []loggedReq {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]loggedReq(nil), s.reqs...)
}

func executorFor(t *testing.T, srvURL, profile string, bulkSize, idemKeys int) (*executor, *collector) {
	t.Helper()
	plan := smallLoadPlan(t)
	m, err := newMix(profile)
	if err != nil {
		t.Fatalf("newMix: %v", err)
	}
	coll := newCollector(bucketSpecsFor(m))
	client := newClient(srvURL, "tok", 5*time.Second, 16)
	return newExecutor(client, dataset.NewGen(plan, 42), plan, m, coll, 42, bulkSize, 5, idemKeys), coll
}

func bucketsByName(coll *collector) map[string]report.BucketResult {
	by := map[string]report.BucketResult{}
	for _, r := range coll.results(time.Second) {
		by[r.Name] = r
	}
	return by
}

func storageObjectIDs(t *testing.T, r loggedReq) []string {
	t.Helper()
	var body struct {
		ObjectIDs []string `json:"object_ids"`
		Limit     *int     `json:"limit"`
	}
	if err := json.Unmarshal(r.body, &body); err != nil {
		t.Fatalf("storage body not JSON: %v (%s)", err, r.body)
	}
	if body.Limit != nil {
		t.Errorf("allocation before registration used limit mode: %s", r.body)
	}
	return body.ObjectIDs
}

func segmentObjectIDs(t *testing.T, r loggedReq) []string {
	t.Helper()
	var segs []segmentPost
	if err := json.Unmarshal(r.body, &segs); err != nil {
		t.Fatalf("segments body not a JSON array: %v (%s)", err, r.body)
	}
	ids := make([]string, len(segs))
	for i, s := range segs {
		ids[i] = s.ObjectID
	}
	return ids
}

// TestExecutorAllocatesObjectIDsBeforeEachRegistration covers BR-GC-12
// (a controlled segment needs an allocation row) for scale case S1. Each
// registration op first allocates exactly its own object IDs on the same
// flow, and the allocation lands in its own bucket.
func TestExecutorAllocatesObjectIDsBeforeEachRegistration(t *testing.T) {
	cases := []struct {
		name        string
		run         func(x *executor) bool
		allocBucket string
		regBucket   string
	}{
		{"insert-deep", func(x *executor) bool { return x.doInsertDeep(context.Background(), true) }, bktInsertDeepAlloc, bktInsertDeep},
		{"insert-shallow", func(x *executor) bool { return x.doInsertShallow(context.Background(), true) }, bktInsertShallowAlloc, bktInsertShallow},
		{"register-bulk", func(x *executor) bool { return x.doRegisterBulk(context.Background(), true) }, bktRegisterBulkAlloc, bktRegisterBulk},
		{"idempotent-retry", func(x *executor) bool { return x.doIdempotent(context.Background(), true) }, bktIdempotentAlloc, bktIdempotentFirst},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, log := newScriptedServer(t, http.StatusCreated, http.StatusCreated, "")
			x, coll := executorFor(t, srv.URL, "steady", 3, 1000)

			if !tc.run(x) {
				t.Fatal("op reported no response")
			}

			reqs := log.requests()
			if len(reqs) < 2 {
				t.Fatalf("got %d requests, want allocation + registration", len(reqs))
			}
			alloc, reg := reqs[0], reqs[1]
			if alloc.method != http.MethodPost || !strings.HasSuffix(alloc.path, "/storage") {
				t.Fatalf("first request = %s %s, want POST …/storage", alloc.method, alloc.path)
			}
			if reg.method != http.MethodPost || !strings.HasSuffix(reg.path, "/segments") {
				t.Fatalf("second request = %s %s, want POST …/segments", reg.method, reg.path)
			}
			if strings.TrimSuffix(alloc.path, "/storage") != strings.TrimSuffix(reg.path, "/segments") {
				t.Errorf("allocation flow %s differs from registration flow %s", alloc.path, reg.path)
			}
			allocIDs, regIDs := storageObjectIDs(t, alloc), segmentObjectIDs(t, reg)
			if strings.Join(allocIDs, ",") != strings.Join(regIDs, ",") {
				t.Errorf("allocated %v, registered %v", allocIDs, regIDs)
			}

			by := bucketsByName(coll)
			if got := by[tc.allocBucket]; got.Samples != 1 || got.Errors != 0 {
				t.Errorf("%s = %d samples / %d errors, want 1/0", tc.allocBucket, got.Samples, got.Errors)
			}
			if got := by[tc.regBucket]; got.Samples != 1 || got.Errors != 0 {
				t.Errorf("%s = %d samples / %d errors, want 1/0", tc.regBucket, got.Samples, got.Errors)
			}
		})
	}
}

// TestExecutorSplitsBulkAllocationAtServerMaximum: POST …/storage takes
// at most 100 object_ids, so a larger bulk registration allocates in
// several calls that together cover every ID once.
func TestExecutorSplitsBulkAllocationAtServerMaximum(t *testing.T) {
	srv, log := newScriptedServer(t, http.StatusCreated, http.StatusCreated, "")
	x, coll := executorFor(t, srv.URL, "steady", 250, 1000)

	if !x.doRegisterBulk(context.Background(), true) {
		t.Fatal("op reported no response")
	}

	reqs := log.requests()
	var allocated []string
	var allocCalls int
	for _, r := range reqs {
		if !strings.HasSuffix(r.path, "/storage") {
			continue
		}
		ids := storageObjectIDs(t, r)
		if len(ids) > 100 {
			t.Errorf("allocation call carries %d object_ids, max 100", len(ids))
		}
		allocCalls++
		allocated = append(allocated, ids...)
	}
	last := reqs[len(reqs)-1]
	if !strings.HasSuffix(last.path, "/segments") {
		t.Fatalf("last request = %s, want the registration after all allocations", last.path)
	}
	if regIDs := segmentObjectIDs(t, last); strings.Join(allocated, ",") != strings.Join(regIDs, ",") {
		t.Errorf("allocated %d IDs, registered %d; lists differ", len(allocated), len(regIDs))
	}
	if allocCalls != 3 {
		t.Errorf("allocation calls = %d, want 3 for 250 IDs", allocCalls)
	}
	if got := bucketsByName(coll)[bktRegisterBulkAlloc]; got.Samples != 3 {
		t.Errorf("%s samples = %d, want 3", bktRegisterBulkAlloc, got.Samples)
	}
}

// TestExecutorCountsFailedSegmentsAsRegistrationError: a 200 with
// failed_segments (for example object-not-allocated, BR-GC-71) is a
// failed registration, not a latency sample.
func TestExecutorCountsFailedSegmentsAsRegistrationError(t *testing.T) {
	const failed = `{"failed_segments":[{"object_id":"x","timerange":"[0:0_6:0)",` +
		`"error":{"type":"https://github.com/amagioss/opentams/problems/object-not-allocated","title":"Object Not Allocated"}}]}`
	srv, _ := newScriptedServer(t, http.StatusCreated, http.StatusOK, failed)
	x, coll := executorFor(t, srv.URL, "steady", 3, 1000)

	if !x.doInsertDeep(context.Background(), true) {
		t.Fatal("a 200 response is still a response; op must report completed")
	}

	by := bucketsByName(coll)
	if got := by[bktInsertDeep]; got.Errors != 1 || got.Samples != 0 {
		t.Errorf("%s = %d samples / %d errors, want 0/1", bktInsertDeep, got.Samples, got.Errors)
	}
	if got := by[bktInsertDeepAlloc]; got.Samples != 1 {
		t.Errorf("%s samples = %d, want 1", bktInsertDeepAlloc, got.Samples)
	}
}

// TestExecutorSkipsRegistrationWhenAllocationFails: without an
// allocation the registration is bound to fail (BR-GC-12 row 6), so the
// executor does not send it and counts it as a registration error.
func TestExecutorSkipsRegistrationWhenAllocationFails(t *testing.T) {
	srv, log := newScriptedServer(t, http.StatusBadRequest, http.StatusCreated, "")
	x, coll := executorFor(t, srv.URL, "steady", 3, 1000)

	x.doInsertShallow(context.Background(), true)

	for _, r := range log.requests() {
		if strings.HasSuffix(r.path, "/segments") {
			t.Errorf("registration sent after a failed allocation")
		}
	}
	by := bucketsByName(coll)
	if got := by[bktInsertShallowAlloc]; got.Errors != 1 {
		t.Errorf("%s errors = %d, want 1", bktInsertShallowAlloc, got.Errors)
	}
	if got := by[bktInsertShallow]; got.Errors != 1 || got.Samples != 0 {
		t.Errorf("%s = %d samples / %d errors, want 0/1", bktInsertShallow, got.Samples, got.Errors)
	}
}

// TestExecutorIdempotentAllocatesOnlyOnFirstUseOfSlot: with a shared key
// pool a repeated slot replays a registration whose object already
// exists. Allocating its ID again would be rejected (BR-GC-03 row 2,
// object-exists), so only the first use of a slot allocates.
func TestExecutorIdempotentAllocatesOnlyOnFirstUseOfSlot(t *testing.T) {
	srv, log := newScriptedServer(t, http.StatusCreated, http.StatusCreated, "")
	x, coll := executorFor(t, srv.URL, "idem-contention", 3, 1)

	for range 3 {
		x.doIdempotent(context.Background(), true)
	}

	var allocCalls, regCalls int
	for _, r := range log.requests() {
		switch {
		case strings.HasSuffix(r.path, "/storage"):
			allocCalls++
		case strings.HasSuffix(r.path, "/segments"):
			regCalls++
		}
	}
	if allocCalls != 1 {
		t.Errorf("allocation calls = %d, want 1 (slot 0 used three times)", allocCalls)
	}
	if regCalls != 6 {
		t.Errorf("registration calls = %d, want 6 (first + dedupe per op)", regCalls)
	}
	if got := bucketsByName(coll)[bktIdempotentAlloc]; got.Samples != 1 || got.Errors != 0 {
		t.Errorf("%s = %d samples / %d errors, want 1/0", bktIdempotentAlloc, got.Samples, got.Errors)
	}
}

// TestBucketSpecsRegisterAllocationBuckets: every registration op
// pre-registers its allocation bucket as a write, so the report shows it.
func TestBucketSpecsRegisterAllocationBuckets(t *testing.T) {
	m, err := newMix("steady")
	if err != nil {
		t.Fatalf("newMix: %v", err)
	}
	ops := map[string]report.Op{}
	for _, s := range bucketSpecsFor(m) {
		ops[s.name] = s.op
	}
	for _, name := range []string{bktRegisterBulkAlloc, bktInsertDeepAlloc, bktInsertShallowAlloc, bktIdempotentAlloc} {
		op, has := ops[name]
		if !has {
			t.Errorf("steady mix missing bucket %q", name)
			continue
		}
		if op != report.OpWrite {
			t.Errorf("%s op = %s, want write", name, op)
		}
	}
}
