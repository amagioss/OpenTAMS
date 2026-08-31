package flow_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"

	"github.com/amagioss/opentams/internal/apperror"
	"github.com/amagioss/opentams/internal/metastore"
	"github.com/amagioss/opentams/internal/service"
	"github.com/amagioss/opentams/internal/service/flow"
	"github.com/amagioss/opentams/internal/timerange"
)

// --- mocks ---

type mockFlowStore struct {
	upsertFn       func(context.Context, *metastore.Flow) (bool, error)
	getFn          func(context.Context, uuid.UUID) (*metastore.Flow, error)
	getTimerangeFn func(context.Context, uuid.UUID) (*string, error)
	listFn         func(context.Context, metastore.ListFlowsParams) (*metastore.FlowPage, error)
	deleteFn       func(context.Context, uuid.UUID) error
}

func (m *mockFlowStore) UpsertFlow(ctx context.Context, f *metastore.Flow) (bool, error) {
	if m.upsertFn != nil {
		return m.upsertFn(ctx, f)
	}
	return false, nil
}
func (m *mockFlowStore) GetFlow(ctx context.Context, id uuid.UUID) (*metastore.Flow, error) {
	if m.getFn != nil {
		return m.getFn(ctx, id)
	}
	return &metastore.Flow{ID: id}, nil
}
func (m *mockFlowStore) GetFlowTimerange(ctx context.Context, id uuid.UUID) (*string, error) {
	if m.getTimerangeFn != nil {
		return m.getTimerangeFn(ctx, id)
	}
	return nil, nil
}
func (m *mockFlowStore) ListFlows(ctx context.Context, p metastore.ListFlowsParams) (*metastore.FlowPage, error) {
	if m.listFn != nil {
		return m.listFn(ctx, p)
	}
	return &metastore.FlowPage{}, nil
}
func (m *mockFlowStore) DeleteFlow(ctx context.Context, id uuid.UUID) error {
	if m.deleteFn != nil {
		return m.deleteFn(ctx, id)
	}
	return nil
}
func (m *mockFlowStore) PutFlowTag(ctx context.Context, id uuid.UUID, name string, value json.RawMessage) error {
	return nil
}
func (m *mockFlowStore) DeleteFlowTag(ctx context.Context, id uuid.UUID, name string) error {
	return nil
}
func (m *mockFlowStore) PutFlowLabel(ctx context.Context, id uuid.UUID, label string) error {
	return nil
}
func (m *mockFlowStore) DeleteFlowLabel(ctx context.Context, id uuid.UUID) error { return nil }
func (m *mockFlowStore) PutFlowDescription(ctx context.Context, id uuid.UUID, desc string) error {
	return nil
}
func (m *mockFlowStore) DeleteFlowDescription(ctx context.Context, id uuid.UUID) error { return nil }
func (m *mockFlowStore) PutFlowReadOnly(ctx context.Context, id uuid.UUID, readOnly bool) error {
	return nil
}
func (m *mockFlowStore) PutFlowCollection(ctx context.Context, id uuid.UUID, items []metastore.CollectionItem) error {
	return nil
}
func (m *mockFlowStore) DeleteFlowCollection(ctx context.Context, id uuid.UUID) error { return nil }
func (m *mockFlowStore) PutFlowAvgBitRate(ctx context.Context, id uuid.UUID, rate int64) error {
	return nil
}
func (m *mockFlowStore) DeleteFlowAvgBitRate(ctx context.Context, id uuid.UUID) error { return nil }
func (m *mockFlowStore) PutFlowMaxBitRate(ctx context.Context, id uuid.UUID, rate int64) error {
	return nil
}
func (m *mockFlowStore) DeleteFlowMaxBitRate(ctx context.Context, id uuid.UUID) error { return nil }

// --- helpers ---

func newSvc(t *testing.T, store metastore.FlowStore) flow.FlowService {
	t.Helper()
	reg := prometheus.NewRegistry()
	m, err := service.NewAppServiceMetrics(reg)
	if err != nil {
		t.Fatalf("NewAppServiceMetrics: %v", err)
	}
	return flow.New(store, zap.NewNop(), m)
}

// --- TC-SVC-FL-01: UpsertFlow with no essence_parameters — delegates to store ---

func TestUpsertFlow_NoEssenceParameters(t *testing.T) {
	store := &mockFlowStore{
		upsertFn: func(_ context.Context, f *metastore.Flow) (bool, error) { return true, nil },
	}
	svc := newSvc(t, store)
	created, err := svc.UpsertFlow(context.Background(), &metastore.Flow{
		ID:     uuid.New(),
		Format: "urn:x-nmos:format:video",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !created {
		t.Error("expected created=true")
	}
}

// --- TC-SVC-FL-02: UpsertFlow non-video format — no vfr check ---

func TestUpsertFlow_NonVideoFormat(t *testing.T) {
	svc := newSvc(t, &mockFlowStore{})
	_, err := svc.UpsertFlow(context.Background(), &metastore.Flow{
		Format:            "urn:x-nmos:format:audio",
		EssenceParameters: json.RawMessage(`{"sample_rate":48000}`),
	})
	if err != nil {
		t.Fatalf("unexpected error for audio format: %v", err)
	}
}

// --- TC-SVC-FL-03: UpsertFlow video, vfr absent, frame_rate absent — ErrVFRFrameRateConflict ---

func TestUpsertFlow_VideoVFRAbsentNoFrameRate(t *testing.T) {
	svc := newSvc(t, &mockFlowStore{})
	_, err := svc.UpsertFlow(context.Background(), &metastore.Flow{
		Format:            "urn:x-nmos:format:video",
		EssenceParameters: json.RawMessage(`{"frame_width":1920}`),
	})
	var ae *apperror.AppError
	if !errors.As(err, &ae) || ae.Code != apperror.ErrVFRFrameRateConflict {
		t.Errorf("expected ErrVFRFrameRateConflict, got %v", err)
	}
}

// --- TC-SVC-FL-04: UpsertFlow video, vfr=false, frame_rate absent — ErrVFRFrameRateConflict ---

func TestUpsertFlow_VideoVFRFalseNoFrameRate(t *testing.T) {
	svc := newSvc(t, &mockFlowStore{})
	_, err := svc.UpsertFlow(context.Background(), &metastore.Flow{
		Format:            "urn:x-nmos:format:video",
		EssenceParameters: json.RawMessage(`{"vfr":false,"frame_width":1920}`),
	})
	var ae *apperror.AppError
	if !errors.As(err, &ae) || ae.Code != apperror.ErrVFRFrameRateConflict {
		t.Errorf("expected ErrVFRFrameRateConflict, got %v", err)
	}
}

// --- TC-SVC-FL-05: UpsertFlow video, vfr=false, frame_rate present — success ---

func TestUpsertFlow_VideoVFRFalseWithFrameRate(t *testing.T) {
	svc := newSvc(t, &mockFlowStore{})
	_, err := svc.UpsertFlow(context.Background(), &metastore.Flow{
		Format:            "urn:x-nmos:format:video",
		EssenceParameters: json.RawMessage(`{"vfr":false,"frame_rate":"25/1"}`),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// --- TC-SVC-FL-06: UpsertFlow video, vfr=true — frame_rate not required ---

func TestUpsertFlow_VideoVFRTrue(t *testing.T) {
	svc := newSvc(t, &mockFlowStore{})
	_, err := svc.UpsertFlow(context.Background(), &metastore.Flow{
		Format:            "urn:x-nmos:format:video",
		EssenceParameters: json.RawMessage(`{"vfr":true,"frame_width":1920}`),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// --- TC-SVC-FL-06b: UpsertFlow video, vfr=true, frame_rate present — ErrVFRFrameRateConflict ---

func TestUpsertFlow_VideoVFRTrueWithFrameRate(t *testing.T) {
	svc := newSvc(t, &mockFlowStore{})
	_, err := svc.UpsertFlow(context.Background(), &metastore.Flow{
		Format:            "urn:x-nmos:format:video",
		EssenceParameters: json.RawMessage(`{"vfr":true,"frame_rate":"25/1"}`),
	})
	var ae *apperror.AppError
	if !errors.As(err, &ae) || ae.Code != apperror.ErrVFRFrameRateConflict {
		t.Errorf("expected ErrVFRFrameRateConflict, got %v", err)
	}
}

// --- TC-SVC-FL-05b: UpsertFlow video, vfr=false, frame_rate as spec-compliant object — success ---

func TestUpsertFlow_VideoVFRFalseWithFrameRateObject(t *testing.T) {
	svc := newSvc(t, &mockFlowStore{})
	_, err := svc.UpsertFlow(context.Background(), &metastore.Flow{
		Format:            "urn:x-nmos:format:video",
		EssenceParameters: json.RawMessage(`{"vfr":false,"frame_rate":{"numerator":25,"denominator":1}}`),
	})
	if err != nil {
		t.Fatalf("unexpected error for spec-compliant frame_rate object: %v", err)
	}
}

// --- TC-SVC-FL-06c: UpsertFlow video, vfr=true, frame_rate as object — ErrVFRFrameRateConflict ---

func TestUpsertFlow_VideoVFRTrueWithFrameRateObject(t *testing.T) {
	svc := newSvc(t, &mockFlowStore{})
	_, err := svc.UpsertFlow(context.Background(), &metastore.Flow{
		Format:            "urn:x-nmos:format:video",
		EssenceParameters: json.RawMessage(`{"vfr":true,"frame_rate":{"numerator":25}}`),
	})
	var ae *apperror.AppError
	if !errors.As(err, &ae) || ae.Code != apperror.ErrVFRFrameRateConflict {
		t.Errorf("expected ErrVFRFrameRateConflict, got %v", err)
	}
}

// --- TC-SVC-FL-07: UpsertFlow invalid essence_parameters JSON — ErrSchemaValidation ---

func TestUpsertFlow_InvalidEssenceParametersJSON(t *testing.T) {
	svc := newSvc(t, &mockFlowStore{})
	_, err := svc.UpsertFlow(context.Background(), &metastore.Flow{
		Format:            "urn:x-nmos:format:video",
		EssenceParameters: json.RawMessage(`{invalid`),
	})
	var ae *apperror.AppError
	if !errors.As(err, &ae) || ae.Code != apperror.ErrSchemaValidation {
		t.Errorf("expected ErrSchemaValidation, got %v", err)
	}
}

// --- TC-SVC-FL-08: UpsertFlow store error propagated ---

func TestUpsertFlow_StoreError(t *testing.T) {
	storeErr := apperror.New(apperror.ErrNotFound, "source not found")
	store := &mockFlowStore{
		upsertFn: func(_ context.Context, _ *metastore.Flow) (bool, error) { return false, storeErr },
	}
	svc := newSvc(t, store)
	_, err := svc.UpsertFlow(context.Background(), &metastore.Flow{Format: "urn:x-nmos:format:audio"})
	if !errors.Is(err, storeErr) {
		t.Errorf("expected store error, got %v", err)
	}
}

// --- TC-SVC-FL-09: GetFlow without include_timerange ---

func TestGetFlow_NoIncludeTimerange(t *testing.T) {
	id := uuid.New()
	var getTimerangeCalled bool
	store := &mockFlowStore{
		getFn: func(_ context.Context, _ uuid.UUID) (*metastore.Flow, error) {
			return &metastore.Flow{ID: id}, nil
		},
		getTimerangeFn: func(_ context.Context, _ uuid.UUID) (*string, error) {
			getTimerangeCalled = true
			return nil, nil
		},
	}
	svc := newSvc(t, store)
	f, err := svc.GetFlow(context.Background(), id, false, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.Timerange != nil {
		t.Error("expected nil timerange when include_timerange=false")
	}
	if getTimerangeCalled {
		t.Error("GetFlowTimerange must not be called when include_timerange=false")
	}
}

// --- TC-SVC-FL-10: GetFlow with include_timerange=true, no segments ---

func TestGetFlow_IncludeTimerangeNoSegments(t *testing.T) {
	id := uuid.New()
	store := &mockFlowStore{
		getFn: func(_ context.Context, _ uuid.UUID) (*metastore.Flow, error) {
			return &metastore.Flow{ID: id}, nil
		},
		getTimerangeFn: func(_ context.Context, _ uuid.UUID) (*string, error) {
			return nil, nil
		},
	}
	svc := newSvc(t, store)
	f, err := svc.GetFlow(context.Background(), id, true, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.Timerange != nil {
		t.Errorf("expected nil timerange for flow with no segments, got %q", *f.Timerange)
	}
}

// --- TC-SVC-FL-11: GetFlow with include_timerange=true, segments exist, no filter ---

func TestGetFlow_IncludeTimerangeWithSegments(t *testing.T) {
	id := uuid.New()
	trStr := "[0:0_10:0)"
	store := &mockFlowStore{
		getFn: func(_ context.Context, _ uuid.UUID) (*metastore.Flow, error) {
			return &metastore.Flow{ID: id}, nil
		},
		getTimerangeFn: func(_ context.Context, _ uuid.UUID) (*string, error) {
			return &trStr, nil
		},
	}
	svc := newSvc(t, store)
	f, err := svc.GetFlow(context.Background(), id, true, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.Timerange == nil || *f.Timerange != trStr {
		t.Errorf("expected timerange %q, got %v", trStr, f.Timerange)
	}
}

// --- TC-SVC-FL-12: GetFlow with include_timerange=true and timerange filter — intersection ---

func TestGetFlow_IncludeTimerangeWithFilter(t *testing.T) {
	id := uuid.New()
	trStr := "[0:0_20:0)"
	store := &mockFlowStore{
		getFn: func(_ context.Context, _ uuid.UUID) (*metastore.Flow, error) {
			return &metastore.Flow{ID: id}, nil
		},
		getTimerangeFn: func(_ context.Context, _ uuid.UUID) (*string, error) {
			return &trStr, nil
		},
	}
	svc := newSvc(t, store)
	filter, _ := timerange.Parse("[5:0_15:0)")
	f, err := svc.GetFlow(context.Background(), id, true, &filter)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.Timerange == nil || *f.Timerange != "[5:0_15:0)" {
		t.Errorf("expected intersection [5:0_15:0), got %v", f.Timerange)
	}
}

// --- TC-SVC-FL-13: GetFlow store error propagated ---

func TestGetFlow_StoreError(t *testing.T) {
	storeErr := apperror.New(apperror.ErrNotFound, "flow not found")
	store := &mockFlowStore{
		getFn: func(_ context.Context, _ uuid.UUID) (*metastore.Flow, error) {
			return nil, storeErr
		},
	}
	svc := newSvc(t, store)
	_, err := svc.GetFlow(context.Background(), uuid.New(), false, nil)
	if !errors.Is(err, storeErr) {
		t.Errorf("expected store error, got %v", err)
	}
}

// --- TC-SVC-FL-13b: GetFlow GetFlowTimerange returns unparseable timerange string ---

func TestGetFlow_InvalidTimerangeFromStore(t *testing.T) {
	bad := "not-a-valid-timerange"
	store := &mockFlowStore{
		getTimerangeFn: func(_ context.Context, _ uuid.UUID) (*string, error) {
			return &bad, nil
		},
	}
	svc := newSvc(t, store)
	filter, _ := timerange.Parse("[0:0_10:0)")
	_, err := svc.GetFlow(context.Background(), uuid.New(), true, &filter)
	if err == nil {
		t.Error("expected error for invalid timerange from store")
	}
}

// --- TC-SVC-FL-14: GetFlow GetFlowTimerange error propagated ---

func TestGetFlow_GetTimerangeError(t *testing.T) {
	trErr := errors.New("db error")
	store := &mockFlowStore{
		getTimerangeFn: func(_ context.Context, _ uuid.UUID) (*string, error) {
			return nil, trErr
		},
	}
	svc := newSvc(t, store)
	_, err := svc.GetFlow(context.Background(), uuid.New(), true, nil)
	if !errors.Is(err, trErr) {
		t.Errorf("expected GetFlowTimerange error, got %v", err)
	}
}

// --- TC-SVC-FL-15: DeleteFlow delegates to store, no objectstore call ---
//
// D-25 / D-29: the flow service has no objectstore dependency on its DELETE
// path. Zero-ref reaping is the GC worker's responsibility (BR-OBJ-11).
func TestDeleteFlow_DelegatesToStore(t *testing.T) {
	var called bool
	store := &mockFlowStore{
		deleteFn: func(_ context.Context, _ uuid.UUID) error {
			called = true
			return nil
		},
	}
	svc := newSvc(t, store)
	if err := svc.DeleteFlow(context.Background(), uuid.New()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Error("expected store.DeleteFlow to be called")
	}
}

// --- TC-SVC-FL-18: DeleteFlow store error propagated ---

func TestDeleteFlow_StoreError(t *testing.T) {
	storeErr := apperror.New(apperror.ErrNotFound, "flow not found")
	store := &mockFlowStore{
		deleteFn: func(_ context.Context, _ uuid.UUID) error {
			return storeErr
		},
	}
	svc := newSvc(t, store)
	err := svc.DeleteFlow(context.Background(), uuid.New())
	if !errors.Is(err, storeErr) {
		t.Errorf("expected store error, got %v", err)
	}
}

// --- TC-SVC-FL-19: ListFlows delegates to store ---

func TestListFlows_Delegates(t *testing.T) {
	expected := &metastore.FlowPage{Items: []*metastore.Flow{{ID: uuid.New()}}}
	store := &mockFlowStore{
		listFn: func(_ context.Context, _ metastore.ListFlowsParams) (*metastore.FlowPage, error) {
			return expected, nil
		},
	}
	svc := newSvc(t, store)
	page, err := svc.ListFlows(context.Background(), metastore.ListFlowsParams{Limit: 10})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(page.Items) != 1 {
		t.Errorf("expected 1 item, got %d", len(page.Items))
	}
}

// --- TC-SVC-FL-20 through TC-SVC-FL-31: sub-resource passthrough coverage ---

func TestSubResourcePassthrough(t *testing.T) {
	svc := newSvc(t, &mockFlowStore{})
	ctx := context.Background()
	id := uuid.New()

	if err := svc.PutFlowTag(ctx, id, "key", json.RawMessage(`"val"`)); err != nil {
		t.Errorf("PutFlowTag: %v", err)
	}
	if err := svc.DeleteFlowTag(ctx, id, "key"); err != nil {
		t.Errorf("DeleteFlowTag: %v", err)
	}
	if err := svc.PutFlowLabel(ctx, id, "lbl"); err != nil {
		t.Errorf("PutFlowLabel: %v", err)
	}
	if err := svc.DeleteFlowLabel(ctx, id); err != nil {
		t.Errorf("DeleteFlowLabel: %v", err)
	}
	if err := svc.PutFlowDescription(ctx, id, "desc"); err != nil {
		t.Errorf("PutFlowDescription: %v", err)
	}
	if err := svc.DeleteFlowDescription(ctx, id); err != nil {
		t.Errorf("DeleteFlowDescription: %v", err)
	}
	if err := svc.PutFlowReadOnly(ctx, id, true); err != nil {
		t.Errorf("PutFlowReadOnly: %v", err)
	}
	if err := svc.PutFlowCollection(ctx, id, nil); err != nil {
		t.Errorf("PutFlowCollection: %v", err)
	}
	if err := svc.DeleteFlowCollection(ctx, id); err != nil {
		t.Errorf("DeleteFlowCollection: %v", err)
	}
	if err := svc.PutFlowAvgBitRate(ctx, id, 1000); err != nil {
		t.Errorf("PutFlowAvgBitRate: %v", err)
	}
	if err := svc.DeleteFlowAvgBitRate(ctx, id); err != nil {
		t.Errorf("DeleteFlowAvgBitRate: %v", err)
	}
	if err := svc.PutFlowMaxBitRate(ctx, id, 2000); err != nil {
		t.Errorf("PutFlowMaxBitRate: %v", err)
	}
	if err := svc.DeleteFlowMaxBitRate(ctx, id); err != nil {
		t.Errorf("DeleteFlowMaxBitRate: %v", err)
	}
}
