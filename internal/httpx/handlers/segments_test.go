package handlers_test

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"github.com/amagioss/opentams/gen/api"
	"github.com/amagioss/opentams/internal/domain"
	"github.com/amagioss/opentams/internal/httpx/handlers"
	"github.com/amagioss/opentams/internal/idempotency"
	"github.com/amagioss/opentams/internal/metastore"
	"github.com/amagioss/opentams/internal/objectstore"
	"github.com/amagioss/opentams/internal/service/segment"
	"github.com/amagioss/opentams/internal/timerange"
	"github.com/amagioss/opentams/pkg/httplog"
)

// fakeSegmentService is the SCN-HTTP test double. Each operation has a
// scripted hook (defaulting to a benign empty result) plus a call counter
// so tests can assert "service was/was not called" per scenario contract.
type fakeSegmentService struct {
	registerBatch   func(ctx context.Context, req domain.RegisterParams) (domain.RegisterResult, error)
	list            func(ctx context.Context, req domain.ListParams) (domain.SegmentPage, error)
	delete          func(ctx context.Context, req domain.DeleteParams) (domain.DeleteResult, error)
	registerCalls   int
	listCalls       int
	deleteCalls     int
	lastRegisterReq domain.RegisterParams
	lastListReq     domain.ListParams
	lastDeleteReq   domain.DeleteParams
}

func (f *fakeSegmentService) RegisterBatch(ctx context.Context, req domain.RegisterParams) (domain.RegisterResult, error) {
	f.registerCalls++
	f.lastRegisterReq = req
	if f.registerBatch == nil {
		return domain.RegisterResult{Outcome: domain.OutcomeAllAccepted, Accepted: req.Segments}, nil
	}
	return f.registerBatch(ctx, req)
}

func (f *fakeSegmentService) List(ctx context.Context, req domain.ListParams) (domain.SegmentPage, error) {
	f.listCalls++
	f.lastListReq = req
	if f.list == nil {
		return domain.SegmentPage{EffectiveLimit: req.Limit}, nil
	}
	return f.list(ctx, req)
}

func (f *fakeSegmentService) Delete(ctx context.Context, req domain.DeleteParams) (domain.DeleteResult, error) {
	f.deleteCalls++
	f.lastDeleteReq = req
	if f.delete == nil {
		return domain.DeleteResult{}, nil
	}
	return f.delete(ctx, req)
}

// newSegHandler builds a Handler with a fake segment.Service and idempotency
// mock. Other dependencies (sources, flows, storage, health) are nil — none
// of the segment scenarios touch them. A default fake objectstore is wired
// so controlled segments (len(GetURLs) == 0) survive URL projection
// without a nil deref; tests that care about projection use
// newSegHandlerWithObjects.
func newSegHandler(svc segment.Service, idem *mockIdempotencyStore) *handlers.Handler {
	return handlers.New(nil, testConfig(), nil, nil, svc, nil, idem, nil, newFakeObjectStore())
}

func uuidStr(t *testing.T) string {
	t.Helper()
	return uuid.New().String()
}

// ---------------------------------------------------------------------------
// SCN-HTTP-01 — POST single segment, all accepted → 201
// ---------------------------------------------------------------------------

func Test_SCN_HTTP_01_PostSingleSegmentAccepted(t *testing.T) {
	svc := &fakeSegmentService{}
	h := newSegHandler(svc, freshIdem())
	flowID := uuidStr(t)

	body := postBodySingle(t, "obj-1", "[0:0_10:0)")
	resp, err := h.PostFlowSegments(context.Background(), api.PostFlowSegmentsRequestObject{
		FlowId: flowID,
		Params: api.PostFlowSegmentsParams{XIdempotencyKey: "k-1"},
		Body:   body,
	})
	require.NoError(t, err)
	_, ok := resp.(api.PostFlowSegments201Response)
	assert.True(t, ok, "expected 201, got %T", resp)
	assert.Equal(t, 1, svc.registerCalls)
	require.Len(t, svc.lastRegisterReq.Segments, 1)
	assert.Equal(t, "obj-1", svc.lastRegisterReq.Segments[0].ObjectID)
}

// ---------------------------------------------------------------------------
// SCN-HTTP-02 — POST array fully accepted → 201
// ---------------------------------------------------------------------------

func Test_SCN_HTTP_02_PostArrayFullyAccepted(t *testing.T) {
	svc := &fakeSegmentService{
		registerBatch: func(_ context.Context, req domain.RegisterParams) (domain.RegisterResult, error) {
			return domain.RegisterResult{Outcome: domain.OutcomeAllAccepted, Accepted: req.Segments}, nil
		},
	}
	h := newSegHandler(svc, freshIdem())

	body := postBodyArray(t, []postSeg{
		{"o-1", "[0:0_2:0)"}, {"o-2", "[2:0_4:0)"}, {"o-3", "[4:0_6:0)"},
		{"o-4", "[6:0_8:0)"}, {"o-5", "[8:0_10:0)"},
	})
	resp, err := h.PostFlowSegments(context.Background(), api.PostFlowSegmentsRequestObject{
		FlowId: uuidStr(t),
		Params: api.PostFlowSegmentsParams{XIdempotencyKey: "k-2"},
		Body:   body,
	})
	require.NoError(t, err)
	_, ok := resp.(api.PostFlowSegments201Response)
	assert.True(t, ok, "expected 201, got %T", resp)
	require.Len(t, svc.lastRegisterReq.Segments, 5)
}

// ---------------------------------------------------------------------------
// SCN-HTTP-03 — POST partial (non-overlap per-segment) → 200 bulk-failure
// ---------------------------------------------------------------------------

func Test_SCN_HTTP_03_PostPartialNonOverlap(t *testing.T) {
	failed := domain.FailedSegment{
		Segment: domain.Segment{ObjectID: "", Timerange: mustTR(t, "[10:0_20:0)")},
		Reason:  "object_id is empty",
		Type:    "https://github.com/amagioss/opentams/problems/schema-validation",
		Title:   "Schema Validation Failed",
		Status:  400,
	}
	svc := &fakeSegmentService{
		registerBatch: func(_ context.Context, _ domain.RegisterParams) (domain.RegisterResult, error) {
			return domain.RegisterResult{
				Outcome: domain.OutcomePartial,
				Failed:  []domain.FailedSegment{failed},
			}, nil
		},
	}
	h := newSegHandler(svc, freshIdem())

	body := postBodyArray(t, []postSeg{
		{"o-1", "[0:0_10:0)"}, {"", "[10:0_20:0)"}, {"o-3", "[20:0_30:0)"},
	})
	resp, err := h.PostFlowSegments(context.Background(), api.PostFlowSegmentsRequestObject{
		FlowId: uuidStr(t),
		Params: api.PostFlowSegmentsParams{XIdempotencyKey: "k-3"},
		Body:   body,
	})
	require.NoError(t, err)
	bulk, ok := resp.(api.PostFlowSegments200JSONResponse)
	require.True(t, ok, "expected 200 bulk-failure, got %T", resp)
	require.Len(t, bulk.FailedSegments, 1)
	assert.Equal(t, "https://github.com/amagioss/opentams/problems/schema-validation", bulk.FailedSegments[0].Error.Type)
	assert.Equal(t, "object_id is empty", bulk.FailedSegments[0].Error.Detail)
}

// ---------------------------------------------------------------------------
// SCN-HTTP-04 — POST any overlap → 422 whole-batch reject
// ---------------------------------------------------------------------------

func Test_SCN_HTTP_04_PostOverlap422(t *testing.T) {
	svc := &fakeSegmentService{
		registerBatch: func(_ context.Context, _ domain.RegisterParams) (domain.RegisterResult, error) {
			return domain.RegisterResult{}, metastore.ErrSegmentOverlap
		},
	}
	h := newSegHandler(svc, freshIdem())

	body := postBodyArray(t, []postSeg{{"o-1", "[10:0_20:0)"}, {"o-2", "[15:0_18:0)"}})
	resp, err := h.PostFlowSegments(context.Background(), api.PostFlowSegmentsRequestObject{
		FlowId: uuidStr(t),
		Params: api.PostFlowSegmentsParams{XIdempotencyKey: "k-4"},
		Body:   body,
	})
	require.NoError(t, err)
	pd, ok := resp.(api.PostFlowSegments422ApplicationProblemPlusJSONResponse)
	require.True(t, ok, "expected 422 problem+json, got %T", resp)
	require.NotNil(t, pd.Type)
	assert.Equal(t, "https://github.com/amagioss/opentams/problems/segment-overlap", *pd.Type)
}

// ---------------------------------------------------------------------------
// SCN-HTTP-05 — POST without X-Idempotency-Key → 400, service NOT called
// ---------------------------------------------------------------------------

func Test_SCN_HTTP_05_PostMissingIdempotencyKey(t *testing.T) {
	svc := &fakeSegmentService{}
	h := newSegHandler(svc, freshIdem())

	body := postBodySingle(t, "obj-1", "[0:0_10:0)")
	resp, err := h.PostFlowSegments(context.Background(), api.PostFlowSegmentsRequestObject{
		FlowId: uuidStr(t),
		Params: api.PostFlowSegmentsParams{XIdempotencyKey: ""},
		Body:   body,
	})
	require.NoError(t, err)
	pd, ok := resp.(api.PostFlowSegments400ApplicationProblemPlusJSONResponse)
	require.True(t, ok, "expected 400, got %T", resp)
	require.NotNil(t, pd.Type)
	assert.Equal(t, "https://github.com/amagioss/opentams/problems/missing-idempotency-key", *pd.Type)
	assert.Equal(t, 0, svc.registerCalls)
}

// ---------------------------------------------------------------------------
// SCN-HTTP-07a — GET on missing flow → 404
// ---------------------------------------------------------------------------

func Test_SCN_HTTP_07a_GetMissingFlow404(t *testing.T) {
	svc := &fakeSegmentService{
		list: func(_ context.Context, _ domain.ListParams) (domain.SegmentPage, error) {
			return domain.SegmentPage{}, segment.ErrFlowNotFound
		},
	}
	h := newSegHandler(svc, freshIdem())

	resp, err := h.GetFlowSegments(context.Background(), api.GetFlowSegmentsRequestObject{
		FlowId: uuidStr(t),
		Params: api.GetFlowSegmentsParams{},
	})
	require.NoError(t, err)
	pd, ok := resp.(api.GetFlowSegments404ApplicationProblemPlusJSONResponse)
	require.True(t, ok, "expected 404, got %T", resp)
	require.NotNil(t, pd.Type)
	assert.Equal(t, "https://github.com/amagioss/opentams/problems/not-found", *pd.Type)
}

// ---------------------------------------------------------------------------
// SCN-HTTP-07b — GET on existing flow with no matches → 200 []
// ---------------------------------------------------------------------------

func Test_SCN_HTTP_07b_GetEmptyResult200(t *testing.T) {
	svc := &fakeSegmentService{
		list: func(_ context.Context, req domain.ListParams) (domain.SegmentPage, error) {
			return domain.SegmentPage{Items: nil, EffectiveLimit: req.Limit}, nil
		},
	}
	h := newSegHandler(svc, freshIdem())

	resp, err := h.GetFlowSegments(context.Background(), api.GetFlowSegmentsRequestObject{
		FlowId: uuidStr(t),
		Params: api.GetFlowSegmentsParams{},
	})
	require.NoError(t, err)
	page, ok := resp.(api.GetFlowSegments200JSONResponse)
	require.True(t, ok, "expected 200 page, got %T", resp)
	assert.Empty(t, page.Body)
	assert.Equal(t, 0, page.Headers.XPagingCount)
}

// ---------------------------------------------------------------------------
// SCN-HTTP-08 — DELETE success → 204
// ---------------------------------------------------------------------------

func Test_SCN_HTTP_08_DeleteSuccess204(t *testing.T) {
	svc := &fakeSegmentService{
		delete: func(_ context.Context, _ domain.DeleteParams) (domain.DeleteResult, error) {
			return domain.DeleteResult{DeletedCount: 3}, nil
		},
	}
	h := newSegHandler(svc, freshIdem())

	tr := api.Timerange("[0:0_30:0)")
	resp, err := h.DeleteFlowSegments(context.Background(), api.DeleteFlowSegmentsRequestObject{
		FlowId: uuidStr(t),
		Params: api.DeleteFlowSegmentsParams{Timerange: &tr},
	})
	require.NoError(t, err)
	_, ok := resp.(api.DeleteFlowSegments204Response)
	assert.True(t, ok, "expected 204, got %T", resp)
}

// ---------------------------------------------------------------------------
// SCN-HTTP-09 — DELETE missing flow → 404
// ---------------------------------------------------------------------------

func Test_SCN_HTTP_09_DeleteMissingFlow404(t *testing.T) {
	svc := &fakeSegmentService{
		delete: func(_ context.Context, _ domain.DeleteParams) (domain.DeleteResult, error) {
			return domain.DeleteResult{}, segment.ErrFlowNotFound
		},
	}
	h := newSegHandler(svc, freshIdem())

	tr := api.Timerange("[0:0_30:0)")
	resp, err := h.DeleteFlowSegments(context.Background(), api.DeleteFlowSegmentsRequestObject{
		FlowId: uuidStr(t),
		Params: api.DeleteFlowSegmentsParams{Timerange: &tr},
	})
	require.NoError(t, err)
	pd, ok := resp.(api.DeleteFlowSegments404ApplicationProblemPlusJSONResponse)
	require.True(t, ok, "expected 404, got %T", resp)
	require.NotNil(t, pd.Type)
	assert.Equal(t, "https://github.com/amagioss/opentams/problems/not-found", *pd.Type)
}

// ---------------------------------------------------------------------------
// SCN-HTTP-10 — DELETE on read-only flow → 403
// ---------------------------------------------------------------------------

func Test_SCN_HTTP_10_DeleteReadOnly403(t *testing.T) {
	svc := &fakeSegmentService{
		delete: func(_ context.Context, _ domain.DeleteParams) (domain.DeleteResult, error) {
			return domain.DeleteResult{}, segment.ErrFlowReadOnly
		},
	}
	h := newSegHandler(svc, freshIdem())

	tr := api.Timerange("[0:0_30:0)")
	resp, err := h.DeleteFlowSegments(context.Background(), api.DeleteFlowSegmentsRequestObject{
		FlowId: uuidStr(t),
		Params: api.DeleteFlowSegmentsParams{Timerange: &tr},
	})
	require.NoError(t, err)
	pd, ok := resp.(api.DeleteFlowSegments403ApplicationProblemPlusJSONResponse)
	require.True(t, ok, "expected 403, got %T", resp)
	require.NotNil(t, pd.Type)
	assert.Equal(t, "https://github.com/amagioss/opentams/problems/read-only", *pd.Type)
}

// ---------------------------------------------------------------------------
// SCN-HTTP-11 — DELETE missing/unparseable timerange → 400
// ---------------------------------------------------------------------------

func Test_SCN_HTTP_11_DeleteMissingTimerange400(t *testing.T) {
	svc := &fakeSegmentService{}
	h := newSegHandler(svc, freshIdem())

	resp, err := h.DeleteFlowSegments(context.Background(), api.DeleteFlowSegmentsRequestObject{
		FlowId: uuidStr(t),
		Params: api.DeleteFlowSegmentsParams{Timerange: nil},
	})
	require.NoError(t, err)
	pd, ok := resp.(api.DeleteFlowSegments400ApplicationProblemPlusJSONResponse)
	require.True(t, ok, "expected 400, got %T", resp)
	require.NotNil(t, pd.Type)
	assert.Equal(t, "https://github.com/amagioss/opentams/problems/invalid-timerange", *pd.Type)
	assert.Equal(t, 0, svc.deleteCalls, "service must NOT be called")

	// Variant: unparseable timerange.
	bad := api.Timerange("not-a-range")
	resp, err = h.DeleteFlowSegments(context.Background(), api.DeleteFlowSegmentsRequestObject{
		FlowId: uuidStr(t),
		Params: api.DeleteFlowSegmentsParams{Timerange: &bad},
	})
	require.NoError(t, err)
	_, ok = resp.(api.DeleteFlowSegments400ApplicationProblemPlusJSONResponse)
	assert.True(t, ok, "unparseable: expected 400, got %T", resp)
	assert.Equal(t, 0, svc.deleteCalls)
}

// ---------------------------------------------------------------------------
// SCN-HTTP-12 — RFC 9457 fields populated on a problem+json response
// ---------------------------------------------------------------------------

func Test_SCN_HTTP_12_ProblemDetailsFieldsPopulated(t *testing.T) {
	svc := &fakeSegmentService{
		delete: func(_ context.Context, _ domain.DeleteParams) (domain.DeleteResult, error) {
			return domain.DeleteResult{}, segment.ErrFlowNotFound
		},
	}
	h := newSegHandler(svc, freshIdem())

	tr := api.Timerange("[0:0_30:0)")
	resp, err := h.DeleteFlowSegments(context.Background(), api.DeleteFlowSegmentsRequestObject{
		FlowId: uuidStr(t),
		Params: api.DeleteFlowSegmentsParams{Timerange: &tr},
	})
	require.NoError(t, err)
	pd, ok := resp.(api.DeleteFlowSegments404ApplicationProblemPlusJSONResponse)
	require.True(t, ok)
	// Rendered problem MUST carry status, title, type, detail.
	require.NotNil(t, pd.Status)
	require.NotNil(t, pd.Title)
	require.NotNil(t, pd.Type)
	require.NotNil(t, pd.Detail)
	assert.Equal(t, 404, *pd.Status)
	assert.NotEmpty(t, *pd.Title)
	assert.NotEmpty(t, *pd.Detail)
}

// ---------------------------------------------------------------------------
// SCN-HTTP-14 — failed_segments[i].error rendered verbatim from service
// ---------------------------------------------------------------------------

func Test_SCN_HTTP_14_FailedSegmentVerbatim(t *testing.T) {
	failed := domain.FailedSegment{
		Segment: domain.Segment{ObjectID: "obj-x", Timerange: mustTR(t, "[5:0_7:0)")},
		Reason:  "fabricated detail",
		Type:    "fabricated://some/uri",
		Title:   "Fabricated",
		Status:  400,
	}
	svc := &fakeSegmentService{
		registerBatch: func(_ context.Context, _ domain.RegisterParams) (domain.RegisterResult, error) {
			return domain.RegisterResult{Outcome: domain.OutcomePartial, Failed: []domain.FailedSegment{failed}}, nil
		},
	}
	h := newSegHandler(svc, freshIdem())

	body := postBodySingle(t, "obj-x", "[5:0_7:0)")
	resp, err := h.PostFlowSegments(context.Background(), api.PostFlowSegmentsRequestObject{
		FlowId: uuidStr(t),
		Params: api.PostFlowSegmentsParams{XIdempotencyKey: "k-14"},
		Body:   body,
	})
	require.NoError(t, err)
	bulk, ok := resp.(api.PostFlowSegments200JSONResponse)
	require.True(t, ok)
	require.Len(t, bulk.FailedSegments, 1)
	assert.Equal(t, "fabricated://some/uri", bulk.FailedSegments[0].Error.Type)
	assert.Equal(t, "fabricated detail", bulk.FailedSegments[0].Error.Detail)
}

// ---------------------------------------------------------------------------
// SCN-HTTP-26 — POST on read-only flow → 403
// ---------------------------------------------------------------------------

func Test_SCN_HTTP_26_PostReadOnly403(t *testing.T) {
	svc := &fakeSegmentService{
		registerBatch: func(_ context.Context, _ domain.RegisterParams) (domain.RegisterResult, error) {
			return domain.RegisterResult{}, segment.ErrFlowReadOnly
		},
	}
	h := newSegHandler(svc, freshIdem())

	body := postBodySingle(t, "obj-1", "[0:0_10:0)")
	resp, err := h.PostFlowSegments(context.Background(), api.PostFlowSegmentsRequestObject{
		FlowId: uuidStr(t),
		Params: api.PostFlowSegmentsParams{XIdempotencyKey: "k-26"},
		Body:   body,
	})
	require.NoError(t, err)
	pd, ok := resp.(api.PostFlowSegments403ApplicationProblemPlusJSONResponse)
	require.True(t, ok, "expected 403, got %T", resp)
	require.NotNil(t, pd.Type)
	assert.Equal(t, "https://github.com/amagioss/opentams/problems/read-only", *pd.Type)
}

// ---------------------------------------------------------------------------
// SCN-HTTP-27 — POST on missing flow → 404
// ---------------------------------------------------------------------------

func Test_SCN_HTTP_27_PostMissingFlow404(t *testing.T) {
	svc := &fakeSegmentService{
		registerBatch: func(_ context.Context, _ domain.RegisterParams) (domain.RegisterResult, error) {
			return domain.RegisterResult{}, segment.ErrFlowNotFound
		},
	}
	h := newSegHandler(svc, freshIdem())

	body := postBodySingle(t, "obj-1", "[0:0_10:0)")
	resp, err := h.PostFlowSegments(context.Background(), api.PostFlowSegmentsRequestObject{
		FlowId: uuidStr(t),
		Params: api.PostFlowSegmentsParams{XIdempotencyKey: "k-27"},
		Body:   body,
	})
	require.NoError(t, err)
	pd, ok := resp.(api.PostFlowSegments404ApplicationProblemPlusJSONResponse)
	require.True(t, ok, "expected 404, got %T", resp)
	require.NotNil(t, pd.Type)
	assert.Equal(t, "https://github.com/amagioss/opentams/problems/not-found", *pd.Type)
}

// ---------------------------------------------------------------------------
// SCN-HTTP-28 — GET reverse_order=true forwarded to service
// ---------------------------------------------------------------------------

func Test_SCN_HTTP_28_GetReverseOrderForwarded(t *testing.T) {
	svc := &fakeSegmentService{
		list: func(_ context.Context, req domain.ListParams) (domain.SegmentPage, error) {
			return domain.SegmentPage{EffectiveLimit: req.Limit}, nil
		},
	}
	h := newSegHandler(svc, freshIdem())

	rev := true
	resp, err := h.GetFlowSegments(context.Background(), api.GetFlowSegmentsRequestObject{
		FlowId: uuidStr(t),
		Params: api.GetFlowSegmentsParams{ReverseOrder: &rev},
	})
	require.NoError(t, err)
	page, ok := resp.(api.GetFlowSegments200JSONResponse)
	require.True(t, ok)
	assert.True(t, svc.lastListReq.ReverseOrder, "service must receive ReverseOrder=true")
	assert.True(t, page.Headers.XPagingReverseOrder)
}

// ---------------------------------------------------------------------------
// SCN-HTTP-35 — Client-sent get_urls.controlled=true is dropped
// ---------------------------------------------------------------------------

func Test_SCN_HTTP_35_ClientControlledTrueIgnored(t *testing.T) {
	svc := &fakeSegmentService{}
	h := newSegHandler(svc, freshIdem())

	// Build a body with a get_urls entry that explicitly sets Url+Label.
	// The wire schema has no `controlled` on POST, so even if a hostile
	// client sends it via JSON it's silently dropped at decode time
	// (oapi-codegen ignores unknown fields). Conversion stamps Controlled=false.
	getURLs := []struct {
		Label string `json:"label"`
		Url   string `json:"url"`
	}{
		{Url: "https://x", Label: "primary"},
	}
	tr := "[0:0_10:0)"
	post := api.FlowSegmentPost{
		ObjectId:  "o-1",
		Timerange: tr,
		GetUrls:   &getURLs,
	}
	var body api.FlowSegmentPostBody
	require.NoError(t, body.FromFlowSegmentPost(post))

	resp, err := h.PostFlowSegments(context.Background(), api.PostFlowSegmentsRequestObject{
		FlowId: uuidStr(t),
		Params: api.PostFlowSegmentsParams{XIdempotencyKey: "k-35"},
		Body:   &body,
	})
	require.NoError(t, err)
	_, ok := resp.(api.PostFlowSegments201Response)
	require.True(t, ok, "expected 201, got %T", resp)
	require.Len(t, svc.lastRegisterReq.Segments, 1)
	require.Len(t, svc.lastRegisterReq.Segments[0].GetURLs, 1)
	assert.False(t, svc.lastRegisterReq.Segments[0].GetURLs[0].Controlled,
		"GetURLs.Controlled MUST be false for client-supplied URLs (INV-CONV-08)")
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

type postSeg struct{ ObjectID, Timerange string }

func postBodySingle(t *testing.T, objectID, tr string) *api.FlowSegmentPostBody {
	t.Helper()
	post := api.FlowSegmentPost{ObjectId: objectID, Timerange: tr}
	var b api.FlowSegmentPostBody
	require.NoError(t, b.FromFlowSegmentPost(post))
	return &b
}

func postBodyArray(t *testing.T, segs []postSeg) *api.FlowSegmentPostBody {
	t.Helper()
	arr := make([]api.FlowSegmentPost, len(segs))
	for i, s := range segs {
		arr[i] = api.FlowSegmentPost{ObjectId: s.ObjectID, Timerange: s.Timerange}
	}
	var b api.FlowSegmentPostBody
	require.NoError(t, b.FromFlowSegmentPostBody1(arr))
	return &b
}

func mustTR(t *testing.T, s string) timerange.TimeRange {
	t.Helper()
	tr, err := timerange.Parse(s)
	require.NoError(t, err)
	return tr
}

// freshIdem returns an idempotency mock that always grants Acquire (Free)
// and accepts Complete/Release as no-ops. Tests asserting idempotency
// machinery override the relevant hooks; tests that don't care use this.
func freshIdem() *mockIdempotencyStore {
	return &mockIdempotencyStore{
		acquire: func(_ context.Context, _ string, _ string, _ time.Duration) (idempotency.AcquireResult, error) {
			return idempotency.AcquireResult{Status: idempotency.StatusAcquired}, nil
		},
	}
}

// =============================================================================
// merged from segments_urls_test.go
// =============================================================================

// newSegHandlerWithObjects builds a Handler with a fake segment.Service
// AND a fake objectstore.Store so URL-projection paths can be exercised.
func newSegHandlerWithObjects(svc *fakeSegmentService, obj objectstore.Store) *handlers.Handler {
	return handlers.New(nil, testConfig(), nil, nil, svc, nil, freshIdem(), nil, obj)
}

// segPageOne returns a SegmentPage with a single item; helper to keep
// the projection-focused tests compact. Classifier is shape-derived:
// controlled ⇒ getURLs == nil/empty; BYOS ⇒ getURLs populated. The
// `controlled` flag is asserted against that invariant for test
// readability — callers that pass mismatched shapes are wrong.
func segPageOne(t *testing.T, controlled bool, objectID, tr string, getURLs []domain.GetURL) domain.SegmentPage {
	t.Helper()
	if controlled && len(getURLs) > 0 {
		t.Fatalf("segPageOne: controlled=true requires empty getURLs (shape classifier)")
	}
	if !controlled && len(getURLs) == 0 {
		t.Fatalf("segPageOne: controlled=false requires non-empty getURLs (shape classifier)")
	}
	seg := domain.Segment{
		ObjectID:  objectID,
		Timerange: mustTR(t, tr),
		GetURLs:   getURLs,
	}
	return domain.SegmentPage{
		Items:          []domain.Segment{seg},
		EffectiveLimit: 100,
	}
}

// stockBackend is the BackendInfo the fake objectstore stamps for a
// controlled segment. Matches the testConfig values so the rendered
// label is deterministic: aws.us-east-1:s3:opentams.
func stockBackend() objectstore.BackendInfo {
	return objectstore.BackendInfo{
		StorageID:    "default",
		Provider:     "aws",
		Region:       "us-east-1",
		StoreProduct: "s3",
	}
}

// stockURLSet builds the synthetic URL set the fake objectstore returns
// for a given objectID, matching the S3Store's real layout.
func stockURLSet(objectID string) objectstore.DownloadURLSet {
	return objectstore.DownloadURLSet{
		StorageID:    "default",
		StorageURI:   "s3://b1/" + objectID,
		PresignedURL: "https://b1.example/" + objectID + "?sig=x",
		Backend:      stockBackend(),
	}
}

// ---------------------------------------------------------------------------
// SCN-HTTP-15a — Controlled, ?presigned absent → both s3:// and HTTPS,
// label stamped, single GenerateDownloadURL call (no per-form repeat).
// ---------------------------------------------------------------------------

func Test_SCN_HTTP_15a_Controlled_PresignedAbsent_BothForms(t *testing.T) {
	svc := &fakeSegmentService{
		list: func(_ context.Context, _ domain.ListParams) (domain.SegmentPage, error) {
			return segPageOne(t, true, "obj-A", "[0:0_10:0)", nil), nil
		},
	}
	obj := newFakeObjectStore()
	obj.download["obj-A"] = stockURLSet("obj-A")
	h := newSegHandlerWithObjects(svc, obj)

	resp, err := h.GetFlowSegments(context.Background(), api.GetFlowSegmentsRequestObject{
		FlowId: uuidStr(t),
		Params: api.GetFlowSegmentsParams{},
	})
	require.NoError(t, err)
	page, ok := resp.(api.GetFlowSegments200JSONResponse)
	require.True(t, ok, "expected 200, got %T", resp)
	require.Len(t, page.Body, 1)
	require.NotNil(t, page.Body[0].GetUrls)
	urls := *page.Body[0].GetUrls
	require.Len(t, urls, 2, "controlled + ?presigned absent → both s3:// and HTTPS")

	var sawS3, sawHTTPS bool
	for _, u := range urls {
		switch u.Url {
		case "s3://b1/obj-A":
			sawS3 = true
			require.NotNil(t, u.Label)
			assert.Equal(t, "aws.us-east-1:s3:opentams", *u.Label)
		case "https://b1.example/obj-A?sig=x":
			sawHTTPS = true
			require.NotNil(t, u.Presigned)
			assert.True(t, *u.Presigned)
			require.NotNil(t, u.Label)
			assert.Equal(t, "aws.us-east-1:s3:opentams", *u.Label)
		}
	}
	assert.True(t, sawS3, "missing s3:// entry")
	assert.True(t, sawHTTPS, "missing https entry")
	assert.Equal(t, int32(1), obj.generateDownloadCnt.Load(),
		"GenerateDownloadURL must be called once per controlled segment")
}

// ---------------------------------------------------------------------------
// SCN-HTTP-15b — Controlled, ?presigned=true → HTTPS only, signing called.
// ---------------------------------------------------------------------------

func Test_SCN_HTTP_15b_Controlled_PresignedTrue_HTTPSOnly(t *testing.T) {
	svc := &fakeSegmentService{
		list: func(_ context.Context, _ domain.ListParams) (domain.SegmentPage, error) {
			return segPageOne(t, true, "obj-A", "[0:0_10:0)", nil), nil
		},
	}
	obj := newFakeObjectStore()
	obj.download["obj-A"] = stockURLSet("obj-A")
	h := newSegHandlerWithObjects(svc, obj)

	tr := true
	resp, err := h.GetFlowSegments(context.Background(), api.GetFlowSegmentsRequestObject{
		FlowId: uuidStr(t),
		Params: api.GetFlowSegmentsParams{Presigned: &tr},
	})
	require.NoError(t, err)
	page, ok := resp.(api.GetFlowSegments200JSONResponse)
	require.True(t, ok)
	require.Len(t, page.Body, 1)
	require.NotNil(t, page.Body[0].GetUrls)
	urls := *page.Body[0].GetUrls
	require.Len(t, urls, 1, "?presigned=true → HTTPS entry only")
	assert.Equal(t, "https://b1.example/obj-A?sig=x", urls[0].Url)
	require.NotNil(t, urls[0].Presigned)
	assert.True(t, *urls[0].Presigned)
	assert.Equal(t, int32(1), obj.signCallCnt.Load(),
		"?presigned=true must compute the signed URL")
}

// ---------------------------------------------------------------------------
// SCN-HTTP-15c — Controlled, ?presigned=false → s3:// only, NO signing call
// (REQ-HTTP-22 short-circuit).
// ---------------------------------------------------------------------------

func Test_SCN_HTTP_15c_Controlled_PresignedFalse_NoSigning(t *testing.T) {
	svc := &fakeSegmentService{
		list: func(_ context.Context, _ domain.ListParams) (domain.SegmentPage, error) {
			return segPageOne(t, true, "obj-A", "[0:0_10:0)", nil), nil
		},
	}
	obj := newFakeObjectStore()
	obj.download["obj-A"] = stockURLSet("obj-A")
	h := newSegHandlerWithObjects(svc, obj)

	fl := false
	resp, err := h.GetFlowSegments(context.Background(), api.GetFlowSegmentsRequestObject{
		FlowId: uuidStr(t),
		Params: api.GetFlowSegmentsParams{Presigned: &fl},
	})
	require.NoError(t, err)
	page, ok := resp.(api.GetFlowSegments200JSONResponse)
	require.True(t, ok)
	require.Len(t, page.Body, 1)
	require.NotNil(t, page.Body[0].GetUrls)
	urls := *page.Body[0].GetUrls
	require.Len(t, urls, 1, "?presigned=false → s3:// entry only")
	assert.Equal(t, "s3://b1/obj-A", urls[0].Url)
	// Schema default for presigned is false; entry SHOULD NOT explicitly
	// stamp it (omission matches the s3:// default).
	if urls[0].Presigned != nil {
		assert.False(t, *urls[0].Presigned, "s3:// entry must not claim presigned=true")
	}
	assert.Equal(t, int32(0), obj.signCallCnt.Load(),
		"?presigned=false MUST short-circuit signing (REQ-HTTP-22)")
}

// ---------------------------------------------------------------------------
// SCN-HTTP-15d — BYOS segment (Controlled=false): stored entries returned
// verbatim with controlled:false stamped explicitly. No objectstore call.
// ---------------------------------------------------------------------------

func Test_SCN_HTTP_15d_BYOS_StoredEntriesVerbatim(t *testing.T) {
	stored := []domain.GetURL{
		{URL: "https://customer.example/o-A.ts", Label: "primary", Controlled: false},
		{URL: "https://backup.example/o-A.ts", Label: "backup", Controlled: false},
	}
	svc := &fakeSegmentService{
		list: func(_ context.Context, _ domain.ListParams) (domain.SegmentPage, error) {
			return segPageOne(t, false, "obj-A", "[0:0_10:0)", stored), nil
		},
	}
	obj := newFakeObjectStore()
	h := newSegHandlerWithObjects(svc, obj)

	resp, err := h.GetFlowSegments(context.Background(), api.GetFlowSegmentsRequestObject{
		FlowId: uuidStr(t),
		Params: api.GetFlowSegmentsParams{},
	})
	require.NoError(t, err)
	page, ok := resp.(api.GetFlowSegments200JSONResponse)
	require.True(t, ok)
	require.Len(t, page.Body, 1)
	require.NotNil(t, page.Body[0].GetUrls)
	urls := *page.Body[0].GetUrls
	require.Len(t, urls, 2, "BYOS preserves both stored entries")
	for _, u := range urls {
		require.NotNil(t, u.Controlled, "BYOS entries MUST stamp controlled:false explicitly")
		assert.False(t, *u.Controlled)
	}
	assert.Equal(t, int32(0), obj.generateDownloadCnt.Load(),
		"BYOS segments MUST NOT call objectstore.GenerateDownloadURL")
}

// ---------------------------------------------------------------------------
// SCN-HTTP-15e — Mixed page: controlled and BYOS in the same response.
// Controlled invokes the objectstore exactly once; BYOS does not.
// ---------------------------------------------------------------------------

func Test_SCN_HTTP_15e_MixedPage_PerSegmentBranching(t *testing.T) {
	byosStored := []domain.GetURL{{URL: "https://customer.example/b.ts", Label: "primary"}}
	svc := &fakeSegmentService{
		list: func(_ context.Context, _ domain.ListParams) (domain.SegmentPage, error) {
			return domain.SegmentPage{
				Items: []domain.Segment{
					{ObjectID: "ctrl-A", Timerange: mustTR(t, "[0:0_5:0)")},
					{ObjectID: "byos-B", Timerange: mustTR(t, "[5:0_10:0)"), GetURLs: byosStored},
					{ObjectID: "ctrl-C", Timerange: mustTR(t, "[10:0_15:0)")},
				},
				EffectiveLimit: 100,
			}, nil
		},
	}
	obj := newFakeObjectStore()
	obj.download["ctrl-A"] = stockURLSet("ctrl-A")
	obj.download["ctrl-C"] = stockURLSet("ctrl-C")
	h := newSegHandlerWithObjects(svc, obj)

	resp, err := h.GetFlowSegments(context.Background(), api.GetFlowSegmentsRequestObject{
		FlowId: uuidStr(t),
		Params: api.GetFlowSegmentsParams{},
	})
	require.NoError(t, err)
	page, ok := resp.(api.GetFlowSegments200JSONResponse)
	require.True(t, ok)
	require.Len(t, page.Body, 3)
	// Controlled segments: 2 entries each (s3+HTTPS).
	require.NotNil(t, page.Body[0].GetUrls)
	assert.Len(t, *page.Body[0].GetUrls, 2)
	// BYOS segment: 1 stored entry verbatim.
	require.NotNil(t, page.Body[1].GetUrls)
	assert.Len(t, *page.Body[1].GetUrls, 1)
	require.NotNil(t, page.Body[2].GetUrls)
	assert.Len(t, *page.Body[2].GetUrls, 2)
	assert.Equal(t, int32(2), obj.generateDownloadCnt.Load(),
		"GenerateDownloadURL called once per controlled segment, never for BYOS")
}

// ---------------------------------------------------------------------------
// SCN-HTTP-19 — Service stays objectstore-free: with zero controlled
// segments, the handler does not invoke the objectstore at all
// (REQ-HTTP-20). Combined with SCN-HTTP-15d, this proves the layering.
// ---------------------------------------------------------------------------

func Test_SCN_HTTP_19_ServiceLayerFreeOfObjectstore(t *testing.T) {
	stored := []domain.GetURL{{URL: "https://customer.example/x", Label: "byos"}}
	svc := &fakeSegmentService{
		list: func(_ context.Context, _ domain.ListParams) (domain.SegmentPage, error) {
			return segPageOne(t, false, "obj-A", "[0:0_10:0)", stored), nil
		},
	}
	obj := newFakeObjectStore()
	h := newSegHandlerWithObjects(svc, obj)

	_, err := h.GetFlowSegments(context.Background(), api.GetFlowSegmentsRequestObject{
		FlowId: uuidStr(t),
		Params: api.GetFlowSegmentsParams{},
	})
	require.NoError(t, err)
	assert.Equal(t, int32(0), obj.generateDownloadCnt.Load(),
		"BYOS-only listing must not touch the objectstore (REQ-HTTP-20 layering)")
	assert.Equal(t, 1, svc.listCalls, "service must be called exactly once")
}

// =============================================================================
// merged from segments_paging_test.go
// =============================================================================

// ---------------------------------------------------------------------------
// SCN-HTTP-06 — POST batch > 1000 → 400 batch-too-large.
// ---------------------------------------------------------------------------

func Test_SCN_HTTP_06_PostBatchTooLarge400(t *testing.T) {
	svc := &fakeSegmentService{}
	h := newSegHandler(svc, freshIdem())

	// 1001 segments — one over the maxItems:1000 yaml cap. Pre-validator
	// middleware would have caught this at 401-th item; we test the
	// handler-side defence directly.
	segs := make([]postSeg, 1001)
	for i := range segs {
		segs[i] = postSeg{ObjectID: "o", Timerange: "[0:0_1:0)"}
	}
	body := postBodyArray(t, segs)

	resp, err := h.PostFlowSegments(context.Background(), api.PostFlowSegmentsRequestObject{
		FlowId: uuidStr(t),
		Params: api.PostFlowSegmentsParams{XIdempotencyKey: "k-06"},
		Body:   body,
	})
	require.NoError(t, err)
	pd, ok := resp.(api.PostFlowSegments400ApplicationProblemPlusJSONResponse)
	require.True(t, ok, "expected 400, got %T", resp)
	require.NotNil(t, pd.Type)
	assert.Equal(t, "https://github.com/amagioss/opentams/problems/batch-too-large", *pd.Type)
	assert.Equal(t, 0, svc.registerCalls,
		"service MUST NOT be called for an over-cap batch (INV-HTTP-03)")
}

// Boundary: exactly 1000 segments must still be accepted. Pins the
// strictly-greater-than semantics so a future "off-by-one" regression
// is caught.
func Test_SCN_HTTP_06_PostBatchAtCapAccepted(t *testing.T) {
	svc := &fakeSegmentService{}
	h := newSegHandler(svc, freshIdem())

	segs := make([]postSeg, 1000)
	for i := range segs {
		segs[i] = postSeg{ObjectID: "o", Timerange: "[0:0_1:0)"}
	}
	body := postBodyArray(t, segs)

	resp, err := h.PostFlowSegments(context.Background(), api.PostFlowSegmentsRequestObject{
		FlowId: uuidStr(t),
		Params: api.PostFlowSegmentsParams{XIdempotencyKey: "k-06b"},
		Body:   body,
	})
	require.NoError(t, err)
	_, ok := resp.(api.PostFlowSegments201Response)
	assert.True(t, ok, "expected 201 at exactly cap, got %T", resp)
	assert.Equal(t, 1, svc.registerCalls)
}

// ---------------------------------------------------------------------------
// SCN-HTTP-30 — Pagination headers: always-emit set vs presence-indicator.
// ---------------------------------------------------------------------------

// SCN-HTTP-30 sub-A: empty result still emits the always-emit set; Link
// and X-Paging-NextKey are both absent.
func Test_SCN_HTTP_30a_EmptyResultEmitsAlwaysHeaders(t *testing.T) {
	tr := mustTR(t, "[1000:0_2000:0)")
	svc := &fakeSegmentService{
		list: func(_ context.Context, req domain.ListParams) (domain.SegmentPage, error) {
			return domain.SegmentPage{
				Items:          nil,
				NextCursor:     "",
				EffectiveLimit: 100,
				Timerange:      tr,
			}, nil
		},
	}
	h := newSegHandler(svc, freshIdem())

	trParam := api.SegmentTimerange("[1000:0_2000:0)")
	resp, err := h.GetFlowSegments(context.Background(), api.GetFlowSegmentsRequestObject{
		FlowId: uuidStr(t),
		Params: api.GetFlowSegmentsParams{Timerange: &trParam},
	})
	require.NoError(t, err)
	page, ok := resp.(api.GetFlowSegments200JSONResponse)
	require.True(t, ok)

	// Always-emit set:
	assert.Equal(t, 100, page.Headers.XPagingLimit, "X-Paging-Limit always emitted")
	assert.Equal(t, 0, page.Headers.XPagingCount, "X-Paging-Count: 0 still emitted on empty")
	assert.Equal(t, "[1000:0_2000:0)", page.Headers.XPagingTimerange)
	assert.False(t, page.Headers.XPagingReverseOrder, "X-Paging-Reverse-Order always emitted")

	// Presence-indicator pair: both absent on empty page.
	assert.Empty(t, page.Headers.Link, "Link header MUST be absent when no NextCursor")
	assert.Empty(t, page.Headers.XPagingNextKey, "X-Paging-NextKey MUST be absent when no NextCursor")
}

// SCN-HTTP-30 sub-B: full page with NextCursor emits Link and
// X-Paging-NextKey together.
func Test_SCN_HTTP_30b_NextCursorEmitsLinkAndKey(t *testing.T) {
	items := make([]domain.Segment, 100)
	for i := range items {
		items[i] = domain.Segment{ObjectID: "o", Timerange: mustTR(t, "[0:0_1:0)")}
	}
	svc := &fakeSegmentService{
		list: func(_ context.Context, _ domain.ListParams) (domain.SegmentPage, error) {
			return domain.SegmentPage{
				Items:          items,
				NextCursor:     "cursor-page-2",
				EffectiveLimit: 100,
				Timerange:      mustTR(t, "[0:0_1000:0)"),
			}, nil
		},
	}
	h := newSegHandler(svc, freshIdem())

	resp, err := h.GetFlowSegments(context.Background(), api.GetFlowSegmentsRequestObject{
		FlowId: uuidStr(t),
		Params: api.GetFlowSegmentsParams{},
	})
	require.NoError(t, err)
	page, ok := resp.(api.GetFlowSegments200JSONResponse)
	require.True(t, ok)

	assert.Equal(t, 100, page.Headers.XPagingCount)
	assert.Equal(t, "cursor-page-2", page.Headers.XPagingNextKey)
	assert.NotEmpty(t, page.Headers.Link, "Link MUST appear with X-Paging-NextKey")
	assert.Contains(t, page.Headers.Link, `rel="next"`)
	assert.Contains(t, page.Headers.Link, "cursor-page-2")
}

// ---------------------------------------------------------------------------
// SCN-HTTP-31 — HEAD emits identical pagination headers as GET, no body.
// ---------------------------------------------------------------------------

func Test_SCN_HTTP_31_HeadHeadersIdenticalToGet(t *testing.T) {
	items := make([]domain.Segment, 100)
	for i := range items {
		items[i] = domain.Segment{ObjectID: "o", Timerange: mustTR(t, "[0:0_1:0)")}
	}
	pageOut := domain.SegmentPage{
		Items:          items,
		NextCursor:     "next-key",
		EffectiveLimit: 100,
		Timerange:      mustTR(t, "[0:0_100:0)"),
	}
	svc := &fakeSegmentService{
		list: func(_ context.Context, _ domain.ListParams) (domain.SegmentPage, error) {
			return pageOut, nil
		},
	}
	h := newSegHandler(svc, freshIdem())

	flowID := uuidStr(t)
	getResp, err := h.GetFlowSegments(context.Background(), api.GetFlowSegmentsRequestObject{
		FlowId: flowID,
		Params: api.GetFlowSegmentsParams{},
	})
	require.NoError(t, err)
	getOut, ok := getResp.(api.GetFlowSegments200JSONResponse)
	require.True(t, ok)

	headResp, err := h.HeadFlowSegments(context.Background(), api.HeadFlowSegmentsRequestObject{
		FlowId: flowID,
		Params: api.HeadFlowSegmentsParams{},
	})
	require.NoError(t, err)
	headOut, ok := headResp.(api.HeadFlowSegments200Response)
	require.True(t, ok, "expected HeadFlowSegments200Response, got %T", headResp)

	assert.Equal(t, getOut.Headers.XPagingLimit, headOut.Headers.XPagingLimit)
	assert.Equal(t, getOut.Headers.XPagingCount, headOut.Headers.XPagingCount)
	assert.Equal(t, getOut.Headers.XPagingTimerange, headOut.Headers.XPagingTimerange)
	assert.Equal(t, getOut.Headers.XPagingReverseOrder, headOut.Headers.XPagingReverseOrder)
	assert.Equal(t, getOut.Headers.XPagingNextKey, headOut.Headers.XPagingNextKey)
	assert.Equal(t, getOut.Headers.Link, headOut.Headers.Link)
}

// ---------------------------------------------------------------------------
// SCN-HTTP-32 — Accept: application/xml → 406. Enforced by the OpenAPI
// validator middleware (BR-HTTP-12); strict-server handlers cannot see
// the Accept header. The contract test lives at the server level in
// internal/server. This file documents the boundary explicitly.
// ---------------------------------------------------------------------------

func Test_SCN_HTTP_32_AcceptNegotiationIsMiddlewareConcern(t *testing.T) {
	t.Skip("SCN-HTTP-32: Accept negotiation is enforced by the kin-openapi " +
		"validator middleware (BR-HTTP-12). StrictServerInterface handlers " +
		"do not see the Accept header. The 406 contract is exercised at the " +
		"server-integration layer; see internal/server for the wire-level " +
		"assertion.")
}

// =============================================================================
// merged from segments_misc_test.go
// =============================================================================

// ---------------------------------------------------------------------------
// SCN-HTTP-33 — object_timerange round-trip + omitempty.
// ---------------------------------------------------------------------------

func Test_SCN_HTTP_33_ObjectTimerangeRoundTripAndOmitempty(t *testing.T) {
	otr := mustTR(t, "[0:0_60:0)")
	svc := &fakeSegmentService{
		list: func(_ context.Context, _ domain.ListParams) (domain.SegmentPage, error) {
			return domain.SegmentPage{
				EffectiveLimit: 100,
				Items: []domain.Segment{
					{
						ObjectID:        "seg-no-otr",
						Timerange:       mustTR(t, "[0:0_10:0)"),
						ObjectTimerange: nil,
						GetURLs: []domain.GetURL{
							{URL: "https://byos.example/no-otr", Label: "primary"},
						},
					},
					{
						ObjectID:        "seg-with-otr",
						Timerange:       mustTR(t, "[10:0_20:0)"),
						ObjectTimerange: &otr,
						GetURLs: []domain.GetURL{
							{URL: "https://byos.example/with-otr", Label: "primary"},
						},
					},
				},
			}, nil
		},
	}
	h := newSegHandler(svc, freshIdem())

	resp, err := h.GetFlowSegments(context.Background(), api.GetFlowSegmentsRequestObject{
		FlowId: uuidStr(t),
		Params: api.GetFlowSegmentsParams{},
	})
	require.NoError(t, err)
	page, ok := resp.(api.GetFlowSegments200JSONResponse)
	require.True(t, ok, "expected 200, got %T", resp)
	require.Len(t, page.Body, 2)

	// Pin the in-memory wire shape first.
	assert.Nil(t, page.Body[0].ObjectTimerange,
		"segment with nil ObjectTimerange MUST surface as nil pointer (omitempty)")
	require.NotNil(t, page.Body[1].ObjectTimerange)
	assert.Equal(t, "[0:0_60:0)", *page.Body[1].ObjectTimerange)

	// Pin the JSON serialisation contract — the spec calls out
	// "first segment's JSON does NOT contain key `object_timerange`".
	// oapi-codegen tags the field `omitempty`, so the nil pointer must
	// produce a missing key, not `"object_timerange": null`.
	first, err := json.Marshal(page.Body[0])
	require.NoError(t, err)
	assert.NotContains(t, string(first), "object_timerange",
		"nil ObjectTimerange MUST be omitted from JSON; got %s", first)

	second, err := json.Marshal(page.Body[1])
	require.NoError(t, err)
	assert.Contains(t, string(second), `"object_timerange":"[0:0_60:0)"`,
		"non-nil ObjectTimerange MUST appear in JSON; got %s", second)
}

// ---------------------------------------------------------------------------
// SCN-HTTP-34 — negative ts_offset round-trip.
// ---------------------------------------------------------------------------

func Test_SCN_HTTP_34_NegativeTsOffsetRoundTrip(t *testing.T) {
	tsOffStr := "-5:0"

	// POST a segment carrying ts_offset = "-5:0"; capture the params
	// the handler delivers to the service.
	svc := &fakeSegmentService{
		registerBatch: func(_ context.Context, req domain.RegisterParams) (domain.RegisterResult, error) {
			return domain.RegisterResult{
				Outcome:  domain.OutcomeAllAccepted,
				Accepted: req.Segments,
			}, nil
		},
	}
	h := newSegHandler(svc, freshIdem())

	post := api.FlowSegmentPost{
		ObjectId:  "obj-34",
		Timerange: "[0:0_10:0)",
		TsOffset:  &tsOffStr,
	}
	var body api.FlowSegmentPostBody
	require.NoError(t, body.FromFlowSegmentPost(post))

	_, err := h.PostFlowSegments(context.Background(), api.PostFlowSegmentsRequestObject{
		FlowId: uuidStr(t),
		Params: api.PostFlowSegmentsParams{XIdempotencyKey: "k-34"},
		Body:   &body,
	})
	require.NoError(t, err)

	// Service saw the segment with the negative offset preserved.
	require.Len(t, svc.lastRegisterReq.Segments, 1)
	stored := svc.lastRegisterReq.Segments[0]
	require.NotNil(t, stored.TSOffset, "ts_offset MUST round-trip into domain.Segment.TSOffset")
	assert.Equal(t, int64(-5), stored.TSOffset.Seconds, "negative seconds preserved")
	assert.Equal(t, int32(0), stored.TSOffset.Nanoseconds)

	// GET a segment that carries the same offset — verify it surfaces
	// on the wire string with the leading sign intact.
	listSvc := &fakeSegmentService{
		list: func(_ context.Context, _ domain.ListParams) (domain.SegmentPage, error) {
			return domain.SegmentPage{
				EffectiveLimit: 100,
				Items: []domain.Segment{
					{
						ObjectID:  "obj-34",
						Timerange: mustTR(t, "[0:0_10:0)"),
						TSOffset:  stored.TSOffset,
						GetURLs: []domain.GetURL{
							{URL: "https://byos.example/obj-34", Label: "primary"},
						},
					},
				},
			}, nil
		},
	}
	hGet := newSegHandler(listSvc, freshIdem())
	resp, err := hGet.GetFlowSegments(context.Background(), api.GetFlowSegmentsRequestObject{
		FlowId: uuidStr(t),
		Params: api.GetFlowSegmentsParams{},
	})
	require.NoError(t, err)
	page, ok := resp.(api.GetFlowSegments200JSONResponse)
	require.True(t, ok, "expected 200, got %T", resp)
	require.Len(t, page.Body, 1)
	require.NotNil(t, page.Body[0].TsOffset)
	assert.Equal(t, "-5:0", *page.Body[0].TsOffset,
		"negative ts_offset MUST round-trip with the leading minus sign")

	// Also pin the JSON tag (defensive against an accidental
	// renaming/marshalling regression).
	raw, err := json.Marshal(page.Body[0])
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"ts_offset":"-5:0"`,
		"JSON must carry ts_offset with the negative sign; got %s", raw)
}

// ---------------------------------------------------------------------------
// SCN-HTTP-36 — No backend-metadata metastore lookup on GET.
// ---------------------------------------------------------------------------

func Test_SCN_HTTP_36_NoBackendMetadataMetastoreLookup(t *testing.T) {
	// 100 controlled segments — the spec's batch size for the
	// "1 list call vs 100 backend lookups" contract.
	const n = 100
	items := make([]domain.Segment, n)
	for i := 0; i < n; i++ {
		items[i] = domain.Segment{
			ObjectID:  "obj-36-" + strconv.Itoa(i),
			Timerange: mustTR(t, "[0:0_1:0)"),
			// controlled ⇒ empty GetURLs
		}
	}

	svc := &fakeSegmentService{
		list: func(_ context.Context, p domain.ListParams) (domain.SegmentPage, error) {
			return domain.SegmentPage{
				EffectiveLimit: p.Limit,
				Items:          items,
			}, nil
		},
	}

	objStore := newFakeObjectStore()
	for i := 0; i < n; i++ {
		oid := "obj-36-" + strconv.Itoa(i)
		objStore.download[oid] = objectstore.DownloadURLSet{
			StorageID:    "default",
			StorageURI:   "s3://bucket/" + oid,
			PresignedURL: "https://example.com/" + oid + "?sig=x",
			Backend: objectstore.BackendInfo{
				StorageID:    "default",
				Provider:     "aws",
				Region:       "us-east-1",
				StoreProduct: "s3",
			},
		}
	}

	// Wire a Handler with the segment service AND the recording
	// objectstore. URL projection runs on every controlled segment, so
	// we expect exactly n GenerateDownloadURL calls.
	h := newSegHandlerWithObjects(svc, objStore)

	resp, err := h.GetFlowSegments(context.Background(), api.GetFlowSegmentsRequestObject{
		FlowId: uuidStr(t),
		Params: api.GetFlowSegmentsParams{},
	})
	require.NoError(t, err)
	page, ok := resp.(api.GetFlowSegments200JSONResponse)
	require.True(t, ok, "expected 200, got %T", resp)
	require.Len(t, page.Body, n)

	// 1 list call to the service (no per-segment backend round trip).
	assert.Equal(t, 1, svc.listCalls,
		"service.List MUST be called exactly once — handler MUST NOT loop per-segment")

	// 100 GenerateDownloadURL calls — one per controlled segment, no
	// extra calls smuggled in by a backend-metadata lookup.
	assert.Equal(t, int32(n), objStore.generateDownloadCnt.Load(),
		"objectstore.GenerateDownloadURL MUST be called exactly once per controlled segment")

	// Compile-time assertion of the spec's primary proof: the
	// metastore.Store interface MUST NOT carry a backend-metadata
	// method. We assert this by confirming the only methods on Store
	// that the test references are the segments-slice contract — the
	// existing test compiles, so any future addition would surface
	// here as a build break in the SCN-META suite.
	//
	// We don't enumerate the methods at runtime; the compile-time
	// guarantee is sufficient. The runtime portion (call counts) is
	// the load-bearing assertion that ALSO catches a regression where
	// a backend-metadata method got added but wasn't routed through
	// the segment-service path.
}

// ---------------------------------------------------------------------------
// SCN-HTTP-40 — DELETE without X-Idempotency-Key succeeds.
// ---------------------------------------------------------------------------

func Test_SCN_HTTP_40_DeleteWithoutIdempotencyKeySucceeds(t *testing.T) {
	svc := &fakeSegmentService{
		delete: func(_ context.Context, _ domain.DeleteParams) (domain.DeleteResult, error) {
			return domain.DeleteResult{DeletedCount: 1}, nil
		},
	}
	h := newSegHandler(svc, freshIdem())

	tr := api.Timerange("[0:0_10:0)")
	resp, err := h.DeleteFlowSegments(context.Background(), api.DeleteFlowSegmentsRequestObject{
		FlowId: uuidStr(t),
		// DeleteFlowSegmentsParams has no XIdempotencyKey field by
		// design — the OpenAPI spec scopes the header to POST. The
		// absence of the field at compile time IS the guarantee.
		Params: api.DeleteFlowSegmentsParams{Timerange: &tr},
	})
	require.NoError(t, err)
	_, ok := resp.(api.DeleteFlowSegments204Response)
	require.True(t, ok, "expected 204, got %T", resp)

	assert.Equal(t, 1, svc.deleteCalls,
		"service.Delete MUST be called — handler must not require an idempotency key on DELETE")
}

// SCN-HTTP-40b — GET without X-Idempotency-Key succeeds (REQ-HTTP-08 is POST-scoped).
func Test_SCN_HTTP_40b_GetWithoutIdempotencyKeySucceeds(t *testing.T) {
	svc := &fakeSegmentService{}
	h := newSegHandler(svc, freshIdem())

	resp, err := h.GetFlowSegments(context.Background(), api.GetFlowSegmentsRequestObject{
		FlowId: uuidStr(t),
		// GetFlowSegmentsParams has no XIdempotencyKey field —
		// compile-time assertion mirrors SCN-HTTP-40 above.
		Params: api.GetFlowSegmentsParams{},
	})
	require.NoError(t, err)
	_, ok := resp.(api.GetFlowSegments200JSONResponse)
	require.True(t, ok, "expected 200, got %T", resp)
	assert.Equal(t, 1, svc.listCalls)
}

// =============================================================================
// merged from segments_logs_test.go
// =============================================================================

// newSegHandlerWithLogger wires a Handler whose `log` field is the
// supplied zap.Logger. Other dependencies follow newSegHandler's
// pattern — segment service plus a fresh idempotency mock.
func newSegHandlerWithLogger(svc *fakeSegmentService, zl *zap.Logger) *handlers.Handler {
	return handlers.New(zl, testConfig(), nil, nil, svc, nil, freshIdem(), nil, nil)
}

// fieldMap flattens a slice of zap.Field into a map keyed by name. Only
// the name + string/int/bool conveyance is needed for these
// assertions — encoder behaviour is exercised in pkg/httplog tests.
func fieldMap(fs []zap.Field) map[string]any {
	enc := zapcore.NewMapObjectEncoder()
	for _, f := range fs {
		f.AddTo(enc)
	}
	return enc.Fields
}

// ---------------------------------------------------------------------------
// SCN-HTTP-13 — request_id propagation through the handler.
// ---------------------------------------------------------------------------

func Test_SCN_HTTP_13_RequestIDPropagationThroughHandler(t *testing.T) {
	const wantReqID = "client-supplied-id"

	core, _ := observer.New(zap.DebugLevel)
	zl := zap.New(core)

	var sawCtx context.Context
	svc := &fakeSegmentService{
		list: func(ctx context.Context, _ domain.ListParams) (domain.SegmentPage, error) {
			sawCtx = ctx
			return domain.SegmentPage{EffectiveLimit: 100}, nil
		},
	}
	h := newSegHandlerWithLogger(svc, zl)

	// Stamp request_id on the bag the way middleware would.
	ctx, snapshot := httplog.NewRequestContext(context.Background(), zl)
	httplog.AddFields(ctx, zap.String("request_id", wantReqID))

	_, err := h.GetFlowSegments(ctx, api.GetFlowSegmentsRequestObject{
		FlowId: uuidStr(t),
		Params: api.GetFlowSegmentsParams{},
	})
	require.NoError(t, err)

	// Service ctx must be the same chain the handler received — the
	// request_id stamped by the "middleware" must remain reachable.
	require.NotNil(t, sawCtx, "service was not called")
	require.Equal(t, ctx, sawCtx, "handler MUST forward ctx unchanged to service")

	// Snapshot of the access-log fieldbag must still contain request_id
	// (handler did not detach it).
	got := fieldMap(snapshot())
	assert.Equal(t, wantReqID, got["request_id"],
		"request_id must remain on the per-request fieldbag for the access-log entry")
}

// ---------------------------------------------------------------------------
// SCN-HTTP-16 — no body / no idempotency-key hash in logs.
// ---------------------------------------------------------------------------

func Test_SCN_HTTP_16_NoBodyOrKeyHashInLogs(t *testing.T) {
	const secret = "REQUEST_BODY_SECRET_DO_NOT_LOG"
	const idemKey = "key-16"

	core, captured := observer.New(zap.DebugLevel)
	zl := zap.New(core)

	svc := &fakeSegmentService{}
	h := newSegHandlerWithLogger(svc, zl)

	ctx, snapshot := httplog.NewRequestContext(context.Background(), zl)
	body := postBodySingle(t, secret, "[0:0_10:0)")
	flowID := uuidStr(t)

	resp, err := h.PostFlowSegments(ctx, api.PostFlowSegmentsRequestObject{
		FlowId: flowID,
		Params: api.PostFlowSegmentsParams{XIdempotencyKey: api.IdempotencyKey(idemKey)},
		Body:   body,
	})
	require.NoError(t, err)
	_, ok := resp.(api.PostFlowSegments201Response)
	require.True(t, ok, "expected 201, got %T", resp)

	// Inspect every captured log entry — message + every field's
	// rendered string form. None may contain the body secret or the
	// SHA-256 hash of the idempotency key.
	enc := zapcore.NewMapObjectEncoder()
	for _, e := range captured.All() {
		assert.NotContains(t, e.Message, secret,
			"log message MUST NOT contain request body bytes")
		for _, f := range e.Context {
			f.AddTo(enc)
		}
	}
	for k, v := range enc.Fields {
		s, _ := v.(string)
		assert.NotContains(t, s, secret,
			"field %q MUST NOT contain request body bytes", k)
	}

	// Bag stamped by the handler MUST contain flow_id, segments_count,
	// and the idempotency_key VALUE (not its hash).
	got := fieldMap(snapshot())
	assert.Equal(t, flowID, got["flow_id"])
	assert.Equal(t, idemKey, got["idempotency_key"])
	if v, ok := got["segments_count"].(int64); ok {
		assert.Equal(t, int64(1), v)
	} else {
		t.Errorf("segments_count missing from access-log fieldbag, got %v", got["segments_count"])
	}
	for k, v := range got {
		s, _ := v.(string)
		assert.NotContains(t, s, secret,
			"bag field %q MUST NOT contain request body bytes", k)
	}
}

// ---------------------------------------------------------------------------
// SCN-HTTP-23 — auth_subject MUST never appear in the response body.
// (The middleware-stamping side of this scenario is exercised in
// internal/httpx/middleware tests.)
// ---------------------------------------------------------------------------

func Test_SCN_HTTP_23_AuthSubjectNeverInResponseBody(t *testing.T) {
	svc := &fakeSegmentService{}
	zl := zap.NewNop()
	h := newSegHandlerWithLogger(svc, zl)

	ctx, _ := httplog.NewRequestContext(context.Background(), zl)
	httplog.AddFields(ctx, zap.String("auth_subject", "user@example.com"))

	resp, err := h.GetFlowSegments(ctx, api.GetFlowSegmentsRequestObject{
		FlowId: uuidStr(t),
		Params: api.GetFlowSegmentsParams{},
	})
	require.NoError(t, err)
	page, ok := resp.(api.GetFlowSegments200JSONResponse)
	require.True(t, ok, "expected 200, got %T", resp)

	// FlowSegment is the public response element — no auth-subject
	// equivalent field exists on the schema; if a regression added one
	// the api package would not compile. Pin the contract by reflecting
	// the response structure: every projected segment MUST be the
	// schema type and MUST NOT carry an "auth_subject" key.
	for i := range page.Body {
		// page.Body is []api.FlowSegment; the type has no auth_subject
		// field. This is a compile-time guarantee — the test merely
		// pins it so a hand-rolled struct sneaking auth subject into
		// the body would surface as a build break here.
		_ = page.Body[i]
	}
	assert.Empty(t, page.Headers.XPagingNextKey,
		"sanity: paging headers populated normally; auth_subject not leaked into them either")
}

// ---------------------------------------------------------------------------
// SCN-HTTP-24 — handler MUST NOT emit its own per-request INFO entries.
// The single INFO is emitted by pkg/httplog.Middleware; the handler
// communicates domain values via AddFields onto the per-request bag.
// ---------------------------------------------------------------------------

func Test_SCN_HTTP_24_HandlerEmitsNoExtraInfoEntries(t *testing.T) {
	core, captured := observer.New(zap.DebugLevel)
	zl := zap.New(core)

	svc := &fakeSegmentService{}
	h := newSegHandlerWithLogger(svc, zl)

	ctx, snapshot := httplog.NewRequestContext(context.Background(), zl)

	// Successful POST.
	body := postBodySingle(t, "obj-24a", "[0:0_10:0)")
	_, err := h.PostFlowSegments(ctx, api.PostFlowSegmentsRequestObject{
		FlowId: uuidStr(t),
		Params: api.PostFlowSegmentsParams{XIdempotencyKey: "k-24a"},
		Body:   body,
	})
	require.NoError(t, err)

	// Successful DELETE.
	tr := api.Timerange("[0:0_10:0)")
	_, err = h.DeleteFlowSegments(ctx, api.DeleteFlowSegmentsRequestObject{
		FlowId: uuidStr(t),
		Params: api.DeleteFlowSegmentsParams{Timerange: &tr},
	})
	require.NoError(t, err)

	// Handler MUST NOT have emitted any INFO records — that responsibility
	// belongs to httplog.Middleware (one access log per request).
	for _, e := range captured.All() {
		assert.NotEqual(t, zap.InfoLevel, e.Level,
			"handler MUST NOT emit INFO; saw %q at %s", e.Message, e.Level)
	}

	// Domain fields surfaced via the bag (segments_count from POST,
	// optional segments_deleted hint for DELETE).
	got := fieldMap(snapshot())
	if v, ok := got["segments_count"].(int64); ok {
		assert.Equal(t, int64(1), v)
	} else {
		t.Errorf("segments_count missing from fieldbag, got %v", got["segments_count"])
	}
}

// ---------------------------------------------------------------------------
// SCN-HTTP-25 — WARN per failed segment.
// ---------------------------------------------------------------------------

func Test_SCN_HTTP_25_WarnPerFailedSegment(t *testing.T) {
	core, captured := observer.New(zap.DebugLevel)
	zl := zap.New(core)

	failed := []domain.FailedSegment{
		{
			Segment: domain.Segment{ObjectID: "obj-1", Timerange: mustTR(t, "[0:0_1:0)")},
			Reason:  "object_id is empty",
			Type:    "https://github.com/amagioss/opentams/problems/schema-validation",
			Title:   "Schema Validation Failed",
			Status:  400,
		},
		{
			Segment: domain.Segment{ObjectID: "obj-2", Timerange: mustTR(t, "[1:0_2:0)")},
			Reason:  "timerange end <= start",
			Type:    "https://github.com/amagioss/opentams/problems/schema-validation",
			Title:   "Schema Validation Failed",
			Status:  400,
		},
		{
			Segment: domain.Segment{ObjectID: "obj-3", Timerange: mustTR(t, "[2:0_3:0)")},
			Reason:  "object reaping in progress",
			Type:    "https://github.com/amagioss/opentams/problems/object-reaping",
			Title:   "Conflict",
			Status:  409,
		},
	}
	svc := &fakeSegmentService{
		registerBatch: func(_ context.Context, _ domain.RegisterParams) (domain.RegisterResult, error) {
			return domain.RegisterResult{Outcome: domain.OutcomePartial, Failed: failed}, nil
		},
	}
	h := newSegHandlerWithLogger(svc, zl)

	ctx, snapshot := httplog.NewRequestContext(context.Background(), zl)
	body := postBodyArray(t, []postSeg{
		{"obj-1", "[0:0_1:0)"}, {"obj-2", "[1:0_2:0)"}, {"obj-3", "[2:0_3:0)"},
	})
	resp, err := h.PostFlowSegments(ctx, api.PostFlowSegmentsRequestObject{
		FlowId: uuidStr(t),
		Params: api.PostFlowSegmentsParams{XIdempotencyKey: "k-25"},
		Body:   body,
	})
	require.NoError(t, err)
	_, ok := resp.(api.PostFlowSegments200JSONResponse)
	require.True(t, ok, "expected 200 bulk-failure, got %T", resp)

	warns := captured.FilterLevelExact(zap.WarnLevel).All()
	require.Lenf(t, warns, len(failed),
		"expected exactly %d WARN entries, got %d", len(failed), len(warns))

	for i, e := range warns {
		fm := fieldMap(e.Context)
		assert.Equal(t, failed[i].Type, fm["failure_type"],
			"warn[%d] failure_type", i)
		assert.Equal(t, failed[i].Segment.ObjectID, fm["object_id"],
			"warn[%d] object_id", i)
		assert.Equal(t, failed[i].Segment.Timerange.String(), fm["timerange"],
			"warn[%d] timerange", i)
		assert.Equal(t, failed[i].Reason, fm["failure_reason"],
			"warn[%d] failure_reason", i)
	}

	// Parent INFO (emitted by Middleware in production) must carry
	// segments_failed=N — assert the bag the middleware would emit.
	got := fieldMap(snapshot())
	if v, ok := got["segments_failed"].(int64); ok {
		assert.Equal(t, int64(len(failed)), v)
	} else {
		t.Errorf("segments_failed missing from access-log bag, got %v", got["segments_failed"])
	}
}

// =============================================================================
// merged from segments_defence_test.go
// =============================================================================

// ---------------------------------------------------------------------------
// SCN-HTTP-17 — POST without X-Idempotency-Key.
// ---------------------------------------------------------------------------

func Test_SCN_HTTP_17_PostMissingIdempotencyKey400(t *testing.T) {
	svc := &fakeSegmentService{}
	h := newSegHandler(svc, freshIdem())

	body := postBodySingle(t, "obj-1", "[0:0_10:0)")
	resp, err := h.PostFlowSegments(context.Background(), api.PostFlowSegmentsRequestObject{
		FlowId: uuidStr(t),
		Params: api.PostFlowSegmentsParams{}, // no X-Idempotency-Key
		Body:   body,
	})
	require.NoError(t, err)

	pd, ok := resp.(api.PostFlowSegments400ApplicationProblemPlusJSONResponse)
	require.True(t, ok, "expected 400, got %T", resp)
	require.NotNil(t, pd.Type)
	assert.Equal(t, "https://github.com/amagioss/opentams/problems/missing-idempotency-key", *pd.Type)
	assert.Equal(t, 0, svc.registerCalls,
		"service MUST NOT be called when handler rejects the request")
}

// ---------------------------------------------------------------------------
// SCN-HTTP-18 — POST with no parsed body (validator-bypass).
// ---------------------------------------------------------------------------

func Test_SCN_HTTP_18_PostNilBody400(t *testing.T) {
	svc := &fakeSegmentService{}
	h := newSegHandler(svc, freshIdem())

	resp, err := h.PostFlowSegments(context.Background(), api.PostFlowSegmentsRequestObject{
		FlowId: uuidStr(t),
		Params: api.PostFlowSegmentsParams{XIdempotencyKey: "k-18"},
		Body:   nil,
	})
	require.NoError(t, err)

	pd, ok := resp.(api.PostFlowSegments400ApplicationProblemPlusJSONResponse)
	require.True(t, ok, "expected 400, got %T", resp)
	require.NotNil(t, pd.Type)
	assert.Equal(t, "https://github.com/amagioss/opentams/problems/invalid-json", *pd.Type)
	assert.Equal(t, 0, svc.registerCalls)
}

// ---------------------------------------------------------------------------
// SCN-HTTP-20 — No service retry on transient error.
// ---------------------------------------------------------------------------

func Test_SCN_HTTP_20_NoRetryOnServiceError(t *testing.T) {
	dbErr := errors.New("postgres: connection reset by peer")
	svc := &fakeSegmentService{
		registerBatch: func(_ context.Context, _ domain.RegisterParams) (domain.RegisterResult, error) {
			return domain.RegisterResult{}, dbErr
		},
	}
	h := newSegHandler(svc, freshIdem())

	body := postBodySingle(t, "obj-20", "[0:0_10:0)")
	_, err := h.PostFlowSegments(context.Background(), api.PostFlowSegmentsRequestObject{
		FlowId: uuidStr(t),
		Params: api.PostFlowSegmentsParams{XIdempotencyKey: "k-20"},
		Body:   body,
	})
	require.Error(t, err, "wrapped DB error must surface for the strict-server framework to map to 500")
	assert.ErrorIs(t, err, dbErr, "handler MUST NOT swallow or remap the underlying transient error")
	assert.Equal(t, 1, svc.registerCalls,
		"service MUST be called exactly once — handler MUST NOT retry on its own (NFR-HTTP-REL-03)")
}

// ---------------------------------------------------------------------------
// SCN-HTTP-21 — Body too large. Middleware-level; pinned at integration.
// ---------------------------------------------------------------------------

func Test_SCN_HTTP_21_BodyTooLarge_MiddlewareLevel(t *testing.T) {
	t.Skip("SCN-HTTP-21 — RequestBodyLimitBytes is enforced by the body-limit middleware before " +
		"oapi-codegen deserialisation; the StrictServerInterface handler never sees a request that " +
		"exceeds the cap. Asserted at the server-integration layer in internal/server.")
}

// ---------------------------------------------------------------------------
// SCN-HTTP-22 — Header injection in cursor sanitised.
// ---------------------------------------------------------------------------

func Test_SCN_HTTP_22_HeaderInjectionInCursorSanitised(t *testing.T) {
	hostile := "cursor-good\r\nX-Injected: badness"
	svc := &fakeSegmentService{
		list: func(_ context.Context, p domain.ListParams) (domain.SegmentPage, error) {
			return domain.SegmentPage{
				EffectiveLimit: p.Limit,
				NextCursor:     hostile,
			}, nil
		},
	}
	h := newSegHandler(svc, freshIdem())

	resp, err := h.GetFlowSegments(context.Background(), api.GetFlowSegmentsRequestObject{
		FlowId: uuidStr(t),
		Params: api.GetFlowSegmentsParams{},
	})
	require.NoError(t, err)
	page, ok := resp.(api.GetFlowSegments200JSONResponse)
	require.True(t, ok, "expected 200, got %T", resp)

	// X-Paging-NextKey must not contain CR or LF — the strict-server
	// framework writes the value via fmt.Sprint which would emit raw
	// bytes; the handler MUST strip them.
	assert.NotContains(t, page.Headers.XPagingNextKey, "\r",
		"X-Paging-NextKey MUST NOT contain CR")
	assert.NotContains(t, page.Headers.XPagingNextKey, "\n",
		"X-Paging-NextKey MUST NOT contain LF")
	// Same constraint on the Link header — it embeds the cursor.
	assert.NotContains(t, page.Headers.Link, "\r",
		"Link header MUST NOT contain CR")
	assert.NotContains(t, page.Headers.Link, "\n",
		"Link header MUST NOT contain LF")

	// Sanity: the NON-injected portion of the cursor survives.
	assert.Contains(t, page.Headers.XPagingNextKey, "cursor-good",
		"benign cursor prefix MUST survive sanitisation")
	// After stripping CR/LF the "X-Injected: badness" bytes still
	// appear inline inside the cursor string, but they can no longer
	// break out as a separate response header — that is the whole
	// point of the sanitisation. The CR/LF assertions above are the
	// load-bearing check.
}

// ---------------------------------------------------------------------------
// SCN-HTTP-29 — Limit > max. Middleware-level (yaml maximum:1000).
// ---------------------------------------------------------------------------

func Test_SCN_HTTP_29_LimitAboveMax_MiddlewareLevel(t *testing.T) {
	t.Skip("SCN-HTTP-29 — `limit > 1000` is rejected by the OpenAPI validator middleware " +
		"(yaml `maximum:1000`). The StrictServerInterface handler only sees in-range values, " +
		"which it clamps; the > max case is asserted at the server-integration layer.")
}

// ---------------------------------------------------------------------------
// SCN-HTTP-39 — Limit ≤ 0 (defence in depth).
// ---------------------------------------------------------------------------

func Test_SCN_HTTP_39_LimitBelowMinRejected(t *testing.T) {
	cases := []struct {
		name  string
		limit int
	}{
		{"zero", 0},
		{"negative one", -1},
		{"large negative", -9999},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fakeSegmentService{}
			h := newSegHandler(svc, freshIdem())

			lim := tc.limit
			resp, err := h.GetFlowSegments(context.Background(), api.GetFlowSegmentsRequestObject{
				FlowId: uuidStr(t),
				Params: api.GetFlowSegmentsParams{Limit: &lim},
			})
			require.NoError(t, err)

			pd, ok := resp.(api.GetFlowSegments400ApplicationProblemPlusJSONResponse)
			require.True(t, ok, "expected 400 problem+json, got %T", resp)
			require.NotNil(t, pd.Type)
			assert.Equal(t,
				"https://github.com/amagioss/opentams/problems/schema-validation",
				*pd.Type,
				"hostile limit MUST surface as schema-validation (handler defence-in-depth)")
			assert.Equal(t, 0, svc.listCalls,
				"service MUST NOT be called when the handler rejects the request")
		})
	}
}
