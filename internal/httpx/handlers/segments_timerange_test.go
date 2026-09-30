package handlers_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/amagioss/opentams/gen/api"
	"github.com/amagioss/opentams/internal/domain"
	"github.com/amagioss/opentams/internal/httpx/handlers"
	"github.com/amagioss/opentams/internal/idempotency"
	"github.com/amagioss/opentams/internal/metastore"
	"github.com/amagioss/opentams/internal/service"
	"github.com/amagioss/opentams/internal/service/segment"
	"github.com/amagioss/opentams/internal/timerange"
)

const invalidTimerangeType = "https://github.com/amagioss/opentams/problems/invalid-timerange"

// memSegmentStore is an in-memory metastore.Store. It keeps what the
// service hands it, raw strings included, and returns it on List, the way
// the real store does (BR-META-10).
type memSegmentStore struct {
	mu          sync.Mutex
	segments    []domain.Segment
	insertCalls int
	insertErr   error
}

func (m *memSegmentStore) GetFlowForSegmentWrite(_ context.Context, _ metastore.Tx, id uuid.UUID) (metastore.FlowGuard, error) {
	return metastore.FlowGuard{FlowID: id, Exists: true}, nil
}

func (m *memSegmentStore) GetFlowForSegmentRead(_ context.Context, id uuid.UUID) (metastore.FlowGuard, error) {
	return metastore.FlowGuard{FlowID: id, Exists: true}, nil
}

func (m *memSegmentStore) InsertSegments(_ context.Context, b metastore.InsertBatch) (metastore.InsertResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.insertCalls++
	if m.insertErr != nil {
		return metastore.InsertResult{}, m.insertErr
	}
	idx := make([]int, len(b.Segments))
	for i := range b.Segments {
		idx[i] = i
		m.segments = append(m.segments, b.Segments[i])
	}
	return metastore.InsertResult{AcceptedIndices: idx}, nil
}

func (m *memSegmentStore) ListSegments(_ context.Context, q metastore.ListQuery) (domain.SegmentPage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	items := append([]domain.Segment(nil), m.segments...)
	return domain.SegmentPage{Items: items, Count: len(items), EffectiveLimit: q.Limit}, nil
}

func (m *memSegmentStore) DeleteSegmentsByTimerange(_ context.Context, _ metastore.DeleteQuery) (metastore.DeleteResult, error) {
	return metastore.DeleteResult{}, nil
}

// statefulIdem follows the idempotency contract: Complete caches, Release
// forgets the key, and a key held in flight blocks a second Acquire.
type statefulIdem struct {
	mu        sync.Mutex
	inFlight  map[string]bool
	cached    map[string]*idempotency.CachedResponse
	releases  int
	completes int
}

func newStatefulIdem() *statefulIdem {
	return &statefulIdem{inFlight: map[string]bool{}, cached: map[string]*idempotency.CachedResponse{}}
}

func (s *statefulIdem) Acquire(_ context.Context, key, _ string, _ time.Duration) (idempotency.AcquireResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.cached[key]; ok {
		return idempotency.AcquireResult{Status: idempotency.StatusCached, Cached: c}, nil
	}
	if s.inFlight[key] {
		return idempotency.AcquireResult{Status: idempotency.StatusInFlight}, nil
	}
	s.inFlight[key] = true
	return idempotency.AcquireResult{Status: idempotency.StatusAcquired}, nil
}

func (s *statefulIdem) Complete(_ context.Context, key string, status int, body json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.completes++
	delete(s.inFlight, key)
	s.cached[key] = &idempotency.CachedResponse{Status: status, Body: body}
	return nil
}

func (s *statefulIdem) Release(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.releases++
	delete(s.inFlight, key)
	return nil
}

// newRealSegHandler wires the real segment service over store, so a test
// covers conversion, service validation, and the handler together.
func newRealSegHandler(t *testing.T, store metastore.Store, idem *statefulIdem) *handlers.Handler {
	t.Helper()
	m, err := service.NewAppServiceMetrics(prometheus.NewRegistry())
	require.NoError(t, err)
	svc := segment.New(segment.Deps{Meta: store, Logger: zap.NewNop(), Metrics: m})
	return handlers.New(nil, testConfig(), nil, nil, svc, nil, idem, nil, newFakeObjectStore())
}

func postOne(t *testing.T, h *handlers.Handler, flowID, key, tr string) (api.PostFlowSegmentsResponseObject, error) {
	t.Helper()
	return h.PostFlowSegments(context.Background(), api.PostFlowSegmentsRequestObject{
		FlowId: flowID,
		Params: api.PostFlowSegmentsParams{XIdempotencyKey: key},
		Body:   postBodySingle(t, "obj", tr),
	})
}

// Regression for the original bug: POST [0:0] returned 500. It is a valid
// instantaneous segment (ADR-0040), and it reads back as sent (ADR-0039).
func Test_Regression_PostInstantaneousSegmentReadsBackAsSent(t *testing.T) {
	for _, tr := range []string{"[0:0]", "10:0"} {
		t.Run(tr, func(t *testing.T) {
			h := newRealSegHandler(t, &memSegmentStore{}, newStatefulIdem())
			flowID := uuidStr(t)

			resp, err := postOne(t, h, flowID, "k-"+tr, tr)
			require.NoError(t, err)
			require.IsType(t, api.PostFlowSegments201Response{}, resp)

			got, err := h.GetFlowSegments(context.Background(), api.GetFlowSegmentsRequestObject{FlowId: flowID})
			require.NoError(t, err)
			page, ok := got.(api.GetFlowSegments200JSONResponse)
			require.True(t, ok, "expected 200, got %T", got)
			require.Len(t, page.Body, 1)
			assert.Equal(t, tr, page.Body[0].Timerange)
		})
	}
}

// ADR-0040 rule 2: a batch with valid and invalid segment timeranges is a
// 200 flow-segment-bulk-failure; the valid segments are stored.
func Test_ADR0040_MixedBatchIs200BulkFailure(t *testing.T) {
	store := &memSegmentStore{}
	h := newRealSegHandler(t, store, newStatefulIdem())

	resp, err := h.PostFlowSegments(context.Background(), api.PostFlowSegmentsRequestObject{
		FlowId: uuidStr(t),
		Params: api.PostFlowSegmentsParams{XIdempotencyKey: "k-mixed"},
		Body: postBodyArray(t, []postSeg{
			{"ok-1", "[0:0_1:0)"}, {"bad-1", "_"}, {"ok-2", "[1:0]"}, {"bad-2", "[5:0_5:0)"},
		}),
	})
	require.NoError(t, err)
	bulk, ok := resp.(api.PostFlowSegments200JSONResponse)
	require.True(t, ok, "expected 200 bulk-failure, got %T", resp)

	require.Len(t, bulk.FailedSegments, 2)
	for i, want := range []struct{ id, tr string }{{"bad-1", "_"}, {"bad-2", "[5:0_5:0)"}} {
		f := bulk.FailedSegments[i]
		assert.Equal(t, want.id, f.ObjectId)
		require.NotNil(t, f.Timerange)
		assert.Equal(t, want.tr, *f.Timerange, "failed entry carries the client's string")
		assert.Equal(t, invalidTimerangeType, f.Error.Type)
		assert.Equal(t, "Invalid Timerange", f.Error.Title)
		assert.NotEmpty(t, f.Error.Detail)
	}

	require.Len(t, store.segments, 2)
	assert.Equal(t, "ok-1", store.segments[0].ObjectID)
	assert.Equal(t, "ok-2", store.segments[1].ObjectID)
}

// Instantaneous ranges with exclusive markers match the schema regex but
// do not parse (BR-TR-05).
var unparseableQueryTimeranges = []string{"(10:0)", "(10:0]", "[10:0)"}

// BR-CONV-09: a body string that does not parse is a 400 schema-validation
// for the whole request; no segment reaches the service.
func Test_BR_CONV_09_UnparseableBodyTimerangeIs400(t *testing.T) {
	for _, tr := range unparseableQueryTimeranges {
		t.Run(tr, func(t *testing.T) {
			svc := &fakeSegmentService{}
			h := newSegHandler(svc, freshIdem())

			resp, err := h.PostFlowSegments(context.Background(), api.PostFlowSegmentsRequestObject{
				FlowId: uuidStr(t),
				Params: api.PostFlowSegmentsParams{XIdempotencyKey: "k"},
				Body:   postBodyArray(t, []postSeg{{"ok", "[0:0_1:0)"}, {"bad", tr}}),
			})
			require.NoError(t, err)
			pd, ok := resp.(api.PostFlowSegments400ApplicationProblemPlusJSONResponse)
			require.True(t, ok, "expected 400, got %T", resp)
			require.NotNil(t, pd.Type)
			assert.Equal(t, "https://github.com/amagioss/opentams/problems/schema-validation", *pd.Type)
			assert.Equal(t, 0, svc.registerCalls)
		})
	}
}

// BR-CONV-09: the same strings as a query parameter are a 400 on every
// endpoint that takes a timerange filter, and the service is not called.
func Test_BR_CONV_09_UnparseableQueryTimerangeIs400(t *testing.T) {
	for _, tr := range unparseableQueryTimeranges {
		t.Run(tr, func(t *testing.T) {
			v := tr
			ctx := context.Background()
			svc := &fakeSegmentService{}
			h := newSegHandler(svc, freshIdem())
			flowID := uuidStr(t)

			get, err := h.GetFlowSegments(ctx, api.GetFlowSegmentsRequestObject{
				FlowId: flowID, Params: api.GetFlowSegmentsParams{Timerange: &v},
			})
			require.NoError(t, err)
			assert.IsType(t, api.GetFlowSegments400ApplicationProblemPlusJSONResponse{}, get)

			head, err := h.HeadFlowSegments(ctx, api.HeadFlowSegmentsRequestObject{
				FlowId: flowID, Params: api.HeadFlowSegmentsParams{Timerange: &v},
			})
			require.NoError(t, err)
			assert.IsType(t, api.HeadFlowSegments400ApplicationProblemPlusJSONResponse{}, head)

			del, err := h.DeleteFlowSegments(ctx, api.DeleteFlowSegmentsRequestObject{
				FlowId: flowID, Params: api.DeleteFlowSegmentsParams{Timerange: &v},
			})
			require.NoError(t, err)
			assert.IsType(t, api.DeleteFlowSegments400ApplicationProblemPlusJSONResponse{}, del)

			assert.Zero(t, svc.listCalls+svc.deleteCalls, "service must not be called")

			flowsCalled := 0
			fh := newFlowHandler(&mockFlowService{
				listFlows: func(_ context.Context, _ metastore.ListFlowsParams) (*metastore.FlowPage, error) {
					flowsCalled++
					return &metastore.FlowPage{}, nil
				},
			})
			gf, err := fh.GetFlows(ctx, api.GetFlowsRequestObject{Params: api.GetFlowsParams{Timerange: &v}})
			require.NoError(t, err)
			assert.IsType(t, api.GetFlows400ApplicationProblemPlusJSONResponse{}, gf)

			hf, err := fh.HeadFlows(ctx, api.HeadFlowsRequestObject{Params: api.HeadFlowsParams{Timerange: &v}})
			require.NoError(t, err)
			assert.IsType(t, api.HeadFlows400ApplicationProblemPlusJSONResponse{}, hf)

			assert.Zero(t, flowsCalled, "flow service must not be called")
		})
	}
}

// ADR-0039 rule 8: an empty query range is valid. It reaches the service
// as an empty range; the store decides it matches nothing.
func Test_ADR0039_EmptyQueryTimerangeIsNot400(t *testing.T) {
	v := "[10:0_5:0)"
	ctx := context.Background()
	svc := &fakeSegmentService{}
	h := newSegHandler(svc, freshIdem())
	flowID := uuidStr(t)

	get, err := h.GetFlowSegments(ctx, api.GetFlowSegmentsRequestObject{
		FlowId: flowID, Params: api.GetFlowSegmentsParams{Timerange: &v},
	})
	require.NoError(t, err)
	assert.IsType(t, api.GetFlowSegments200JSONResponse{}, get)
	require.Equal(t, 1, svc.listCalls)
	require.NotNil(t, svc.lastListReq.Timerange)
	assert.True(t, svc.lastListReq.Timerange.IsEmpty())

	del, err := h.DeleteFlowSegments(ctx, api.DeleteFlowSegmentsRequestObject{
		FlowId: flowID, Params: api.DeleteFlowSegmentsParams{Timerange: &v},
	})
	require.NoError(t, err)
	assert.IsType(t, api.DeleteFlowSegments204Response{}, del)
	require.Equal(t, 1, svc.deleteCalls)
	assert.True(t, svc.lastDeleteReq.Timerange.IsEmpty())

	var gotFlows *timerange.TimeRange
	fh := newFlowHandler(&mockFlowService{
		listFlows: func(_ context.Context, p metastore.ListFlowsParams) (*metastore.FlowPage, error) {
			gotFlows = p.Timerange
			return &metastore.FlowPage{}, nil
		},
	})
	gf, err := fh.GetFlows(ctx, api.GetFlowsRequestObject{Params: api.GetFlowsParams{Timerange: &v}})
	require.NoError(t, err)
	assert.IsType(t, api.GetFlows200JSONResponse{}, gf)
	require.NotNil(t, gotFlows, "empty range must reach ListFlows, not be dropped")
	assert.True(t, gotFlows.IsEmpty())
}

// BR-SEG-09: a query bound outside int64 nanoseconds is a 400
// invalid-timerange, not a 500.
func Test_BR_SEG_09_OutOfRangeQueryTimerangeIs400(t *testing.T) {
	v := "[9223372036:854775807]"
	ctx := context.Background()
	h := newRealSegHandler(t, &memSegmentStore{}, newStatefulIdem())
	flowID := uuidStr(t)

	get, err := h.GetFlowSegments(ctx, api.GetFlowSegmentsRequestObject{
		FlowId: flowID, Params: api.GetFlowSegmentsParams{Timerange: &v},
	})
	require.NoError(t, err)
	pd, ok := get.(api.GetFlowSegments400ApplicationProblemPlusJSONResponse)
	require.True(t, ok, "expected 400, got %T", get)
	require.NotNil(t, pd.Type)
	assert.Equal(t, invalidTimerangeType, *pd.Type)

	head, err := h.HeadFlowSegments(ctx, api.HeadFlowSegmentsRequestObject{
		FlowId: flowID, Params: api.HeadFlowSegmentsParams{Timerange: &v},
	})
	require.NoError(t, err)
	assert.IsType(t, api.HeadFlowSegments400ApplicationProblemPlusJSONResponse{}, head)

	del, err := h.DeleteFlowSegments(ctx, api.DeleteFlowSegmentsRequestObject{
		FlowId: flowID, Params: api.DeleteFlowSegmentsParams{Timerange: &v},
	})
	require.NoError(t, err)
	dpd, ok := del.(api.DeleteFlowSegments400ApplicationProblemPlusJSONResponse)
	require.True(t, ok, "expected 400, got %T", del)
	require.NotNil(t, dpd.Type)
	assert.Equal(t, invalidTimerangeType, *dpd.Type)
}

// ADR-0040 rule 3 / BR-SEG-14 / BR-IDMP-02: a constraint violation from
// the store is a server defect. The handler returns the error (the strict
// server renders 500) and releases the key, so a retry with the same key
// is processed again rather than replayed.
func Test_BR_SEG_14_ConstraintViolationIs500AndReleasesKey(t *testing.T) {
	store := &memSegmentStore{insertErr: fmt.Errorf("metastore: InsertSegments: insert seg 0: %w",
		&pgconn.PgError{Code: "23514", ConstraintName: "segments_bounds_nonempty"})}
	idem := newStatefulIdem()
	h := newRealSegHandler(t, store, idem)
	flowID := uuidStr(t)

	resp, err := postOne(t, h, flowID, "k-23514", "[0:0_1:0)")
	require.Error(t, err, "a constraint violation must surface as a handler error (500)")
	assert.Nil(t, resp)
	var pgErr *pgconn.PgError
	assert.ErrorAs(t, err, &pgErr)
	assert.Equal(t, 1, idem.releases, "the key must be released")
	assert.Zero(t, idem.completes, "a 5xx must not be cached")

	store.insertErr = nil
	resp, err = postOne(t, h, flowID, "k-23514", "[0:0_1:0)")
	require.NoError(t, err)
	assert.IsType(t, api.PostFlowSegments201Response{}, resp, "the retry must be processed, not replayed")
	assert.Equal(t, 2, store.insertCalls)
}

// ADR-0039 rule 7: HEAD /flows uses the same filter as GET /flows.
func Test_ADR0039_HeadFlowsFiltersLikeGet(t *testing.T) {
	v := "[0:0_10:0)"
	limit := 5
	var calls []metastore.ListFlowsParams
	fh := newFlowHandler(&mockFlowService{
		listFlows: func(_ context.Context, p metastore.ListFlowsParams) (*metastore.FlowPage, error) {
			calls = append(calls, p)
			return &metastore.FlowPage{}, nil
		},
	})
	ctx := context.Background()

	gf, err := fh.GetFlows(ctx, api.GetFlowsRequestObject{Params: api.GetFlowsParams{Timerange: &v, Limit: &limit}})
	require.NoError(t, err)
	assert.IsType(t, api.GetFlows200JSONResponse{}, gf)
	hf, err := fh.HeadFlows(ctx, api.HeadFlowsRequestObject{Params: api.HeadFlowsParams{Timerange: &v, Limit: &limit}})
	require.NoError(t, err)
	assert.IsType(t, api.HeadFlows200Response{}, hf)

	require.Len(t, calls, 2)
	require.NotNil(t, calls[0].Timerange)
	require.NotNil(t, calls[1].Timerange)
	assert.Equal(t, calls[0].Timerange.String(), calls[1].Timerange.String())
	assert.Equal(t, calls[0].Limit, calls[1].Limit)
}
