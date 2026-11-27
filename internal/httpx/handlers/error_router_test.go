package handlers_test

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/amagioss/opentams/gen/api"
	"github.com/amagioss/opentams/internal/httpx/handlers"
	"github.com/amagioss/opentams/internal/httpx/middleware"
)

// newErrorMiddlewareRouter mounts the strict handler behind
// middleware.ErrorHandler with the error funcs of internal/server, so a
// returned *apperror.AppError renders as it does in production.
func newErrorMiddlewareRouter(h *handlers.Handler) *gin.Engine {
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
	api.RegisterHandlersWithOptions(engine, api.NewStrictHandlerWithOptions(h, nil, strictOpts), api.GinServerOptions{})
	return engine
}
