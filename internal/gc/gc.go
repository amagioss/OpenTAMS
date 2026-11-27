// Package gc is the object garbage collector of OpenTAMS (ADR-0037).
// Sweep 1 deletes released objects (ref_count = 0). Sweep 2 deletes the
// bytes of expired allocations that no segment registered (ADR-0041).
// The design is docs/design/internal-gc/.
package gc

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/amagioss/opentams/internal/objectstore"
)

// Config holds the settings of the worker. cmd/opentams/gc.go fills it
// from config.Config.
type Config struct {
	PollInterval    time.Duration // GC_POLL_INTERVAL, default 15m
	BatchSize       int           // GC_BATCH_SIZE, default 1000, range 1..1000
	AllocationGrace time.Duration // GC_ALLOCATION_GRACE, default 5m
	StorageID       string        // OPENTAMS_OBJECT_STORE_ID, default "default"
}

const (
	dbTimeout  = 30 * time.Second // BR-GC-68
	s3Timeout  = 60 * time.Second // BR-GC-68
	claimLease = 5 * time.Minute  // BR-GC-65
)

const maxBatchSize = 1000

// Sweep names. They are also the values of the metric label "sweep".
const (
	SweepReleased   = "released"
	SweepAllocation = "allocation"
)

// Outcomes. They are the values of the metric label "outcome" and of
// the log field "outcome".
const (
	OutcomeDeleted            = "deleted"
	OutcomeBYOSRowOnly        = "byos_row_only"
	OutcomeRegisteredLeftover = "registered_leftover"
)

// Store is the database surface of the GC. *metastore.PostgresStore
// implements it.
type Store interface {
	ClaimReleasedObjects(ctx context.Context, storageID string, limit int) (ClaimResult, error)
	ReclaimRetryObjects(ctx context.Context, storageID string, lease time.Duration, cycleStart time.Time, limit int) ([]string, error)
	DeleteClaimedObjects(ctx context.Context, ids []string) ([]string, error)
	ReleasedBacklog(ctx context.Context, storageID string) (Backlog, error)

	ClaimExpiredAllocations(ctx context.Context, storageID string, grace time.Duration, limit int) (ClaimResult, error)
	ReclaimRetryAllocations(ctx context.Context, storageID string, lease time.Duration, cycleStart time.Time, limit int) ([]string, error)
	DeleteClaimedAllocations(ctx context.Context, ids []string) ([]string, error)
	AllocationBacklog(ctx context.Context, storageID string, grace time.Duration) (Backlog, error)
}

// ClaimResult is the result of one claim transaction.
type ClaimResult struct {
	Selected int           // rows that the SELECT returned; drives the drain rule
	Claimed  []string      // IDs set to deleting = true, in ascending byte order
	RowOnly  []string      // sweep 1: BYOS rows deleted; sweep 2: leftover allocation rows deleted
	Drift    []DriftRecord // sweep 1 only
}

// DriftRecord is one released object that still had segments.
type DriftRecord struct {
	ObjectID string
	Actual   int64 // real segment count; the stored ref_count was 0
}

// Backlog holds the counts that the gauges of one sweep show.
type Backlog struct {
	Pending        int64
	Retrying       int64
	UnknownStorage int64
	Rows           int64   // sweep 2 only: all rows of allocations
	OldestOverdue  float64 // sweep 2 only: seconds; 0 if Pending is 0
}

// Deleter is the object-store surface of the GC.
// *objectstore.S3Store implements it.
type Deleter interface {
	DeleteObjects(ctx context.Context, ids []string) ([]objectstore.DeleteResult, error)
}

// Report is the result of RunOnce.
type Report struct {
	Released   CycleResult
	Allocation CycleResult
}

// CycleResult is the result of one cycle of one sweep.
type CycleResult struct {
	Deleted    int // rows removed with OutcomeDeleted
	RowOnly    int // rows removed with OutcomeBYOSRowOnly or OutcomeRegisteredLeftover
	Failed     int // per-ID object-store errors
	Drift      int
	BatchError bool // the cycle stopped because of a batch-level error
}

// Worker runs sweep 1 and sweep 2.
type Worker struct {
	store   Store
	deleter Deleter
	cfg     Config
	metrics *Metrics
	log     *zap.Logger

	dbTimeout  time.Duration
	s3Timeout  time.Duration
	claimLease time.Duration
	now        func() time.Time
}

// New returns a Worker. It returns an error if cfg is not valid.
func New(store Store, del Deleter, cfg Config, m *Metrics, log *zap.Logger) (*Worker, error) {
	if err := validate(cfg); err != nil {
		return nil, err
	}
	return &Worker{
		store:      store,
		deleter:    del,
		cfg:        cfg,
		metrics:    m,
		log:        log,
		dbTimeout:  dbTimeout,
		s3Timeout:  s3Timeout,
		claimLease: claimLease,
		now:        time.Now,
	}, nil
}

func validate(cfg Config) error {
	var errs []error
	if cfg.BatchSize < 1 || cfg.BatchSize > maxBatchSize {
		errs = append(errs, fmt.Errorf("gc: BatchSize %d is outside 1..%d", cfg.BatchSize, maxBatchSize))
	}
	if cfg.PollInterval <= 0 {
		errs = append(errs, fmt.Errorf("gc: PollInterval %s must be more than 0", cfg.PollInterval))
	}
	if cfg.AllocationGrace < 0 {
		errs = append(errs, fmt.Errorf("gc: AllocationGrace %s must not be less than 0", cfg.AllocationGrace))
	}
	if cfg.StorageID == "" {
		errs = append(errs, errors.New("gc: StorageID must not be empty"))
	}
	return errors.Join(errs...)
}

// Run starts the two loops and blocks. When stop is closed, the loops
// claim no new batch and return after the current batch. ctx is the hard
// limit. Run returns nil after a shutdown.
func (w *Worker) Run(ctx context.Context, stop <-chan struct{}) error {
	var wg sync.WaitGroup
	for _, s := range []sweep{w.releasedSweep(), w.allocationSweep()} {
		wg.Go(func() { w.loop(ctx, stop, s) })
	}
	wg.Wait()
	return nil
}

// RunOnce does one cycle of each sweep at the same time and returns.
func (w *Worker) RunOnce(ctx context.Context) (Report, error) {
	var report Report
	var wg sync.WaitGroup
	wg.Go(func() { report.Released = w.cycle(ctx, nil, w.releasedSweep()) })
	wg.Go(func() { report.Allocation = w.cycle(ctx, nil, w.allocationSweep()) })
	wg.Wait()
	return report, nil
}

// sweep holds what differs between sweep 1 and sweep 2. The cycle
// algorithm (logic model section 9) is the same for both.
type sweep struct {
	name             string
	rowOnlyOutcome   string
	rowOnlyStorageID string

	reclaim      func(ctx context.Context, cycleStart time.Time) ([]string, error)
	claim        func(ctx context.Context) (ClaimResult, error)
	deleteRows   func(ctx context.Context, ids []string) ([]string, error)
	updateGauges func(ctx context.Context) error
}

// Values of the log field "ended_by".
const (
	endedEmpty      = "empty"
	endedBatchError = "batch_error"
	endedShutdown   = "shutdown"
)

var errBatch = errors.New("gc: batch-level error")

func (w *Worker) loop(ctx context.Context, stop <-chan struct{}, s sweep) {
	for {
		select {
		case <-stop:
			return
		default:
		}
		w.cycle(ctx, stop, s)
		idle := time.NewTimer(w.cfg.PollInterval)
		select {
		case <-stop:
			idle.Stop()
			return
		case <-ctx.Done():
			idle.Stop()
			return
		case <-idle.C:
		}
	}
}

func stopped(ctx context.Context, stop <-chan struct{}) bool {
	select {
	case <-stop:
		return true
	case <-ctx.Done():
		return true
	default:
		return false
	}
}

// cycle has one exit point (BR-GC-60): the gauges and the summary line
// follow each phase result, also a batch-level error and a shutdown.
func (w *Worker) cycle(ctx context.Context, stop <-chan struct{}, s sweep) CycleResult {
	var result CycleResult
	cycleStart := w.now()

	ended := w.retryPhase(ctx, stop, s, cycleStart, &result)
	if ended == "" {
		ended = w.claimPhase(ctx, stop, s, &result)
	}

	if stopped(ctx, stop) {
		ended = endedShutdown
	}
	result.BatchError = ended == endedBatchError
	if ctx.Err() == nil {
		w.updateGauges(ctx, s)
	}
	w.log.Info("gc cycle completed",
		zap.String("sweep", s.name),
		zap.Int("deleted", result.Deleted),
		zap.Int("row_only", result.RowOnly),
		zap.Int("failed", result.Failed),
		zap.Int("drift", result.Drift),
		zap.Int64("duration_ms", w.now().Sub(cycleStart).Milliseconds()),
		zap.String("ended_by", ended),
	)
	return result
}

// retryPhase returns "" when the claim phase may start (BR-GC-64).
func (w *Worker) retryPhase(ctx context.Context, stop <-chan struct{}, s sweep, cycleStart time.Time, result *CycleResult) string {
	for {
		if stopped(ctx, stop) {
			return endedShutdown
		}
		ids, err := withTimeout(ctx, w.dbTimeout, func(ctx context.Context) ([]string, error) {
			return s.reclaim(ctx, cycleStart)
		})
		claimedAt := w.now()
		if err != nil {
			w.batchError(s.name, stageReclaim, err, true)
			return endedBatchError
		}
		if len(ids) > 0 && w.deleteAndRemove(ctx, s, ids, claimedAt, result) != nil {
			return endedBatchError
		}
		if len(ids) < w.cfg.BatchSize {
			return ""
		}
	}
}

// claimPhase is the drain loop of BR-GC-61.
func (w *Worker) claimPhase(ctx context.Context, stop <-chan struct{}, s sweep, result *CycleResult) string {
	for {
		if stopped(ctx, stop) {
			return endedShutdown
		}
		start := w.now()
		claim, err := withTimeout(ctx, w.dbTimeout, s.claim)
		claimedAt := w.now()
		if err != nil {
			w.batchError(s.name, stageClaim, err, true)
			return endedBatchError
		}
		w.log.Debug("gc batch claimed",
			zap.String("sweep", s.name),
			zap.Int("selected", claim.Selected),
			zap.Int("claimed", len(claim.Claimed)),
			zap.Int("row_only", len(claim.RowOnly)),
		)
		for _, id := range claim.RowOnly {
			w.recordRemoved(s.name, id, s.rowOnlyStorageID, s.rowOnlyOutcome)
			result.RowOnly++
		}
		for _, d := range claim.Drift {
			w.log.Warn("gc refcount drift",
				zap.String("object_id", d.ObjectID),
				zap.Int64("stored_ref_count", 0),
				zap.Int64("actual_ref_count", d.Actual),
			)
			w.metrics.refcountDrift.Inc()
			result.Drift++
		}
		if len(claim.Claimed) > 0 && w.deleteAndRemove(ctx, s, claim.Claimed, claimedAt, result) != nil {
			return endedBatchError
		}
		w.metrics.recordBatchSuccess(s.name, start, w.now())
		if claim.Selected < w.cfg.BatchSize {
			return endedEmpty
		}
	}
}

// deleteAndRemove deletes the bytes of ids and then their rows (logic
// model section 9.2). A non-nil result is a batch-level error.
func (w *Worker) deleteAndRemove(ctx context.Context, s sweep, ids []string, claimedAt time.Time, result *CycleResult) error {
	if age := w.now().Sub(claimedAt); age > w.claimLease/2 {
		w.log.Warn("gc batch skipped",
			zap.String("sweep", s.name),
			zap.Int("ids", len(ids)),
			zap.Int64("age_ms", age.Milliseconds()),
		)
		return nil
	}

	results, err := withTimeout(ctx, w.s3Timeout, func(ctx context.Context) ([]objectstore.DeleteResult, error) {
		return w.deleter.DeleteObjects(ctx, ids)
	})
	if err != nil {
		w.batchError(s.name, stageS3Delete, err, true)
		return errBatch
	}

	ok := make([]string, 0, len(results))
	var firstErr, authErr error
	for _, r := range results {
		if r.Error == nil {
			ok = append(ok, r.ID)
			continue
		}
		w.metrics.countError(s.name, stageS3Delete)
		w.log.Warn("gc object delete failed",
			zap.String("sweep", s.name),
			zap.String("object_id", r.ID),
			zap.Error(r.Error),
		)
		result.Failed++
		if firstErr == nil {
			firstErr = r.Error
		}
		if authErr == nil && errors.Is(r.Error, objectstore.ErrAuth) {
			authErr = r.Error
		}
	}

	if len(ok) > 0 {
		removed, err := withTimeout(ctx, w.dbTimeout, func(ctx context.Context) ([]string, error) {
			return s.deleteRows(ctx, ok)
		})
		if err != nil {
			w.batchError(s.name, stageRowDelete, err, true)
			return errBatch
		}
		for _, id := range removed {
			w.recordRemoved(s.name, id, w.cfg.StorageID, OutcomeDeleted)
			result.Deleted++
		}
	}

	if authErr != nil {
		w.batchError(s.name, stageS3Delete, authErr, false)
		return errBatch
	}
	if len(ok) == 0 && firstErr != nil {
		w.batchError(s.name, stageS3Delete, firstErr, false)
		return errBatch
	}
	return nil
}

// batchError reports a batch-level error (BR-GC-62). count is false when
// BR-GC-63 already counted each failed ID.
func (w *Worker) batchError(sweepName, stage string, err error, count bool) {
	w.log.Error("gc batch error",
		zap.String("sweep", sweepName),
		zap.String("stage", stage),
		zap.Error(err),
	)
	if count {
		w.metrics.countError(sweepName, stage)
	}
}

func (w *Worker) recordRemoved(sweepName, objectID, storageID, outcome string) {
	w.log.Info("gc object removed",
		zap.String("object_id", objectID),
		zap.String("storage_id", storageID),
		zap.String("sweep", sweepName),
		zap.String("outcome", outcome),
	)
	w.metrics.countItems(sweepName, outcome)
}

func (w *Worker) updateGauges(ctx context.Context, s sweep) {
	ctx, cancel := context.WithTimeout(ctx, w.dbTimeout)
	defer cancel()
	if err := s.updateGauges(ctx); err != nil {
		w.metrics.countError(s.name, stageGauge)
		w.log.Warn("gc gauge update failed", zap.String("sweep", s.name), zap.Error(err))
	}
}

func withTimeout[T any](ctx context.Context, timeout time.Duration, fn func(context.Context) (T, error)) (T, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return fn(ctx)
}
