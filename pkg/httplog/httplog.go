package httplog

import (
	"context"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	pkglogger "github.com/amagioss/opentams/pkg/logger"
	"github.com/amagioss/opentams/pkg/requestid/ginadapter"
)

type contextKey struct{}

// fieldsKey is the context key for the per-request mutable fieldbag
// merged into the terminal access-log entry by Middleware. Kept
// distinct from contextKey (which carries the per-request *zap.Logger)
// so the two are independently retrievable.
type fieldsKey struct{}

// fieldBag accumulates zap.Fields stamped via AddFields during the
// lifetime of a single request. Guarded because handler code may be
// re-entered from helper goroutines (e.g. URL-projection in future
// concurrent paths); the cost is one cheap mutex per request and
// avoids a data-race surprise later.
type fieldBag struct {
	mu     sync.Mutex
	fields []zap.Field
}

const ginKey = "github.com/amagioss/opentams/pkg/httplog"

var defaultWarnStatuses = map[int]bool{401: true, 403: true, 429: true}

type config struct {
	warnStatuses map[int]bool
}

// Option configures the HTTP request-logging middleware. Apply
// options to Middleware via the variadic opts parameter.
type Option func(*config)

// WarnStatuses overrides the set of HTTP status codes logged at WARN
// level (everything ≥ 500 is always ERROR). The default WARN set is
// 401, 403, 429.
func WarnStatuses(codes ...int) Option {
	return func(c *config) {
		c.warnStatuses = make(map[int]bool, len(codes))
		for _, code := range codes {
			c.warnStatuses[code] = true
		}
	}
}

// Middleware returns a gin handler that logs each request with
// `request_id`, `method`, `path`, `status`, and `latency_ms` fields
// at INFO / WARN / ERROR depending on the response status. The
// per-request logger is attached to the gin context (retrievable via
// Logger) and to the standard context (retrievable via
// LoggerFromContext) so handlers can borrow it without re-deriving
// fields.
func Middleware(logger *zap.Logger, opts ...Option) gin.HandlerFunc {
	cfg := &config{warnStatuses: defaultWarnStatuses}
	for _, o := range opts {
		o(cfg)
	}
	return func(c *gin.Context) {
		start := time.Now()
		child := logger.With(
			zap.String("request_id", ginadapter.FromContext(c)),
			zap.String("method", c.Request.Method),
			zap.String("path", c.Request.URL.Path),
		)
		c.Set(ginKey, child)
		bag := &fieldBag{}
		reqCtx := context.WithValue(c.Request.Context(), contextKey{}, child)
		reqCtx = context.WithValue(reqCtx, fieldsKey{}, bag)
		reqCtx = pkglogger.WithContext(reqCtx, child)
		c.Request = c.Request.WithContext(reqCtx)
		c.Next()
		status := c.Writer.Status()
		latency := time.Since(start).Milliseconds()
		bag.mu.Lock()
		extra := append([]zap.Field(nil), bag.fields...)
		bag.mu.Unlock()
		fields := make([]zap.Field, 0, len(extra)+2)
		fields = append(fields, extra...)
		fields = append(fields,
			zap.Int("status", status),
			zap.Int64("latency_ms", latency),
		)
		switch {
		case status >= 500:
			child.Error("request", fields...)
		case cfg.warnStatuses[status]:
			child.Warn("request", fields...)
		default:
			child.Info("request", fields...)
		}
	}
}

// NewRequestContext attaches a per-request fieldbag plus the supplied
// logger to ctx — the same plumbing Middleware applies on a real
// request — and returns a snapshot func that copies the fields
// accumulated via AddFields. Intended for handler-level unit tests
// that exercise log-stamping behaviour without standing up the full
// gin middleware stack. Production code should rely on Middleware.
func NewRequestContext(ctx context.Context, l *zap.Logger) (reqCtx context.Context, snapshot func() []zap.Field) {
	if l == nil {
		l = zap.NewNop()
	}
	bag := &fieldBag{}
	reqCtx = context.WithValue(ctx, contextKey{}, l)
	reqCtx = context.WithValue(reqCtx, fieldsKey{}, bag)
	reqCtx = pkglogger.WithContext(reqCtx, l)
	snapshot = func() []zap.Field {
		bag.mu.Lock()
		defer bag.mu.Unlock()
		out := make([]zap.Field, len(bag.fields))
		copy(out, bag.fields)
		return out
	}
	return reqCtx, snapshot
}

// AddFields appends zap.Fields to the per-request fieldbag attached by
// Middleware. The fields surface on the terminal access-log entry
// alongside the wire-level fields (status, latency_ms, request_id,
// method, path). Calling outside the middleware chain is a no-op so
// handlers can be exercised in unit tests without the full stack.
func AddFields(ctx context.Context, fields ...zap.Field) {
	if len(fields) == 0 {
		return
	}
	bag, _ := ctx.Value(fieldsKey{}).(*fieldBag)
	if bag == nil {
		return
	}
	bag.mu.Lock()
	bag.fields = append(bag.fields, fields...)
	bag.mu.Unlock()
}

// LoggerFromContext returns the per-request logger attached by
// Middleware to the standard context. Returns a no-op logger if the
// context is not under a request-scoped chain.
func LoggerFromContext(ctx context.Context) *zap.Logger {
	l, _ := ctx.Value(contextKey{}).(*zap.Logger)
	if l == nil {
		return zap.NewNop()
	}
	return l
}

// Logger returns the per-request logger attached by Middleware to
// the gin context. Returns a no-op logger if invoked outside the
// middleware chain.
func Logger(c *gin.Context) *zap.Logger {
	v, exists := c.Get(ginKey)
	if !exists {
		return zap.NewNop()
	}
	l, _ := v.(*zap.Logger)
	if l == nil {
		return zap.NewNop()
	}
	return l
}
