//go:build integration

package metastore

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/amagioss/opentams/internal/gc"
)

func gcRequireIDs(t *testing.T, name string, got []string, want ...string) {
	t.Helper()
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !slices.Equal(got, want) {
		t.Errorf("%s: got %v, want %v", name, got, want)
	}
}

func gcInsertObject(t *testing.T, id string, refCount int, storageID *string, deleting bool) {
	t.Helper()
	gcExec(t, `INSERT INTO objects (id, ref_count, storage_id, deleting, claimed_at)
		VALUES ($1, $2, $3, $4, CASE WHEN $4 THEN now() END)`, id, refCount, storageID, deleting)
}

func gcInsertSegment(t *testing.T, objectID string, lowerSec int64) {
	t.Helper()
	gcExec(t, `INSERT INTO segments (flow_id, object_id, timerange, lower_ns, upper_ns)
		VALUES ($1, $2, '[' || $3::bigint || ':0_' || ($3::bigint + 1) || ':0)', $3::bigint * 1000000000, ($3::bigint + 1) * 1000000000)`,
		gcFlowF, objectID, lowerSec)
}

func gcPtr(s string) *string { return &s }

// TC-GC-060: A released controlled object is claimed and deleted.
func TestClaimReleasedObjects_ReleasedControlledObject_ClaimedAndDeleted(t *testing.T) {
	resetGCTables(t)
	gcInsertObject(t, "r1", 0, gcPtr("default"), false)
	s := New(testPool)
	ctx := context.Background()

	claim, err := s.ClaimReleasedObjects(ctx, "default", 1000)
	if err != nil {
		t.Fatalf("ClaimReleasedObjects: %v", err)
	}
	if claim.Selected != 1 {
		t.Errorf("Selected: got %d, want 1", claim.Selected)
	}
	gcRequireIDs(t, "Claimed", claim.Claimed, "r1")
	if n := gcCount(t, `SELECT count(*) FROM objects WHERE id = 'r1' AND deleting = true`); n != 1 {
		t.Errorf("r1 with deleting = true: got %d rows, want 1", n)
	}
	if d := gcSecondsFromNow(t, "objects", "claimed_at", "r1", 0); d > gcTolerance {
		t.Errorf("claimed_at: %.1f s from now()", d)
	}

	deleted, err := s.DeleteClaimedObjects(ctx, []string{"r1"})
	if err != nil {
		t.Fatalf("DeleteClaimedObjects: %v", err)
	}
	gcRequireIDs(t, "deleted", deleted, "r1")
	if n := gcCount(t, `SELECT count(*) FROM objects WHERE id = 'r1'`); n != 0 {
		t.Errorf("object rows r1: got %d, want 0", n)
	}
}

// TC-GC-061: A released BYOS object is a row-only delete.
func TestClaimReleasedObjects_ReleasedBYOSObject_RowOnlyDelete(t *testing.T) {
	resetGCTables(t)
	gcInsertObject(t, "y1", 0, nil, false)

	claim, err := New(testPool).ClaimReleasedObjects(context.Background(), "default", 1000)
	if err != nil {
		t.Fatalf("ClaimReleasedObjects: %v", err)
	}
	gcRequireIDs(t, "RowOnly", claim.RowOnly, "y1")
	gcRequireIDs(t, "Claimed", claim.Claimed)
	if n := gcCount(t, `SELECT count(*) FROM objects WHERE id = 'y1'`); n != 0 {
		t.Errorf("object rows y1: got %d, want 0", n)
	}
}

// TC-GC-062: An object in use is not a candidate.
func TestClaimReleasedObjects_ObjectInUse_NotCandidate(t *testing.T) {
	resetGCTables(t)
	gcSeedFlow(t, gcFlowF)
	gcInsertObject(t, "u1", 1, gcPtr("default"), false)
	gcInsertSegment(t, "u1", 0)

	claim, err := New(testPool).ClaimReleasedObjects(context.Background(), "default", 1000)
	if err != nil {
		t.Fatalf("ClaimReleasedObjects: %v", err)
	}
	if claim.Selected != 0 {
		t.Errorf("Selected: got %d, want 0", claim.Selected)
	}
	if n := gcCount(t, `SELECT count(*) FROM objects
		WHERE id = 'u1' AND ref_count = 1 AND deleting = false AND claimed_at IS NULL`); n != 1 {
		t.Errorf("u1 unchanged: got %d rows, want 1", n)
	}
}

// TC-GC-063: Drift is repaired, not deleted.
func TestClaimReleasedObjects_Drift_RepairedNotClaimed(t *testing.T) {
	resetGCTables(t)
	gcSeedFlow(t, gcFlowF)
	gcInsertObject(t, "d1", 0, gcPtr("default"), false)
	gcInsertSegment(t, "d1", 0)

	claim, err := New(testPool).ClaimReleasedObjects(context.Background(), "default", 1000)
	if err != nil {
		t.Fatalf("ClaimReleasedObjects: %v", err)
	}
	if want := []gc.DriftRecord{{ObjectID: "d1", Actual: 1}}; !slices.Equal(claim.Drift, want) {
		t.Errorf("Drift: got %v, want %v", claim.Drift, want)
	}
	gcRequireIDs(t, "Claimed", claim.Claimed)
	if n := gcCount(t, `SELECT count(*) FROM objects WHERE id = 'd1' AND ref_count = 1 AND deleting = false`); n != 1 {
		t.Errorf("d1 with ref_count = 1 and deleting = false: got %d rows, want 1", n)
	}
}

// TC-GC-064: An unknown storage ID is not a candidate.
func TestClaimReleasedObjects_UnknownStorageID_NotCandidateAndCounted(t *testing.T) {
	resetGCTables(t)
	gcInsertObject(t, "o1", 0, gcPtr("other"), false)
	s := New(testPool)
	ctx := context.Background()

	claim, err := s.ClaimReleasedObjects(ctx, "default", 1000)
	if err != nil {
		t.Fatalf("ClaimReleasedObjects: %v", err)
	}
	backlog, err := s.ReleasedBacklog(ctx, "default")
	if err != nil {
		t.Fatalf("ReleasedBacklog: %v", err)
	}

	if claim.Selected != 0 {
		t.Errorf("Selected: got %d, want 0", claim.Selected)
	}
	if backlog.UnknownStorage != 1 {
		t.Errorf("UnknownStorage: got %d, want 1", backlog.UnknownStorage)
	}
	if n := gcCount(t, `SELECT count(*) FROM objects
		WHERE id = 'o1' AND ref_count = 0 AND storage_id = 'other' AND deleting = false AND claimed_at IS NULL`); n != 1 {
		t.Errorf("o1 unchanged: got %d rows, want 1", n)
	}
}

// TC-GC-065: A claimed row is not a candidate again, and the lease protects it.
func TestReclaimRetryObjects_ClaimedRow_LeaseProtectsUntilExpired(t *testing.T) {
	resetGCTables(t)
	gcInsertObject(t, "r1", 0, gcPtr("default"), true)
	s := New(testPool)
	ctx := context.Background()

	claim, err := s.ClaimReleasedObjects(ctx, "default", 1000)
	if err != nil {
		t.Fatalf("ClaimReleasedObjects: %v", err)
	}
	if claim.Selected != 0 {
		t.Errorf("claim Selected: got %d, want 0", claim.Selected)
	}

	held, err := s.ReclaimRetryObjects(ctx, "default", 5*time.Minute, time.Now().Add(time.Minute), 1000)
	if err != nil {
		t.Fatalf("ReclaimRetryObjects (lease held): %v", err)
	}
	gcRequireIDs(t, "reclaim with lease held", held)

	gcExec(t, `UPDATE objects SET claimed_at = now() - interval '10 minutes' WHERE id = 'r1'`)
	expired, err := s.ReclaimRetryObjects(ctx, "default", 5*time.Minute, time.Now().Add(time.Minute), 1000)
	if err != nil {
		t.Fatalf("ReclaimRetryObjects (lease expired): %v", err)
	}
	gcRequireIDs(t, "reclaim with lease expired", expired, "r1")
	if d := gcSecondsFromNow(t, "objects", "claimed_at", "r1", 0); d > gcTolerance {
		t.Errorf("claimed_at: %.1f s from now()", d)
	}
}

// TC-GC-066: Two claims at the same time get different rows.
func TestClaimReleasedObjects_ConcurrentClaims_GetDifferentRows(t *testing.T) {
	resetGCTables(t)
	gcInsertObject(t, "r1", 0, gcPtr("default"), false)
	gcInsertObject(t, "r2", 0, gcPtr("default"), false)
	s := New(testPool)

	start := make(chan struct{})
	claims := make([]gc.ClaimResult, 2)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			claims[i], errs[i] = s.ClaimReleasedObjects(context.Background(), "default", 1)
		}()
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("claim %d: %v", i, err)
		}
	}
	got := append(slices.Clone(claims[0].Claimed), claims[1].Claimed...)
	slices.Sort(got)
	if !slices.Equal(got, []string{"r1", "r2"}) {
		t.Errorf("Claimed lists %v and %v, want [r1] and [r2] in some sequence", claims[0].Claimed, claims[1].Claimed)
	}
}

// TC-GC-067: The reclaim gives one attempt in each cycle.
func TestReclaimRetryObjects_RepeatedInOneCycle_EachRowOnce(t *testing.T) {
	resetGCTables(t)
	cycleStart := time.Now()
	for _, id := range []string{"k1", "k2", "k3"} {
		gcInsertObject(t, id, 0, gcPtr("default"), true)
	}
	gcExec(t, `UPDATE objects SET claimed_at = now() - interval '10 minutes'`)
	s := New(testPool)
	ctx := context.Background()

	for i, want := range [][]string{{"k1", "k2"}, {"k3"}, nil} {
		got, err := s.ReclaimRetryObjects(ctx, "default", 5*time.Minute, cycleStart, 2)
		if err != nil {
			t.Fatalf("reclaim %d: %v", i+1, err)
		}
		gcRequireIDs(t, fmt.Sprintf("reclaim %d", i+1), got, want...)
	}
	if n := gcCount(t, `SELECT count(*) FROM objects WHERE claimed_at > $1`, cycleStart); n != 3 {
		t.Errorf("rows with claimed_at later than the cycle start: got %d, want 3", n)
	}
}

// TC-GC-068: The row delete removes only claimed rows.
func TestDeleteClaimedObjects_UnclaimedRow_NotDeleted(t *testing.T) {
	resetGCTables(t)
	gcInsertObject(t, "r1", 0, gcPtr("default"), false)

	deleted, err := New(testPool).DeleteClaimedObjects(context.Background(), []string{"r1"})
	if err != nil {
		t.Fatalf("DeleteClaimedObjects: %v", err)
	}
	gcRequireIDs(t, "deleted", deleted)
	if n := gcCount(t, `SELECT count(*) FROM objects WHERE id = 'r1'`); n != 1 {
		t.Errorf("object rows r1: got %d, want 1", n)
	}
}

// TC-GC-069: The foreign key stops a row delete with a segment.
func TestDeleteClaimedObjects_RowWithSegment_ForeignKeyError(t *testing.T) {
	resetGCTables(t)
	gcSeedFlow(t, gcFlowF)
	gcInsertObject(t, "r1", 0, gcPtr("default"), true)
	gcInsertSegment(t, "r1", 0)

	_, err := New(testPool).DeleteClaimedObjects(context.Background(), []string{"r1"})

	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23503" {
		t.Fatalf("error: got %v, want SQLSTATE 23503", err)
	}
	if n := gcCount(t, `SELECT count(*) FROM objects WHERE id = 'r1'`); n != 1 {
		t.Errorf("object rows r1: got %d, want 1", n)
	}
}

// gcInsertAllocation writes allocation id on flow F with expires_at =
// now() + expiresIn.
func gcInsertAllocation(t *testing.T, id, storageID string, expiresIn time.Duration, deleting bool) {
	t.Helper()
	gcExec(t, `INSERT INTO allocations (id, flow_id, storage_id, expires_at, deleting, claimed_at)
		VALUES ($1, $2, $3, now() + make_interval(secs => $4), $5, CASE WHEN $5 THEN now() END)`,
		id, gcFlowF, storageID, expiresIn.Seconds(), deleting)
}

func gcClaimExpired(t *testing.T, grace time.Duration) gc.ClaimResult {
	t.Helper()
	claim, err := New(testPool).ClaimExpiredAllocations(context.Background(), "default", grace, 1000)
	if err != nil {
		t.Fatalf("ClaimExpiredAllocations: %v", err)
	}
	return claim
}

const gcExpired = -5*time.Minute - time.Second

// TC-GC-080: An allocation after expires_at + grace is claimed.
func TestClaimExpiredAllocations_PastGrace_ClaimedAndDeleted(t *testing.T) {
	resetGCTables(t)
	gcInsertAllocation(t, "e1", "default", gcExpired, false)

	claim := gcClaimExpired(t, 5*time.Minute)

	gcRequireIDs(t, "Claimed", claim.Claimed, "e1")
	if n := gcCount(t, `SELECT count(*) FROM allocations WHERE id = 'e1' AND deleting = true`); n != 1 {
		t.Errorf("e1 with deleting = true: got %d rows, want 1", n)
	}
	if d := gcSecondsFromNow(t, "allocations", "claimed_at", "e1", 0); d > gcTolerance {
		t.Errorf("claimed_at: %.1f s from now()", d)
	}

	deleted, err := New(testPool).DeleteClaimedAllocations(context.Background(), []string{"e1"})
	if err != nil {
		t.Fatalf("DeleteClaimedAllocations: %v", err)
	}
	gcRequireIDs(t, "deleted", deleted, "e1")
	if n := gcCount(t, `SELECT count(*) FROM allocations WHERE id = 'e1'`); n != 0 {
		t.Errorf("allocation rows e1: got %d, want 0", n)
	}
}

// TC-GC-081: An allocation before expires_at + grace is not claimed.
func TestClaimExpiredAllocations_WithinGrace_NotClaimed(t *testing.T) {
	resetGCTables(t)
	gcInsertAllocation(t, "e2", "default", -4*time.Minute, false)

	claim := gcClaimExpired(t, 5*time.Minute)

	if claim.Selected != 0 {
		t.Errorf("Selected: got %d, want 0", claim.Selected)
	}
}

// TC-GC-082: Grace 0.
func TestClaimExpiredAllocations_GraceZero_ClaimsJustExpired(t *testing.T) {
	resetGCTables(t)
	gcInsertAllocation(t, "e3", "default", -time.Second, false)

	claim := gcClaimExpired(t, 0)

	gcRequireIDs(t, "Claimed", claim.Claimed, "e3")
}

// TC-GC-083: An allocation with an object row is a leftover.
func TestClaimExpiredAllocations_WithObjectRow_LeftoverRowOnly(t *testing.T) {
	resetGCTables(t)
	gcInsertAllocation(t, "l1", "default", gcExpired, false)
	gcInsertObject(t, "l1", 1, gcPtr("default"), false)

	claim := gcClaimExpired(t, 5*time.Minute)

	gcRequireIDs(t, "RowOnly", claim.RowOnly, "l1")
	gcRequireIDs(t, "Claimed", claim.Claimed)
	if n := gcCount(t, `SELECT count(*) FROM allocations WHERE id = 'l1'`); n != 0 {
		t.Errorf("allocation rows l1: got %d, want 0", n)
	}
	if n := gcCount(t, `SELECT count(*) FROM objects
		WHERE id = 'l1' AND ref_count = 1 AND storage_id = 'default' AND deleting = false AND claimed_at IS NULL`); n != 1 {
		t.Errorf("object l1 unchanged: got %d rows, want 1", n)
	}
}

// TC-GC-084: A leftover with an object at ref_count = 0.
func TestClaimExpiredAllocations_WithReleasedObjectRow_LeftoverObjectUntouched(t *testing.T) {
	resetGCTables(t)
	gcInsertAllocation(t, "l2", "default", gcExpired, false)
	gcInsertObject(t, "l2", 0, gcPtr("default"), false)

	claim := gcClaimExpired(t, 5*time.Minute)

	gcRequireIDs(t, "RowOnly", claim.RowOnly, "l2")
	if n := gcCount(t, `SELECT count(*) FROM objects
		WHERE id = 'l2' AND ref_count = 0 AND storage_id = 'default' AND deleting = false AND claimed_at IS NULL`); n != 1 {
		t.Errorf("object l2 unchanged: got %d rows, want 1", n)
	}
}

// TC-GC-085: A leftover with a BYOS object row.
func TestClaimExpiredAllocations_WithBYOSObjectRow_LeftoverRowOnly(t *testing.T) {
	resetGCTables(t)
	gcInsertAllocation(t, "l3", "default", gcExpired, false)
	gcInsertObject(t, "l3", 1, nil, false)

	claim := gcClaimExpired(t, 5*time.Minute)

	gcRequireIDs(t, "RowOnly", claim.RowOnly, "l3")
	gcRequireIDs(t, "Claimed", claim.Claimed)
}

// TC-GC-086: A claimed allocation is not a candidate again, and the lease protects it.
func TestReclaimRetryAllocations_ClaimedRow_LeaseProtectsUntilExpired(t *testing.T) {
	resetGCTables(t)
	gcInsertAllocation(t, "e4", "default", gcExpired, true)
	s := New(testPool)
	ctx := context.Background()

	claim := gcClaimExpired(t, 5*time.Minute)
	if claim.Selected != 0 {
		t.Errorf("claim Selected: got %d, want 0", claim.Selected)
	}

	held, err := s.ReclaimRetryAllocations(ctx, "default", 5*time.Minute, time.Now().Add(time.Minute), 1000)
	if err != nil {
		t.Fatalf("ReclaimRetryAllocations (lease held): %v", err)
	}
	gcRequireIDs(t, "reclaim with lease held", held)

	gcExec(t, `UPDATE allocations SET claimed_at = now() - interval '10 minutes' WHERE id = 'e4'`)
	expired, err := s.ReclaimRetryAllocations(ctx, "default", 5*time.Minute, time.Now().Add(time.Minute), 1000)
	if err != nil {
		t.Fatalf("ReclaimRetryAllocations (lease expired): %v", err)
	}
	gcRequireIDs(t, "reclaim with lease expired", expired, "e4")
}

// TC-GC-087: An allocation with an unknown storage ID.
func TestClaimExpiredAllocations_UnknownStorageID_NotCandidateAndCounted(t *testing.T) {
	resetGCTables(t)
	gcInsertAllocation(t, "e5", "other", gcExpired, false)

	claim := gcClaimExpired(t, 5*time.Minute)
	backlog, err := New(testPool).AllocationBacklog(context.Background(), "default", 5*time.Minute)
	if err != nil {
		t.Fatalf("AllocationBacklog: %v", err)
	}

	if claim.Selected != 0 {
		t.Errorf("Selected: got %d, want 0", claim.Selected)
	}
	if backlog.UnknownStorage != 1 {
		t.Errorf("UnknownStorage: got %d, want 1", backlog.UnknownStorage)
	}
}

// TC-GC-088: The backlog values of sweep 2.
func TestAllocationBacklog_MixedRows_ReturnsGaugeValues(t *testing.T) {
	resetGCTables(t)
	gcInsertAllocation(t, "p1", "default", -15*time.Minute, false)
	gcInsertAllocation(t, "p2", "default", time.Hour, false)
	gcInsertAllocation(t, "p3", "default", time.Hour, true)

	b, err := New(testPool).AllocationBacklog(context.Background(), "default", 5*time.Minute)
	if err != nil {
		t.Fatalf("AllocationBacklog: %v", err)
	}

	if b.Pending != 1 || b.Retrying != 1 || b.Rows != 3 {
		t.Errorf("Pending, Retrying, Rows: got %d, %d, %d, want 1, 1, 3", b.Pending, b.Retrying, b.Rows)
	}
	if math.Abs(b.OldestOverdue-600) > gcTolerance {
		t.Errorf("OldestOverdue: got %.1f, want 600 (tolerance %.0f)", b.OldestOverdue, gcTolerance)
	}
}
