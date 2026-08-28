package middleware

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/amagioss/opentams/internal/apperror"
	"github.com/amagioss/opentams/internal/auth"
	"github.com/amagioss/opentams/pkg/httprecovery"
	"github.com/amagioss/opentams/pkg/requestid/ginadapter"
)

const authSubjectKey = "github.com/amagioss/opentams/internal/httpx/middleware:subject"

// Auth returns a gin middleware that requires a Bearer token on
// every request. The token is verified via the supplied auth.Provider
// and the resulting principal's Subject is stored on the gin context
// (retrievable via AuthSubject). On any failure the middleware emits
// an apperror.ErrUnauthorized for ErrorHandler to format and aborts
// the chain.
func Auth(provider auth.Provider) gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.GetHeader("Authorization")
		token, ok := strings.CutPrefix(header, "Bearer ")
		if !ok || token == "" {
			_ = c.Error(apperror.New(apperror.ErrUnauthorized, "missing or malformed Authorization header"))
			c.Abort()
			return
		}
		principal, err := provider.Authenticate(c.Request.Context(), token)
		if err != nil {
			_ = c.Error(apperror.New(apperror.ErrUnauthorized, "authentication failed"))
			c.Abort()
			return
		}
		c.Set(authSubjectKey, principal.Subject)
		c.Next()
	}
}

// AuthSubject returns the authenticated subject ID stored on the gin
// context by Auth. Returns the empty string when no Auth middleware
// ran or no Bearer token was attached.
func AuthSubject(c *gin.Context) string {
	v, _ := c.Get(authSubjectKey)
	s, _ := v.(string)
	return s
}

// ErrorHandler returns a gin middleware that converts trailing
// errors on the gin context into RFC 9457 problem+json responses.
// AppError values are mapped to their catalogued status and Problem
// Details URI; any other error is mapped to a generic
// 500 Internal Server Error.
func ErrorHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Next()
		if c.Writer.Written() || len(c.Errors) == 0 {
			return
		}
		err := c.Errors.Last().Err
		var ae *apperror.AppError
		if errors.As(err, &ae) {
			pd := ae.ToProblemDetails(c.Request.RequestURI, ginadapter.FromContext(c))
			c.Header("Content-Type", "application/problem+json")
			c.JSON(ae.Status, pd)
			return
		}
		pd := apperror.ProblemDetails{
			Title:     "Internal Server Error",
			Status:    http.StatusInternalServerError,
			Instance:  c.Request.RequestURI,
			RequestID: ginadapter.FromContext(c),
		}
		c.Header("Content-Type", "application/problem+json")
		c.JSON(http.StatusInternalServerError, pd)
	}
}

// PanicWriter returns a httprecovery.ResponseWriter that emits a
// generic 500 problem+json body. It is consumed by the
// `pkg/httprecovery/ginadapter` middleware so panics surface as
// well-formed TAMS errors rather than the gin default plaintext.
func PanicWriter() httprecovery.ResponseWriter {
	return func(w http.ResponseWriter, _ any, _ []byte) {
		pd := apperror.ProblemDetails{
			Title:  "Internal Server Error",
			Status: http.StatusInternalServerError,
		}
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(pd)
	}
}
