package service_test

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/amagioss/opentams/internal/service"
)

// TC-SVC-METRICS-01: NewAppServiceMetrics registers both metric families.
func TestNewAppServiceMetrics_Success(t *testing.T) {
	reg := prometheus.NewRegistry()
	m, err := service.NewAppServiceMetrics(reg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.Duration == nil || m.Errors == nil {
		t.Error("expected non-nil Duration and Errors")
	}
}

// TC-SVC-METRICS-02: NewAppServiceMetrics fails when duration metric already registered.
func TestNewAppServiceMetrics_DurationConflict(t *testing.T) {
	reg := prometheus.NewRegistry()
	conflict := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Subsystem: "app_service_operation",
		Name:      "duration_seconds",
	}, []string{"app_service", "operation"})
	if err := reg.Register(conflict); err != nil {
		t.Fatalf("pre-register: %v", err)
	}
	_, err := service.NewAppServiceMetrics(reg)
	if err == nil {
		t.Error("expected error when duration metric already registered")
	}
}

// TC-SVC-METRICS-03: NewAppServiceMetrics fails when errors metric already registered.
func TestNewAppServiceMetrics_ErrorsConflict(t *testing.T) {
	reg := prometheus.NewRegistry()
	conflict := prometheus.NewCounterVec(prometheus.CounterOpts{
		Subsystem: "app_service_operation",
		Name:      "errors_total",
	}, []string{"app_service", "operation", "error_code"})
	if err := reg.Register(conflict); err != nil {
		t.Fatalf("pre-register: %v", err)
	}
	_, err := service.NewAppServiceMetrics(reg)
	if err == nil {
		t.Error("expected error when errors metric already registered")
	}
}
