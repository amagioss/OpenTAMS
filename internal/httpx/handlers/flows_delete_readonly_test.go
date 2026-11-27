package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/amagioss/opentams/gen/api"
	"github.com/amagioss/opentams/internal/apperror"
	"github.com/amagioss/opentams/internal/httpx/middleware"
)

// newErrorMiddlewareRouter mounts the strict handler behind
// middleware.ErrorHandler with the error funcs of internal/server, so a
// returned *apperror.AppError renders as it does in production.
func newErrorMiddlewareRouter(svc *mockFlowService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(middleware.ErrorHandler())
	strictOpts := api.StrictGinServerOptions{
		RequestErrorHandlerFunc: func(c *gin.Context, err error) {
			c.Status(http.StatusBadRequest)
			_ = c.Error(err)
		},
		HandlerErrorFunc: func(c *gin.Context, err error) {
			_ = c.Error(err)
			c.Status(http.StatusInternalServerError)
		},
		ResponseErrorHandlerFunc: func(c *gin.Context, err error) {
			_ = c.Error(err)
		},
	}
	api.RegisterHandlersWithOptions(engine, api.NewStrictHandlerWithOptions(newFlowHandler(svc), nil, strictOpts), api.GinServerOptions{})
	return engine
}

// TC-GC-048: DELETE /flows/{flowId} of a read-only flow gives 403.
func TestDeleteFlow_readOnlyFlow_returns403ReadOnly(t *testing.T) {
	svc := &mockFlowService{
		deleteFlow: func(_ context.Context, _ uuid.UUID) error {
			return apperror.New(apperror.ErrReadOnly, "flow is read-only")
		},
	}
	router := newErrorMiddlewareRouter(svc)

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
