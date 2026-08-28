package storage_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/amagioss/opentams/internal/apperror"
	"github.com/amagioss/opentams/internal/metastore"
	"github.com/amagioss/opentams/internal/service"
	"github.com/amagioss/opentams/internal/service/storage"
)

type mockFlowReader struct {
	getFlowFn            func(context.Context, uuid.UUID) (*metastore.Flow, error)
	isObjectRegisteredFn func(context.Context, string) (bool, error)
}

func (m *mockFlowReader) GetFlow(ctx context.Context, id uuid.UUID) (*metastore.Flow, error) {
	return m.getFlowFn(ctx, id)
}
func (m *mockFlowReader) IsObjectRegistered(ctx context.Context, objectID string) (bool, error) {
	return m.isObjectRegisteredFn(ctx, objectID)
}

type mockObjectStore struct {
	generateUploadURLFn func(context.Context, string, string) (string, error)
}

func (m *mockObjectStore) GenerateUploadURL(ctx context.Context, objectID, contentType string) (string, error) {
	return m.generateUploadURLFn(ctx, objectID, contentType)
}

func newSvc(t *testing.T, store storage.FlowReader, obj storage.ObjectStore) storage.StorageService {
	t.Helper()
	reg := prometheus.NewRegistry()
	m, err := service.NewAppServiceMetrics(reg)
	if err != nil {
		t.Fatalf("NewAppServiceMetrics: %v", err)
	}
	return storage.New(store, obj, m)
}

func okFlow(codec *string) *metastore.Flow {
	return &metastore.Flow{ID: uuid.New(), Codec: codec}
}

func okUploadURL(_ context.Context, objectID, _ string) (string, error) {
	return "https://s3.example.com/" + objectID, nil
}

// TC-SVC-STG-01: GetFlow error (raw, non-apperror) — propagated, observe records "internal".
func TestAllocateStorage_GetFlowError(t *testing.T) {
	storeErr := errors.New("db timeout")
	store := &mockFlowReader{
		getFlowFn: func(_ context.Context, _ uuid.UUID) (*metastore.Flow, error) {
			return nil, storeErr
		},
	}
	svc := newSvc(t, store, &mockObjectStore{})
	_, err := svc.AllocateStorage(context.Background(), uuid.New(), storage.AllocateRequest{Limit: intPtr(1)})
	if !errors.Is(err, storeErr) {
		t.Errorf("expected storeErr, got %v", err)
	}
}

// TC-SVC-STG-02: read_only flow — ErrReadOnly.
func TestAllocateStorage_ReadOnly(t *testing.T) {
	store := &mockFlowReader{
		getFlowFn: func(_ context.Context, _ uuid.UUID) (*metastore.Flow, error) {
			return &metastore.Flow{ReadOnly: true}, nil
		},
	}
	svc := newSvc(t, store, &mockObjectStore{})
	_, err := svc.AllocateStorage(context.Background(), uuid.New(), storage.AllocateRequest{Limit: intPtr(1)})
	var ae *apperror.AppError
	if !errors.As(err, &ae) || ae.Code != apperror.ErrReadOnly {
		t.Errorf("expected ErrReadOnly, got %v", err)
	}
}

// TC-SVC-STG-03: limit mode, nil codec — content-type defaults to "application/octet-stream".
func TestAllocateStorage_LimitMode_NilCodec(t *testing.T) {
	store := &mockFlowReader{
		getFlowFn: func(_ context.Context, _ uuid.UUID) (*metastore.Flow, error) {
			return okFlow(nil), nil
		},
	}
	obj := &mockObjectStore{generateUploadURLFn: okUploadURL}
	svc := newSvc(t, store, obj)
	result, err := svc.AllocateStorage(context.Background(), uuid.New(), storage.AllocateRequest{Limit: intPtr(2)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 objects, got %d", len(result))
	}
	for _, mo := range result {
		if mo.ContentType != "application/octet-stream" {
			t.Errorf("expected application/octet-stream, got %q", mo.ContentType)
		}
		if mo.ObjectID == "" {
			t.Error("ObjectID must not be empty")
		}
		if mo.PutURL == "" {
			t.Error("PutURL must not be empty")
		}
	}
}

// TC-SVC-STG-04: limit mode, codec set — content-type matches codec.
func TestAllocateStorage_LimitMode_WithCodec(t *testing.T) {
	codec := "video/mp4"
	store := &mockFlowReader{
		getFlowFn: func(_ context.Context, _ uuid.UUID) (*metastore.Flow, error) {
			return okFlow(&codec), nil
		},
	}
	obj := &mockObjectStore{generateUploadURLFn: okUploadURL}
	svc := newSvc(t, store, obj)
	result, err := svc.AllocateStorage(context.Background(), uuid.New(), storage.AllocateRequest{Limit: intPtr(1)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 1 || result[0].ContentType != codec {
		t.Errorf("expected ContentType %q, got %v", codec, result)
	}
}

// TC-SVC-STG-05: limit mode, GenerateUploadURL error — propagated immediately.
func TestAllocateStorage_LimitMode_URLError(t *testing.T) {
	urlErr := errors.New("presign failed")
	store := &mockFlowReader{
		getFlowFn: func(_ context.Context, _ uuid.UUID) (*metastore.Flow, error) {
			return okFlow(nil), nil
		},
	}
	obj := &mockObjectStore{
		generateUploadURLFn: func(_ context.Context, _, _ string) (string, error) {
			return "", urlErr
		},
	}
	svc := newSvc(t, store, obj)
	_, err := svc.AllocateStorage(context.Background(), uuid.New(), storage.AllocateRequest{Limit: intPtr(1)})
	if !errors.Is(err, urlErr) {
		t.Errorf("expected urlErr, got %v", err)
	}
}

// TC-SVC-STG-06: object_ids mode, IsObjectRegistered returns error — propagated.
func TestAllocateStorage_ObjectIDsMode_IsRegisteredError(t *testing.T) {
	regErr := errors.New("db error")
	store := &mockFlowReader{
		getFlowFn: func(_ context.Context, _ uuid.UUID) (*metastore.Flow, error) {
			return okFlow(nil), nil
		},
		isObjectRegisteredFn: func(_ context.Context, _ string) (bool, error) {
			return false, regErr
		},
	}
	svc := newSvc(t, store, &mockObjectStore{})
	_, err := svc.AllocateStorage(context.Background(), uuid.New(), storage.AllocateRequest{
		ObjectIDs: []string{"obj-1"},
	})
	if !errors.Is(err, regErr) {
		t.Errorf("expected regErr, got %v", err)
	}
}

// TC-SVC-STG-07: object_ids mode, objectID already registered — ErrObjectIDExists.
func TestAllocateStorage_ObjectIDsMode_AlreadyRegistered(t *testing.T) {
	store := &mockFlowReader{
		getFlowFn: func(_ context.Context, _ uuid.UUID) (*metastore.Flow, error) {
			return okFlow(nil), nil
		},
		isObjectRegisteredFn: func(_ context.Context, _ string) (bool, error) {
			return true, nil
		},
	}
	svc := newSvc(t, store, &mockObjectStore{})
	_, err := svc.AllocateStorage(context.Background(), uuid.New(), storage.AllocateRequest{
		ObjectIDs: []string{"obj-1"},
	})
	var ae *apperror.AppError
	if !errors.As(err, &ae) || ae.Code != apperror.ErrObjectIDExists {
		t.Errorf("expected ErrObjectIDExists, got %v", err)
	}
}

// TC-SVC-STG-08: object_ids mode, success — correct ObjectIDs, PutURLs, and ContentTypes returned.
func TestAllocateStorage_ObjectIDsMode_Success(t *testing.T) {
	codec := "video/h264"
	store := &mockFlowReader{
		getFlowFn: func(_ context.Context, _ uuid.UUID) (*metastore.Flow, error) {
			return okFlow(&codec), nil
		},
		isObjectRegisteredFn: func(_ context.Context, _ string) (bool, error) {
			return false, nil
		},
	}
	obj := &mockObjectStore{generateUploadURLFn: okUploadURL}
	svc := newSvc(t, store, obj)
	ids := []string{"obj-1", "obj-2"}
	result, err := svc.AllocateStorage(context.Background(), uuid.New(), storage.AllocateRequest{ObjectIDs: ids})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 objects, got %d", len(result))
	}
	for i, mo := range result {
		if mo.ObjectID != ids[i] {
			t.Errorf("item %d: expected ObjectID %q, got %q", i, ids[i], mo.ObjectID)
		}
		if mo.ContentType != codec {
			t.Errorf("item %d: expected ContentType %q, got %q", i, codec, mo.ContentType)
		}
	}
}

// TC-SVC-STG-09: object_ids mode, GenerateUploadURL error — propagated immediately.
func TestAllocateStorage_ObjectIDsMode_URLError(t *testing.T) {
	urlErr := errors.New("presign failed")
	store := &mockFlowReader{
		getFlowFn: func(_ context.Context, _ uuid.UUID) (*metastore.Flow, error) {
			return okFlow(nil), nil
		},
		isObjectRegisteredFn: func(_ context.Context, _ string) (bool, error) {
			return false, nil
		},
	}
	obj := &mockObjectStore{
		generateUploadURLFn: func(_ context.Context, _, _ string) (string, error) {
			return "", urlErr
		},
	}
	svc := newSvc(t, store, obj)
	_, err := svc.AllocateStorage(context.Background(), uuid.New(), storage.AllocateRequest{
		ObjectIDs: []string{"obj-1"},
	})
	if !errors.Is(err, urlErr) {
		t.Errorf("expected urlErr, got %v", err)
	}
}

func intPtr(n int) *int { return &n }
