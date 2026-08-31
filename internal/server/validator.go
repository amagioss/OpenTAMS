package server

import (
	"errors"
	"net/http"

	"github.com/getkin/kin-openapi/routers"
	"github.com/gin-gonic/gin"
)

// validatorErrorHandler is the ErrorHandler passed to
// ginmiddleware.OapiRequestValidatorWithOptions. It funnels kin-openapi's
// validation errors through paramParseErrorHandler so the response shape
// stays a single RFC 9457 ProblemDetails contract regardless of which
// rejection layer fired (validator middleware, oapi-codegen siw param
// parser, or middleware.ErrorHandler downstream). BR-SRV-05, BR-SRV-14.
//
// Status mapping:
//   - *routers.RouteError       -> 404 (no operation defined for path/method)
//   - everything else           -> the statusCode the middleware passed in
//     (400 by default for spec violations).
//
// In the current wiring the validator attaches as group-level middleware
// on `authed`, so RouteError is structurally unreachable today (every gin
// route that reaches the group is also a spec route). The branch is kept
// as defence-in-depth: if a future change moves the validator to engine
// root, or attaches it via gin's NoRoute, the 404 branch fires correctly
// without revisiting this handler.
//
// Today the kin-openapi error message lands in pd.Detail as an unstructured
// string. Surfacing per-field errors into a typed pd.Errors slice is a
// known follow-up, not yet scheduled.
func validatorErrorHandler(c *gin.Context, message string, statusCode int) {
	if rerr := errFromContext(c); rerr != nil {
		var routeErr *routers.RouteError
		if errors.As(rerr, &routeErr) {
			statusCode = http.StatusNotFound
		}
	}
	paramParseErrorHandler(c, errors.New(message), statusCode)
}

// errFromContext extracts the structured error the validator middleware
// stashes on the gin.Context (the middleware's source of truth — the
// `message` parameter is just `err.Error()`). Returns nil when no such
// error is present so the call site can treat the middleware-supplied
// statusCode as the default.
func errFromContext(c *gin.Context) error {
	last := c.Errors.Last()
	if last == nil {
		return nil
	}
	return last.Err
}
