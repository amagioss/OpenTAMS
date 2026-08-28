package httplog_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"github.com/amagioss/opentams/pkg/httplog"
	"github.com/amagioss/opentams/pkg/logger"
	"github.com/amagioss/opentams/pkg/requestid/ginadapter"
)

func init() { gin.SetMode(gin.TestMode) }

func newRouter(zl *zap.Logger, opts ...httplog.Option) *gin.Engine {
	r := gin.New()
	r.Use(ginadapter.Middleware())
	r.Use(httplog.Middleware(zl, opts...))
	return r
}

// TC-LOG-01: Logger(c) returns the child logger stored by middleware.
func TestLogger_ReturnsChildLogger(t *testing.T) {
	var captured *zap.Logger
	r := newRouter(zap.NewNop())
	r.GET("/", func(c *gin.Context) {
		captured = httplog.Logger(c)
		c.Status(http.StatusOK)
	})
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", http.NoBody))
	if captured == nil {
		t.Error("Logger(c) must return a non-nil logger")
	}
}

// TC-LOG-02: Logger(c) falls back to root logger when middleware not in chain.
func TestLogger_FallbackToRoot(t *testing.T) {
	root := zap.NewNop()
	r := gin.New()
	r.GET("/", func(c *gin.Context) {
		if httplog.Logger(c) == nil {
			t.Error("Logger(c) must never return nil")
		}
		c.Status(http.StatusOK)
	})
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", http.NoBody))
	_ = root
}

// TC-LOG-03: completion log is written after handler runs.
func TestMiddleware_LogsCompletion(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	zl := zap.New(core)
	r := newRouter(zl)
	r.GET("/", func(c *gin.Context) { c.Status(http.StatusOK) })
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", http.NoBody))
	if logs.Len() == 0 {
		t.Error("expected at least one log entry after request")
	}
}

// TC-LOG-04: 5xx response logs at ERROR level.
func TestMiddleware_LogsErrorFor5xx(t *testing.T) {
	core, logs := observer.New(zap.DebugLevel)
	zl := zap.New(core)
	r := newRouter(zl)
	r.GET("/", func(c *gin.Context) { c.Status(http.StatusInternalServerError) })
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", http.NoBody))
	last := logs.All()[logs.Len()-1]
	if last.Level != zap.ErrorLevel {
		t.Errorf("expected ERROR for 5xx, got %s", last.Level)
	}
}

// TC-LOG-05: default WARN status (401) logs at WARN level.
func TestMiddleware_LogsWarnFor401(t *testing.T) {
	core, logs := observer.New(zap.DebugLevel)
	zl := zap.New(core)
	r := newRouter(zl)
	r.GET("/", func(c *gin.Context) { c.Status(http.StatusUnauthorized) })
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", http.NoBody))
	last := logs.All()[logs.Len()-1]
	if last.Level != zap.WarnLevel {
		t.Errorf("expected WARN for 401, got %s", last.Level)
	}
}

// TC-LOG-06: custom WarnStatuses option overrides defaults.
func TestMiddleware_CustomWarnStatuses(t *testing.T) {
	core, logs := observer.New(zap.DebugLevel)
	zl := zap.New(core)
	r := newRouter(zl, httplog.WarnStatuses(http.StatusTeapot))
	r.GET("/", func(c *gin.Context) { c.Status(http.StatusTeapot) })
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", http.NoBody))
	last := logs.All()[logs.Len()-1]
	if last.Level != zap.WarnLevel {
		t.Errorf("expected WARN for 418, got %s", last.Level)
	}
}

// TC-LOG-07b: Logger(c) returns nop when stored value is wrong type.
func TestLogger_WrongTypeInContext(t *testing.T) {
	r := gin.New()
	r.GET("/", func(c *gin.Context) {
		c.Set("github.com/amagioss/opentams/pkg/httplog", "not-a-logger")
		if httplog.Logger(c) == nil {
			t.Error("Logger(c) must never return nil")
		}
		c.Status(http.StatusOK)
	})
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", http.NoBody))
}

// TC-LOG-08: LoggerFromContext returns the child logger injected into request context by middleware.
func TestLoggerFromContext_ReturnsChildLogger(t *testing.T) {
	var captured *zap.Logger
	r := newRouter(zap.NewNop())
	r.GET("/", func(c *gin.Context) {
		captured = httplog.LoggerFromContext(c.Request.Context())
		c.Status(http.StatusOK)
	})
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", http.NoBody))
	if captured == nil {
		t.Error("LoggerFromContext must return non-nil logger")
	}
}

// TC-LOG-09: LoggerFromContext returns nop when middleware not in chain.
func TestLoggerFromContext_FallbackNop(t *testing.T) {
	r := gin.New()
	r.GET("/", func(c *gin.Context) {
		if httplog.LoggerFromContext(c.Request.Context()) == nil {
			t.Error("LoggerFromContext must never return nil")
		}
		c.Status(http.StatusOK)
	})
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", http.NoBody))
}

// TC-LOG-10: httplog.Middleware stores child logger under pkg/logger's context key.
func TestMiddleware_SetsLoggerPackageContext(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	base := zap.New(core)
	r := newRouter(base)
	r.GET("/", func(c *gin.Context) {
		l := logger.FromContext(c.Request.Context())
		l.Info("test-from-handler")
		c.Status(http.StatusOK)
	})
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", http.NoBody))

	found := false
	for _, entry := range logs.All() {
		if entry.Message == "test-from-handler" {
			found = true
			break
		}
	}
	if !found {
		t.Error("logger.FromContext(ctx) must return a logger writing to the same core as the base logger after httplog.Middleware runs")
	}
}

// TC-LOG-07: 2xx response logs at INFO level.
func TestMiddleware_LogsInfoFor2xx(t *testing.T) {
	core, logs := observer.New(zap.DebugLevel)
	zl := zap.New(core)
	r := newRouter(zl)
	r.GET("/", func(c *gin.Context) { c.Status(http.StatusOK) })
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", http.NoBody))
	last := logs.All()[logs.Len()-1]
	if last.Level != zap.InfoLevel {
		t.Errorf("expected INFO for 200, got %s", last.Level)
	}
}

// TC-LOG-11 — AddFields appends to the per-request fieldbag and the
// values surface on the terminal access-log entry. Underpins
// SCN-HTTP-13/16/23/24/25: the canonical access log gathers domain
// fields stamped by handlers (flow_id, segments_count, segments_failed,
// idempotency_key) and by upstream middleware (auth_subject) into ONE
// terminal entry rather than the handler emitting a second INFO.
func TestAddFields_MergesIntoAccessLog(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	zl := zap.New(core)
	r := newRouter(zl)
	r.GET("/", func(c *gin.Context) {
		httplog.AddFields(c.Request.Context(),
			zap.String("flow_id", "fid-1"),
			zap.Int("segments_count", 3),
			zap.String("idempotency_key", "key-7"),
		)
		c.Status(http.StatusOK)
	})
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", http.NoBody))

	if logs.Len() != 1 {
		t.Fatalf("expected exactly one access-log entry, got %d", logs.Len())
	}
	entry := logs.All()[0]
	got := entry.ContextMap()
	if got["flow_id"] != "fid-1" {
		t.Errorf("expected flow_id=fid-1 on access log, got %v", got["flow_id"])
	}
	if v, ok := got["segments_count"].(int64); !ok || v != 3 {
		t.Errorf("expected segments_count=3 (int64), got %v", got["segments_count"])
	}
	if got["idempotency_key"] != "key-7" {
		t.Errorf("expected idempotency_key=key-7, got %v", got["idempotency_key"])
	}
}

// TC-LOG-12 — AddFields invoked outside the middleware chain is a no-op
// (no panic). Guards handlers that may run in tests without the full
// middleware stack mounted.
func TestAddFields_NoopOutsideMiddleware(t *testing.T) {
	// Plain context, no httplog middleware ran.
	ctx := context.Background()
	httplog.AddFields(ctx, zap.String("k", "v")) // must not panic
}
