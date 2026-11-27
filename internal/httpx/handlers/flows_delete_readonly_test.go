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

	"github.com/amagioss/opentams/internal/apperror"
)

// TC-GC-048: DELETE /flows/{flowId} of a read-only flow gives 403.
func TestDeleteFlow_readOnlyFlow_returns403ReadOnly(t *testing.T) {
	svc := &mockFlowService{
		deleteFlow: func(_ context.Context, _ uuid.UUID) error {
			return apperror.New(apperror.ErrReadOnly, "flow is read-only")
		},
	}
	router := newErrorMiddlewareRouter(newFlowHandler(svc))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/tams/v1/flows/"+uuid.New().String(), http.NoBody)
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusForbidden, rec.Code, "body=%s", rec.Body.String())
	var body struct {
		Type string `json:"type"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), "body=%s", rec.Body.String())
	assert.True(t, strings.HasSuffix(body.Type, "read-only"), "type=%q", body.Type)
}
