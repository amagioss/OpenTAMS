package gc

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/amagioss/opentams/internal/objectstore"
)

// TC-GC-117: Sweep 2 leftovers and its outcome.
func TestAllocationSweep_LeftoversAreRowOnlyWithTheRegisteredLeftoverOutcome(t *testing.T) {
	h := newHarness(t)
	h.store.claimAllocations = func(context.Context, string, time.Duration, int) (ClaimResult, error) {
		return ClaimResult{Selected: 1, RowOnly: []string{"l1"}}, nil
	}

	report := runOnce(t, h)

	assert.Empty(t, h.deleter.callIDs())
	assert.Equal(t, 1, report.Allocation.RowOnly)
	assert.Equal(t, 1.0, h.value(t, itemsTotal, "sweep", SweepAllocation, "outcome", OutcomeRegisteredLeftover))
	lines := h.linesFor("gc object removed", SweepAllocation)
	require.Len(t, lines, 1)
	assert.Equal(t, OutcomeRegisteredLeftover, lines[0].ContextMap()["outcome"])
}

func TestAllocationSweep_ClaimedAllocationsAreDeletedThenRemoved(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.AllocationGrace = 7 * time.Minute })
	var gotGrace time.Duration
	h.store.claimAllocations = func(_ context.Context, _ string, grace time.Duration, _ int) (ClaimResult, error) {
		gotGrace = grace
		return ClaimResult{Selected: 2, Claimed: []string{"x", "y"}}, nil
	}
	h.deleter.deleteObjects = failIDs(objectstore.ErrTransient, "y")

	report := runOnce(t, h)

	assert.Equal(t, 7*time.Minute, gotGrace)
	assert.Equal(t, [][]string{{"x", "y"}}, h.deleter.callIDs())
	rowDeletes := h.store.callsNamed(callDeleteAllocations)
	require.Len(t, rowDeletes, 1)
	assert.Equal(t, []string{"x"}, rowDeletes[0].ids)
	assert.Empty(t, h.store.callsNamed(callDeleteObjects))
	assert.Equal(t, CycleResult{Deleted: 1, Failed: 1}, report.Allocation)
	assert.Equal(t, 1.0, h.value(t, itemsTotal, "sweep", SweepAllocation, "outcome", OutcomeDeleted))
	assert.Equal(t, 1.0, h.value(t, errorsTotal, "sweep", SweepAllocation, "stage", "s3_delete"))
}

func TestAllocationSweep_TheRetryPhaseUsesTheAllocationReclaim(t *testing.T) {
	h := newHarness(t)
	h.store.reclaimAllocations = func(context.Context, string, time.Duration, time.Time, int) ([]string, error) {
		return []string{"r1"}, nil
	}

	report := runOnce(t, h)

	reclaims := h.store.callsNamed(callReclaimAllocations)
	require.Len(t, reclaims, 1)
	assert.Equal(t, claimLease, reclaims[0].lease)
	assert.Equal(t, clockStart, reclaims[0].cycleStart)
	assert.Less(t, h.store.indexOf(callReclaimAllocations), h.store.indexOf(callClaimAllocations))
	assert.Equal(t, 1, report.Allocation.Deleted)
}

func TestAllocationSweep_GaugesShowTheAllocationBacklog(t *testing.T) {
	h := newHarness(t)
	h.store.allocationBacklog = func(context.Context, string, time.Duration) (Backlog, error) {
		return Backlog{Pending: 4, Retrying: 3, UnknownStorage: 2, Rows: 40, OldestOverdue: 12.5}, nil
	}

	runOnce(t, h)

	assert.Equal(t, 4.0, h.value(t, "opentams_gc_backlog", "sweep", SweepAllocation, "state", "pending"))
	assert.Equal(t, 3.0, h.value(t, "opentams_gc_backlog", "sweep", SweepAllocation, "state", "retrying"))
	assert.Equal(t, 2.0, h.value(t, "opentams_gc_backlog", "sweep", SweepAllocation, "state", "unknown_storage"))
	assert.Equal(t, 40.0, h.value(t, "opentams_gc_allocations_rows"))
	assert.Equal(t, 12.5, h.value(t, "opentams_gc_oldest_overdue_allocation_seconds"))
}
