package ginadapter_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/amagioss/opentams/pkg/httprecovery"
	"github.com/amagioss/opentams/pkg/httprecovery/ginadapter"
)

func init() { gin.SetMode(gin.TestMode) }

func newRouter(writer httprecovery.ResponseWriter) *gin.Engine {
	r := gin.New()
	r.Use(ginadapter.Middleware(zap.NewNop(), writer))
	return r
}

// TC-REC-GIN-01: no panic — handler runs normally, writer not called.
func TestMiddleware_NoPanic(t *testing.T) {
	var writerCalled bool
	writer := httprecovery.ResponseWriter(func(w http.ResponseWriter, _ any, _ []byte) {
		writerCalled = true
	})
	r := newRouter(writer)
	r.GET("/", func(c *gin.Context) { c.Status(http.StatusOK) })
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", http.NoBody))
	if writerCalled {
		t.Error("writer must not be called when no panic occurs")
	}
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

// TC-REC-GIN-02: panic — writer is called with recovered value and non-empty stack.
func TestMiddleware_PanicCallsWriter(t *testing.T) {
	var gotRecovered any
	var gotStack []byte
	writer := httprecovery.ResponseWriter(func(w http.ResponseWriter, recovered any, stack []byte) {
		gotRecovered = recovered
		gotStack = stack
		w.WriteHeader(http.StatusInternalServerError)
	})
	r := newRouter(writer)
	r.GET("/", func(c *gin.Context) { panic("something went wrong") })
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", http.NoBody))
	if gotRecovered != "something went wrong" {
		t.Errorf("expected recovered value, got %v", gotRecovered)
	}
	if len(gotStack) == 0 {
		t.Error("expected non-empty stack trace")
	}
	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", w.Code)
	}
}

// TC-REC-GIN-03: panic — downstream handlers do not run after recovery.
func TestMiddleware_PanicAbortsChain(t *testing.T) {
	var downstreamRan bool
	writer := httprecovery.ResponseWriter(func(w http.ResponseWriter, _ any, _ []byte) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	r := gin.New()
	r.Use(ginadapter.Middleware(zap.NewNop(), writer))
	r.Use(func(c *gin.Context) {
		c.Next()
		downstreamRan = true
	})
	r.GET("/", func(c *gin.Context) { panic("boom") })
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", http.NoBody))
	if downstreamRan {
		t.Error("downstream middleware must not run after panic recovery")
	}
}
