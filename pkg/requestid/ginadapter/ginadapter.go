package ginadapter

import (
	"github.com/gin-gonic/gin"

	"github.com/amagioss/opentams/pkg/requestid"
)

const ginKey = "github.com/amagioss/opentams/pkg/requestid/ginadapter"

// Middleware returns a gin handler that ensures every request
// carries an X-Request-ID. If the inbound request omits the header a
// fresh ID is generated; the chosen ID is echoed back in the response
// header and stored on both the gin context and the standard context
// so downstream handlers can correlate logs.
func Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader(requestid.Header)
		if id == "" {
			id = requestid.Generate()
		}
		c.Header(requestid.Header, id)
		c.Set(ginKey, id)
		c.Request = c.Request.WithContext(requestid.WithContext(c.Request.Context(), id))
		c.Next()
	}
}

// FromContext returns the request ID attached by Middleware to the
// gin context. Returns the empty string when invoked outside the
// middleware chain.
func FromContext(c *gin.Context) string {
	v, _ := c.Get(ginKey)
	s, _ := v.(string)
	return s
}
