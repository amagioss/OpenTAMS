// Package health provides a poll-on-demand liveness / readiness / detailed-health
// aggregator for OpenTAMS. It is consumed by the HTTP handlers for the three
// spec endpoints /healthz, /readyz, /health/details (M13a) and has no
// dependency on the HTTP stack itself — it only depends on narrow, structural
// probe interfaces satisfied by the metastore, objectstore, and jwtauth
// packages.
//
// Design: see docs/design/internal-httpx-health/.
package health

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
)

// Status is the three-valued overall / per-component health enum exposed in
// the /health/details response. "degraded" is defined here for API stability
// (per BR-HLTH-04) but not produced by the initial implementation — probes
// are boolean today.
type Status string

// Status enum values exposed in the /health/details response. The
// rank ordering used by worstOf treats StatusHealthy < StatusDegraded
// < StatusUnhealthy.
const (
	StatusHealthy   Status = "healthy"
	StatusDegraded  Status = "degraded"
	StatusUnhealthy Status = "unhealthy"
)

// ComponentStatus is the per-component slice of the /health/details report.
type ComponentStatus struct {
	Status    Status
	LatencyMs *int64
}

// DBProbe is the narrow subset of metastore used by the health package.
// It is satisfied by *metastore.PostgresStore and any hand-rolled fake.
type DBProbe interface {
	Ping(ctx context.Context) error
}

// ObjectStoreProbe is the narrow subset of objectstore used by the health
// package. It is satisfied by *objectstore.S3Store and any hand-rolled fake.
type ObjectStoreProbe interface {
	HealthCheck(ctx context.Context) error
}

// JWKSProbe is the narrow subset of jwtauth used by the health package. It is
// satisfied by *jwtauth.Validator (cached JWKS probe). Optional — leave
// Options.JWKS nil when running with the DevProvider.
type JWKSProbe interface {
	Check(ctx context.Context) error
}

// Options configures the Checker. Zero-valued timeouts are replaced with the
// package defaults (ReadyTimeout=500ms, DetailsTimeout=2s).
type Options struct {
	Version        string
	ReadyTimeout   time.Duration
	DetailsTimeout time.Duration

	DB     DBProbe          // required
	Object ObjectStoreProbe // required
	JWKS   JWKSProbe        // optional (BR-HLTH-09)
}

// Details is the full response payload for GET /health/details.
type Details struct {
	Status     Status
	Version    string
	Components map[string]ComponentStatus
}

// Checker is the public aggregator surface. It is safe for concurrent use.
type Checker interface {
	Ready(ctx context.Context) bool
	Details(ctx context.Context) Details
}

// Sentinel errors returned by New on misconfiguration. Callers are expected
// to treat any non-nil error from New as fatal and abort process startup
// (BR-HLTH-13).
var (
	ErrMissingDBProbe          = errors.New("health: DBProbe is required")
	ErrMissingObjectStoreProbe = errors.New("health: ObjectStoreProbe is required")
)

const (
	defaultReadyTimeout   = 500 * time.Millisecond
	defaultDetailsTimeout = 2 * time.Second
)

// New constructs a Checker. It returns ErrMissingDBProbe or
// ErrMissingObjectStoreProbe when a required dependency is nil. JWKS is
// optional and not validated here.
func New(opts Options) (Checker, error) {
	if opts.DB == nil {
		return nil, ErrMissingDBProbe
	}
	if opts.Object == nil {
		return nil, ErrMissingObjectStoreProbe
	}
	if opts.ReadyTimeout <= 0 {
		opts.ReadyTimeout = defaultReadyTimeout
	}
	if opts.DetailsTimeout <= 0 {
		opts.DetailsTimeout = defaultDetailsTimeout
	}
	return &checker{opts: opts}, nil
}

type checker struct {
	opts Options
}

// Ready runs DB + Object probes concurrently under ReadyTimeout. JWKS is
// intentionally NOT consulted (BR-HLTH-02).
func (c *checker) Ready(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, c.opts.ReadyTimeout)
	defer cancel()

	g, gctx := errgroup.WithContext(ctx)
	g.Go(safeProbe(func() error { return c.opts.DB.Ping(gctx) }))
	g.Go(safeProbe(func() error { return c.opts.Object.HealthCheck(gctx) }))
	return g.Wait() == nil
}

// Details runs all configured probes concurrently under DetailsTimeout and
// returns a per-component report plus a worst-of overall status.
//
// Unlike Ready, Details does NOT use errgroup: a failure in one component must
// NOT cancel siblings — we need every probe's real state to assemble the
// report. Per-component panics are converted to StatusUnhealthy via recover.
func (c *checker) Details(ctx context.Context) Details {
	ctx, cancel := context.WithTimeout(ctx, c.opts.DetailsTimeout)
	defer cancel()

	components := make(map[string]ComponentStatus, 3)
	var mu sync.Mutex
	var wg sync.WaitGroup

	run := func(name string, probe func(context.Context) error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cs := runProbe(ctx, probe)
			mu.Lock()
			components[name] = cs
			mu.Unlock()
		}()
	}

	run("metastore", c.opts.DB.Ping)
	run("objectstore", c.opts.Object.HealthCheck)
	if c.opts.JWKS != nil {
		run("jwtauth", c.opts.JWKS.Check)
	}
	wg.Wait()

	return Details{
		Status:     worstOf(components),
		Version:    c.opts.Version,
		Components: components,
	}
}

// safeProbe wraps a boolean probe call in panic recovery so a probe that
// panics is surfaced to errgroup as an error, not a crashed goroutine
// (NFR-HLTH-R1).
func safeProbe(fn func() error) func() error {
	return func() (err error) {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("health: probe panicked: %v", r)
			}
		}()
		return fn()
	}
}

// runProbe executes a per-dependency health check and records latency. Any
// panic is recovered and mapped to StatusUnhealthy (NFR-HLTH-R1).
func runProbe(ctx context.Context, fn func(context.Context) error) ComponentStatus {
	start := time.Now()
	var probeErr error
	func() {
		defer func() {
			if r := recover(); r != nil {
				probeErr = fmt.Errorf("health: probe panicked: %v", r)
			}
		}()
		probeErr = fn(ctx)
	}()
	latency := time.Since(start).Milliseconds()

	status := StatusHealthy
	if probeErr != nil {
		status = StatusUnhealthy
	}
	return ComponentStatus{Status: status, LatencyMs: &latency}
}

// worstOf returns the highest-severity status in the components map, per
// BR-HLTH-04 (healthy=0 < degraded=1 < unhealthy=2). Empty map → healthy.
func worstOf(components map[string]ComponentStatus) Status {
	rank := func(s Status) int {
		switch s {
		case StatusUnhealthy:
			return 2
		case StatusDegraded:
			return 1
		default:
			return 0
		}
	}
	worst := StatusHealthy
	for _, cs := range components {
		if rank(cs.Status) > rank(worst) {
			worst = cs.Status
		}
	}
	return worst
}
