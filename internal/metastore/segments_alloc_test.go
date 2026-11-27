//go:build integration || perf

package metastore_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/amagioss/opentams/internal/apperror"
	"github.com/amagioss/opentams/internal/domain"
	"github.com/amagioss/opentams/internal/metastore"
)

// Allocation enforcement at segment registration (BR-GC-10 to BR-GC-17).

// startAllocTest empties the object, allocation, segment, and flow tables
// before and after the test, so that the fixed object IDs of the test
// plan cannot meet rows of other tests.
func startAllocTest(t *testing.T) {
	t.Helper()
	metastore.ResetGCTables(t)
	t.Cleanup(func() { metastore.ResetGCTables(t) })
}

func byosSegment(t *testing.T, objectID, tr string) domain.Segment {
	t.Helper()
	seg := makeSegment(t, objectID, tr)
	seg.GetURLs = []domain.GetURL{{URL: "https://cdn.example/x", Label: "cdn"}}
	return seg
}

func insertControlled(t *testing.T, store *metastore.PostgresStore, flowID uuid.UUID, segs ...domain.Segment) metastore.InsertResult {
	t.Helper()
	res, err := store.InsertSegments(context.Background(), metastore.InsertBatch{
		FlowID: flowID, Segments: segs, ControlledStorageID: "default",
	})
	if err != nil {
		t.Fatalf("InsertSegments: %v", err)
	}
	return res
}

func rowExists(t *testing.T, table, id string) bool {
	t.Helper()
	return queryBool(t, `SELECT EXISTS (SELECT 1 FROM `+table+` WHERE id = $1)`, id)
}

func derefOrNull(s *string) string {
	if s == nil {
		return "NULL"
	}
	return *s
}

func objectSegmentCount(t *testing.T, objectID string) int64 {
	t.Helper()
	return queryInt(t, `SELECT COUNT(*) FROM segments WHERE object_id = $1`, objectID)
}

type allocationRow struct {
	flowID    uuid.UUID
	storageID string
	expiresAt time.Time
	deleting  bool
}

func readAllocation(t *testing.T, id string) allocationRow {
	t.Helper()
	var r allocationRow
	if err := metastore.SharedTestPool().QueryRow(context.Background(),
		`SELECT flow_id, storage_id, expires_at, deleting FROM allocations WHERE id = $1`, id,
	).Scan(&r.flowID, &r.storageID, &r.expiresAt, &r.deleting); err != nil {
		t.Fatalf("read allocation %q: %v", id, err)
	}
	return r
}

func requireRejected(t *testing.T, res metastore.InsertResult, code apperror.ErrorCode, objectID string, indices ...int) {
	t.Helper()
	if !equalInts(res.RejectedIndices, indices) {
		t.Fatalf("RejectedIndices = %v, want %v", res.RejectedIndices, indices)
	}
	if len(res.RejectReasons) != len(indices) {
		t.Fatalf("RejectReasons = %v, want %d entries", res.RejectReasons, len(indices))
	}
	for i, r := range res.RejectReasons {
		if r.Code != code {
			t.Errorf("RejectReasons[%d].Code = %q, want %q", i, r.Code, code)
		}
		if !strings.Contains(r.Detail, objectID) {
			t.Errorf("RejectReasons[%d].Detail = %q, want it to name %q", i, r.Detail, objectID)
		}
	}
}

// TC-GC-020: A controlled ID with no allocation is rejected, and the other segments continue
func Test_TC_GC_020_UnallocatedControlledIDIsRejectedAndOthersContinue(t *testing.T) {
	startAllocTest(t)
	flowF := seedFlow(t, false)
	metastore.SeedAllocation(t, flowF, "a1")

	res := insertControlled(t, newStore(t), flowF,
		makeSegment(t, "a1", "[0:0_2:0)"), makeSegment(t, "zz", "[2:0_4:0)"))

	if !equalInts(res.AcceptedIndices, []int{0}) {
		t.Errorf("AcceptedIndices = %v, want [0]", res.AcceptedIndices)
	}
	requireRejected(t, res, apperror.ErrObjectNotAllocated, "zz", 1)
	if got := objectSegmentCount(t, "a1"); got != 1 {
		t.Errorf("segments on a1 = %d, want 1", got)
	}
	if got := objectSegmentCount(t, "zz"); got != 0 {
		t.Errorf("segments on zz = %d, want 0", got)
	}
	if rowExists(t, "objects", "zz") {
		t.Error("object row zz exists, want none")
	}
}

// TC-GC-021: Registration uses the allocation
func Test_TC_GC_021_RegistrationConsumesTheAllocation(t *testing.T) {
	startAllocTest(t)
	flowF := seedFlow(t, false)
	metastore.SeedAllocation(t, flowF, "a1")

	res := insertControlled(t, newStore(t), flowF, makeSegment(t, "a1", "[0:0_2:0)"))

	if !equalInts(res.AcceptedIndices, []int{0}) || len(res.RejectedIndices) != 0 {
		t.Fatalf("result = %+v, want a1 accepted", res)
	}
	if got := queryInt(t, `SELECT ref_count FROM objects WHERE id = 'a1'`); got != 1 {
		t.Errorf("ref_count = %d, want 1", got)
	}
	if sid := queryStrPtr(t, `SELECT storage_id FROM objects WHERE id = 'a1'`); sid == nil || *sid != "default" {
		t.Errorf("storage_id = %v, want default", derefOrNull(sid))
	}
	if queryBool(t, `SELECT deleting FROM objects WHERE id = 'a1'`) {
		t.Error("deleting = true, want false")
	}
	if rowExists(t, "allocations", "a1") {
		t.Error("allocation a1 still exists, want it deleted")
	}
}

// TC-GC-022: The object row gets storage_id from the allocation
func Test_TC_GC_022_ObjectStorageIDComesFromTheAllocation(t *testing.T) {
	startAllocTest(t)
	flowF := seedFlow(t, false)
	exec(t, `INSERT INTO allocations (id, flow_id, storage_id, expires_at)
		VALUES ('a1', $1, 'store-b', now() + interval '1 hour')`, flowF)

	insertControlled(t, newStore(t), flowF, makeSegment(t, "a1", "[0:0_2:0)"))

	sid := queryStrPtr(t, `SELECT storage_id FROM objects WHERE id = 'a1'`)
	if sid == nil {
		t.Fatal("storage_id = NULL, want store-b")
	}
	if *sid != "store-b" {
		t.Errorf("storage_id = %q, want store-b", *sid)
	}
}

// TC-GC-023: A late registration is accepted
func Test_TC_GC_023_ExpiredButUnclaimedAllocationIsAccepted(t *testing.T) {
	startAllocTest(t)
	flowF := seedFlow(t, false)
	exec(t, `INSERT INTO allocations (id, flow_id, storage_id, expires_at)
		VALUES ('a1', $1, 'default', now() - interval '2 hours')`, flowF)

	res := insertControlled(t, newStore(t), flowF, makeSegment(t, "a1", "[0:0_2:0)"))

	if !equalInts(res.AcceptedIndices, []int{0}) {
		t.Fatalf("AcceptedIndices = %v, want [0]", res.AcceptedIndices)
	}
	if got := queryInt(t, `SELECT ref_count FROM objects WHERE id = 'a1'`); got != 1 {
		t.Errorf("ref_count = %d, want 1", got)
	}
	if rowExists(t, "allocations", "a1") {
		t.Error("allocation a1 still exists, want it deleted")
	}
}

// TC-GC-024: Two segments with the same ID give ref_count = 2
func Test_TC_GC_024_TwoSegmentsOnOneAllocationCountTwice(t *testing.T) {
	startAllocTest(t)
	flowF := seedFlow(t, false)
	metastore.SeedAllocation(t, flowF, "a1")

	res := insertControlled(t, newStore(t), flowF,
		makeSegment(t, "a1", "[0:0_2:0)"), makeSegment(t, "a1", "[2:0_4:0)"))

	if !equalInts(res.AcceptedIndices, []int{0, 1}) {
		t.Fatalf("AcceptedIndices = %v, want [0 1]", res.AcceptedIndices)
	}
	if got := queryInt(t, `SELECT ref_count FROM objects WHERE id = 'a1'`); got != 2 {
		t.Errorf("ref_count = %d, want 2", got)
	}
	if rowExists(t, "allocations", "a1") {
		t.Error("allocation a1 still exists, want it deleted")
	}
}

// TC-GC-025: Reuse on a different flow needs no allocation
func Test_TC_GC_025_ReuseOnAnotherFlowNeedsNoAllocation(t *testing.T) {
	startAllocTest(t)
	flowF := seedFlow(t, false)
	flowG := seedFlow(t, false)
	metastore.SeedAllocation(t, flowF, "a1")
	store := newStore(t)
	insertControlled(t, store, flowF, makeSegment(t, "a1", "[0:0_2:0)"))

	res := insertControlled(t, store, flowG, makeSegment(t, "a1", "[0:0_2:0)"))

	if !equalInts(res.AcceptedIndices, []int{0}) {
		t.Fatalf("AcceptedIndices = %v, want [0]", res.AcceptedIndices)
	}
	if got := queryInt(t, `SELECT ref_count FROM objects WHERE id = 'a1'`); got != 2 {
		t.Errorf("ref_count = %d, want 2", got)
	}
}

// TC-GC-026: A claimed object gives object-deleting
func Test_TC_GC_026_ClaimedObjectIsRejectedAsDeleting(t *testing.T) {
	startAllocTest(t)
	flowF := seedFlow(t, false)
	exec(t, `INSERT INTO objects (id, ref_count, storage_id, deleting, claimed_at)
		VALUES ('a1', 0, 'default', true, now())`)

	res := insertControlled(t, newStore(t), flowF, makeSegment(t, "a1", "[0:0_2:0)"))

	requireRejected(t, res, apperror.ErrObjectDeleting, "a1", 0)
	if got := queryInt(t, `SELECT ref_count FROM objects WHERE id = 'a1'`); got != 0 {
		t.Errorf("ref_count = %d, want 0", got)
	}
	if !queryBool(t, `SELECT deleting FROM objects WHERE id = 'a1'`) {
		t.Error("deleting = false, want true")
	}
	if got := objectSegmentCount(t, "a1"); got != 0 {
		t.Errorf("segments on a1 = %d, want 0", got)
	}
}

// TC-GC-028: A claimed allocation gives object-deleting
func Test_TC_GC_028_ClaimedAllocationIsRejectedAsDeleting(t *testing.T) {
	startAllocTest(t)
	flowF := seedFlow(t, false)
	exec(t, `INSERT INTO allocations (id, flow_id, storage_id, expires_at, deleting, claimed_at)
		VALUES ('a1', $1, 'default', now() + interval '1 hour', true, now())`, flowF)
	before := readAllocation(t, "a1")

	res := insertControlled(t, newStore(t), flowF, makeSegment(t, "a1", "[0:0_2:0)"))

	requireRejected(t, res, apperror.ErrObjectDeleting, "a1", 0)
	if after := readAllocation(t, "a1"); after != before {
		t.Errorf("allocation changed: before %+v, after %+v", before, after)
	}
	if rowExists(t, "objects", "a1") {
		t.Error("object row a1 exists, want none")
	}
}

// TC-GC-029: An allocation for a different flow gives object-wrong-flow
func Test_TC_GC_029_AllocationOfAnotherFlowIsRejectedAsWrongFlow(t *testing.T) {
	startAllocTest(t)
	flowF := seedFlow(t, false)
	flowG := seedFlow(t, false)
	metastore.SeedAllocation(t, flowG, "a1")
	before := readAllocation(t, "a1")

	res := insertControlled(t, newStore(t), flowF, makeSegment(t, "a1", "[0:0_2:0)"))

	requireRejected(t, res, apperror.ErrObjectWrongFlow, "a1", 0)
	if strings.Contains(res.RejectReasons[0].Detail, flowG.String()) {
		t.Errorf("detail %q names the other flow", res.RejectReasons[0].Detail)
	}
	if after := readAllocation(t, "a1"); after != before {
		t.Errorf("allocation changed: before %+v, after %+v", before, after)
	}
}

// TC-GC-030: A comeback before the claim is accepted
func Test_TC_GC_030_ComebackOnReleasedObjectIsAccepted(t *testing.T) {
	startAllocTest(t)
	flowF := seedFlow(t, false)
	exec(t, `INSERT INTO objects (id, ref_count, storage_id) VALUES ('a1', 0, 'default')`)

	res := insertControlled(t, newStore(t), flowF, makeSegment(t, "a1", "[0:0_2:0)"))

	if !equalInts(res.AcceptedIndices, []int{0}) {
		t.Fatalf("AcceptedIndices = %v, want [0]", res.AcceptedIndices)
	}
	if got := queryInt(t, `SELECT ref_count FROM objects WHERE id = 'a1'`); got != 1 {
		t.Errorf("ref_count = %d, want 1", got)
	}
}

// TC-GC-031: A BYOS segment needs no allocation
func Test_TC_GC_031_BYOSSegmentNeedsNoAllocation(t *testing.T) {
	startAllocTest(t)
	flowF := seedFlow(t, false)

	res := insertControlled(t, newStore(t), flowF, byosSegment(t, "b1", "[0:0_2:0)"))

	if !equalInts(res.AcceptedIndices, []int{0}) {
		t.Fatalf("AcceptedIndices = %v, want [0]", res.AcceptedIndices)
	}
	if got := queryInt(t, `SELECT ref_count FROM objects WHERE id = 'b1'`); got != 1 {
		t.Errorf("ref_count = %d, want 1", got)
	}
	if sid := queryStrPtr(t, `SELECT storage_id FROM objects WHERE id = 'b1'`); sid != nil {
		t.Errorf("storage_id = %q, want NULL", *sid)
	}
}

// TC-GC-032: A BYOS segment on a claimed allocation gives object-deleting
func Test_TC_GC_032_BYOSSegmentOnClaimedAllocationIsRejectedAsDeleting(t *testing.T) {
	startAllocTest(t)
	flowF := seedFlow(t, false)
	exec(t, `INSERT INTO allocations (id, flow_id, storage_id, expires_at, deleting, claimed_at)
		VALUES ('b1', $1, 'default', now() + interval '1 hour', true, now())`, flowF)

	res := insertControlled(t, newStore(t), flowF, byosSegment(t, "b1", "[0:0_2:0)"))

	requireRejected(t, res, apperror.ErrObjectDeleting, "b1", 0)
	if rowExists(t, "objects", "b1") {
		t.Error("object row b1 exists, want none")
	}
}

// TC-GC-033: A BYOS segment does not delete the allocation
func Test_TC_GC_033_BYOSSegmentKeepsTheAllocation(t *testing.T) {
	startAllocTest(t)
	flowF := seedFlow(t, false)
	metastore.SeedAllocation(t, flowF, "b1")

	res := insertControlled(t, newStore(t), flowF, byosSegment(t, "b1", "[0:0_2:0)"))

	if !equalInts(res.AcceptedIndices, []int{0}) {
		t.Fatalf("AcceptedIndices = %v, want [0]", res.AcceptedIndices)
	}
	if sid := queryStrPtr(t, `SELECT storage_id FROM objects WHERE id = 'b1'`); sid != nil {
		t.Errorf("storage_id = %q, want NULL", *sid)
	}
	if !rowExists(t, "allocations", "b1") {
		t.Error("allocation b1 was deleted, want it kept")
	}
}

// TC-GC-034: Two first registrations of one ID at the same time
func Test_TC_GC_034_ConcurrentFirstRegistrationsKeepRefCountExact(t *testing.T) {
	startAllocTest(t)
	flowF := seedFlow(t, false)
	flowG := seedFlow(t, false)
	store := newStore(t)

	const reps = 50
	for rep := 0; rep < reps; rep++ {
		id := fmt.Sprintf("a1-%d", rep)
		tr := fmt.Sprintf("[%d:0_%d:0)", rep*2, rep*2+2)
		metastore.SeedAllocation(t, flowF, id)

		var (
			wg      sync.WaitGroup
			barrier = make(chan struct{})
			results [2]metastore.InsertResult
			errs    [2]error
		)
		for g, flowID := range []uuid.UUID{flowF, flowG} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-barrier
				results[g], errs[g] = store.InsertSegments(context.Background(), metastore.InsertBatch{
					FlowID:              flowID,
					Segments:            []domain.Segment{makeSegment(t, id, tr)},
					ControlledStorageID: "default",
				})
			}()
		}
		close(barrier)
		wg.Wait()

		for g, err := range errs {
			if err != nil {
				t.Fatalf("rep %d goroutine %d: %v", rep, g, err)
			}
		}
		if !equalInts(results[0].AcceptedIndices, []int{0}) {
			t.Fatalf("rep %d: flow F result %+v, want accepted", rep, results[0])
		}
		gAccepted := equalInts(results[1].AcceptedIndices, []int{0})
		gWrongFlow := len(results[1].RejectReasons) == 1 && results[1].RejectReasons[0].Code == apperror.ErrObjectWrongFlow
		if !gAccepted && !gWrongFlow {
			t.Fatalf("rep %d: flow G result %+v, want accepted or object-wrong-flow", rep, results[1])
		}
		if rc, n := queryInt(t, `SELECT ref_count FROM objects WHERE id = $1`, id), objectSegmentCount(t, id); rc != n {
			t.Errorf("rep %d: ref_count = %d, segment rows = %d", rep, rc, n)
		}
		if rowExists(t, "allocations", id) {
			t.Errorf("rep %d: allocation %s still exists", rep, id)
		}
	}
}

// TC-GC-036: Sorted locks prevent deadlocks in registration
func Test_TC_GC_036_OppositeRequestOrderDoesNotDeadlockUnderICUCollation(t *testing.T) {
	pool := metastore.ICUTestPool(t)
	store := metastore.ICUTestStore(t)
	ctx := context.Background()
	flowF, flowG := seedICUFlow(t, pool), seedICUFlow(t, pool)
	icuExec(t, pool, `INSERT INTO objects (id, ref_count, storage_id) VALUES ('a1', 0, 'default'), ('B1', 0, 'default')`)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM segments WHERE object_id IN ('a1', 'B1')`)
		_, _ = pool.Exec(context.Background(), `DELETE FROM objects WHERE id IN ('a1', 'B1')`)
	})

	const reps = 200
	for rep := 0; rep < reps; rep++ {
		trFirst := fmt.Sprintf("[%d:0_%d:0)", rep*4, rep*4+2)
		trSecond := fmt.Sprintf("[%d:0_%d:0)", rep*4+2, rep*4+4)
		batches := []metastore.InsertBatch{
			{FlowID: flowF, ControlledStorageID: "default", Segments: []domain.Segment{
				makeSegment(t, "a1", trFirst), makeSegment(t, "B1", trSecond)}},
			{FlowID: flowG, ControlledStorageID: "default", Segments: []domain.Segment{
				makeSegment(t, "B1", trFirst), makeSegment(t, "a1", trSecond)}},
		}
		var (
			wg      sync.WaitGroup
			barrier = make(chan struct{})
			errs    [2]error
		)
		for g := range batches {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-barrier
				_, errs[g] = store.InsertSegments(ctx, batches[g])
			}()
		}
		close(barrier)
		wg.Wait()
		for g, err := range errs {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "40P01" {
				t.Fatalf("rep %d goroutine %d: deadlock: %v", rep, g, err)
			}
			if err != nil {
				t.Fatalf("rep %d goroutine %d: %v", rep, g, err)
			}
		}
	}

	for _, id := range []string{"a1", "B1"} {
		var refCount, segments int64
		if err := pool.QueryRow(ctx, `
			SELECT o.ref_count, (SELECT COUNT(*) FROM segments s WHERE s.object_id = o.id)
			FROM objects o WHERE o.id = $1`, id).Scan(&refCount, &segments); err != nil {
			t.Fatalf("read %s: %v", id, err)
		}
		if refCount != segments || refCount != 2*reps {
			t.Errorf("%s: ref_count = %d, segment rows = %d, want both %d", id, refCount, segments, 2*reps)
		}
	}
}

func icuExec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

func seedICUFlow(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	srcID, flowID := uuid.New(), uuid.New()
	icuExec(t, pool, `INSERT INTO sources (id, format) VALUES ($1, 'urn:x-nmos:format:video')`, srcID)
	icuExec(t, pool, `INSERT INTO flows (id, source_id, format) VALUES ($1, $2, 'urn:x-nmos:format:video')`, flowID, srcID)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM flows WHERE id = $1`, flowID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM sources WHERE id = $1`, srcID)
	})
	return flowID
}

// TC-GC-037: An overlap still rejects the full request
func Test_TC_GC_037_OverlapRejectsTheRequestAndKeepsTheAllocation(t *testing.T) {
	startAllocTest(t)
	flowF := seedFlow(t, false)
	store := newStore(t)
	insertControlled(t, store, flowF, byosSegment(t, "e1", "[0:0_2:0)"))
	metastore.SeedAllocation(t, flowF, "a1")

	_, err := store.InsertSegments(context.Background(), metastore.InsertBatch{
		FlowID: flowF, Segments: []domain.Segment{makeSegment(t, "a1", "[1:0_3:0)")}, ControlledStorageID: "default",
	})

	if !errors.Is(err, metastore.ErrSegmentOverlap) {
		t.Fatalf("err = %v, want ErrSegmentOverlap", err)
	}
	if !rowExists(t, "allocations", "a1") {
		t.Error("allocation a1 was deleted, want it kept")
	}
	if rowExists(t, "objects", "a1") {
		t.Error("object row a1 exists, want none")
	}
}

// TC-GC-038: A group with a controlled and a BYOS segment is controlled
func Test_TC_GC_038_MixedGroupIsTreatedAsControlled(t *testing.T) {
	startAllocTest(t)
	flowF := seedFlow(t, false)

	res := insertControlled(t, newStore(t), flowF,
		makeSegment(t, "m1", "[0:0_2:0)"), byosSegment(t, "m1", "[2:0_4:0)"))

	requireRejected(t, res, apperror.ErrObjectNotAllocated, "m1", 0, 1)
	if len(res.AcceptedIndices) != 0 {
		t.Errorf("AcceptedIndices = %v, want none", res.AcceptedIndices)
	}
	if rowExists(t, "objects", "m1") {
		t.Error("object row m1 exists, want none")
	}
}

// TC-GC-039: A request with no accepted segment changes nothing
func Test_TC_GC_039_RequestWithNoAcceptedSegmentChangesNothing(t *testing.T) {
	startAllocTest(t)
	flowF := seedFlow(t, false)
	exec(t, `UPDATE flows SET segments_updated = '2020-01-01T00:00:00Z' WHERE id = $1`, flowF)

	res := insertControlled(t, newStore(t), flowF, makeSegment(t, "zz", "[0:0_2:0)"))

	requireRejected(t, res, apperror.ErrObjectNotAllocated, "zz", 0)
	if len(res.AcceptedIndices) != 0 {
		t.Errorf("AcceptedIndices = %v, want none", res.AcceptedIndices)
	}
	want := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	if got := queryTime(t, `SELECT segments_updated FROM flows WHERE id = $1`, flowF); !got.Equal(want) {
		t.Errorf("segments_updated = %v, want %v", got, want)
	}
	if rowExists(t, "objects", "zz") {
		t.Error("object row zz exists, want none")
	}
	if got := queryInt(t, `SELECT COUNT(*) FROM segments WHERE flow_id = $1`, flowF); got != 0 {
		t.Errorf("segments on F = %d, want 0", got)
	}
}
