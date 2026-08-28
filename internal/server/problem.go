package server

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/amagioss/opentams/internal/apperror"
	requestidginadapter "github.com/amagioss/opentams/pkg/requestid/ginadapter"
)

// paramParseErrorHandler satisfies api.GinServerOptions.ErrorHandler. It
// emits an RFC 9457 ProblemDetails body instead of the oapi-codegen default
// ({"msg":"..."}), mirroring middleware.ErrorHandler so clients see a single
// error schema whether the failure came from param parsing or from a handler
// return. BR-SRV-05.
func paramParseErrorHandler(c *gin.Context, err error, status int) {
	title := http.StatusText(status)
	if title == "" {
		title = "Bad Request"
	}
	pd := apperror.ProblemDetails{
		Title:     title,
		Status:    status,
		Detail:    err.Error(),
		Instance:  c.Request.RequestURI,
		RequestID: requestidginadapter.FromContext(c),
	}
	c.Header("Content-Type", "application/problem+json")
	c.JSON(status, pd)
	c.Abort()
}
