// Package server wires the generated OpenAPI handlers (M12), the health
// Checker (M13a), and the Prometheus metrics handler (M13b) onto a gin
// router behind the M11 middleware chain, and hosts the resulting
// *http.Server lifecycle (Start / Shutdown / Handler / Addr).
//
// Design: see docs/design/internal-server/.
package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/gin-gonic/gin"
	ginmiddleware "github.com/oapi-codegen/gin-middleware"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"

	"github.com/amagioss/opentams/gen/api"
	"github.com/amagioss/opentams/internal/auth"
	"github.com/amagioss/opentams/internal/config"
	"github.com/amagioss/opentams/internal/httpx/health"
	metricsep "github.com/amagioss/opentams/internal/httpx/metrics"
	"github.com/amagioss/opentams/internal/httpx/middleware"
	"github.com/amagioss/opentams/pkg/httplog"
	"github.com/amagioss/opentams/pkg/httpmetrics"
	httprecoveryginadapter "github.com/amagioss/opentams/pkg/httprecovery/ginadapter"
	requestidginadapter "github.com/amagioss/opentams/pkg/requestid/ginadapter"
)

// HTTP timeouts per BR-SRV-10. Not configurable in MVP (NFR-SRV-C2).
const (
	defaultReadHeaderTimeout = 10 * time.Second
	defaultReadTimeout       = 30 * time.Second
	defaultWriteTimeout      = 60 * time.Second
	defaultIdleTimeout       = 120 * time.Second
)

// httpMetricsBuckets is the histogram bucket profile passed to
// pkg/httpmetrics for the http_request_duration_seconds histogram.
//
// Why we override prometheus.DefBuckets:
//   - DefBuckets caps at 10s, but our http.Server.WriteTimeout is 60s
//     (defaultWriteTimeout). Anything between 10s and 60s collapses
//     into the +Inf bucket, destroying p99/p99.9 visibility for the
//     tail we most care about (slow segment registrations, idempotency
//     conflicts blocking on the in_flight row, S3 presign latency
//     spikes, JWKS-fetch-induced auth latency).
//   - DefBuckets starts at 5ms, but the idempotency cached-replay
//     path and segment GETs commonly finish in 1–2ms. Without a sub-
//     5ms boundary every fast path collapses into the first bucket
//     and percentile estimates in that range are useless.
//
// 13 boundaries, 1ms..30s, log-ish spacing. The 30s ceiling sits below
// the 60s WriteTimeout intentionally so anything beyond 30s is visibly
// "approaching the timeout" in dashboards, not "off the chart".
var httpMetricsBuckets = []float64{
	0.001, 0.005, 0.010, 0.025, 0.050, 0.100, 0.250, 0.500,
	1.0, 2.5, 5.0, 10.0, 30.0,
}

// DefaultWriteTimeout is exported so the runtime startup logic in cmd/opentams
// can clamp IDEMPOTENCY_STALE_THRESHOLD to be >= the server's WriteTimeout.
// Reaping faster than this would race against in-flight handler work and
// silently drop legitimate segment registrations before Complete/Release runs.
const DefaultWriteTimeout = defaultWriteTimeout

// ServeTLSFn is the package-private seam covering BR-SRV-09 / NFR-SRV-T3.
// The default wraps http.Server.ServeTLS; tests swap it to exercise the TLS
// branch deterministically without generating a self-signed certificate.
//
// Exported so tests in package server_test can replace it via a t.Cleanup
// restore. Production code MUST NOT reassign it.
var ServeTLSFn = func(srv *http.Server, lis net.Listener, cert, key string) error {
	return srv.ServeTLS(lis, cert, key)
}

// Deps is the fully-constructed dependency bundle passed to New. Every
// pointer/interface field is REQUIRED.
type Deps struct {
	Config                *config.Config
	Logger                *zap.Logger
	Handlers              api.StrictServerInterface
	Checker               health.Checker
	AuthProvider          auth.Provider
	Gatherer              prometheus.Gatherer
	HTTPMetricsRegisterer prometheus.Registerer
}

// Server is the HTTP application. One Server per process.
type Server struct {
	cfg     *config.Config
	log     *zap.Logger
	engine  *gin.Engine
	srv     *http.Server
	tlsCert string
	tlsKey  string

	addr atomic.Value // string — configured port until listener binds, then real addr
}

// New validates Deps, builds the Gin router with the M11 middleware chain,
// wires the generated strict server onto the authenticated group, mounts
// public endpoints, and constructs the underlying *http.Server with package
// timeouts. Returns a sentinel error on misconfiguration (BR-SRV-11).
func New(d Deps) (*Server, error) {
	if err := validateDeps(d); err != nil {
		return nil, err
	}

	applyGinMode(d.Config.AppEnv)

	// d.HTTPMetricsRegisterer must be the un-prefixed registerer (the
	// pkg/metrics.Registry exposes RawRegisterer() for exactly this); the
	// HTTP histograms ride the canonical, portable Prometheus names
	// (`http_request_duration_seconds`, `http_requests_total`) — same
	// treatment as go_* / process_*. See pkg-metrics BR-MET-08.
	//
	// /healthz, /readyz, and /metrics are skipped so liveness / scrape
	// traffic does not dominate the histograms.
	//
	// httpMetricsBuckets overrides prometheus.DefBuckets — see the
	// rationale on the package-level var above (1ms..30s span, sized to
	// our 60s WriteTimeout and the sub-5ms cached-replay floor).
	httpMetricsMW, err := httpmetrics.New(
		d.HTTPMetricsRegisterer,
		httpmetrics.WithSkipPaths("/healthz", "/readyz", "/metrics"),
		httpmetrics.WithBuckets(httpMetricsBuckets),
	)
	if err != nil {
		return nil, fmt.Errorf("server: build http metrics: %w", err)
	}

	engine := gin.New()
	engine.Use(
		requestidginadapter.Middleware(),
		httprecoveryginadapter.Middleware(d.Logger, middleware.PanicWriter()),
		httplog.Middleware(d.Logger),
		httpMetricsMW,
		middleware.ErrorHandler(),
	)

	// Public, unauthenticated endpoints (BR-SRV-03, BR-SRV-06).
	public := engine.Group("")
	public.GET("/healthz", health.HealthzHandler())
	public.GET("/readyz", health.ReadyzHandler(d.Checker))
	public.GET("/metrics", gin.WrapH(metricsep.Handler(d.Gatherer)))

	// Spec-driven request validation (BR-SRV-14). Loaded once at boot from
	// the embedded OpenAPI document; all request shape rules (required,
	// pattern, enum, min/max, nested object validation) are enforced
	// uniformly here rather than hand-rolled per handler. Servers slice is
	// nil'd because we sit behind reverse proxies and do not want
	// kin-openapi's host-matching logic to reject otherwise-valid requests
	// based on the Host header.
	swagger, err := api.GetSwagger()
	if err != nil {
		return nil, fmt.Errorf("server: load OpenAPI spec: %w", err)
	}
	swagger.Servers = nil
	validatorMW := ginmiddleware.OapiRequestValidatorWithOptions(swagger, &ginmiddleware.Options{
		ErrorHandler: validatorErrorHandler,
		Options: openapi3filter.Options{
			// AuthN is enforced by middleware.Auth above this point in the
			// chain. If we let kin-openapi run its own authentication pass
			// it would reject every request because we have no
			// AuthenticationFunc registered against the bearer scheme; the
			// noop tells it "trust upstream".
			AuthenticationFunc: openapi3filter.NoopAuthenticationFunc,
		},
		// Servers slice is nil'd above; silence the resulting noisy warning
		// at boot — the absence is intentional.
		SilenceServersWarning: true,
	})

	// Authenticated TAMS API surface (incl. /health/details). Safe because
	// GetHealthz / GetReadyz / GetMetrics are excluded from oapi-codegen, so
	// RegisterHandlersWithOptions will not collide with the public mounts.
	// validatorMW runs AFTER auth (auth-first preserves current ordering;
	// unauthenticated callers get 401 not 400) and BEFORE the strict
	// handlers — the strict handler stack assumes a spec-valid request.
	authed := engine.Group("")
	authed.Use(middleware.Auth(d.AuthProvider))
	authed.Use(validatorMW)
	api.RegisterHandlersWithOptions(authed, api.NewStrictHandler(d.Handlers, nil), api.GinServerOptions{
		ErrorHandler: paramParseErrorHandler,
	})

	httpSrv := &http.Server{
		Handler:           engine,
		ReadHeaderTimeout: defaultReadHeaderTimeout,
		ReadTimeout:       defaultReadTimeout,
		WriteTimeout:      defaultWriteTimeout,
		IdleTimeout:       defaultIdleTimeout,
	}

	s := &Server{
		cfg:     d.Config,
		log:     d.Logger,
		engine:  engine,
		srv:     httpSrv,
		tlsCert: d.Config.ServerTLSCertFile,
		tlsKey:  d.Config.ServerTLSKeyFile,
	}
	s.addr.Store(fmt.Sprintf(":%d", d.Config.ServerPort))
	return s, nil
}

// Start binds the configured port and serves HTTP(S) until either (a) ctx
// is cancelled — which closes the listener as a safety net — or (b) a fatal
// error occurs. A clean shutdown (http.ErrServerClosed) is returned as nil
// per BR-SRV-12.
func (s *Server) Start(ctx context.Context) error {
	addr := fmt.Sprintf(":%d", s.cfg.ServerPort)
	lc := net.ListenConfig{}
	lis, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("server: listen: %w", err)
	}
	s.addr.Store(lis.Addr().String())

	tlsEnabled := s.tlsCert != "" && s.tlsKey != ""
	s.log.Info("server starting",
		zap.String("addr", lis.Addr().String()),
		zap.Bool("tls_enabled", tlsEnabled),
	)

	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			// Safety net per BR-SRV-08: callers should Shutdown explicitly
			// for graceful drain; this path is abrupt.
			_ = s.srv.Close()
		case <-done:
		}
	}()

	var serveErr error
	if tlsEnabled {
		serveErr = ServeTLSFn(s.srv, lis, s.tlsCert, s.tlsKey)
	} else {
		serveErr = s.srv.Serve(lis)
	}
	s.log.Info("server stopped")

	if errors.Is(serveErr, http.ErrServerClosed) {
		return nil
	}
	return serveErr
}

// Shutdown delegates to http.Server.Shutdown. The caller's ctx carries the
// graceful-drain deadline (typically cfg.ServerGracefulShutdownPeriod).
func (s *Server) Shutdown(ctx context.Context) error {
	return s.srv.Shutdown(ctx)
}

// Handler returns the underlying *gin.Engine typed as http.Handler for
// use with httptest.NewServer in integration tests (BR-SRV-07).
func (s *Server) Handler() http.Handler {
	return s.engine
}

// Addr reports the bound listener address once Start has called net.Listen,
// or the configured "host:port" form before that. Useful when ServerPort=0
// lets the OS pick a free port (test mode).
func (s *Server) Addr() string {
	v, _ := s.addr.Load().(string)
	return v
}

func validateDeps(d Deps) error {
	if d.Config == nil {
		return ErrMissingConfig
	}
	if d.Logger == nil {
		return ErrMissingLogger
	}
	if d.Handlers == nil {
		return ErrMissingHandlers
	}
	if d.Checker == nil {
		return ErrMissingChecker
	}
	if d.AuthProvider == nil {
		return ErrMissingAuth
	}
	if d.Gatherer == nil {
		return ErrMissingGatherer
	}
	if d.HTTPMetricsRegisterer == nil {
		return ErrMissingRegisterer
	}
	// Exactly-one-of cert/key is a misconfiguration per BR-SRV-09 / NFR-SRV-R4.
	if (d.Config.ServerTLSCertFile == "") != (d.Config.ServerTLSKeyFile == "") {
		return ErrInvalidTLSConfig
	}
	return nil
}

func applyGinMode(appEnv string) {
	switch appEnv {
	case "production":
		gin.SetMode(gin.ReleaseMode)
	case "test":
		gin.SetMode(gin.TestMode)
	}
	// else: leave whatever mode the caller set (tests set TestMode in init()).
}
