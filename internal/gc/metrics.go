package gc

import (
	"fmt"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

const subsystem = "gc"

// Values of the metric label "stage".
const (
	stageReclaim   = "reclaim"
	stageClaim     = "claim"
	stageS3Delete  = "s3_delete"
	stageRowDelete = "row_delete"
	stageGauge     = "gauge"
)

// Values of the metric label "state".
const (
	statePending        = "pending"
	stateRetrying       = "retrying"
	stateUnknownStorage = "unknown_storage"
)

// Metrics holds the Prometheus metrics of the GC (interface spec
// section 4). Make it with NewMetrics.
type Metrics struct {
	items          *prometheus.CounterVec
	refcountDrift  prometheus.Counter
	errors         *prometheus.CounterVec
	backlog        *prometheus.GaugeVec
	oldestOverdue  prometheus.Gauge
	allocationRows prometheus.Gauge
	lastSuccess    *prometheus.GaugeVec
	batchDuration  *prometheus.HistogramVec
}

// NewMetrics registers the metrics of the GC on reg with the subsystem
// "gc". cmd/opentams passes Registry.NamespacedRegisterer(), which adds
// the namespace "opentams".
func NewMetrics(reg prometheus.Registerer) (*Metrics, error) {
	m := &Metrics{
		items: prometheus.NewCounterVec(prometheus.CounterOpts{
			Subsystem: subsystem,
			Name:      "items_total",
			Help:      "Rows that the GC removed, by sweep and outcome.",
		}, []string{"sweep", "outcome"}),
		refcountDrift: prometheus.NewCounter(prometheus.CounterOpts{
			Subsystem: subsystem,
			Name:      "refcount_drift_total",
			Help:      "Released objects that still had segments; the GC corrected ref_count.",
		}),
		errors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Subsystem: subsystem,
			Name:      "errors_total",
			Help:      "GC errors, by sweep and stage.",
		}, []string{"sweep", "stage"}),
		backlog: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Subsystem: subsystem,
			Name:      "backlog",
			Help:      "Rows that wait for the GC, by sweep and state. Updated at the end of each cycle.",
		}, []string{"sweep", "state"}),
		oldestOverdue: prometheus.NewGauge(prometheus.GaugeOpts{
			Subsystem: subsystem,
			Name:      "oldest_overdue_allocation_seconds",
			Help:      "Time since the oldest sweep 2 candidate passed expires_at plus the allocation grace.",
		}),
		allocationRows: prometheus.NewGauge(prometheus.GaugeOpts{
			Subsystem: subsystem,
			Name:      "allocations_rows",
			Help:      "All rows of the allocations table.",
		}),
		lastSuccess: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Subsystem: subsystem,
			Name:      "last_success_timestamp_seconds",
			Help:      "Unix time of the last successful claim batch, by sweep.",
		}, []string{"sweep"}),
		batchDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Subsystem: subsystem,
			Name:      "batch_duration_seconds",
			Help:      "Time of one claim batch, from the claim to the row delete.",
			Buckets:   prometheus.DefBuckets,
		}, []string{"sweep"}),
	}

	collectors := []struct {
		name string
		c    prometheus.Collector
	}{
		{"items_total", m.items},
		{"refcount_drift_total", m.refcountDrift},
		{"errors_total", m.errors},
		{"backlog", m.backlog},
		{"oldest_overdue_allocation_seconds", m.oldestOverdue},
		{"allocations_rows", m.allocationRows},
		{"last_success_timestamp_seconds", m.lastSuccess},
		{"batch_duration_seconds", m.batchDuration},
	}
	for _, c := range collectors {
		if err := reg.Register(c.c); err != nil {
			return nil, fmt.Errorf("gc metrics: register %s: %w", c.name, err)
		}
	}

	m.initialiseSeries()
	return m, nil
}

// initialiseSeries exports the counters and the histogram at zero, so
// that increase() works from the first event. last_success_timestamp_seconds
// stays absent until the first successful batch (alert GCDown).
func (m *Metrics) initialiseSeries() {
	outcomes := map[string][]string{
		SweepReleased:   {OutcomeDeleted, OutcomeBYOSRowOnly},
		SweepAllocation: {OutcomeDeleted, OutcomeRegisteredLeftover},
	}
	for sweepName, sweepOutcomes := range outcomes {
		for _, outcome := range sweepOutcomes {
			m.items.WithLabelValues(sweepName, outcome)
		}
		for _, stage := range []string{stageReclaim, stageClaim, stageS3Delete, stageRowDelete, stageGauge} {
			m.errors.WithLabelValues(sweepName, stage)
		}
		m.batchDuration.WithLabelValues(sweepName)
	}
}

func (m *Metrics) countItems(sweepName, outcome string) {
	m.items.WithLabelValues(sweepName, outcome).Inc()
}

func (m *Metrics) countError(sweepName, stage string) {
	m.errors.WithLabelValues(sweepName, stage).Inc()
}

func (m *Metrics) setBacklog(sweepName string, b Backlog) {
	m.backlog.WithLabelValues(sweepName, statePending).Set(float64(b.Pending))
	m.backlog.WithLabelValues(sweepName, stateRetrying).Set(float64(b.Retrying))
	m.backlog.WithLabelValues(sweepName, stateUnknownStorage).Set(float64(b.UnknownStorage))
}

func (m *Metrics) recordBatchSuccess(sweepName string, start, end time.Time) {
	m.lastSuccess.WithLabelValues(sweepName).Set(float64(end.UnixNano()) / float64(time.Second))
	m.batchDuration.WithLabelValues(sweepName).Observe(end.Sub(start).Seconds())
}
