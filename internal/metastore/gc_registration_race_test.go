//go:build integration

package metastore_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/amagioss/opentams/internal/apperror"
	"github.com/amagioss/opentams/internal/domain"
	"github.com/amagioss/opentams/internal/metastore"
)

// Races between segment registration and the GC claims and row deletes.

const raceStepTimeout = 5 * time.Second

func expiredAllocation(t *testing.T, flowID uuid.UUID, id string) {
	t.Helper()
	metastore.SeedAllocation(t, flowID, id)
	exec(t, `UPDATE allocations SET expires_at = now() - interval '1 hour' WHERE id = $1`, id)
}

// TC-GC-035, case A: the sweep 2 claim commits first. Registration then
// reads deleting = true and rejects the segment (BR-GC-46).
func Test_TC_GC_035_ClaimFirst_RegistrationIsRejectedAsDeleting(t *testing.T) {
	startAllocTest(t)
	flowID := seedFlow(t, false)
	expiredAllocation(t, flowID, "a1")
	store := newStore(t)

	claim, err := store.ClaimExpiredAllocations(context.Background(), "default", 5*time.Minute, 1000)
	if err != nil {
		t.Fatalf("ClaimExpiredAllocations: %v", err)
	}
	if !slices.Equal(claim.Claimed, []string{"a1"}) {
		t.Fatalf("Claimed = %v, want [a1]", claim.Claimed)
	}

	res := insertControlled(t, store, flowID, makeSegment(t, "a1", "[0:0_2:0)"))

	requireRejected(t, res, apperror.ErrObjectDeleting, "a1", 0)
	if rowExists(t, "objects", "a1") {
		t.Error("object row a1 exists, want none")
	}
	if !readAllocation(t, "a1").deleting {
		t.Error("allocation a1 lost its claim")
	}
}

// TC-GC-035, case B: registration holds the allocation row first. The
// sweep 2 claim skips the locked row, registration uses the allocation,
// and the next claim finds no allocation for the ID (BR-GC-46).
func Test_TC_GC_035_RegistrationFirst_ClaimSkipsTheRowAndRegistrationWins(t *testing.T) {
	startAllocTest(t)
	flowID := seedFlow(t, false)
	expiredAllocation(t, flowID, "a1")
	store := newStore(t)
	ctx := context.Background()

	lock, err := metastore.SharedTestPool().Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := lock.Exec(ctx, `SELECT 1 FROM allocations WHERE id = 'a1' FOR UPDATE`); err != nil {
		t.Fatalf("lock a1: %v", err)
	}

	claimCtx, cancel := context.WithTimeout(ctx, raceStepTimeout)
	defer cancel()
	claim, err := store.ClaimExpiredAllocations(claimCtx, "default", 5*time.Minute, 1000)
	if err != nil {
		t.Fatalf("ClaimExpiredAllocations with the row locked: %v", err)
	}
	if claim.Selected != 0 {
		t.Errorf("Selected = %d while the row is locked, want 0", claim.Selected)
	}
	if err := lock.Rollback(ctx); err != nil {
		t.Fatalf("release lock: %v", err)
	}

	res := insertControlled(t, store, flowID, makeSegment(t, "a1", "[0:0_2:0)"))
	if !equalInts(res.AcceptedIndices, []int{0}) {
		t.Fatalf("AcceptedIndices = %v, want [0] (rejected: %v)", res.AcceptedIndices, res.RejectReasons)
	}

	next, err := store.ClaimExpiredAllocations(ctx, "default", 5*time.Minute, 1000)
	if err != nil {
		t.Fatalf("second ClaimExpiredAllocations: %v", err)
	}
	if slices.Contains(next.Claimed, "a1") || slices.Contains(next.RowOnly, "a1") {
		t.Errorf("second claim acted on a1: %+v", next)
	}
	if rowExists(t, "allocations", "a1") {
		t.Error("allocation a1 still exists after registration")
	}
	if n := queryInt(t, `SELECT ref_count FROM objects WHERE id = 'a1'`); n != 1 {
		t.Errorf("ref_count(a1) = %d, want 1", n)
	}
}

// TC-GC-071: the sweep 1 row delete and a registration of the same
// claimed IDs do not deadlock, also in a database whose collation does not
// sort in byte order (BR-GC-23, BR-GC-36).
//
// The test forces the interleaving that a wrong lock order turns into a
// deadlock. In byte order B<i> < Z<i> < a<i>; in the ICU collation
// a<i> < B<i> < Z<i>. A raw lock on Z<i> stops the registration after it
// locked B<i> and before it locks a<i>. Then the row delete starts. With
// byte order it waits for B<i> and holds nothing. With collation order it
// would take a<i> first, and the two transactions would wait for each
// other when the raw lock goes.
func Test_TC_GC_071_RowDeleteAndRegistrationDoNotDeadlockUnderICUCollation(t *testing.T) {
	pool := metastore.ICUTestPool(t)
	store := metastore.ICUTestStore(t)
	ctx := context.Background()
	flowID := seedICUFlow(t, pool)

	const reps = 5
	var ids []string
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM segments WHERE object_id = ANY($1)`, ids)
		_, _ = pool.Exec(context.Background(), `DELETE FROM objects WHERE id = ANY($1)`, ids)
	})
	for rep := range reps {
		upper, middle, lower := fmt.Sprintf("B%d", rep), fmt.Sprintf("Z%d", rep), fmt.Sprintf("a%d", rep)
		ids = append(ids, upper, middle, lower)
		icuExec(t, pool, `INSERT INTO objects (id, ref_count, storage_id, deleting, claimed_at)
			VALUES ($1, 0, 'default', true, now()), ($2, 0, 'default', true, now())`, upper, lower)
		icuExec(t, pool, `INSERT INTO objects (id, ref_count, storage_id) VALUES ($1, 1, 'default')`, middle)

		hold, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("rep %d: begin: %v", rep, err)
		}
		if _, err := hold.Exec(ctx, `SELECT 1 FROM objects WHERE id = $1 FOR UPDATE`, middle); err != nil {
			t.Fatalf("rep %d: lock %s: %v", rep, middle, err)
		}

		batch := metastore.InsertBatch{FlowID: flowID, ControlledStorageID: "default", Segments: []domain.Segment{
			makeSegment(t, lower, fmt.Sprintf("[%d:0_%d:0)", rep*6, rep*6+2)),
			makeSegment(t, upper, fmt.Sprintf("[%d:0_%d:0)", rep*6+2, rep*6+4)),
			makeSegment(t, middle, fmt.Sprintf("[%d:0_%d:0)", rep*6+4, rep*6+6)),
		}}
		var (
			wg        sync.WaitGroup
			res       metastore.InsertResult
			removed   []string
			insertErr error
			deleteErr error
		)
		wg.Go(func() { res, insertErr = store.InsertSegments(ctx, batch) })
		waitForLockWaiters(t, 1)
		wg.Go(func() { removed, deleteErr = store.DeleteClaimedObjects(ctx, []string{lower, upper}) })
		waitForLockWaiters(t, 2)
		if err := hold.Rollback(ctx); err != nil {
			t.Fatalf("rep %d: release %s: %v", rep, middle, err)
		}
		wg.Wait()

		for name, err := range map[string]error{"InsertSegments": insertErr, "DeleteClaimedObjects": deleteErr} {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "40P01" {
				t.Fatalf("rep %d %s: deadlock: %v", rep, name, err)
			}
			if err != nil {
				t.Fatalf("rep %d %s: %v", rep, name, err)
			}
		}
		if !equalInts(res.AcceptedIndices, []int{2}) || !equalInts(res.RejectedIndices, []int{0, 1}) {
			t.Fatalf("rep %d: accepted %v, rejected %v, want [2] and [0 1]", rep, res.AcceptedIndices, res.RejectedIndices)
		}
		for i, r := range res.RejectReasons {
			if r.Code != apperror.ErrObjectDeleting {
				t.Errorf("rep %d rejection %d: code %q, want object-deleting", rep, i, r.Code)
			}
		}
		if !slices.Equal(removed, []string{upper, lower}) {
			t.Errorf("rep %d: removed %v, want [%s %s]", rep, removed, upper, lower)
		}
	}
}

// waitForLockWaiters waits until n sessions of the collation database
// wait for a lock.
func waitForLockWaiters(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(raceStepTimeout)
	for {
		var waiting int
		if err := metastore.ICUTestPool(t).QueryRow(context.Background(), `
			SELECT count(*) FROM pg_stat_activity
			WHERE datname = 'gc_icu' AND wait_event_type = 'Lock'`).Scan(&waiting); err != nil {
			t.Fatalf("read pg_stat_activity: %v", err)
		}
		if waiting >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d sessions wait for a lock after %s, want %d", waiting, raceStepTimeout, n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
