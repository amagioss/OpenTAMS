package ginadapter_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/amagioss/opentams/pkg/requestid"
	"github.com/amagioss/opentams/pkg/requestid/ginadapter"
)

func init() { gin.SetMode(gin.TestMode) }

func newRouter() *gin.Engine {
	r := gin.New()
	r.Use(ginadapter.Middleware())
	r.GET("/", func(c *gin.Context) { c.Status(http.StatusOK) })
	return r
}

// TC-RID-GIN-01: incoming X-Request-ID is forwarded as-is.
func TestMiddleware_ForwardsIncomingID(t *testing.T) {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	req.Header.Set(requestid.Header, "existing-id")
	newRouter().ServeHTTP(w, req)
	if got := w.Header().Get(requestid.Header); got != "existing-id" {
		t.Errorf("expected existing-id, got %q", got)
	}
}

// TC-RID-GIN-02: absent X-Request-ID generates a new one.
func TestMiddleware_GeneratesIDWhenAbsent(t *testing.T) {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	newRouter().ServeHTTP(w, req)
	if got := w.Header().Get(requestid.Header); got == "" {
		t.Error("expected a generated request ID on response header")
	}
}

// TC-RID-GIN-03: FromContext returns the ID stored by the middleware.
func TestMiddleware_FromContext(t *testing.T) {
	var captured string
	r := gin.New()
	r.Use(ginadapter.Middleware())
	r.GET("/", func(c *gin.Context) {
		captured = ginadapter.FromContext(c)
		c.Status(http.StatusOK)
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	req.Header.Set(requestid.Header, "ctx-id")
	r.ServeHTTP(w, req)
	if captured != "ctx-id" {
		t.Errorf("expected ctx-id, got %q", captured)
	}
}

// TC-RID-GIN-04: ID is also stored in standard context.Context.
func TestMiddleware_StoresInStdContext(t *testing.T) {
	var captured string
	r := gin.New()
	r.Use(ginadapter.Middleware())
	r.GET("/", func(c *gin.Context) {
		captured = requestid.FromContext(c.Request.Context())
		c.Status(http.StatusOK)
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	req.Header.Set(requestid.Header, "std-ctx-id")
	r.ServeHTTP(w, req)
	if captured != "std-ctx-id" {
		t.Errorf("expected std-ctx-id, got %q", captured)
	}
}

// TC-RID-GIN-05: FromContext returns empty string when middleware not in chain.
func TestFromContext_EmptyWithoutMiddleware(t *testing.T) {
	r := gin.New()
	r.GET("/", func(c *gin.Context) {
		if got := ginadapter.FromContext(c); got != "" {
			t.Errorf("expected empty, got %q", got)
		}
		c.Status(http.StatusOK)
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", http.NoBody))
}
