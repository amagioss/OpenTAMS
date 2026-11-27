package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"github.com/amagioss/opentams/internal/apperror"
	"github.com/amagioss/opentams/internal/httpx/handlers"
	"github.com/amagioss/opentams/internal/service/storage"
)

type fakeStorageService struct {
	allocateStorage func(context.Context, uuid.UUID, storage.AllocateRequest) ([]storage.MediaObject, error)
}

func (f *fakeStorageService) AllocateStorage(ctx context.Context, flowID uuid.UUID, req storage.AllocateRequest) ([]storage.MediaObject, error) {
	return f.allocateStorage(ctx, flowID, req)
}

// TC-GC-014: The handler renders the allocation errors as problem details.
func TestPostFlowStorage_allocationError_rendersProblemDetailsAndLogsWarn(t *testing.T) {
	svc := &fakeStorageService{
		allocateStorage: func(_ context.Context, _ uuid.UUID, _ storage.AllocateRequest) ([]storage.MediaObject, error) {
			return nil, apperror.New(apperror.ErrObjectWrongFlow, `object_id "x1" is allocated to a different flow`)
		},
	}
	core, logs := observer.New(zap.DebugLevel)
	h := handlers.New(zap.New(core), testConfig(), nil, nil, nil, svc, nil, nil, nil)
	router := newErrorMiddlewareRouter(h)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/tams/v1/flows/"+uuid.New().String()+"/storage",
		strings.NewReader(`{"object_ids":["x1"]}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code, "body=%s", rec.Body.String())
	assert.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))
	var body struct {
		Type  string `json:"type"`
		Title string `json:"title"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), "body=%s", rec.Body.String())
	assert.Equal(t, "https://github.com/amagioss/opentams/problems/object-wrong-flow", body.Type)
	assert.Equal(t, "Object Allocated to Another Flow", body.Title)

	assert.Zero(t, logs.FilterLevelExact(zapcore.ErrorLevel).Len(), "unexpected ERROR log: %v", logs.All())
	assert.Equal(t, 1, logs.FilterLevelExact(zapcore.WarnLevel).Len(), "want one WARN log: %v", logs.All())
}
