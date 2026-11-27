//go:build integration || perf

package metastore

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/amagioss/opentams/internal/apperror"
	"github.com/amagioss/opentams/internal/domain"
	"github.com/amagioss/opentams/internal/timerange"
)

const lockWaitTimeout = 10 * time.Second

func gcSeedObject(t *testing.T, pool *pgxpool.Pool, objectID string, refCount int) {
	t.Helper()
	gcExec(t, pool, `INSERT INTO objects (id, ref_count, storage_id) VALUES ($1, $2, 'default')`, objectID, refCount)
}

// gcSeedSegment writes one segment row on [startSec, endSec) seconds.
func gcSeedSegment(t *testing.T, pool *pgxpool.Pool, flowID uuid.UUID, objectID string, startSec, endSec int64) {
	t.Helper()
	gcExec(t, pool, `INSERT INTO segments (flow_id, object_id, timerange, lower_ns, upper_ns) VALUES ($1, $2, $3, $4, $5)`,
		flowID, objectID, fmt.Sprintf("[%d:0_%d:0)", startSec, endSec), startSec*int64(time.Second), endSec*int64(time.Second))
}

func gcRefCount(t *testing.T, pool *pgxpool.Pool, objectID string) int64 {
	t.Helper()
	return gcQueryInt(t, pool, `SELECT ref_count FROM objects WHERE id = $1`, objectID)
}

func gcFlowExists(t *testing.T, pool *pgxpool.Pool, flowID uuid.UUID) bool {
	t.Helper()
	return gcQueryInt(t, pool, `SELECT count(*) FROM flows WHERE id = $1`, flowID) == 1
}

func gcReset(t *testing.T) {
	t.Helper()
	resetGCTables(t)
	t.Cleanup(func() { resetGCTables(t) })
}

// lockWaiters counts the sessions of this database that wait for a lock.
func lockWaiters(t *testing.T, pool *pgxpool.Pool) int64 {
	t.Helper()
	return gcQueryInt(t, pool, `SELECT count(*) FROM pg_stat_activity
		WHERE datname = current_database() AND wait_event_type = 'Lock'`)
}

// waitUntil polls cond every 10 ms and reports whether it became true
// before the timeout.
func waitUntil(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cond()
}

func mustTimerange(t *testing.T, s string) timerange.TimeRange {
	t.Helper()
	tr, err := timerange.Parse(s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return tr
}

// TC-GC-040: DeleteFlow decreases by the number of segments (B1).
func TestDeleteFlow_decreasesRefCountByEachSegment(t *testing.T) {
	gcReset(t)
	gcSeedFlow(t, testPool, gcFlowF, false)
	gcSeedFlow(t, testPool, gcFlowG, false)
	gcSeedObject(t, testPool, "a1", 3)
	gcSeedSegment(t, testPool, gcFlowF, "a1", 0, 2)
	gcSeedSegment(t, testPool, gcFlowF, "a1", 2, 4)
	gcSeedSegment(t, testPool, gcFlowG, "a1", 0, 2)

	if err := New(testPool).DeleteFlow(context.Background(), gcFlowF); err != nil {
		t.Fatalf("DeleteFlow: %v", err)
	}

	if got := gcRefCount(t, testPool, "a1"); got != 1 {
		t.Errorf("ref_count(a1) = %d, want 1", got)
	}
}

// TC-GC-041: DeleteFlow and a timerange delete at the same time (B2).
func TestDeleteFlow_concurrentTimerangeDelete_countsEachSegmentOnce(t *testing.T) {
	gcReset(t)
	ctx := context.Background()
	gcSeedFlow(t, testPool, gcFlowF, false)
	gcSeedFlow(t, testPool, gcFlowG, false)
	gcSeedObject(t, testPool, "a1", 2)
	gcSeedSegment(t, testPool, gcFlowF, "a1", 0, 2)
	gcSeedSegment(t, testPool, gcFlowG, "a1", 0, 2)
	store := New(testPool)

	lockTx, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin L: %v", err)
	}
	defer lockTx.Rollback(ctx) //nolint:errcheck // rollback after the explicit rollback is a no-op
	if _, err := lockTx.Exec(ctx, `SELECT 1 FROM objects WHERE id = 'a1' FOR UPDATE`); err != nil {
		t.Fatalf("lock a1: %v", err)
	}

	var wg sync.WaitGroup
	var timerangeErr, deleteFlowErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, timerangeErr = store.DeleteSegmentsByTimerange(ctx, DeleteQuery{FlowID: gcFlowF, Timerange: mustTimerange(t, "[0:0_2:0)")})
	}()
	if !waitUntil(lockWaitTimeout, func() bool { return lockWaiters(t, testPool) >= 1 }) {
		t.Fatal("timerange delete never waited for the lock on a1")
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		deleteFlowErr = store.DeleteFlow(ctx, gcFlowF)
	}()
	waitUntil(500*time.Millisecond, func() bool { return lockWaiters(t, testPool) >= 2 })

	if err := lockTx.Rollback(ctx); err != nil {
		t.Fatalf("rollback L: %v", err)
	}
	wg.Wait()

	if timerangeErr != nil {
		t.Errorf("DeleteSegmentsByTimerange: %v", timerangeErr)
	}
	if deleteFlowErr != nil {
		t.Errorf("DeleteFlow: %v", deleteFlowErr)
	}
	if gcFlowExists(t, testPool, gcFlowF) {
		t.Error("flow F still exists")
	}
	if got := gcRefCount(t, testPool, "a1"); got != 1 {
		t.Errorf("ref_count(a1) = %d, want 1", got)
	}
}

// TC-GC-042: DeleteFlow and a segment insert at the same time (B8).
func TestDeleteFlow_concurrentSegmentInsert_keepsRefCountEqualToSegments(t *testing.T) {
	gcReset(t)
	ctx := context.Background()
	gcSeedFlow(t, testPool, gcFlowF, false)
	gcSeedFlow(t, testPool, gcFlowG, false)
	gcSeedObject(t, testPool, "c1", 1)
	gcSeedObject(t, testPool, "b1", 1)
	gcSeedSegment(t, testPool, gcFlowF, "c1", 0, 2)
	gcSeedSegment(t, testPool, gcFlowG, "b1", 0, 2)
	seedAllocation(t, gcFlowF, "b1")
	store := New(testPool)

	lockTx, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin L: %v", err)
	}
	defer lockTx.Rollback(ctx) //nolint:errcheck // rollback after the explicit rollback is a no-op
	if _, err := lockTx.Exec(ctx, `SELECT 1 FROM objects WHERE id = 'c1' FOR UPDATE`); err != nil {
		t.Fatalf("lock c1: %v", err)
	}

	var wg sync.WaitGroup
	var deleteFlowErr, insertErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		deleteFlowErr = store.DeleteFlow(ctx, gcFlowF)
	}()
	if !waitUntil(lockWaitTimeout, func() bool { return lockWaiters(t, testPool) >= 1 }) {
		t.Fatal("DeleteFlow never waited for the lock on c1")
	}

	insertDone := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(insertDone)
		_, insertErr = store.InsertSegments(ctx, InsertBatch{
			FlowID: gcFlowF,
			Segments: []domain.Segment{{
				ObjectID:     "b1",
				Timerange:    mustTimerange(t, "[2:0_4:0)"),
				TimerangeRaw: "[2:0_4:0)",
			}},
			ControlledStorageID: "default",
		})
	}()
	insertSettled := waitUntil(lockWaitTimeout, func() bool {
		select {
		case <-insertDone:
			return true
		default:
			return lockWaiters(t, testPool) >= 2
		}
	})
	if !insertSettled {
		t.Fatal("InsertSegments neither returned nor waited for a lock")
	}

	if err := lockTx.Rollback(ctx); err != nil {
		t.Fatalf("rollback L: %v", err)
	}
	wg.Wait()

	if deleteFlowErr != nil {
		t.Errorf("DeleteFlow: %v", deleteFlowErr)
	}
	if !errors.Is(insertErr, ErrFlowNotFound) {
		t.Errorf("InsertSegments error = %v, want ErrFlowNotFound", insertErr)
	}
	segmentRows := gcQueryInt(t, testPool, `SELECT count(*) FROM segments WHERE object_id = 'b1'`)
	if got := gcRefCount(t, testPool, "b1"); got != segmentRows || got != 1 {
		t.Errorf("ref_count(b1) = %d with %d segment rows, want 1 and 1", got, segmentRows)
	}
}

// TC-GC-043: Sorted locks prevent deadlocks in deletes.
func TestDeleteFlow_concurrentOppositeOrder_noDeadlock(t *testing.T) {
	ctx := context.Background()
	pool := icuTestPool(t)
	gcExec(t, pool, `TRUNCATE objects, allocations, segments, flows CASCADE`)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `TRUNCATE objects, allocations, segments, flows CASCADE`)
	})
	store := New(pool)

	const repetitions = 100
	for i := range repetitions {
		flowF, flowG := uuid.New(), uuid.New()
		lower, upper := fmt.Sprintf("a%d", i), fmt.Sprintf("B%d", i)
		gcSeedFlow(t, pool, flowF, false)
		gcSeedFlow(t, pool, flowG, false)
		gcSeedObject(t, pool, lower, 2)
		gcSeedObject(t, pool, upper, 2)
		gcSeedSegment(t, pool, flowF, lower, 0, 2)
		gcSeedSegment(t, pool, flowF, upper, 2, 4)
		gcSeedSegment(t, pool, flowG, upper, 0, 2)
		gcSeedSegment(t, pool, flowG, lower, 2, 4)

		barrier := make(chan struct{})
		errs := make([]error, 2)
		var wg sync.WaitGroup
		for j, flowID := range []uuid.UUID{flowF, flowG} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-barrier
				errs[j] = store.DeleteFlow(ctx, flowID)
			}()
		}
		close(barrier)
		wg.Wait()

		for _, err := range errs {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "40P01" {
				t.Fatalf("repetition %d: deadlock (SQLSTATE 40P01): %v", i, err)
			}
			if err != nil {
				t.Fatalf("repetition %d: DeleteFlow: %v", i, err)
			}
		}
	}

	if n := gcQueryInt(t, pool, `SELECT count(*) FROM objects WHERE ref_count <> 0`); n != 0 {
		t.Errorf("%d objects have ref_count <> 0, want 0", n)
	}
}

// TC-GC-044: The timerange delete decreases by the count.
func TestDeleteSegmentsByTimerange_decreasesRefCountByEachSegment(t *testing.T) {
	gcReset(t)
	gcSeedFlow(t, testPool, gcFlowF, false)
	gcSeedObject(t, testPool, "a1", 3)
	gcSeedSegment(t, testPool, gcFlowF, "a1", 0, 2)
	gcSeedSegment(t, testPool, gcFlowF, "a1", 2, 4)

	if _, err := New(testPool).DeleteSegmentsByTimerange(context.Background(),
		DeleteQuery{FlowID: gcFlowF, Timerange: mustTimerange(t, "[0:0_4:0)")}); err != nil {
		t.Fatalf("DeleteSegmentsByTimerange: %v", err)
	}

	if got := gcRefCount(t, testPool, "a1"); got != 1 {
		t.Errorf("ref_count(a1) = %d, want 1", got)
	}
}

// TC-GC-045: DeleteFlow of a missing flow.
func TestDeleteFlow_missingFlow_returnsNotFound(t *testing.T) {
	gcReset(t)
	gcSeedFlow(t, testPool, gcFlowF, false)
	gcSeedObject(t, testPool, "a1", 1)
	gcSeedSegment(t, testPool, gcFlowF, "a1", 0, 2)

	err := New(testPool).DeleteFlow(context.Background(), uuid.New())

	requireErrCode(t, err, apperror.ErrNotFound)
	if !gcFlowExists(t, testPool, gcFlowF) {
		t.Error("flow F was deleted")
	}
	if n := gcQueryInt(t, testPool, `SELECT count(*) FROM segments`); n != 1 {
		t.Errorf("segment rows = %d, want 1", n)
	}
	if got := gcRefCount(t, testPool, "a1"); got != 1 {
		t.Errorf("ref_count(a1) = %d, want 1", got)
	}
}

// TC-GC-046: DeleteFlow of a flow with no segments.
func TestDeleteFlow_flowWithoutSegments_deletesFlow(t *testing.T) {
	gcReset(t)
	gcSeedFlow(t, testPool, gcFlowF, false)

	if err := New(testPool).DeleteFlow(context.Background(), gcFlowF); err != nil {
		t.Fatalf("DeleteFlow: %v", err)
	}

	if gcFlowExists(t, testPool, gcFlowF) {
		t.Error("flow F still exists")
	}
}

// TC-GC-047: DeleteFlow of a read-only flow (B9).
func TestDeleteFlow_readOnlyFlow_returnsReadOnlyAndChangesNothing(t *testing.T) {
	gcReset(t)
	gcSeedFlow(t, testPool, gcFlowF, true)
	gcSeedObject(t, testPool, "a1", 1)
	gcSeedSegment(t, testPool, gcFlowF, "a1", 0, 2)

	err := New(testPool).DeleteFlow(context.Background(), gcFlowF)

	requireErrCode(t, err, apperror.ErrReadOnly)
	if !gcFlowExists(t, testPool, gcFlowF) {
		t.Error("flow F was deleted")
	}
	if n := gcQueryInt(t, testPool, `SELECT count(*) FROM segments WHERE flow_id = $1`, gcFlowF); n != 1 {
		t.Errorf("segment rows of F = %d, want 1", n)
	}
	if got := gcRefCount(t, testPool, "a1"); got != 1 {
		t.Errorf("ref_count(a1) = %d, want 1", got)
	}
}
