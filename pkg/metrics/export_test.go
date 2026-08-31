package metrics

import "github.com/prometheus/client_golang/prometheus"

// WithRawRegistry injects a pre-existing registry into New.
// Only used in tests to exercise error paths.
func WithRawRegistry(reg *prometheus.Registry) Option {
	return func(c *config) { c.promRegistry = reg }
}
