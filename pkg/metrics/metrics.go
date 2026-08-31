package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

type config struct {
	namespace      string
	withoutProcess bool
	withoutGo      bool
	promRegistry   *prometheus.Registry
}

// Option configures the Registry returned by New.
type Option func(*config)

// WithNamespace sets the prefix applied to all app metrics registered through
// NamespacedRegisterer. Go and process collectors are unaffected, and metrics
// registered through RawRegisterer are unaffected (that is the whole point of
// the split — community-portable names like http_request_duration_seconds keep
// their canonical spelling).
// An empty string is treated as no namespace — even NamespacedRegisterer then
// registers metrics without a prefix.
// When registering through NamespacedRegisterer, callers must not set
// Namespace in prometheus.Opts; only Subsystem and Name should be set,
// otherwise metrics will be double-prefixed.
func WithNamespace(ns string) Option {
	return func(c *config) { c.namespace = ns }
}

// WithoutProcessCollector disables the default process collector (process_*).
func WithoutProcessCollector() Option {
	return func(c *config) { c.withoutProcess = true }
}

// WithoutGoCollector disables the default Go runtime collector (go_*).
func WithoutGoCollector() Option {
	return func(c *config) { c.withoutGo = true }
}

// Registry wraps a Prometheus registry with three sensible defaults:
// a custom (non-global) registry, an optional namespace prefix that applies
// to app metrics registered via NamespacedRegisterer(), and the standard
// process / Go runtime collectors registered un-prefixed (so they keep
// their canonical process_* / go_* names regardless of the configured
// namespace).
type Registry struct {
	promRegistry *prometheus.Registry
	registerer   prometheus.Registerer
}

// New creates a Registry. Process and Go collectors are registered by default;
// use WithoutProcessCollector / WithoutGoCollector to opt out.
// Returns an error if any collector registration fails.
func New(opts ...Option) (*Registry, error) {
	cfg := &config{}
	for _, o := range opts {
		o(cfg)
	}

	promRegistry := cfg.promRegistry
	if promRegistry == nil {
		promRegistry = prometheus.NewRegistry()
	}

	if !cfg.withoutProcess {
		if err := promRegistry.Register(collectors.NewProcessCollector(
			collectors.ProcessCollectorOpts{},
		)); err != nil {
			return nil, err
		}
	}

	if !cfg.withoutGo {
		if err := promRegistry.Register(collectors.NewGoCollector()); err != nil {
			return nil, err
		}
	}

	var registerer prometheus.Registerer = promRegistry
	if cfg.namespace != "" {
		registerer = prometheus.WrapRegistererWithPrefix(cfg.namespace+"_", promRegistry)
	}

	return &Registry{promRegistry: promRegistry, registerer: registerer}, nil
}

// NamespacedRegisterer returns the registerer that applies the configured
// namespace prefix to every registered metric. Callers must set only
// Subsystem and Name in prometheus.Opts — never Namespace, as the prefix is
// applied automatically here. Use this for app-owned metric families that
// should be scoped to the application's namespace.
func (r *Registry) NamespacedRegisterer() prometheus.Registerer { return r.registerer }

// RawRegisterer returns the underlying, un-prefixed registerer. It is
// intended for community-portable metric families that should keep their
// well-known names regardless of the configured namespace — for example
// the canonical http_request_duration_seconds / http_requests_total emitted
// by pkg/httpmetrics — mirroring the convention already applied to the
// standard go_* and process_* collectors.
//
// App-owned metrics that should pick up the namespace prefix MUST use
// NamespacedRegisterer() instead.
func (r *Registry) RawRegisterer() prometheus.Registerer { return r.promRegistry }

// Gatherer returns the underlying gatherer. Pass to promhttp.HandlerFor to
// serve the /metrics endpoint.
func (r *Registry) Gatherer() prometheus.Gatherer { return r.promRegistry }
