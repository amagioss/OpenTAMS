package gc

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/amagioss/opentams/internal/objectstore"
)

const (
	errorsTotal = "opentams_gc_errors_total"
	itemsTotal  = "opentams_gc_items_total"
)

func runOnce(t *testing.T, h *harness) Report {
	t.Helper()
	report, err := h.w.RunOnce(context.Background(), nil)
	require.NoError(t, err)
	return report
}

// TC-GC-100: An error for each ID stops the cycle.
func TestReleasedSweep_AnErrorForEveryIDIsABatchErrorCountedOncePerID(t *testing.T) {
	h := newHarness(t)
	h.store.claimReleased = claimed("a", "b")
	h.deleter.deleteObjects = failIDs(objectstore.ErrTransient, "a", "b")

	report := runOnce(t, h)

	assert.Equal(t, CycleResult{Failed: 2, BatchError: true}, report.Released)
	assert.Empty(t, h.store.callsNamed(callDeleteObjects))
	assert.Len(t, h.store.callsNamed(callClaimReleased), 1)
	assert.Equal(t, 2.0, h.value(t, errorsTotal, "sweep", SweepReleased, "stage", "s3_delete"))
	lines := h.linesFor("gc batch error", SweepReleased)
	require.Len(t, lines, 1)
	assert.Equal(t, "s3_delete", lines[0].ContextMap()["stage"])
}

// TC-GC-101: The next cycle completes the delete.
func TestReleasedSweep_TheNextCycleReclaimsAndCompletesTheFailedDelete(t *testing.T) {
	h := newHarness(t)
	h.store.claimReleased = claimed("a", "b")
	h.deleter.deleteObjects = failIDs(objectstore.ErrTransient, "a", "b")
	first := runOnce(t, h)
	require.True(t, first.Released.BatchError)

	h.store.resetCalls()
	h.store.claimReleased = nil
	h.store.reclaimObjects = func(context.Context, string, time.Duration, time.Time, int) ([]string, error) {
		return []string{"a", "b"}, nil
	}
	h.deleter.deleteObjects = nil

	second := runOnce(t, h)

	reclaimAt := h.store.indexOf(callReclaimObjects)
	claimAt := h.store.indexOf(callClaimReleased)
	require.GreaterOrEqual(t, reclaimAt, 0)
	require.GreaterOrEqual(t, claimAt, 0)
	assert.Less(t, reclaimAt, claimAt)
	rowDeletes := h.store.callsNamed(callDeleteObjects)
	require.Len(t, rowDeletes, 1)
	assert.Equal(t, []string{"a", "b"}, rowDeletes[0].ids)
	assert.Equal(t, 2, second.Released.Deleted)
	assert.False(t, second.Released.BatchError)
}

// TC-GC-102: An error for one ID does not stop the cycle.
func TestReleasedSweep_AnErrorForOneIDKeepsTheOthersAndTheCycle(t *testing.T) {
	h := newHarness(t)
	h.store.claimReleased = claimed("a", "b", "c")
	h.deleter.deleteObjects = failIDs(objectstore.ErrTransient, "b")

	report := runOnce(t, h)

	rowDeletes := h.store.callsNamed(callDeleteObjects)
	require.Len(t, rowDeletes, 1)
	assert.Equal(t, []string{"a", "c"}, rowDeletes[0].ids)
	assert.Equal(t, 1, report.Released.Failed)
	assert.False(t, report.Released.BatchError)
	assert.Equal(t, 1.0, h.value(t, errorsTotal, "sweep", SweepReleased, "stage", "s3_delete"))
	lines := h.linesFor("gc object delete failed", SweepReleased)
	require.Len(t, lines, 1)
	assert.Equal(t, "b", lines[0].ContextMap()["object_id"])
	assert.Contains(t, lines[0].ContextMap()["error"], objectstore.ErrTransient.Error())
	assert.Empty(t, h.linesFor("gc batch error", ""))
}

// TC-GC-103: ErrAuth for the call stops the cycle.
func TestReleasedSweep_ErrAuthForTheCallIsABatchError(t *testing.T) {
	h := newHarness(t)
	h.store.claimReleased = claimed("a")
	h.deleter.deleteObjects = func(context.Context, []string) ([]objectstore.DeleteResult, error) {
		return nil, objectstore.ErrAuth
	}

	report := runOnce(t, h)

	assert.True(t, report.Released.BatchError)
	assert.Empty(t, h.store.callsNamed(callDeleteObjects))
	lines := h.linesFor("gc batch error", SweepReleased)
	require.Len(t, lines, 1)
	assert.Equal(t, "s3_delete", lines[0].ContextMap()["stage"])
	assert.Equal(t, 1.0, h.value(t, errorsTotal, "sweep", SweepReleased, "stage", "s3_delete"))
}

// TC-GC-104: ErrAuth for one key deletes the others first.
func TestReleasedSweep_ErrAuthForOneKeyRemovesTheDeletedRowsFirst(t *testing.T) {
	h := newHarness(t)
	h.store.claimReleased = claimed("a", "b")
	h.deleter.deleteObjects = failIDs(objectstore.ErrAuth, "b")

	report := runOnce(t, h)

	rowDeletes := h.store.callsNamed(callDeleteObjects)
	require.Len(t, rowDeletes, 1)
	assert.Equal(t, []string{"a"}, rowDeletes[0].ids)
	assert.True(t, report.Released.BatchError)
	assert.Equal(t, 1, report.Released.Deleted)
	assert.Equal(t, 1, report.Released.Failed)
	assert.Equal(t, 1.0, h.value(t, errorsTotal, "sweep", SweepReleased, "stage", "s3_delete"))
	assert.Len(t, h.linesFor("gc batch error", SweepReleased), 1)
}

// TC-GC-105: A database error in the claim.
func TestReleasedSweep_ADatabaseErrorInTheClaimIsABatchError(t *testing.T) {
	h := newHarness(t)
	h.store.claimReleased = func(context.Context, string, int) (ClaimResult, error) {
		return ClaimResult{}, errors.New("connection refused")
	}

	report := runOnce(t, h)

	assert.True(t, report.Released.BatchError)
	assert.Equal(t, 1.0, h.value(t, errorsTotal, "sweep", SweepReleased, "stage", "claim"))
	lines := h.linesFor("gc batch error", SweepReleased)
	require.Len(t, lines, 1)
	assert.Equal(t, "claim", lines[0].ContextMap()["stage"])
	assert.Equal(t, "connection refused", lines[0].ContextMap()["error"])
}

// TC-GC-106: A database error in the row delete.
func TestReleasedSweep_ADatabaseErrorInTheRowDeleteIsABatchError(t *testing.T) {
	h := newHarness(t)
	h.store.claimReleased = claimed("a")
	h.store.deleteObjects = func(context.Context, []string) ([]string, error) {
		return nil, errors.New("foreign key violation")
	}

	report := runOnce(t, h)

	assert.True(t, report.Released.BatchError)
	assert.Equal(t, 1.0, h.value(t, errorsTotal, "sweep", SweepReleased, "stage", "row_delete"))
}

// TC-GC-107: The drain rule.
func TestReleasedSweep_TheClaimPhaseDrainsUntilASelectIsSmallerThanTheBatch(t *testing.T) {
	cases := []struct {
		name      string
		selected  []int
		wantCalls int
	}{
		{"full, full, partial", []int{3, 3, 1}, 3},
		{"full, empty", []int{3, 0}, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, func(c *Config) { c.BatchSize = 3 })
			next := 0
			h.store.claimReleased = func(context.Context, string, int) (ClaimResult, error) {
				n := tc.selected[min(next, len(tc.selected)-1)]
				next++
				return ClaimResult{Selected: n}, nil
			}

			runOnce(t, h)

			assert.Len(t, h.store.callsNamed(callClaimReleased), tc.wantCalls)
		})
	}
}

// TC-GC-108: The retry phase repeats the reclaim with one cycle start.
func TestReleasedSweep_TheRetryPhaseRepeatsTheReclaimWithOneCycleStart(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.BatchSize = 2 })
	batches := [][]string{{"a", "b"}, {"c"}}
	next := 0
	h.store.reclaimObjects = func(context.Context, string, time.Duration, time.Time, int) ([]string, error) {
		ids := batches[next]
		next++
		h.clock.thenReturn(h.clock.now().Add(time.Second))
		return ids, nil
	}

	runOnce(t, h)

	reclaims := h.store.callsNamed(callReclaimObjects)
	require.Len(t, reclaims, 2)
	for _, c := range reclaims {
		assert.Equal(t, clockStart, c.cycleStart)
		assert.Equal(t, claimLease, c.lease)
		assert.Equal(t, "default", c.storageID)
		assert.Equal(t, 2, c.limit)
	}
	assert.Equal(t, []string{"a", "b", "c"}, slices.Concat(h.deleter.callIDs()...))
}

// TC-GC-109: A batch error in the retry phase skips the claim phase.
func TestReleasedSweep_ABatchErrorInTheRetryPhaseSkipsTheClaimPhase(t *testing.T) {
	h := newHarness(t)
	h.store.reclaimObjects = func(context.Context, string, time.Duration, time.Time, int) ([]string, error) {
		return []string{"a"}, nil
	}
	h.deleter.deleteObjects = func(context.Context, []string) ([]objectstore.DeleteResult, error) {
		return nil, objectstore.ErrAuth
	}

	report := runOnce(t, h)

	assert.Empty(t, h.store.callsNamed(callClaimReleased))
	assert.True(t, report.Released.BatchError)
}

func TestReleasedSweep_ADatabaseErrorInTheReclaimIsABatchError(t *testing.T) {
	h := newHarness(t)
	h.store.reclaimObjects = func(context.Context, string, time.Duration, time.Time, int) ([]string, error) {
		return nil, errors.New("timeout")
	}

	report := runOnce(t, h)

	assert.True(t, report.Released.BatchError)
	assert.Empty(t, h.store.callsNamed(callClaimReleased))
	assert.Equal(t, 1.0, h.value(t, errorsTotal, "sweep", SweepReleased, "stage", "reclaim"))
}

// TC-GC-111: An empty batch is a success.
func TestReleasedSweep_AnEmptyBatchSetsTheLastSuccessTimestamp(t *testing.T) {
	h := newHarness(t)

	runOnce(t, h)

	assert.Equal(t, float64(clockStart.Unix()), h.value(t, "opentams_gc_last_success_timestamp_seconds", "sweep", SweepReleased))
	assert.Equal(t, uint64(1), h.histogramCount(t, "opentams_gc_batch_duration_seconds", "sweep", SweepReleased))
}

// TC-GC-114: Only removed rows are counted.
func TestReleasedSweep_OnlyTheRowsThatTheRowDeleteRemovedAreCounted(t *testing.T) {
	h := newHarness(t)
	h.store.claimReleased = claimed("a", "b")
	h.store.deleteObjects = func(context.Context, []string) ([]string, error) {
		return []string{"a"}, nil
	}

	report := runOnce(t, h)

	assert.Equal(t, 1.0, h.value(t, itemsTotal, "sweep", SweepReleased, "outcome", OutcomeDeleted))
	assert.Equal(t, 1, report.Released.Deleted)
	lines := h.linesFor("gc object removed", SweepReleased)
	require.Len(t, lines, 1)
	assert.Equal(t, "a", lines[0].ContextMap()["object_id"])
}

// TC-GC-116: The database timeout.
func TestReleasedSweep_TheDatabaseTimeoutEndsABlockedClaim(t *testing.T) {
	h := newHarness(t)
	h.w.dbTimeout = 50 * time.Millisecond
	h.store.claimReleased = func(ctx context.Context, _ string, _ int) (ClaimResult, error) {
		<-ctx.Done()
		return ClaimResult{}, ctx.Err()
	}

	started := time.Now()
	report := runOnce(t, h)

	assert.Less(t, time.Since(started), time.Second)
	assert.True(t, report.Released.BatchError)
	assert.Len(t, h.linesFor("gc cycle completed", SweepReleased), 1)
}

// TC-GC-119: The object-store timeout.
func TestReleasedSweep_TheObjectStoreTimeoutEndsABlockedDelete(t *testing.T) {
	h := newHarness(t)
	h.w.s3Timeout = 50 * time.Millisecond
	h.store.claimReleased = claimed("a")
	hadDeadline := make(chan bool, 1)
	h.deleter.deleteObjects = func(ctx context.Context, ids []string) ([]objectstore.DeleteResult, error) {
		_, ok := ctx.Deadline()
		hadDeadline <- ok
		<-ctx.Done()
		return failIDs(objectstore.ErrTransient, ids...)(ctx, ids)
	}

	started := time.Now()
	report := runOnce(t, h)

	assert.Less(t, time.Since(started), time.Second)
	assert.True(t, waitFor(t, hadDeadline, "the deleter call"))
	assert.Equal(t, 1, report.Released.Failed)
	assert.True(t, report.Released.BatchError)
}

// TC-GC-118: Sweep 1 drift is recorded.
func TestReleasedSweep_DriftIsCountedAndLogged(t *testing.T) {
	h := newHarness(t)
	h.store.claimReleased = func(context.Context, string, int) (ClaimResult, error) {
		return ClaimResult{Selected: 1, Drift: []DriftRecord{{ObjectID: "d1", Actual: 2}}}, nil
	}

	report := runOnce(t, h)

	assert.Equal(t, 1.0, h.value(t, "opentams_gc_refcount_drift_total"))
	assert.Equal(t, 1, report.Released.Drift)
	lines := h.linesFor("gc refcount drift", "")
	require.Len(t, lines, 1)
	fields := lines[0].ContextMap()
	assert.Equal(t, "d1", fields["object_id"])
	assert.EqualValues(t, 0, fields["stored_ref_count"])
	assert.EqualValues(t, 2, fields["actual_ref_count"])
	assert.Empty(t, h.deleter.callIDs())
}

// TC-GC-096: A batch older than half the lease is not sent.
func TestReleasedSweep_ABatchOlderThanHalfTheLeaseIsNotSent(t *testing.T) {
	h := newHarness(t)
	allocationDone := h.firstCycleCompleted()
	claimTime := clockStart.Add(time.Minute)
	h.store.claimReleased = func(context.Context, string, int) (ClaimResult, error) {
		if err := waitInFake(allocationDone); err != nil {
			return ClaimResult{}, err
		}
		h.clock.thenReturn(claimTime, claimTime.Add(claimLease/2+time.Second))
		return ClaimResult{Selected: 1, Claimed: []string{"a"}}, nil
	}

	report := runOnce(t, h)

	assert.Empty(t, h.deleter.callIDs())
	assert.Empty(t, h.store.callsNamed(callDeleteObjects))
	lines := h.linesFor("gc batch skipped", SweepReleased)
	require.Len(t, lines, 1)
	assert.EqualValues(t, 1, lines[0].ContextMap()["ids"])
	assert.EqualValues(t, (claimLease/2 + time.Second).Milliseconds(), lines[0].ContextMap()["age_ms"])
	assert.False(t, report.Released.BatchError)
	assert.Zero(t, report.Released.Failed)
}

func TestReleasedSweep_ABatchAtExactlyHalfTheLeaseIsSent(t *testing.T) {
	h := newHarness(t)
	allocationDone := h.firstCycleCompleted()
	h.store.claimReleased = func(context.Context, string, int) (ClaimResult, error) {
		if err := waitInFake(allocationDone); err != nil {
			return ClaimResult{}, err
		}
		h.clock.thenReturn(clockStart, clockStart.Add(claimLease/2))
		return ClaimResult{Selected: 1, Claimed: []string{"a"}}, nil
	}

	report := runOnce(t, h)

	assert.Equal(t, [][]string{{"a"}}, h.deleter.callIDs())
	assert.Equal(t, 1, report.Released.Deleted)
}

func TestReleasedSweep_BYOSRowsAreCountedAsRowOnly(t *testing.T) {
	h := newHarness(t)
	h.store.claimReleased = func(context.Context, string, int) (ClaimResult, error) {
		return ClaimResult{Selected: 2, RowOnly: []string{"b1", "b2"}}, nil
	}

	report := runOnce(t, h)

	assert.Equal(t, 2, report.Released.RowOnly)
	assert.Equal(t, 2.0, h.value(t, itemsTotal, "sweep", SweepReleased, "outcome", OutcomeBYOSRowOnly))
	assert.Empty(t, h.deleter.callIDs())
}

func TestReleasedSweep_TheClaimUsesTheConfiguredStorageAndBatchSize(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.BatchSize = 7; c.StorageID = "store-x" })

	runOnce(t, h)

	claims := h.store.callsNamed(callClaimReleased)
	require.Len(t, claims, 1)
	assert.Equal(t, "store-x", claims[0].storageID)
	assert.Equal(t, 7, claims[0].limit)
	backlogs := h.store.callsNamed(callReleasedBacklog)
	require.Len(t, backlogs, 1)
	assert.Equal(t, "store-x", backlogs[0].storageID)
}

func TestReleasedSweep_EachClaimWritesADebugLine(t *testing.T) {
	h := newHarness(t)
	h.store.claimReleased = func(context.Context, string, int) (ClaimResult, error) {
		return ClaimResult{Selected: 3, Claimed: []string{"a"}, RowOnly: []string{"b"}}, nil
	}

	runOnce(t, h)

	lines := h.linesFor("gc batch claimed", SweepReleased)
	require.Len(t, lines, 1)
	fields := lines[0].ContextMap()
	assert.EqualValues(t, 3, fields["selected"])
	assert.EqualValues(t, 1, fields["claimed"])
	assert.EqualValues(t, 1, fields["row_only"])
}
