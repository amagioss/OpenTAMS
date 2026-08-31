// Package segment provides the service layer for Segment operations.
// It sits between HTTP handlers and the metastore/objectstore backends,
// delegating persistence to the store. Object reclamation on delete is the
// garbage collector's job, not this package's.
package segment

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"

	"github.com/amagioss/opentams/internal/apperror"
	"github.com/amagioss/opentams/internal/domain"
	"github.com/amagioss/opentams/internal/metastore"
	"github.com/amagioss/opentams/internal/service"
)

// Service is the segments service contract; see
// docs/design/internal-service-segment/functional-design/functional-design.md.
//
// All methods MUST be safe for concurrent use by multiple goroutines.
type Service interface {
	RegisterBatch(ctx context.Context, req domain.RegisterParams) (domain.RegisterResult, error)
	List(ctx context.Context, req domain.ListParams) (domain.SegmentPage, error)
	Delete(ctx context.Context, req domain.DeleteParams) (domain.DeleteResult, error)
}

// Deps is the constructor injection bundle. An earlier design draft
// prescribed a `Clock` field; Phase 1 has no clock-dependent code path so it is
// omitted to match the codebase's existing service shape (flow,
// storage). The struct intentionally has NO ObjectStore field — the
// service does not call the object store on any path (D-25, INV-SEG-06).
//
// ControlledStorageID is the deployment-wide controlled-storage backend
// id (BR-META-07; D-24). Service forwards it verbatim into
// metastore.InsertBatch on every RegisterBatch call. Empty value
// disables controlled writes — controlled segments fail with
// metastore.ErrControlledStorageNotConfigured. BYOS-only deployments
// can leave it empty.
type Deps struct {
	Meta                metastore.Store
	Logger              *zap.Logger
	Metrics             *service.AppServiceMetrics
	ControlledStorageID string
}

// segmentService is the production implementation backed by metastore.Store.
type segmentService struct {
	meta                metastore.Store
	log                 *zap.Logger
	dur                 *prometheus.HistogramVec
	errs                *prometheus.CounterVec
	opsTotal            *prometheus.CounterVec
	opsItems            *prometheus.CounterVec
	controlledStorageID string
}

// New constructs a segments-v1 Service. Logger defaults to a no-op when
// nil. Metrics MUST be non-nil — the service emits per-operation
// duration and outcome counters (NFR-SEG-OBS-01 / NFR-SEG-OBS-02).
func New(deps Deps) Service {
	log := deps.Logger
	if log == nil {
		log = zap.NewNop()
	}
	return &segmentService{
		meta:                deps.Meta,
		log:                 log,
		dur:                 deps.Metrics.Duration,
		errs:                deps.Metrics.Errors,
		opsTotal:            deps.Metrics.OpsTotal,
		opsItems:            deps.Metrics.OpsItems,
		controlledStorageID: deps.ControlledStorageID,
	}
}

// Sentinel errors. Distinct from metastore.* sentinels at the package
// boundary; the service does NOT redeclare ErrSegmentOverlap — that
// flows up unwrapped from metastore (interface.md, single-source rule).
var (
	ErrFlowNotFound   = errors.New("segment: flow not found")
	ErrFlowReadOnly   = errors.New("segment: flow is read-only")
	ErrInvalidRequest = errors.New("segment: invalid request")
)

func (s *segmentService) observe(op string, d time.Duration, err error) {
	s.dur.WithLabelValues("segment", op).Observe(d.Seconds())
	if err != nil {
		code := classifyErr(err)
		s.errs.WithLabelValues("segment", op, code).Inc()
	}
}

// classifyErr maps a wrapped error to a metric label. Sentinels first,
// then apperror codes, then "internal" as a catch-all.
func classifyErr(err error) string {
	switch {
	case errors.Is(err, ErrFlowNotFound), errors.Is(err, metastore.ErrFlowNotFound):
		return string(apperror.ErrNotFound)
	case errors.Is(err, ErrFlowReadOnly), errors.Is(err, metastore.ErrFlowReadOnly):
		return string(apperror.ErrReadOnly)
	case errors.Is(err, ErrInvalidRequest):
		return string(apperror.ErrSchemaValidation)
	case errors.Is(err, metastore.ErrSegmentOverlap):
		return string(apperror.ErrSegmentOverlap)
	case errors.Is(err, metastore.ErrSegmentObjectReaping):
		return string(apperror.ErrObjectReaping)
	case errors.Is(err, metastore.ErrInvalidCursor):
		return string(apperror.ErrSchemaValidation)
	}
	var ae *apperror.AppError
	if errors.As(err, &ae) && ae.Code != "" {
		return string(ae.Code)
	}
	return "internal"
}

// RegisterBatch validates the request, hands accepted segments to the
// metastore, and assembles the (Outcome, Accepted, Failed) tuple. On
// any overlap (within-batch or against-existing) the metastore returns
// ErrSegmentOverlap; the service forwards that wrapped — whole-batch
// reject per BR-SEG-03.
func (s *segmentService) RegisterBatch(ctx context.Context, req domain.RegisterParams) (domain.RegisterResult, error) {
	start := time.Now()
	res, err := s.registerBatch(ctx, req)
	s.observe("register", time.Since(start), err)
	return res, err
}

func (s *segmentService) registerBatch(ctx context.Context, req domain.RegisterParams) (domain.RegisterResult, error) {
	if len(req.Segments) == 0 {
		return domain.RegisterResult{}, fmt.Errorf("segment: register: empty batch: %w", ErrInvalidRequest)
	}

	// Per-segment validation the handler/openapi cannot cover: empty
	// ObjectID. Empty Timerange is impossible because conversion
	// rejects it during parse; defensive check kept for direct callers.
	failed := make([]domain.FailedSegment, 0)
	survivors := make([]domain.Segment, 0, len(req.Segments))
	survivorIdx := make([]int, 0, len(req.Segments))
	for i := range req.Segments {
		seg := req.Segments[i]
		// Stamp Controlled=false on every supplied GetURL — INV-SEG-? /
		// REQ-SEG-06. The conversion package already does this for the
		// HTTP path; service-level safety net for direct callers.
		for j := range seg.GetURLs {
			seg.GetURLs[j].Controlled = false
		}
		if seg.ObjectID == "" {
			failed = append(failed, domain.FailedSegment{
				Segment: seg,
				Reason:  "object_id is required",
				Type:    apperror.New(apperror.ErrSchemaValidation, "").ToProblemDetails("", "").Type,
				Title:   "Schema Validation Failed",
				Status:  400,
			})
			continue
		}
		survivors = append(survivors, seg)
		survivorIdx = append(survivorIdx, i)
	}

	if len(survivors) == 0 {
		res := domain.RegisterResult{
			Outcome:  domain.OutcomeAllRejected,
			Accepted: nil,
			Failed:   failed,
		}
		s.emitRegisterObservability(req.FlowID.String(), res)
		return res, nil
	}

	insertRes, err := s.meta.InsertSegments(ctx, metastore.InsertBatch{
		FlowID:              req.FlowID,
		Segments:            survivors,
		ControlledStorageID: s.controlledStorageID,
	})
	if err != nil {
		switch {
		case errors.Is(err, metastore.ErrFlowNotFound):
			return domain.RegisterResult{}, fmt.Errorf("segment: register: %w", ErrFlowNotFound)
		case errors.Is(err, metastore.ErrFlowReadOnly):
			return domain.RegisterResult{}, fmt.Errorf("segment: register: %w", ErrFlowReadOnly)
		case errors.Is(err, metastore.ErrSegmentOverlap), errors.Is(err, metastore.ErrSegmentObjectReaping):
			return domain.RegisterResult{}, err
		default:
			return domain.RegisterResult{}, fmt.Errorf("segment: register: %w", err)
		}
	}

	accepted := make([]domain.Segment, 0, len(insertRes.AcceptedIndices))
	acceptedSet := make(map[int]struct{}, len(insertRes.AcceptedIndices))
	for _, localIdx := range insertRes.AcceptedIndices {
		accepted = append(accepted, survivors[localIdx])
		acceptedSet[localIdx] = struct{}{}
	}
	for k, localIdx := range survivorIdx {
		if _, ok := acceptedSet[k]; ok {
			continue
		}
		// Look up the parallel reject reason if the metastore supplied
		// one; otherwise generic schema-validation. The metastore today
		// returns whole-batch on overlap and otherwise accepts all
		// survivors — this branch is reachable only when the metastore
		// adds per-segment rejection in the future.
		reason := "rejected by metastore"
		typeURI := apperror.New(apperror.ErrSchemaValidation, "").ToProblemDetails("", "").Type
		title := "Schema Validation Failed"
		for ri, idx := range insertRes.RejectedIndices {
			if idx == k && ri < len(insertRes.RejectReasons) {
				reason = insertRes.RejectReasons[ri].Detail
				if insertRes.RejectReasons[ri].Type != "" {
					typeURI = insertRes.RejectReasons[ri].Type
				}
			}
		}
		failed = append(failed, domain.FailedSegment{
			Segment: survivors[localIdx],
			Reason:  reason,
			Type:    typeURI,
			Title:   title,
			Status:  400,
		})
	}

	res := domain.RegisterResult{Accepted: accepted, Failed: failed}
	switch {
	case len(failed) == 0 && len(accepted) > 0:
		res.Outcome = domain.OutcomeAllAccepted
	case len(accepted) == 0 && len(failed) > 0:
		res.Outcome = domain.OutcomeAllRejected
	default:
		res.Outcome = domain.OutcomePartial
	}

	s.emitRegisterObservability(req.FlowID.String(), res)
	return res, nil
}

// emitRegisterObservability emits the per-failed-segment WARN logs
// (SCN-SEG-20 / NFR-SEG-OBS-03) and the outcome counters
// (SCN-SEG-21 / NFR-SEG-OBS-02). Summary counts (segments_count /
// segments_failed) live on the metric families, not on a per-call WARN
// — the contract is one WARN per failed segment, no duplicate
// aggregate WARN.
func (s *segmentService) emitRegisterObservability(flowID string, res domain.RegisterResult) {
	for i := range res.Failed {
		fs := res.Failed[i]
		s.log.Warn("segment: register failed",
			zap.String("domain", "segments"),
			zap.String("action", "register"),
			zap.String("outcome", "partial_failure"),
			zap.String("failure_type", fs.Type),
			zap.String("object_id", fs.Segment.ObjectID),
			zap.String("timerange", fs.Segment.Timerange.String()),
			zap.String("flow_id", flowID),
		)
	}
	s.opsTotal.WithLabelValues("segment", "register", outcomeLabel(res.Outcome)).Inc()
	if n := len(res.Accepted); n > 0 {
		s.opsItems.WithLabelValues("segment", "register", "accepted").Add(float64(n))
	}
	if n := len(res.Failed); n > 0 {
		s.opsItems.WithLabelValues("segment", "register", "failed").Add(float64(n))
	}
}

// outcomeLabel maps domain.RegisterOutcome onto its Prometheus label
// string. Keep these stable — dashboards depend on them (NFR-SEG-OBS-02).
func outcomeLabel(o domain.RegisterOutcome) string {
	switch o {
	case domain.OutcomeAllAccepted:
		return "all_accepted"
	case domain.OutcomePartial:
		return "partial"
	case domain.OutcomeAllRejected:
		return "all_rejected"
	default:
		return "unknown"
	}
}

// List forwards to metastore.ListSegments and surfaces ErrFlowNotFound
// for missing flows (REQ-SEG-04, INV-SEG-07).
func (s *segmentService) List(ctx context.Context, req domain.ListParams) (domain.SegmentPage, error) {
	start := time.Now()
	page, err := s.list(ctx, req)
	s.observe("list", time.Since(start), err)
	return page, err
}

func (s *segmentService) list(ctx context.Context, req domain.ListParams) (domain.SegmentPage, error) {
	page, err := s.meta.ListSegments(ctx, metastore.ListQuery{
		FlowID:                 req.FlowID,
		Timerange:              req.Timerange,
		ObjectID:               req.ObjectID,
		ReverseOrder:           req.ReverseOrder,
		VerboseStorage:         req.VerboseStorage,
		AcceptGetURLs:          req.AcceptGetURLs,
		AcceptStorageIDs:       req.AcceptStorageIDs,
		Presigned:              req.Presigned,
		IncludeObjectTimerange: req.IncludeObjectTimerange,
		Page:                   req.Page,
		Limit:                  req.Limit,
	})
	if err != nil {
		switch {
		case errors.Is(err, metastore.ErrFlowNotFound):
			return domain.SegmentPage{}, fmt.Errorf("segment: list: %w", ErrFlowNotFound)
		case errors.Is(err, metastore.ErrInvalidCursor):
			return domain.SegmentPage{}, err
		default:
			return domain.SegmentPage{}, fmt.Errorf("segment: list: %w", err)
		}
	}
	return page, nil
}

// Delete forwards to metastore.DeleteSegmentsByTimerange. The service
// makes ZERO calls into the object store on this path (D-25, INV-SEG-06).
func (s *segmentService) Delete(ctx context.Context, req domain.DeleteParams) (domain.DeleteResult, error) {
	start := time.Now()
	res, err := s.delete(ctx, req)
	s.observe("delete", time.Since(start), err)
	return res, err
}

func (s *segmentService) delete(ctx context.Context, req domain.DeleteParams) (domain.DeleteResult, error) {
	mres, err := s.meta.DeleteSegmentsByTimerange(ctx, metastore.DeleteQuery{
		FlowID:    req.FlowID,
		Timerange: req.Timerange,
		ObjectID:  req.ObjectID,
	})
	if err != nil {
		var outcome string
		switch {
		case errors.Is(err, metastore.ErrFlowNotFound):
			outcome = "not_found"
			s.opsTotal.WithLabelValues("segment", "delete", outcome).Inc()
			return domain.DeleteResult{}, fmt.Errorf("segment: delete: %w", ErrFlowNotFound)
		case errors.Is(err, metastore.ErrFlowReadOnly):
			outcome = "read_only"
			s.opsTotal.WithLabelValues("segment", "delete", outcome).Inc()
			return domain.DeleteResult{}, fmt.Errorf("segment: delete: %w", ErrFlowReadOnly)
		default:
			outcome = "error"
			s.opsTotal.WithLabelValues("segment", "delete", outcome).Inc()
			return domain.DeleteResult{}, fmt.Errorf("segment: delete: %w", err)
		}
	}
	s.opsTotal.WithLabelValues("segment", "delete", "success").Inc()
	return domain.DeleteResult{DeletedCount: mres.DeletedCount}, nil
}
