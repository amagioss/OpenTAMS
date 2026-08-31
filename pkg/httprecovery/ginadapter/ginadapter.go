package ginadapter

import (
	"runtime/debug"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/amagioss/opentams/pkg/httprecovery"
)

// Middleware returns a gin handler that recovers from panics in
// downstream handlers, logs the panic with its stack at ERROR level,
// and delegates the response shape to the supplied
// httprecovery.ResponseWriter (so callers can produce TAMS-flavoured
// problem+json without coupling this package to the response format).
func Middleware(logger *zap.Logger, writer httprecovery.ResponseWriter) gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if r := recover(); r != nil {
				stack := debug.Stack()
				logger.Error("panic recovered", zap.Any("recovered", r), zap.ByteString("stack", stack))
				writer(c.Writer, r, stack)
				c.Abort()
			}
		}()
		c.Next()
	}
}
