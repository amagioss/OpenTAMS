package gc

import (
	"context"
	"time"
)

// allocationSweep is sweep 2: expired allocations. A leftover allocation
// row has the configured storage ID (BR-GC-40), so its audit line names it.
func (w *Worker) allocationSweep() sweep {
	return sweep{
		name:             SweepAllocation,
		rowOnlyOutcome:   OutcomeRegisteredLeftover,
		rowOnlyStorageID: w.cfg.StorageID,
		reclaim: func(ctx context.Context, cycleStart time.Time) ([]string, error) {
			return w.store.ReclaimRetryAllocations(ctx, w.cfg.StorageID, w.claimLease, cycleStart, w.cfg.BatchSize)
		},
		claim: func(ctx context.Context) (ClaimResult, error) {
			return w.store.ClaimExpiredAllocations(ctx, w.cfg.StorageID, w.cfg.AllocationGrace, w.cfg.BatchSize)
		},
		deleteRows:   w.store.DeleteClaimedAllocations,
		updateGauges: w.updateAllocationGauges,
	}
}

func (w *Worker) updateAllocationGauges(ctx context.Context) error {
	backlog, err := w.store.AllocationBacklog(ctx, w.cfg.StorageID, w.cfg.AllocationGrace)
	if err != nil {
		return err
	}
	w.metrics.setBacklog(SweepAllocation, backlog)
	w.metrics.oldestOverdue.Set(backlog.OldestOverdue)
	w.metrics.allocationRows.Set(float64(backlog.Rows))
	return nil
}
