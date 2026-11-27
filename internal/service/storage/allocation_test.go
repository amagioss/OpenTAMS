package storage_test

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/amagioss/opentams/internal/apperror"
	"github.com/amagioss/opentams/internal/metastore"
	"github.com/amagioss/opentams/internal/service/storage"
)

func h264Flow() *metastore.Flow {
	codec := "video/h264"
	return okFlow(&codec)
}

// TC-GC-002: The service sends the IDs and settings to the store.
func TestAllocateStorage_limitMode_sendsGeneratedIDsAndSettingsToStoreAfterURLs(t *testing.T) {
	var calls []string
	store := &mockFlowReader{
		getFlowFn: func(_ context.Context, _ uuid.UUID) (*metastore.Flow, error) {
			return h264Flow(), nil
		},
		allocateObjectsFn: func(_ context.Context, _ metastore.AllocateParams) error {
			calls = append(calls, "AllocateObjects")
			return nil
		},
	}
	obj := &mockObjectStore{generateUploadURLFn: func(_ context.Context, id, _ string) (string, error) {
		calls = append(calls, "GenerateUploadURL")
		return "https://u/" + id, nil
	}}
	svc := newSvc(t, store, obj)

	result, err := svc.AllocateStorage(context.Background(), uuid.New(), storage.AllocateRequest{Limit: intPtr(2)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wantCalls := []string{"GenerateUploadURL", "GenerateUploadURL", "AllocateObjects"}
	if !slices.Equal(calls, wantCalls) {
		t.Fatalf("call order = %v, want %v", calls, wantCalls)
	}
	if len(store.allocateCalls) != 1 {
		t.Fatalf("AllocateObjects called %d times, want 1", len(store.allocateCalls))
	}
	p := store.allocateCalls[0]
	if p.Requested {
		t.Error("Requested = true, want false in mode limit")
	}
	if p.TTL != testAllocTTL {
		t.Errorf("TTL = %v, want %v", p.TTL, testAllocTTL)
	}
	if p.StorageID != testStorageID {
		t.Errorf("StorageID = %q, want %q", p.StorageID, testStorageID)
	}
	responseIDs := make([]string, len(result))
	for i, mo := range result {
		responseIDs[i] = mo.ObjectID
		if mo.PutURL != "https://u/"+mo.ObjectID {
			t.Errorf("item %d: PutURL = %q, want URL for %q", i, mo.PutURL, mo.ObjectID)
		}
	}
	if !slices.Equal(p.IDs, responseIDs) {
		t.Errorf("store IDs = %v, response IDs = %v", p.IDs, responseIDs)
	}
}

// TC-GC-003: Mode object_ids with new IDs keeps the request sequence.
func TestAllocateStorage_objectIDsMode_keepsRequestSequence(t *testing.T) {
	flowID := uuid.New()
	store := &mockFlowReader{
		getFlowFn: func(_ context.Context, _ uuid.UUID) (*metastore.Flow, error) {
			return h264Flow(), nil
		},
	}
	svc := newSvc(t, store, &mockObjectStore{generateUploadURLFn: okUploadURL})

	result, err := svc.AllocateStorage(context.Background(), flowID, storage.AllocateRequest{ObjectIDs: []string{"z", "a"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result) != 2 || result[0].ObjectID != "z" || result[1].ObjectID != "a" {
		t.Fatalf("response objects = %+v, want z then a", result)
	}
	if len(store.allocateCalls) != 1 {
		t.Fatalf("AllocateObjects called %d times, want 1", len(store.allocateCalls))
	}
	p := store.allocateCalls[0]
	if !slices.Equal(p.IDs, []string{"z", "a"}) {
		t.Errorf("store IDs = %v, want [z a]", p.IDs)
	}
	if !p.Requested {
		t.Error("Requested = false, want true in mode object_ids")
	}
	if p.FlowID != flowID {
		t.Errorf("FlowID = %v, want %v", p.FlowID, flowID)
	}
}

// TC-GC-005: An ID with an object row gives object-exists (service part),
// with the same mapping for the other two allocation errors.
func TestAllocateStorage_objectIDsMode_mapsStoreObjectErrorsTo400Problems(t *testing.T) {
	otherFlow := uuid.New()
	cases := []struct {
		name     string
		sentinel error
		wantCode apperror.ErrorCode
	}{
		{"object exists", metastore.ErrObjectExists, apperror.ErrObjectExists},
		{"object allocated to another flow", metastore.ErrObjectWrongFlow, apperror.ErrObjectWrongFlow},
		{"object being deleted", metastore.ErrObjectDeleting, apperror.ErrObjectDeleting},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			flowID := uuid.New()
			store := &mockFlowReader{
				getFlowFn: func(_ context.Context, _ uuid.UUID) (*metastore.Flow, error) {
					return h264Flow(), nil
				},
				allocateObjectsFn: func(_ context.Context, _ metastore.AllocateParams) error {
					return &metastore.ObjectError{ID: "x1", Err: tc.sentinel}
				},
			}
			svc := newSvc(t, store, &mockObjectStore{generateUploadURLFn: okUploadURL})

			result, err := svc.AllocateStorage(context.Background(), flowID, storage.AllocateRequest{ObjectIDs: []string{"x1"}})

			if result != nil {
				t.Errorf("result = %+v, want nil", result)
			}
			var ae *apperror.AppError
			if !errors.As(err, &ae) {
				t.Fatalf("error = %v, want *apperror.AppError", err)
			}
			if ae.Code != tc.wantCode {
				t.Errorf("code = %q, want %q", ae.Code, tc.wantCode)
			}
			if ae.Status != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", ae.Status)
			}
			if !strings.Contains(ae.Detail, `"x1"`) {
				t.Errorf("detail = %q, want it to name object_id \"x1\"", ae.Detail)
			}
			for _, id := range []uuid.UUID{flowID, otherFlow} {
				if strings.Contains(ae.Detail, id.String()) {
					t.Errorf("detail = %q names a flow ID", ae.Detail)
				}
			}
		})
	}
}

// TC-GC-011: A URL generation error stops the request before the database.
func TestAllocateStorage_limitMode_urlErrorOnSecondID_doesNotCallStore(t *testing.T) {
	urlErr := errors.New("presign failed")
	store := &mockFlowReader{
		getFlowFn: func(_ context.Context, _ uuid.UUID) (*metastore.Flow, error) {
			return h264Flow(), nil
		},
	}
	n := 0
	obj := &mockObjectStore{generateUploadURLFn: func(_ context.Context, id, _ string) (string, error) {
		n++
		if n == 2 {
			return "", urlErr
		}
		return "https://u/" + id, nil
	}}
	svc := newSvc(t, store, obj)

	_, err := svc.AllocateStorage(context.Background(), uuid.New(), storage.AllocateRequest{Limit: intPtr(2)})

	if !errors.Is(err, urlErr) {
		t.Errorf("error = %v, want urlErr", err)
	}
	if len(store.allocateCalls) != 0 {
		t.Errorf("AllocateObjects called %d times, want 0", len(store.allocateCalls))
	}
}

// TC-GC-015: Missing flow and read-only flow keep their status.
func TestAllocateStorage_missingOrReadOnlyFlow_keepsStatusAndDoesNotCallStore(t *testing.T) {
	cases := []struct {
		name       string
		getFlow    func(context.Context, uuid.UUID) (*metastore.Flow, error)
		wantCode   apperror.ErrorCode
		wantStatus int
	}{
		{
			name: "missing flow",
			getFlow: func(_ context.Context, _ uuid.UUID) (*metastore.Flow, error) {
				return nil, apperror.New(apperror.ErrNotFound, "flow not found")
			},
			wantCode:   apperror.ErrNotFound,
			wantStatus: http.StatusNotFound,
		},
		{
			name: "read-only flow",
			getFlow: func(_ context.Context, _ uuid.UUID) (*metastore.Flow, error) {
				return &metastore.Flow{ReadOnly: true}, nil
			},
			wantCode:   apperror.ErrReadOnly,
			wantStatus: http.StatusForbidden,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &mockFlowReader{getFlowFn: tc.getFlow}
			svc := newSvc(t, store, &mockObjectStore{generateUploadURLFn: okUploadURL})

			_, err := svc.AllocateStorage(context.Background(), uuid.New(), storage.AllocateRequest{Limit: intPtr(1)})

			var ae *apperror.AppError
			if !errors.As(err, &ae) || ae.Code != tc.wantCode || ae.Status != tc.wantStatus {
				t.Fatalf("error = %v, want %s (%d)", err, tc.wantCode, tc.wantStatus)
			}
			if len(store.allocateCalls) != 0 {
				t.Errorf("AllocateObjects called %d times, want 0", len(store.allocateCalls))
			}
		})
	}
}
