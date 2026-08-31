package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/amagioss/opentams/gen/api"
	"github.com/amagioss/opentams/internal/apperror"
	"github.com/amagioss/opentams/internal/domain"
	"github.com/amagioss/opentams/internal/httpx/conversion"
	"github.com/amagioss/opentams/internal/idempotency"
	"github.com/amagioss/opentams/internal/metastore"
	"github.com/amagioss/opentams/internal/objectstore"
	"github.com/amagioss/opentams/internal/service/segment"
	"github.com/amagioss/opentams/internal/timerange"
	"github.com/amagioss/opentams/pkg/httplog"
	"github.com/amagioss/opentams/pkg/logger"
)

// segIdempotencyFinaliseTimeout bounds the background context used by the
// defer backstop and the transient-error Release path; the request context
// may already be cancelled when those fire (see the Release call sites below).
const segIdempotencyFinaliseTimeout = 5 * time.Second

// maxSegmentsBatch is the upper bound on the number of segments accepted
// in a single POST. Mirrors `flow-segment-post-body.json`'s `maxItems`
// (1000) and REQ-HTTP-09 / INV-HTTP-03. The OpenAPI validator middleware
// enforces this upstream; the handler-side check is defence-in-depth so
// a misconfigured/bypassed validator still yields a 400 rather than
// reaching the service.
const maxSegmentsBatch = 1000

// problemTypeBase is the prefix for all RFC 9457 type URIs we mint.
const problemTypeBase = "https://github.com/amagioss/opentams/problems/"

func problemType(slug string) string { return problemTypeBase + slug }

// segHashBody is the body-hash for idempotency Acquire. Same algorithm as
// the legacy handler used; renamed to avoid collision when both files
// briefly coexisted.
func segHashBody(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// ---------------------------------------------------------------------------
// GET /flows/{id}/segments
// ---------------------------------------------------------------------------

func (h *Handler) GetFlowSegments(ctx context.Context, req api.GetFlowSegmentsRequestObject) (api.GetFlowSegmentsResponseObject, error) {
	log := logger.FromContext(ctx, h.log).With(zap.String("flow_id", req.FlowId))

	flowID, err := uuid.Parse(req.FlowId)
	if err != nil {
		return getSegments400(problemType("invalid-uuid"), "Bad Request", "flow_id is not a valid UUID"), nil
	}

	params, err := conversion.ListParamsFromAPI(req.Params)
	if err != nil {
		return getSegments400(listParamsErrorType(err), "Bad Request", err.Error()), nil
	}
	params.FlowID = flowID

	page, err := h.segments.List(ctx, params)
	if err != nil {
		switch {
		case errors.Is(err, segment.ErrFlowNotFound):
			return getSegments404("flow not found"), nil
		case errors.Is(err, metastore.ErrInvalidCursor):
			return getSegments400(problemType("invalid-cursor"), "Bad Request", err.Error()), nil
		}
		log.Error("List failed", zap.Error(err))
		return nil, err
	}

	body := conversion.SegmentPageToAPI(page)
	h.projectSegmentURLs(ctx, log, page, body, params.Presigned)
	// SCN-HTTP-22: the strict-server framework writes
	// X-Paging-NextKey via fmt.Sprint, so a CR/LF embedded in the
	// cursor would split into a smuggled response header. Strip CR
	// and LF here so no extra header can be injected through the
	// pagination cursor.
	safeCursor := sanitiseHeaderValue(page.NextCursor)
	headers := api.GetFlowSegments200ResponseHeaders{
		XPagingLimit:        page.EffectiveLimit,
		XPagingCount:        len(body),
		XPagingReverseOrder: params.ReverseOrder,
		XPagingTimerange:    page.Timerange.String(),
	}
	if safeCursor != "" {
		headers.XPagingNextKey = safeCursor
		headers.Link = `<>; rel="next"; key="` + safeCursor + `"`
	}
	return api.GetFlowSegments200JSONResponse{Body: body, Headers: headers}, nil
}

// listParamsErrorType maps a conversion.ListParamsFromAPI error onto
// the RFC 9457 type slug. Hostile `limit` values (SCN-HTTP-39) surface
// as `schema-validation`; malformed `timerange` strings surface as
// `invalid-timerange` (legacy contract per SCN-HTTP-08).
func listParamsErrorType(err error) string {
	var ae *apperror.AppError
	if errors.As(err, &ae) && ae.Code == apperror.ErrSchemaValidation {
		// `timerange:` prefix indicates the parser-error path; preserve
		// that as invalid-timerange to keep SCN-HTTP-08 stable.
		if strings.HasPrefix(ae.Detail, "timerange:") {
			return problemType("invalid-timerange")
		}
		return problemType("schema-validation")
	}
	return problemType("invalid-timerange")
}

// sanitiseHeaderValue strips CR and LF bytes from a string destined
// for an HTTP response header. Defence-in-depth (SCN-HTTP-22): the
// strict-server framework writes some response headers via fmt.Sprint,
// which does not perform CR/LF stripping; without this sanitisation a
// hostile cursor returned by metastore would smuggle additional
// response headers via header injection.
func sanitiseHeaderValue(s string) string {
	if s == "" || (!strings.ContainsRune(s, '\r') && !strings.ContainsRune(s, '\n')) {
		return s
	}
	r := strings.NewReplacer("\r", "", "\n", "")
	return r.Replace(s)
}

// ---------------------------------------------------------------------------
// HEAD /flows/{id}/segments
// ---------------------------------------------------------------------------

func (h *Handler) HeadFlowSegments(ctx context.Context, req api.HeadFlowSegmentsRequestObject) (api.HeadFlowSegmentsResponseObject, error) {
	log := logger.FromContext(ctx, h.log).With(zap.String("flow_id", req.FlowId))

	flowID, err := uuid.Parse(req.FlowId)
	if err != nil {
		return api.HeadFlowSegments400ApplicationProblemPlusJSONResponse{}, nil
	}

	listParams := api.GetFlowSegmentsParams{
		Timerange:              req.Params.Timerange,
		ObjectId:               req.Params.ObjectId,
		ReverseOrder:           req.Params.ReverseOrder,
		VerboseStorage:         req.Params.VerboseStorage,
		AcceptGetUrls:          req.Params.AcceptGetUrls,
		AcceptStorageIds:       req.Params.AcceptStorageIds,
		Presigned:              req.Params.Presigned,
		IncludeObjectTimerange: req.Params.IncludeObjectTimerange,
		Page:                   req.Params.Page,
		Limit:                  req.Params.Limit,
	}
	params, err := conversion.ListParamsFromAPI(listParams)
	if err != nil {
		return api.HeadFlowSegments400ApplicationProblemPlusJSONResponse{}, nil
	}
	params.FlowID = flowID

	page, err := h.segments.List(ctx, params)
	if err != nil {
		log.Error("HeadFlowSegments: List failed", zap.Error(err))
		return nil, err
	}

	safeCursor := sanitiseHeaderValue(page.NextCursor)
	headers := api.HeadFlowSegments200ResponseHeaders{
		XPagingLimit:        page.EffectiveLimit,
		XPagingCount:        len(page.Items),
		XPagingReverseOrder: params.ReverseOrder,
		XPagingTimerange:    page.Timerange.String(),
	}
	if safeCursor != "" {
		headers.XPagingNextKey = safeCursor
		headers.Link = `<>; rel="next"; key="` + safeCursor + `"`
	}
	return api.HeadFlowSegments200Response{Headers: headers}, nil
}

// ---------------------------------------------------------------------------
// POST /flows/{id}/segments
// ---------------------------------------------------------------------------

func (h *Handler) PostFlowSegments(ctx context.Context, req api.PostFlowSegmentsRequestObject) (api.PostFlowSegmentsResponseObject, error) {
	log := logger.FromContext(ctx, h.log).With(zap.String("flow_id", req.FlowId))

	flowID, err := uuid.Parse(req.FlowId)
	if err != nil {
		return postSegments404("flow not found"), nil
	}
	if req.Body == nil {
		return postSegments400(problemType("invalid-json"), "request body required"), nil
	}
	key := string(req.Params.XIdempotencyKey)
	if key == "" {
		return postSegments400(problemType("missing-idempotency-key"), "X-Idempotency-Key header required"), nil
	}

	params, err := conversion.RegisterParamsFromAPI(*req.Body)
	if err != nil {
		return postSegments400(problemType("schema-validation"), err.Error()), nil
	}
	if len(params.Segments) > maxSegmentsBatch {
		// Defence-in-depth (INV-HTTP-03): the validator middleware should
		// have rejected this at the maxItems:1000 schema cap; the handler
		// MUST also refuse so the service is never called with an
		// over-cap batch.
		return postSegments400(problemType("batch-too-large"),
			"batch exceeds maximum of 1000 segments"), nil
	}
	params.FlowID = flowID

	// Stamp domain fields onto the per-request access-log fieldbag
	// (SCN-HTTP-16, SCN-HTTP-24): exactly one INFO per request is
	// emitted by pkg/httplog.Middleware; the handler communicates
	// flow_id, segments_count, and the idempotency-key VALUE (not its
	// hash; the key is not a secret per REQ-OBS-01) by appending to
	// the bag the middleware reads at request end.
	httplog.AddFields(ctx,
		zap.String("flow_id", req.FlowId),
		zap.Int("segments_count", len(params.Segments)),
		zap.String("idempotency_key", key),
	)

	rawBody, err := req.Body.MarshalJSON()
	if err != nil {
		log.Error("failed to serialize request body for idempotency hash", zap.Error(err))
		return nil, err
	}
	bodyHash := segHashBody(rawBody)

	acq, err := h.idempotency.Acquire(ctx, key, bodyHash, h.cfg.IdempotencyKeyTTL)
	if err != nil {
		log.Error("idempotency.Acquire failed", zap.Error(err))
		return nil, err
	}
	switch acq.Status {
	case idempotency.StatusConflict:
		return postSegmentsCachedConflict("idempotency key reused with a different request body"), nil
	case idempotency.StatusInFlight:
		return postSegmentsCachedConflict("idempotency key is already in-flight"), nil
	case idempotency.StatusCached:
		return cachedPostResponse(acq.Cached), nil
	}

	finalised := false
	defer func() {
		if finalised {
			return
		}
		log.Warn("idempotency row not finalised; defer-releasing as safety net",
			zap.String("idempotency_key", key))
		bgCtx, cancel := context.WithTimeout(context.Background(), segIdempotencyFinaliseTimeout)
		defer cancel()
		if rerr := h.idempotency.Release(bgCtx, key); rerr != nil {
			log.Error("defer-Release failed", zap.String("idempotency_key", key), zap.Error(rerr))
		}
	}()

	res, err := h.segments.RegisterBatch(ctx, params)
	if err != nil {
		switch {
		case errors.Is(err, segment.ErrFlowNotFound):
			resp := postSegments404("flow not found")
			finalised = h.completeWithBody(ctx, log, key, 404, resp)
			return resp, nil
		case errors.Is(err, segment.ErrFlowReadOnly):
			resp := postSegments403("flow is read-only")
			finalised = h.completeWithBody(ctx, log, key, 403, resp)
			return resp, nil
		case errors.Is(err, segment.ErrInvalidRequest):
			resp := postSegments400(problemType("schema-validation"), err.Error())
			finalised = h.completeWithBody(ctx, log, key, 400, resp)
			return resp, nil
		case errors.Is(err, metastore.ErrSegmentOverlap):
			resp := postSegments422(problemType("segment-overlap"), "Segment Overlap", err.Error())
			finalised = h.completeWithBody(ctx, log, key, 422, resp)
			return resp, nil
		case errors.Is(err, metastore.ErrSegmentObjectReaping):
			resp := postSegments409(problemType("object-reaping"), err.Error())
			finalised = h.completeWithBody(ctx, log, key, 409, resp)
			return resp, nil
		}
		log.Error("RegisterBatch failed (transient); releasing idempotency key", zap.Error(err))
		bgCtx, cancel := context.WithTimeout(context.Background(), segIdempotencyFinaliseTimeout)
		defer cancel()
		if rerr := h.idempotency.Release(bgCtx, key); rerr == nil {
			finalised = true
		}
		return nil, err
	}

	switch res.Outcome {
	case domain.OutcomeAllAccepted:
		finalised = h.completeNoBody(ctx, log, key, 201)
		// SCN-HTTP-24: domain fields ride the access-log entry via the
		// per-request fieldbag; the handler MUST NOT emit its own INFO.
		httplog.AddFields(ctx, zap.Int("segments_failed", 0))
		return api.PostFlowSegments201Response{}, nil
	case domain.OutcomePartial:
		logFailedSegments(log, ctx, res.Failed)
		body := conversion.RegisterFailureToAPI(res.Failed)
		out := api.PostFlowSegments200JSONResponse(body)
		if raw, jerr := json.Marshal(out); jerr == nil {
			if cerr := h.idempotency.Complete(ctx, key, 200, raw); cerr == nil {
				finalised = true
			} else {
				log.Warn("idempotency.Complete failed", zap.Error(cerr))
			}
		}
		return out, nil
	case domain.OutcomeAllRejected:
		logFailedSegments(log, ctx, res.Failed)
		body := conversion.RegisterFailureToAPI(res.Failed)
		// All-rejected is treated as a 200 bulk-failure body too — the only
		// time the spec would have us return 400 here is when the *batch*
		// itself is invalid, not when every individual segment is. The
		// service emits the same Failed slice either way; the handler keeps
		// the response shape consistent.
		out := api.PostFlowSegments200JSONResponse(body)
		if raw, jerr := json.Marshal(out); jerr == nil {
			if cerr := h.idempotency.Complete(ctx, key, 200, raw); cerr == nil {
				finalised = true
			}
		}
		return out, nil
	default:
		finalised = h.completeNoBody(ctx, log, key, 500)
		return nil, errors.New("segment service returned unknown outcome")
	}
}

// logFailedSegments implements SCN-HTTP-25: emit one WARN entry per
// failed segment with structured failure_type / object_id / timerange
// / failure_reason fields, and stamp segments_failed=N onto the
// per-request access-log fieldbag so the parent INFO emitted by
// pkg/httplog.Middleware carries the count.
func logFailedSegments(log *zap.Logger, ctx context.Context, failed []domain.FailedSegment) {
	httplog.AddFields(ctx, zap.Int("segments_failed", len(failed)))
	for i := range failed {
		f := &failed[i]
		log.Warn("segment registration failed",
			zap.String("failure_type", f.Type),
			zap.String("object_id", f.Segment.ObjectID),
			zap.String("timerange", f.Segment.Timerange.String()),
			zap.String("failure_reason", f.Reason),
		)
	}
}

// completeNoBody records a deterministic outcome on the idempotency row.
// Returns true on success so the caller can flip its `finalised` flag.
// Reserved for finalisations whose response has no payload (e.g. 201
// no-body acceptance, 500 internal error). 4xx finalisations MUST go
// through completeWithBody so the rendered problem-details survive
// replay (REQ-RATE-09 / R7).
func (h *Handler) completeNoBody(ctx context.Context, log *zap.Logger, key string, status int) bool {
	if cerr := h.idempotency.Complete(ctx, key, status, nil); cerr != nil {
		log.Warn("idempotency.Complete failed", zap.Error(cerr))
		return false
	}
	return true
}

// completeWithBody stores the rendered response body alongside the
// status code, so a subsequent replay reproduces byte-identical output
// (REQ-RATE-09). Used for every non-2xx finalisation that carries a
// problem-details payload. If JSON encoding fails (should be
// impossible for handler-built responses) the row is finalised
// body-less as a safety net.
func (h *Handler) completeWithBody(ctx context.Context, log *zap.Logger, key string, status int, body any) bool {
	raw, jerr := json.Marshal(body)
	if jerr != nil {
		log.Warn("idempotency body marshal failed; finalising without body", zap.Int("status", status), zap.Error(jerr))
		return h.completeNoBody(ctx, log, key, status)
	}
	if cerr := h.idempotency.Complete(ctx, key, status, raw); cerr != nil {
		log.Warn("idempotency.Complete failed", zap.Error(cerr))
		return false
	}
	return true
}

// ---------------------------------------------------------------------------
// DELETE /flows/{id}/segments
// ---------------------------------------------------------------------------

func (h *Handler) DeleteFlowSegments(ctx context.Context, req api.DeleteFlowSegmentsRequestObject) (api.DeleteFlowSegmentsResponseObject, error) {
	log := logger.FromContext(ctx, h.log).With(zap.String("flow_id", req.FlowId))

	flowID, err := uuid.Parse(req.FlowId)
	if err != nil {
		return deleteSegments404("flow not found"), nil
	}

	// REQ-BEH-32 / SCN-HTTP-11: timerange is required on DELETE. The
	// conversion helper would coerce nil to "_" (eternity) — we reject
	// here so the missing-header path returns 400 invalid-timerange and
	// the service is never called.
	if req.Params.Timerange == nil {
		return deleteSegments400(problemType("invalid-timerange"), "timerange query parameter is required"), nil
	}
	tr, err := timerange.Parse(string(*req.Params.Timerange))
	if err != nil {
		return deleteSegments400(problemType("invalid-timerange"), "timerange: "+err.Error()), nil
	}

	dp := domain.DeleteParams{FlowID: flowID, Timerange: tr}
	if req.Params.ObjectId != nil {
		v := string(*req.Params.ObjectId)
		dp.ObjectID = &v
	}

	if _, err := h.segments.Delete(ctx, dp); err != nil {
		switch {
		case errors.Is(err, segment.ErrFlowNotFound):
			return deleteSegments404("flow not found"), nil
		case errors.Is(err, segment.ErrFlowReadOnly):
			return deleteSegments403("flow is read-only"), nil
		}
		log.Error("Delete failed", zap.Error(err))
		return nil, err
	}
	// SCN-HTTP-24: stamp the access-log bag rather than emitting a
	// second INFO. The middleware writes the canonical access-log
	// entry; flow_id and the deletion timerange ride along on it.
	httplog.AddFields(ctx,
		zap.String("flow_id", req.FlowId),
		zap.String("timerange", tr.String()),
	)
	return api.DeleteFlowSegments204Response{}, nil
}

// ---------------------------------------------------------------------------
// problem+json builders
// ---------------------------------------------------------------------------

func problemPtrs(typ, title, detail string, status int) (typP, titleP, detailP *string, statusP *int) {
	t, ti, d, s := typ, title, detail, status
	return &t, &ti, &d, &s
}

func getSegments400(typ, title, detail string) api.GetFlowSegments400ApplicationProblemPlusJSONResponse {
	t, ti, d, s := problemPtrs(typ, title, detail, 400)
	return api.GetFlowSegments400ApplicationProblemPlusJSONResponse{
		BadRequestApplicationProblemPlusJSONResponse: api.BadRequestApplicationProblemPlusJSONResponse{
			Type: t, Title: ti, Detail: d, Status: s,
		},
	}
}

func getSegments404(detail string) api.GetFlowSegments404ApplicationProblemPlusJSONResponse {
	t, ti, d, s := problemPtrs(problemType("not-found"), "Not Found", detail, 404)
	return api.GetFlowSegments404ApplicationProblemPlusJSONResponse{
		NotFoundApplicationProblemPlusJSONResponse: api.NotFoundApplicationProblemPlusJSONResponse{
			Type: t, Title: ti, Detail: d, Status: s,
		},
	}
}

func deleteSegments400(typ, detail string) api.DeleteFlowSegments400ApplicationProblemPlusJSONResponse {
	t, ti, d, s := problemPtrs(typ, "Bad Request", detail, 400)
	return api.DeleteFlowSegments400ApplicationProblemPlusJSONResponse{
		BadRequestApplicationProblemPlusJSONResponse: api.BadRequestApplicationProblemPlusJSONResponse{
			Type: t, Title: ti, Detail: d, Status: s,
		},
	}
}

func deleteSegments403(detail string) api.DeleteFlowSegments403ApplicationProblemPlusJSONResponse {
	t, ti, d, s := problemPtrs(problemType("read-only"), "Forbidden", detail, 403)
	return api.DeleteFlowSegments403ApplicationProblemPlusJSONResponse{
		ForbiddenApplicationProblemPlusJSONResponse: api.ForbiddenApplicationProblemPlusJSONResponse{
			Type: t, Title: ti, Detail: d, Status: s,
		},
	}
}

func deleteSegments404(detail string) api.DeleteFlowSegments404ApplicationProblemPlusJSONResponse {
	t, ti, d, s := problemPtrs(problemType("not-found"), "Not Found", detail, 404)
	return api.DeleteFlowSegments404ApplicationProblemPlusJSONResponse{
		NotFoundApplicationProblemPlusJSONResponse: api.NotFoundApplicationProblemPlusJSONResponse{
			Type: t, Title: ti, Detail: d, Status: s,
		},
	}
}

func postSegments400(typ, detail string) api.PostFlowSegments400ApplicationProblemPlusJSONResponse {
	t, ti, d, s := problemPtrs(typ, "Bad Request", detail, 400)
	return api.PostFlowSegments400ApplicationProblemPlusJSONResponse{
		BadRequestApplicationProblemPlusJSONResponse: api.BadRequestApplicationProblemPlusJSONResponse{
			Type: t, Title: ti, Detail: d, Status: s,
		},
	}
}

func postSegments403(detail string) api.PostFlowSegments403ApplicationProblemPlusJSONResponse {
	t, ti, d, s := problemPtrs(problemType("read-only"), "Forbidden", detail, 403)
	return api.PostFlowSegments403ApplicationProblemPlusJSONResponse{
		ForbiddenApplicationProblemPlusJSONResponse: api.ForbiddenApplicationProblemPlusJSONResponse{
			Type: t, Title: ti, Detail: d, Status: s,
		},
	}
}

func postSegments404(detail string) api.PostFlowSegments404ApplicationProblemPlusJSONResponse {
	t, ti, d, s := problemPtrs(problemType("not-found"), "Not Found", detail, 404)
	return api.PostFlowSegments404ApplicationProblemPlusJSONResponse{
		NotFoundApplicationProblemPlusJSONResponse: api.NotFoundApplicationProblemPlusJSONResponse{
			Type: t, Title: ti, Detail: d, Status: s,
		},
	}
}

func postSegments409(typ, detail string) api.PostFlowSegments409ApplicationProblemPlusJSONResponse {
	t, ti, d, s := problemPtrs(typ, "Conflict", detail, 409)
	return api.PostFlowSegments409ApplicationProblemPlusJSONResponse{
		Type: t, Title: ti, Detail: d, Status: s,
	}
}

func postSegments422(typ, title, detail string) api.PostFlowSegments422ApplicationProblemPlusJSONResponse {
	t, ti, d, s := problemPtrs(typ, title, detail, 422)
	return api.PostFlowSegments422ApplicationProblemPlusJSONResponse{
		Type: t, Title: ti, Detail: d, Status: s,
	}
}

// postSegmentsCachedConflict is the 409 path used for idempotency
// reuse-with-different-body and in-flight collisions. The detail strings
// differ but the type/title/status are uniform.
func postSegmentsCachedConflict(detail string) api.PostFlowSegments409ApplicationProblemPlusJSONResponse {
	return postSegments409(problemType("idempotency-conflict"), detail)
}

// cachedPostResponse reconstructs a previously-returned POST response from
// the idempotency cache. It MUST cover every status the handler can
// Complete with — otherwise a cached 4xx replays as 201, defeating the
// idempotency contract (REQ-RATE-09).
//
// Non-2xx finalisations cache the rendered problem-details body. Replay
// unmarshals it back into the matching response struct so detail / type
// / title are preserved verbatim. If the cached body is missing or
// malformed (legacy rows from before this code shipped), a synthesised
// fallback with empty detail is returned — best-effort over corruption.
func cachedPostResponse(c *idempotency.CachedResponse) api.PostFlowSegmentsResponseObject {
	if c == nil {
		return api.PostFlowSegments201Response{}
	}
	switch c.Status {
	case 200:
		var body api.PostFlowSegments200JSONResponse
		if len(c.Body) > 0 {
			if err := json.Unmarshal(c.Body, &body); err == nil {
				return body
			}
		}
		return api.PostFlowSegments201Response{}
	case 201:
		return api.PostFlowSegments201Response{}
	case 400:
		var body api.PostFlowSegments400ApplicationProblemPlusJSONResponse
		if len(c.Body) > 0 && json.Unmarshal(c.Body, &body) == nil {
			return body
		}
		return postSegments400(problemType("schema-validation"), "")
	case 403:
		var body api.PostFlowSegments403ApplicationProblemPlusJSONResponse
		if len(c.Body) > 0 && json.Unmarshal(c.Body, &body) == nil {
			return body
		}
		return postSegments403("")
	case 404:
		var body api.PostFlowSegments404ApplicationProblemPlusJSONResponse
		if len(c.Body) > 0 && json.Unmarshal(c.Body, &body) == nil {
			return body
		}
		return postSegments404("")
	case 409:
		var body api.PostFlowSegments409ApplicationProblemPlusJSONResponse
		if len(c.Body) > 0 && json.Unmarshal(c.Body, &body) == nil {
			return body
		}
		return postSegments409(problemType("object-reaping"), "")
	case 422:
		var body api.PostFlowSegments422ApplicationProblemPlusJSONResponse
		if len(c.Body) > 0 && json.Unmarshal(c.Body, &body) == nil {
			return body
		}
		return postSegments422(problemType("segment-overlap"), "Segment Overlap", "")
	default:
		return api.PostFlowSegments201Response{}
	}
}

// segmentsHandlerNotices keeps apperror imported for future error-code
// classification work without making the import look stray. Once the
// problem+title strings move into apperror.Catalog, this anchor goes too.
//
//nolint:gochecknoglobals,unused // intentional import-anchor; see comment above.
var _ = apperror.ErrSchemaValidation

// =============================================================================
// URL projection — BR-HTTP-05 / SCN-HTTP-15a–e / SCN-HTTP-19.
// =============================================================================
//
// The handler — not the service, not the conversion layer — owns this
// step (REQ-HTTP-20). Classification is shape-derived: a segment with
// empty `GetURLs` is controlled (server owns the bytes); a segment with
// any stored `GetURLs` is BYOS. Each segment's `get_urls` array is
// projected onto the wire shape:
//
//   - Controlled (`len(seg.GetURLs) == 0`): handler synthesises one or
//     two entries from `objectstore.GenerateDownloadURL`, applying the
//     `?presigned` filter. Label format:
//     `<provider>.<region>:<store_product>:opentams`.
//   - BYOS (`len(seg.GetURLs) > 0`): handler emits the stored entries
//     verbatim with `controlled:false` stamped explicitly (GET-schema
//     default is `true`).
//
// `?presigned=false` MUST short-circuit signing (REQ-HTTP-22): the
// objectstore returns an empty `PresignedURL`; we skip the HTTPS entry.

// projectSegmentURLs overwrites `out[i].GetUrls` for each segment in the
// page. The input slice ordering is preserved; failures on a single
// segment are logged and that segment's `GetUrls` is left empty rather
// than failing the whole request — partial visibility is preferable to
// 5xx (per behaviour-doc operational note: signing errors should not
// take a list endpoint down).
func (h *Handler) projectSegmentURLs(
	ctx context.Context,
	log *zap.Logger,
	page domain.SegmentPage,
	out []api.FlowSegment,
	presigned *bool,
) {
	for i := range page.Items {
		seg := &page.Items[i]
		if len(seg.GetURLs) == 0 {
			entries, err := h.controlledGetURLs(ctx, seg.ObjectID, presigned)
			if err != nil {
				log.Warn("URL projection: controlled segment failed",
					zap.String("object_id", seg.ObjectID), zap.Error(err))
				out[i].GetUrls = nil
				continue
			}
			out[i].GetUrls = &entries
			continue
		}
		entries := byosGetURLs(seg.GetURLs, presigned)
		if len(entries) == 0 {
			out[i].GetUrls = nil
			continue
		}
		out[i].GetUrls = &entries
	}
}

// controlledGetURLs synthesises `get_urls` for a controlled segment via
// the objectstore. Implements the algorithm in BR-HTTP-05.
func (h *Handler) controlledGetURLs(
	ctx context.Context,
	objectID string,
	presigned *bool,
) ([]segGetURL, error) {
	set, err := h.objects.GenerateDownloadURL(ctx, objectID, objectstore.DownloadURLOpts{Presigned: presigned})
	if err != nil {
		return nil, err
	}
	label := backendLabel(set.Backend)
	storageID := optStr(set.StorageID)

	out := make([]segGetURL, 0, 2)
	// Emit the storage URI entry unless ?presigned=true filters it out.
	if presigned == nil || !*presigned {
		out = append(out, segGetURL{
			Url:       set.StorageURI,
			Label:     optStr(label),
			StorageId: storageID,
		})
	}
	// HTTPS entry — emitted unless ?presigned=false filters it out.
	// The objectstore short-circuits signing in that case (PresignedURL
	// stays empty); the entry is also omitted if the URL is empty for any
	// other reason.
	if (presigned == nil || *presigned) && set.PresignedURL != "" {
		tr := true
		out = append(out, segGetURL{
			Url:       set.PresignedURL,
			Label:     optStr(label),
			StorageId: storageID,
			Presigned: &tr,
		})
	}
	return out, nil
}

// byosGetURLs maps stored `domain.GetURL` entries to the wire shape,
// stamping `controlled:false` explicitly (GET-schema default is true).
// Applies the `?presigned` filter on each stored entry's canonical
// presigned value.
func byosGetURLs(stored []domain.GetURL, presigned *bool) []segGetURL {
	if len(stored) == 0 {
		return nil
	}
	out := make([]segGetURL, 0, len(stored))
	for i := range stored {
		// Stored BYOS entries are always presigned == false at the schema
		// default. Filter:
		//   ?presigned=true  → drop
		//   ?presigned=false → keep
		//   absent           → keep
		if presigned != nil && *presigned {
			continue
		}
		ctrl := false
		out = append(out, segGetURL{
			Url:        stored[i].URL,
			Label:      optStr(stored[i].Label),
			Controlled: &ctrl,
			// Presigned omitted — schema default false matches stored entry.
		})
	}
	return out
}

// segGetURL is the anonymous struct emitted by oapi-codegen as the
// element type of api.FlowSegment.GetUrls. We mirror it here so the
// projection helper can return a typed slice; the caller assigns it via
// `out[i].GetUrls = &entries`.
//
//nolint:revive // field names mirror oapi-codegen output verbatim.
type segGetURL = struct {
	AvailabilityZone *string                          `json:"availability_zone,omitempty"`
	Controlled       *bool                            `json:"controlled,omitempty"`
	Label            *string                          `json:"label,omitempty"`
	Presigned        *bool                            `json:"presigned,omitempty"`
	Provider         *string                          `json:"provider,omitempty"`
	Region           *string                          `json:"region,omitempty"`
	StorageId        *api.Uuid                        `json:"storage_id,omitempty"`
	StoreProduct     *string                          `json:"store_product,omitempty"`
	StoreType        *api.FlowSegmentGetUrlsStoreType `json:"store_type,omitempty"`
	Url              string                           `json:"url"`
}

// backendLabel renders the per-deployment URL label per BR-HTTP-05:
// `<provider>.<region>:<store_product>:opentams`.
func backendLabel(b objectstore.BackendInfo) string {
	return b.Provider + "." + b.Region + ":" + b.StoreProduct + ":opentams"
}

// optStr returns nil for empty strings — the wire schema treats every
// string field on a get_urls entry as omitempty.
func optStr(s string) *string {
	if s == "" {
		return nil
	}
	v := s
	return &v
}
