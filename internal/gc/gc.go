// Package gc is the object garbage collector of OpenTAMS (ADR-0037).
// Sweep 1 deletes released objects (ref_count = 0). Sweep 2 deletes the
// bytes of expired allocations that no segment registered (ADR-0041).
// The design is docs/design/internal-gc/.
package gc

import (
	"context"
	"time"

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
