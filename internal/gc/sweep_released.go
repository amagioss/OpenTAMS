package gc

import (
	"context"
	"time"
)

// releasedSweep is sweep 1: released objects (ref_count = 0).
func (w *Worker) releasedSweep() sweep {
	return sweep{
		name:             SweepReleased,
		rowOnlyOutcome:   OutcomeBYOSRowOnly,
		rowOnlyStorageID: "",
		reclaim: func(ctx context.Context, cycleStart time.Time) ([]string, error) {
			return w.store.ReclaimRetryObjects(ctx, w.cfg.StorageID, w.claimLease, cycleStart, w.cfg.BatchSize)
		},
		claim: func(ctx context.Context) (ClaimResult, error) {
			return w.store.ClaimReleasedObjects(ctx, w.cfg.StorageID, w.cfg.BatchSize)
		},
		deleteRows:   w.store.DeleteClaimedObjects,
		updateGauges: w.updateReleasedGauges,
	}
}

func (w *Worker) updateReleasedGauges(ctx context.Context) error {
	backlog, err := w.store.ReleasedBacklog(ctx, w.cfg.StorageID)
	if err != nil {
		return err
	}
	w.metrics.setBacklog(SweepReleased, backlog)
	return nil
}
