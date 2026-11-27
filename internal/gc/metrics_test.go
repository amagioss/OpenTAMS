package gc

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TC-GC-160: Metric names.
func TestMetrics_EachMetricHasTheSpecifiedNameTypeAndLabels(t *testing.T) {
	h := newHarness(t)
	runOnce(t, h)

	want := []struct {
		name   string
		kind   dto.MetricType
		labels []string
	}{
		{"opentams_gc_items_total", dto.MetricType_COUNTER, []string{"outcome", "sweep"}},
		{"opentams_gc_refcount_drift_total", dto.MetricType_COUNTER, nil},
		{"opentams_gc_errors_total", dto.MetricType_COUNTER, []string{"stage", "sweep"}},
		{"opentams_gc_backlog", dto.MetricType_GAUGE, []string{"state", "sweep"}},
		{"opentams_gc_oldest_overdue_allocation_seconds", dto.MetricType_GAUGE, nil},
		{"opentams_gc_allocations_rows", dto.MetricType_GAUGE, nil},
		{"opentams_gc_last_success_timestamp_seconds", dto.MetricType_GAUGE, []string{"sweep"}},
		{"opentams_gc_batch_duration_seconds", dto.MetricType_HISTOGRAM, []string{"sweep"}},
	}
	for _, w := range want {
		t.Run(w.name, func(t *testing.T) {
			f := h.family(t, w.name)
			require.NotNil(t, f, "metric family is not registered or has no series")
			assert.Equal(t, w.kind, f.GetType())
			for _, m := range f.GetMetric() {
				var names []string
				for _, l := range m.GetLabel() {
					names = append(names, l.GetName())
				}
				slices.Sort(names)
				assert.Equal(t, w.labels, names)
			}
		})
	}
}

func TestMetrics_TheBatchDurationUsesTheDefaultBuckets(t *testing.T) {
	h := newHarness(t)
	runOnce(t, h)

	m := h.find(t, "opentams_gc_batch_duration_seconds", "sweep", SweepReleased)
	require.NotNil(t, m)
	var bounds []float64
	for _, b := range m.GetHistogram().GetBucket() {
		bounds = append(bounds, b.GetUpperBound())
	}
	assert.Equal(t, []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10}, bounds)
}

func TestMetrics_TheLastSuccessTimestampIsAbsentBeforeTheFirstSuccessfulBatch(t *testing.T) {
	h := newHarness(t)
	h.store.claimReleased = func(context.Context, string, int) (ClaimResult, error) {
		return ClaimResult{}, errors.New("database down")
	}

	runOnce(t, h)

	assert.Nil(t, h.find(t, "opentams_gc_last_success_timestamp_seconds", "sweep", SweepReleased))
	assert.Zero(t, h.histogramCount(t, "opentams_gc_batch_duration_seconds", "sweep", SweepReleased))
	assert.NotNil(t, h.find(t, "opentams_gc_last_success_timestamp_seconds", "sweep", SweepAllocation))
}

func TestMetrics_TheBatchDurationComesFromTheWorkerClock(t *testing.T) {
	h := newHarness(t)
	h.store.claimReleased = func(context.Context, string, int) (ClaimResult, error) {
		h.clock.thenReturn(clockStart.Add(2 * time.Second))
		return ClaimResult{}, nil
	}

	runOnce(t, h)

	m := h.find(t, "opentams_gc_batch_duration_seconds", "sweep", SweepReleased)
	require.NotNil(t, m)
	assert.InDelta(t, 2.0, m.GetHistogram().GetSampleSum(), 1e-9)
	assert.Equal(t, float64(clockStart.Add(2*time.Second).Unix()), h.value(t, "opentams_gc_last_success_timestamp_seconds", "sweep", SweepReleased))
}

// TC-GC-161: Gauges after a cycle.
func TestMetrics_TheReleasedBacklogGaugesAreSetAfterACycle(t *testing.T) {
	h := newHarness(t)
	h.store.releasedBacklog = func(context.Context, string) (Backlog, error) {
		return Backlog{Pending: 5, Retrying: 2, UnknownStorage: 1}, nil
	}

	runOnce(t, h)

	assert.Equal(t, 5.0, h.value(t, "opentams_gc_backlog", "sweep", SweepReleased, "state", "pending"))
	assert.Equal(t, 2.0, h.value(t, "opentams_gc_backlog", "sweep", SweepReleased, "state", "retrying"))
	assert.Equal(t, 1.0, h.value(t, "opentams_gc_backlog", "sweep", SweepReleased, "state", "unknown_storage"))
}

// TC-GC-162: The audit line.
func TestMetrics_EachRemovedRowWritesOneAuditLine(t *testing.T) {
	cases := []struct {
		outcome       string
		sweep         string
		setUp         func(*harness)
		wantStorageID string
	}{
		{
			outcome: OutcomeDeleted,
			sweep:   SweepReleased,
			setUp: func(h *harness) {
				h.store.claimReleased = claimed("o1")
			},
			wantStorageID: "default",
		},
		{
			outcome: OutcomeBYOSRowOnly,
			sweep:   SweepReleased,
			setUp: func(h *harness) {
				h.store.claimReleased = func(context.Context, string, int) (ClaimResult, error) {
					return ClaimResult{Selected: 1, RowOnly: []string{"o1"}}, nil
				}
			},
			wantStorageID: "",
		},
		{
			outcome: OutcomeRegisteredLeftover,
			sweep:   SweepAllocation,
			setUp: func(h *harness) {
				h.store.claimAllocations = func(context.Context, string, time.Duration, int) (ClaimResult, error) {
					return ClaimResult{Selected: 1, RowOnly: []string{"o1"}}, nil
				}
			},
			wantStorageID: "default",
		},
	}
	for _, tc := range cases {
		t.Run(tc.outcome, func(t *testing.T) {
			h := newHarness(t)
			tc.setUp(h)

			runOnce(t, h)

			lines := h.linesFor("gc object removed", "")
			require.Len(t, lines, 1)
			assert.Equal(t, map[string]any{
				"object_id":  "o1",
				"storage_id": tc.wantStorageID,
				"sweep":      tc.sweep,
				"outcome":    tc.outcome,
			}, lines[0].ContextMap())
		})
	}
}

// TC-GC-164: A gauge error does not stop the loop.
func TestMetrics_ABacklogErrorIsCountedAndDoesNotFailTheCycle(t *testing.T) {
	h := newHarness(t)
	h.store.releasedBacklog = func(context.Context, string) (Backlog, error) {
		return Backlog{}, errors.New("statement timeout")
	}

	report := runOnce(t, h)

	assert.Equal(t, 1.0, h.value(t, errorsTotal, "sweep", SweepReleased, "stage", "gauge"))
	lines := h.linesFor("gc gauge update failed", SweepReleased)
	require.Len(t, lines, 1)
	assert.Equal(t, "statement timeout", lines[0].ContextMap()["error"])
	assert.False(t, report.Released.BatchError)
	assert.Equal(t, "empty", endedBy(t, h, SweepReleased))
}

func TestMetrics_TheBacklogQueryHasTheDatabaseTimeout(t *testing.T) {
	h := newHarness(t)
	hadDeadline := false
	h.store.releasedBacklog = func(ctx context.Context, _ string) (Backlog, error) {
		_, hadDeadline = ctx.Deadline()
		return Backlog{}, nil
	}

	runOnce(t, h)

	assert.True(t, hadDeadline)
}
