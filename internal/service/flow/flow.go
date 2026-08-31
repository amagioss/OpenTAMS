// Package flow provides the service layer for Flow operations.
// It sits between HTTP handlers and the metastore, enforcing business rules
// (vfr/frame_rate validation) and emitting Prometheus metrics. It has no
// objectstore dependency: object lifetime belongs to the GC worker.
package flow

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"

	"github.com/amagioss/opentams/internal/apperror"
	"github.com/amagioss/opentams/internal/metastore"
	"github.com/amagioss/opentams/internal/service"
	"github.com/amagioss/opentams/internal/timerange"
)

// FlowService is the interface the HTTP handler layer depends on.
type FlowService interface {
	UpsertFlow(ctx context.Context, f *metastore.Flow) (created bool, err error)
	GetFlow(ctx context.Context, id uuid.UUID, includeTimerange bool, trFilter *timerange.TimeRange) (*metastore.Flow, error)
	ListFlows(ctx context.Context, p metastore.ListFlowsParams) (*metastore.FlowPage, error)
	DeleteFlow(ctx context.Context, id uuid.UUID) error

	PutFlowTag(ctx context.Context, id uuid.UUID, name string, value json.RawMessage) error
	DeleteFlowTag(ctx context.Context, id uuid.UUID, name string) error
	PutFlowLabel(ctx context.Context, id uuid.UUID, label string) error
	DeleteFlowLabel(ctx context.Context, id uuid.UUID) error
	PutFlowDescription(ctx context.Context, id uuid.UUID, desc string) error
	DeleteFlowDescription(ctx context.Context, id uuid.UUID) error
	PutFlowReadOnly(ctx context.Context, id uuid.UUID, readOnly bool) error
	PutFlowCollection(ctx context.Context, id uuid.UUID, items []metastore.CollectionItem) error
	DeleteFlowCollection(ctx context.Context, id uuid.UUID) error
	PutFlowAvgBitRate(ctx context.Context, id uuid.UUID, rate int64) error
	DeleteFlowAvgBitRate(ctx context.Context, id uuid.UUID) error
	PutFlowMaxBitRate(ctx context.Context, id uuid.UUID, rate int64) error
	DeleteFlowMaxBitRate(ctx context.Context, id uuid.UUID) error
}

type flowService struct {
	store metastore.FlowStore
	log   *zap.Logger
	dur   *prometheus.HistogramVec
	errs  *prometheus.CounterVec
}

// New constructs a FlowService. m must be the shared AppServiceMetrics instance
// created at application startup via service.NewAppServiceMetrics.
//
// The flow service has no objectstore dependency: zero-ref reaping is the
// GC worker's responsibility (D-25 / D-29). DeleteFlow returns once the
// metastore tx commits.
func New(store metastore.FlowStore, log *zap.Logger, m *service.AppServiceMetrics) FlowService {
	return &flowService{store: store, log: log, dur: m.Duration, errs: m.Errors}
}

func (s *flowService) observe(op string, d time.Duration, err error) {
	s.dur.WithLabelValues("flow", op).Observe(d.Seconds())
	if err != nil {
		code := "internal"
		var ae *apperror.AppError
		if errors.As(err, &ae) {
			code = string(ae.Code)
		}
		s.errs.WithLabelValues("flow", op, code).Inc()
	}
}

// validateVideoFlow checks vfr/frame_rate consistency for video flows.
// Only called when EssenceParameters is non-empty and format contains "video".
func validateVideoFlow(f *metastore.Flow) error {
	if len(f.EssenceParameters) == 0 {
		return nil
	}
	if !strings.Contains(f.Format, "video") {
		return nil
	}
	var ep struct {
		VFR       *bool           `json:"vfr"`
		FrameRate json.RawMessage `json:"frame_rate"`
	}
	if err := json.Unmarshal(f.EssenceParameters, &ep); err != nil {
		return apperror.New(apperror.ErrSchemaValidation, "essence_parameters: invalid JSON")
	}
	hasFrameRate := len(ep.FrameRate) > 0
	if (ep.VFR == nil || !*ep.VFR) && !hasFrameRate {
		return apperror.New(apperror.ErrVFRFrameRateConflict, "frame_rate required when vfr is false or absent for video flows")
	}
	if ep.VFR != nil && *ep.VFR && hasFrameRate {
		return apperror.New(apperror.ErrVFRFrameRateConflict, "frame_rate must not be set when vfr is true")
	}
	return nil
}

func (s *flowService) UpsertFlow(ctx context.Context, f *metastore.Flow) (bool, error) {
	start := time.Now()
	if err := validateVideoFlow(f); err != nil {
		s.observe("upsert", time.Since(start), err)
		return false, err
	}
	created, err := s.store.UpsertFlow(ctx, f)
	s.observe("upsert", time.Since(start), err)
	return created, err
}

func (s *flowService) GetFlow(ctx context.Context, id uuid.UUID, includeTimerange bool, trFilter *timerange.TimeRange) (*metastore.Flow, error) {
	start := time.Now()
	f, err := s.store.GetFlow(ctx, id)
	if err != nil {
		s.observe("get", time.Since(start), err)
		return nil, err
	}
	if includeTimerange {
		tr, err := s.store.GetFlowTimerange(ctx, id)
		if err != nil {
			s.observe("get", time.Since(start), err)
			return nil, err
		}
		if tr != nil {
			if trFilter != nil {
				computed, err := timerange.Parse(*tr)
				if err != nil {
					s.observe("get", time.Since(start), err)
					return nil, err
				}
				result := timerange.Intersect(computed, *trFilter)
				str := result.String()
				f.Timerange = &str
			} else {
				f.Timerange = tr
			}
		}
	}
	s.observe("get", time.Since(start), nil)
	return f, nil
}

func (s *flowService) ListFlows(ctx context.Context, p metastore.ListFlowsParams) (*metastore.FlowPage, error) {
	start := time.Now()
	page, err := s.store.ListFlows(ctx, p)
	s.observe("list", time.Since(start), err)
	return page, err
}

func (s *flowService) DeleteFlow(ctx context.Context, id uuid.UUID) error {
	start := time.Now()
	err := s.store.DeleteFlow(ctx, id)
	s.observe("delete", time.Since(start), err)
	return err
}

func (s *flowService) PutFlowTag(ctx context.Context, id uuid.UUID, name string, value json.RawMessage) error {
	return s.store.PutFlowTag(ctx, id, name, value)
}

func (s *flowService) DeleteFlowTag(ctx context.Context, id uuid.UUID, name string) error {
	return s.store.DeleteFlowTag(ctx, id, name)
}

func (s *flowService) PutFlowLabel(ctx context.Context, id uuid.UUID, label string) error {
	return s.store.PutFlowLabel(ctx, id, label)
}

func (s *flowService) DeleteFlowLabel(ctx context.Context, id uuid.UUID) error {
	return s.store.DeleteFlowLabel(ctx, id)
}

func (s *flowService) PutFlowDescription(ctx context.Context, id uuid.UUID, desc string) error {
	return s.store.PutFlowDescription(ctx, id, desc)
}

func (s *flowService) DeleteFlowDescription(ctx context.Context, id uuid.UUID) error {
	return s.store.DeleteFlowDescription(ctx, id)
}

func (s *flowService) PutFlowReadOnly(ctx context.Context, id uuid.UUID, readOnly bool) error {
	return s.store.PutFlowReadOnly(ctx, id, readOnly)
}

func (s *flowService) PutFlowCollection(ctx context.Context, id uuid.UUID, items []metastore.CollectionItem) error {
	return s.store.PutFlowCollection(ctx, id, items)
}

func (s *flowService) DeleteFlowCollection(ctx context.Context, id uuid.UUID) error {
	return s.store.DeleteFlowCollection(ctx, id)
}

func (s *flowService) PutFlowAvgBitRate(ctx context.Context, id uuid.UUID, rate int64) error {
	return s.store.PutFlowAvgBitRate(ctx, id, rate)
}

func (s *flowService) DeleteFlowAvgBitRate(ctx context.Context, id uuid.UUID) error {
	return s.store.DeleteFlowAvgBitRate(ctx, id)
}

func (s *flowService) PutFlowMaxBitRate(ctx context.Context, id uuid.UUID, rate int64) error {
	return s.store.PutFlowMaxBitRate(ctx, id, rate)
}

func (s *flowService) DeleteFlowMaxBitRate(ctx context.Context, id uuid.UUID) error {
	return s.store.DeleteFlowMaxBitRate(ctx, id)
}
