// Package httpmetrics provides a Gin middleware that records HTTP request
// duration and count under the canonical Prometheus names
//
//	http_request_duration_seconds (Histogram, labels: method, route, status)
//	http_requests_total           (Counter,   labels: method, route, status)
//
// By default the middleware sets neither Namespace nor Subsystem on its
// metrics; final naming is controlled entirely by the prometheus.Registerer
// the caller passes (a raw *prometheus.Registry, a WrapRegistererWithPrefix
// wrapper, etc.) and by the WithSubsystem / WithConstLabels options.
//
// Passing an already-namespace-prefixed registerer AND setting Namespace or
// Subsystem internally would double-prefix; the middleware deliberately
// leaves Namespace empty so that contract is hard to violate from the
// caller's side.
package httpmetrics

import (
	"errors"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
)

// ErrNilRegisterer is returned by New when the supplied registerer is nil.
// Callers can check it with errors.Is to distinguish wiring bugs from
// genuine registration conflicts.
var ErrNilRegisterer = errors.New("httpmetrics: nil registerer")

// Option configures the middleware returned by New.
type Option func(*config)

type config struct {
	subsystem   string
	buckets     []float64
	constLabels prometheus.Labels
	skipPaths   map[string]struct{}
}

// New builds the Gin metrics middleware and registers its histogram and
// counter on reg. Returns the registration error verbatim if either
// collector cannot be added; returns ErrNilRegisterer when reg is nil;
// never panics.
//
// The middleware itself sets only Name (and, when WithSubsystem is used,
// Subsystem) on its prometheus.Opts. Any namespace prefix is whatever reg
// applies; the canonical, un-prefixed names appear on the wire when reg is
// a raw *prometheus.Registry.
func New(reg prometheus.Registerer, opts ...Option) (gin.HandlerFunc, error) {
	if reg == nil {
		return nil, ErrNilRegisterer
	}
	cfg := &config{}
	for _, o := range opts {
		o(cfg)
	}

	buckets := cfg.buckets
	if len(buckets) == 0 {
		buckets = prometheus.DefBuckets
	}
	duration := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Subsystem:   cfg.subsystem,
		Name:        "http_request_duration_seconds",
		Help:        "HTTP request duration in seconds.",
		Buckets:     buckets,
		ConstLabels: cfg.constLabels,
	}, []string{"method", "route", "status"})

	requests := prometheus.NewCounterVec(prometheus.CounterOpts{
		Subsystem:   cfg.subsystem,
		Name:        "http_requests_total",
		Help:        "Total number of HTTP requests.",
		ConstLabels: cfg.constLabels,
	}, []string{"method", "route", "status"})

	if err := reg.Register(duration); err != nil {
		return nil, err
	}
	if err := reg.Register(requests); err != nil {
		// Roll back the histogram so a caller who fixes the upstream
		// conflict and retries New sees a clean registry. Without this,
		// the retry hits AlreadyRegisteredError on Register(duration) and
		// has no handle to recover the orphaned collector.
		reg.Unregister(duration)
		return nil, err
	}

	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		route := c.FullPath()
		if route == "" {
			route = "unmatched"
		}
		if _, skip := cfg.skipPaths[route]; skip {
			return
		}
		status := strconv.Itoa(c.Writer.Status())
		elapsed := time.Since(start).Seconds()
		duration.WithLabelValues(c.Request.Method, route, status).Observe(elapsed)
		requests.WithLabelValues(c.Request.Method, route, status).Inc()
	}, nil
}

// WithSubsystem sets the prometheus Subsystem segment on both metrics.
// With Subsystem "api", names on the wire become api_http_request_duration_seconds
// and api_http_requests_total. The default is empty (canonical names).
//
// Use this to disambiguate when one process exposes multiple HTTP-shaped
// surfaces on the same registry (e.g. a public API and an admin API).
func WithSubsystem(subsystem string) Option {
	return func(c *config) { c.subsystem = subsystem }
}

// WithBuckets overrides the histogram buckets used for
// http_request_duration_seconds. The default (used when this option is
// omitted, called with nil, or called with an empty slice) is
// prometheus.DefBuckets (.005s … 10s), tuned for in-process services;
// tune for the caller's traffic shape (e.g. coarser buckets for slower
// upstreams, finer buckets for low-latency RPCs).
func WithBuckets(buckets []float64) Option {
	return func(c *config) { c.buckets = buckets }
}

// WithConstLabels stamps every observation with the supplied constant
// labels (applied to both the histogram and the counter). Useful for
// distinguishing instances when several processes scrape into the same
// Prometheus — typical labels are service, region, version, tenant.
//
// The underlying prometheus library copies the map on registration, so the
// caller's map is safe to mutate or drop after New returns.
func WithConstLabels(labels prometheus.Labels) Option {
	return func(c *config) { c.constLabels = labels }
}

// WithSkipPaths suppresses observations for the given matched routes (the
// values returned by gin.Context.FullPath; e.g. "/healthz", "/metrics").
// Skipped requests still flow through the middleware — they just aren't
// counted, so high-frequency liveness / readiness / scrape traffic does
// not dominate the histograms.
func WithSkipPaths(paths ...string) Option {
	return func(c *config) {
		if c.skipPaths == nil {
			c.skipPaths = make(map[string]struct{}, len(paths))
		}
		for _, p := range paths {
			c.skipPaths[p] = struct{}{}
		}
	}
}
