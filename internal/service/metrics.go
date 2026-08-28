// Package service provides shared infrastructure for the OpenTAMS service layer.
package service

import (
	"fmt"

	"github.com/prometheus/client_golang/prometheus"
)

// AppServiceMetrics holds the shared Prometheus metric families for all
// service-layer operations. Create once at application startup via
// NewAppServiceMetrics and inject into each service constructor.
//
// Metric names (after namespace prefix applied by the app registry):
//
//	opentams_app_service_operation_duration_seconds{app_service, operation}
//	opentams_app_service_operation_errors_total{app_service, operation, error_code}
//	opentams_app_service_operation_total{app_service, operation, outcome}
//	opentams_app_service_operation_items_total{app_service, operation, outcome}
//
// `Total` counts requests (one increment per call) labelled by the
// per-call outcome (e.g. all_accepted, partial, all_rejected, success,
// not_found). `ItemsTotal` counts items processed within a single
// request — it answers "how many items were accepted vs failed" for
// operations that batch multiple items per call (RegisterBatch).
// Operations without an item dimension simply don't increment it.
type AppServiceMetrics struct {
	Duration *prometheus.HistogramVec
	Errors   *prometheus.CounterVec
	OpsTotal *prometheus.CounterVec
	OpsItems *prometheus.CounterVec
}

// NewAppServiceMetrics creates and registers the shared service-layer metric
// families. Call exactly once at application startup; pass the result to all
// service constructors that need it.
func NewAppServiceMetrics(reg prometheus.Registerer) (*AppServiceMetrics, error) {
	dur := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Subsystem: "app_service_operation",
		Name:      "duration_seconds",
		Help:      "Duration of app service operations in seconds.",
		Buckets:   prometheus.DefBuckets,
	}, []string{"app_service", "operation"})

	errs := prometheus.NewCounterVec(prometheus.CounterOpts{
		Subsystem: "app_service_operation",
		Name:      "errors_total",
		Help:      "Total errors from app service operations.",
	}, []string{"app_service", "operation", "error_code"})

	opsTotal := prometheus.NewCounterVec(prometheus.CounterOpts{
		Subsystem: "app_service_operation",
		Name:      "total",
		Help:      "Total app service operations partitioned by outcome (per call).",
	}, []string{"app_service", "operation", "outcome"})

	opsItems := prometheus.NewCounterVec(prometheus.CounterOpts{
		Subsystem: "app_service_operation",
		Name:      "items_total",
		Help:      "Total items processed by app service operations partitioned by per-item outcome.",
	}, []string{"app_service", "operation", "outcome"})

	if err := reg.Register(dur); err != nil {
		return nil, fmt.Errorf("service metrics: register duration: %w", err)
	}
	if err := reg.Register(errs); err != nil {
		return nil, fmt.Errorf("service metrics: register errors: %w", err)
	}
	if err := reg.Register(opsTotal); err != nil {
		return nil, fmt.Errorf("service metrics: register ops total: %w", err)
	}
	if err := reg.Register(opsItems); err != nil {
		return nil, fmt.Errorf("service metrics: register ops items: %w", err)
	}

	return &AppServiceMetrics{
		Duration: dur,
		Errors:   errs,
		OpsTotal: opsTotal,
		OpsItems: opsItems,
	}, nil
}
