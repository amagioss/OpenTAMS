package main

import (
	"context"
	"fmt"
	"math/rand"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/amagioss/opentams/internal/auth"
	"github.com/amagioss/opentams/internal/config"
	"github.com/amagioss/opentams/internal/httpx/handlers"
	"github.com/amagioss/opentams/internal/httpx/health"
	"github.com/amagioss/opentams/internal/idempotency"
	"github.com/amagioss/opentams/internal/metastore"
	"github.com/amagioss/opentams/internal/objectstore"
	"github.com/amagioss/opentams/internal/server"
	"github.com/amagioss/opentams/internal/service"
	"github.com/amagioss/opentams/internal/service/flow"
	"github.com/amagioss/opentams/internal/service/segment"
	"github.com/amagioss/opentams/internal/service/storage"
	"github.com/amagioss/opentams/pkg/jwtauth"
	"github.com/amagioss/opentams/pkg/logger"
	"github.com/amagioss/opentams/pkg/metrics"
)

func newServeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Start the OpenTAMS API server",
		Long: `Starts the OpenTAMS HTTP API server.

Reads all configuration from environment variables, connects to the metadata
store (PostgreSQL) and object store (S3-compatible), then serves the TAMS v8.0
API on SERVER_PORT (default 8080).

Blocks until SIGTERM or SIGINT is received, then drains in-flight requests
within SERVER_GRACEFUL_SHUTDOWN_PERIOD before exiting.

Send SIGHUP to reload LOG_LEVEL from the environment without restarting.

Run 'opentams migrate' before the first start to apply the database schema.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return serve(cmd.Context())
		},
	}
}

func serve(ctx context.Context) error {
	cfg, err := config.Load()
	if err != nil {
		// No application logger yet; use a temporary production logger for REQ-USE-05.
		tmpLog := zap.Must(zap.NewProduction())
		defer tmpLog.Sync() //nolint:errcheck // process is exiting; an unflushed final-log error is not actionable.
		tmpLog.Error("configuration invalid",
			zap.Error(err),
			zap.Bool("retry_safe", false),
			zap.String("check", "environment variables listed in the error"))
		return err
	}

	level, _ := logger.ParseLevel(cfg.LogLevel) // validated by config.Load
	log, atom := logger.New(os.Stdout, level, logger.WithStacktrace(zapcore.ErrorLevel))
	defer log.Sync() //nolint:errcheck // process is exiting; an unflushed final-log error is not actionable.

	log.Info("opentams starting", zap.Any("config", cfg.Redacted()))

	// SIGHUP: reload LOG_LEVEL from environment without restart.
	sighupCh := make(chan os.Signal, 1)
	signal.Notify(sighupCh, syscall.SIGHUP)
	defer signal.Stop(sighupCh)
	hupDone := make(chan struct{})
	defer close(hupDone)
	go func() {
		for {
			select {
			case <-sighupCh:
				newLevel, parseErr := logger.ParseLevel(os.Getenv("LOG_LEVEL"))
				if parseErr != nil {
					log.Warn("SIGHUP: invalid LOG_LEVEL, keeping current level", zap.Error(parseErr))
					continue
				}
				atom.SetLevel(newLevel)
				log.Info("SIGHUP: log level updated", zap.String("level", newLevel.String()))
			case <-hupDone:
				return
			}
		}
	}()

	reg, err := metrics.New(metrics.WithNamespace("opentams"))
	if err != nil {
		log.Error("failed to initialize metrics registry", zap.Error(err),
			zap.Bool("retry_safe", false),
			zap.String("check", "duplicate metric registration"))
		return fmt.Errorf("metrics: %w", err)
	}

	dsn := fmt.Sprintf("host=%s port=%d dbname=%s user=%s password=%s sslmode=%s",
		cfg.DBHost, cfg.DBPort, cfg.DBName, cfg.DBUser, cfg.DBPassword, cfg.DBSSLMode)
	poolCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		log.Error("failed to parse database DSN", zap.Error(err),
			zap.Bool("retry_safe", false),
			zap.String("check", "DB_HOST, DB_PORT, DB_NAME, DB_USER, DB_PASSWORD, DB_SSLMODE"))
		return fmt.Errorf("db: parse config: %w", err)
	}
	// DBPoolMin/Max are validated in config.Validate to be in the
	// range [1, 100_000], far below MaxInt32 — the int32 cast cannot
	// overflow in practice. G115 suppressed with this rationale.
	poolCfg.MinConns = int32(cfg.DBPoolMin) //nolint:gosec // bounded above by config.Validate
	poolCfg.MaxConns = int32(cfg.DBPoolMax) //nolint:gosec // bounded above by config.Validate

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		log.Error("failed to connect to database", zap.Error(err),
			zap.Bool("retry_safe", true),
			zap.String("check", "DB_HOST, DB_PORT, DB_NAME, DB_USER, DB_PASSWORD and network"))
		return fmt.Errorf("db: connect: %w", err)
	}
	defer pool.Close()

	obj, err := objectstore.NewS3Store(cfg)
	if err != nil {
		log.Error("failed to initialize object store", zap.Error(err),
			zap.Bool("retry_safe", false),
			zap.String("check", "OBJECT_STORE_* variables"))
		return fmt.Errorf("objectstore: %w", err)
	}

	ms := metastore.New(pool)
	idmStore := idempotency.NewStore(pool)

	// Clamp IDEMPOTENCY_STALE_THRESHOLD to >= http.Server.WriteTimeout. A
	// running request can hold an idempotency row in_flight for the full
	// WriteTimeout window; reaping faster than that races against the
	// handler's own Complete/Release and silently drops legitimate work.
	staleThreshold := cfg.IdempotencyStaleThreshold
	if staleThreshold < server.DefaultWriteTimeout {
		log.Warn("IDEMPOTENCY_STALE_THRESHOLD clamped to WriteTimeout to avoid reaping in-flight requests",
			zap.Duration("configured", cfg.IdempotencyStaleThreshold),
			zap.Duration("clamped_to", server.DefaultWriteTimeout))
		staleThreshold = server.DefaultWriteTimeout
	}

	svcMetrics, err := service.NewAppServiceMetrics(reg.NamespacedRegisterer())
	if err != nil {
		log.Error("failed to register service metrics", zap.Error(err),
			zap.Bool("retry_safe", false),
			zap.String("check", "duplicate metric names"))
		return fmt.Errorf("service metrics: %w", err)
	}

	flowSvc := flow.New(ms, log, svcMetrics)
	// ControlledStorageID is the deployment-wide controlled-storage
	// backend id (BR-META-07 / D-24). "default" matches Phase 1
	// single-backend convention; override via env for multi-backend or
	// integration setups. Empty disables controlled writes (BYOS-only
	// deployments).
	controlledStorageID := os.Getenv("OPENTAMS_OBJECT_STORE_ID")
	if controlledStorageID == "" {
		controlledStorageID = "default"
	}
	segSvc := segment.New(segment.Deps{
		Meta:                ms,
		Logger:              log,
		Metrics:             svcMetrics,
		ControlledStorageID: controlledStorageID,
	})
	storSvc := storage.New(ms, obj, svcMetrics)

	checker, err := health.New(health.Options{DB: ms, Object: obj})
	if err != nil {
		log.Error("failed to initialize health checker", zap.Error(err),
			zap.Bool("retry_safe", false),
			zap.String("check", "internal configuration"))
		return fmt.Errorf("health: %w", err)
	}

	authProvider, err := buildAuth(cfg)
	if err != nil {
		log.Error("failed to initialize auth provider", zap.Error(err),
			zap.Bool("retry_safe", false),
			zap.String("check", "AUTH_* variables"))
		return fmt.Errorf("auth: %w", err)
	}

	h := handlers.New(log, cfg, ms, flowSvc, segSvc, storSvc, idmStore, checker, obj)

	srv, err := server.New(server.Deps{
		Config:       cfg,
		Logger:       log,
		Handlers:     h,
		Checker:      checker,
		AuthProvider: authProvider,
		Gatherer:     reg.Gatherer(),
		// HTTP middleware metrics use canonical portable names
		// (http_request_duration_seconds / http_requests_total) and must
		// register on the un-prefixed registerer — see pkg-metrics
		// BR-MET-08.
		HTTPMetricsRegisterer: reg.RawRegisterer(),
	})
	if err != nil {
		log.Error("failed to initialize HTTP server", zap.Error(err),
			zap.Bool("retry_safe", false),
			zap.String("check", "SERVER_TLS_* variables if TLS is configured"))
		return fmt.Errorf("server: %w", err)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(sigCh)

	// Idempotency reaper: prunes expired keys (TTL) and reaps orphaned
	// in_flight rows whose owning request crashed before Complete/Release.
	// Stopped before serve returns so the deferred pool.Close cannot race
	// with an in-flight DELETE.
	reapCtx, reapCancel := context.WithCancel(ctx) //nolint:gosec // G118 false positive: reapCancel is invoked inside the deferred closure below; gosec's static analysis does not see through deferred anonymous functions.
	reapDone := make(chan struct{})
	go runIdempotencyReaper(reapCtx, log, idmStore, cfg.IdempotencyReaperInterval, staleThreshold, reapDone)
	defer func() {
		reapCancel()
		<-reapDone
	}()

	log.Info("server ready", zap.String("addr", srv.Addr()))
	return runServer(ctx, srv, cfg.ServerGracefulShutdownPeriod, sigCh)
}

// runIdempotencyReaper runs Prune (TTL-expired) and ReapStale (orphaned
// in_flight) on a ticker with up to ±25% jitter. Jitter prevents thundering-
// herd DELETE storms across HA replicas that boot from the same orchestrator
// at the same instant. Logs a structured warning if either DB call fails;
// continues on the next tick.
func runIdempotencyReaper(
	ctx context.Context,
	log *zap.Logger,
	store *idempotency.PostgresStore,
	period time.Duration,
	staleThreshold time.Duration,
	done chan<- struct{},
) {
	defer close(done)

	// rand source seeded per-process for jitter; not security-sensitive.
	r := rand.New(rand.NewSource(time.Now().UnixNano())) //nolint:gosec // jitter source for the idempotency reaper; weak randomness is fine and crypto/rand would be heavyweight overkill.
	maxJitter := period / 4
	jitter := func() time.Duration {
		if maxJitter <= 0 {
			return 0
		}
		return time.Duration(r.Int63n(int64(maxJitter)))
	}

	timer := time.NewTimer(period + jitter())
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}

		if pruned, err := store.Prune(ctx); err != nil {
			if ctx.Err() == nil {
				log.Warn("idempotency Prune failed", zap.Error(err))
			}
		} else if pruned > 0 {
			log.Info("idempotency keys pruned (TTL-expired)", zap.Int64("count", pruned))
		}

		if reaped, err := store.ReapStale(ctx, staleThreshold); err != nil {
			if ctx.Err() == nil {
				log.Warn("idempotency ReapStale failed", zap.Error(err))
			}
		} else if reaped > 0 {
			log.Warn("idempotency keys reaped (orphaned in_flight)",
				zap.Int64("count", reaped),
				zap.Duration("stale_threshold", staleThreshold))
		}

		timer.Reset(period + jitter())
	}
}

func buildAuth(cfg *config.Config) (auth.Provider, error) {
	if cfg.AppEnv == "development" {
		return &auth.DevProvider{}, nil
	}
	extURL, err := url.Parse(cfg.AuthExternalIssuerURL)
	if err != nil {
		return nil, fmt.Errorf("parse external issuer URL: %w", err)
	}
	issuers := []jwtauth.IssuerConfig{
		{Name: "external", IssuerURL: extURL, Audience: []string{cfg.AuthExternalAudience}},
	}
	// Internal auth is optional (config enforces both-or-neither). Only register
	// the internal issuer when it is configured; otherwise jwtauth.New rejects
	// the empty issuer URL.
	if cfg.AuthInternalIssuerURL != "" {
		intURL, err := url.Parse(cfg.AuthInternalIssuerURL)
		if err != nil {
			return nil, fmt.Errorf("parse internal issuer URL: %w", err)
		}
		issuers = append(issuers, jwtauth.IssuerConfig{
			Name: "internal", IssuerURL: intURL, Audience: []string{cfg.AuthInternalAudience},
		})
	}
	v, err := jwtauth.New(cfg.AuthJWKSTTL, issuers...)
	if err != nil {
		return nil, fmt.Errorf("build JWT validator: %w", err)
	}
	return auth.NewJWTProvider(v, cfg.AuthInternalAllowedSubjects), nil
}
