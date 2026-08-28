package logger

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// contextKey is an unexported type for context keys in this package.
// Prevents collision with any other package using context.WithValue.
type contextKey int

const loggerKey contextKey = iota

// config holds options applied to New.
type config struct {
	sampling   *samplingConfig
	addCaller  bool
	stacktrace *zapcore.Level
}

type samplingConfig struct {
	initial    int
	thereafter int
}

// Option configures the logger returned by New.
type Option func(*config)

// WithSampling enables zap's built-in sampler. After initial messages per second,
// only every thereafter-th message is emitted. Useful for very high-throughput
// services where log volume must be bounded. Off by default — every entry is
// emitted, which is correct for an API server where each request must produce a log line.
func WithSampling(initial, thereafter int) Option {
	return func(c *config) {
		c.sampling = &samplingConfig{initial, thereafter}
	}
}

// WithCaller adds a "caller" field (file:line) to every log entry.
// Useful in development and post-mortem debugging. Off by default because
// runtime.Callers allocates on every log call, adding overhead on the hot path.
func WithCaller() Option {
	return func(c *config) { c.addCaller = true }
}

// WithStacktrace automatically attaches a full goroutine stack trace to log
// entries at atLevel and above. Recommended: zapcore.ErrorLevel.
func WithStacktrace(atLevel zapcore.Level) Option {
	return func(c *config) { c.stacktrace = &atLevel }
}

// New returns a JSON zap.Logger writing to w at the given initial level,
// and the AtomicLevel that controls it at runtime.
//
// The caller holds the AtomicLevel and calls atom.SetLevel on SIGHUP after
// re-reading LOG_LEVEL from the environment — no restart required.
//
// Output fields: "time" (RFC 3339), "level", "msg".
// No TAMS-specific fields are added here; those are injected at the call site
// by internal/httpx/middleware.
func New(w io.Writer, level zapcore.Level, opts ...Option) (*zap.Logger, zap.AtomicLevel) {
	cfg := &config{}
	for _, o := range opts {
		o(cfg)
	}

	atom := zap.NewAtomicLevelAt(level)

	enc := zapcore.EncoderConfig{
		TimeKey:     "time",
		LevelKey:    "level",
		MessageKey:  "msg",
		EncodeTime:  zapcore.RFC3339NanoTimeEncoder,
		EncodeLevel: zapcore.LowercaseLevelEncoder,
	}

	if cfg.addCaller {
		enc.CallerKey = "caller"
		enc.EncodeCaller = zapcore.ShortCallerEncoder
	}

	if cfg.stacktrace != nil {
		enc.StacktraceKey = "stacktrace"
	}

	// No sampler wrapping by default: every log entry must be emitted.
	// zap.NewProductionConfig() enables sampling, which is wrong for an API
	// server where every request log line is required for audit and debugging.
	base := zapcore.NewCore(
		zapcore.NewJSONEncoder(enc),
		zapcore.AddSync(w),
		atom,
	)

	var core zapcore.Core
	if cfg.sampling != nil {
		core = zapcore.NewSamplerWithOptions(base, time.Second,
			cfg.sampling.initial, cfg.sampling.thereafter)
	} else {
		core = base
	}

	var zapOpts []zap.Option
	if cfg.addCaller {
		zapOpts = append(zapOpts, zap.AddCaller())
	}
	if cfg.stacktrace != nil {
		zapOpts = append(zapOpts, zap.AddStacktrace(*cfg.stacktrace))
	}

	return zap.New(core, zapOpts...), atom
}

// WithContext stores l in ctx and returns the updated context.
// The middleware layer calls this once per request after attaching
// request-scoped fields (request_id, flow_id, auth_subject) via l.With(...).
func WithContext(ctx context.Context, l *zap.Logger) context.Context {
	return context.WithValue(ctx, loggerKey, l)
}

// FromContext retrieves the logger stored by WithContext.
// If no logger is in ctx, it tries each fallback in order, returning the first
// non-nil one. If none are available it returns a no-op logger — callers never
// receive nil.
//
// Typical use in a background module (e.g. GC worker):
//
//	l := logger.FromContext(ctx, s.log)  // s.log = base logger from constructor
func FromContext(ctx context.Context, fallback ...*zap.Logger) *zap.Logger {
	if l, ok := ctx.Value(loggerKey).(*zap.Logger); ok {
		return l
	}
	for _, f := range fallback {
		if f != nil {
			return f
		}
	}
	return zap.NewNop()
}

// ParseLevel converts a LOG_LEVEL string to a zapcore.Level.
// Accepted values (case-insensitive): debug, info, warn, error.
// Returns an error for any other input, including empty string.
func ParseLevel(s string) (zapcore.Level, error) {
	switch strings.ToLower(s) {
	case "debug":
		return zapcore.DebugLevel, nil
	case "info":
		return zapcore.InfoLevel, nil
	case "warn":
		return zapcore.WarnLevel, nil
	case "error":
		return zapcore.ErrorLevel, nil
	default:
		return zapcore.InfoLevel, fmt.Errorf("unknown log level %q: must be one of debug, info, warn, error", s)
	}
}
