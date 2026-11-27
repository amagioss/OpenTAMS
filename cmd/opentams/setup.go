package main

import (
	"context"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/amagioss/opentams/internal/config"
	"github.com/amagioss/opentams/internal/objectstore"
	"github.com/amagioss/opentams/pkg/logger"
	"github.com/amagioss/opentams/pkg/metrics"
)

// loadConfig logs a configuration error with a temporary logger, because
// the process logger needs the configuration (REQ-USE-05).
func loadConfig() (*config.Config, error) {
	cfg, err := config.Load()
	if err != nil {
		tmpLog := zap.Must(zap.NewProduction())
		defer tmpLog.Sync() //nolint:errcheck // process is exiting; an unflushed final-log error is not actionable.
		tmpLog.Error("configuration invalid",
			zap.Error(err),
			zap.Bool("retry_safe", false),
			zap.String("check", "environment variables listed in the error"))
		return nil, err
	}
	return cfg, nil
}

func newProcessLogger(cfg *config.Config) (*zap.Logger, zap.AtomicLevel) {
	level, _ := logger.ParseLevel(cfg.LogLevel) // validated by config.Load
	return logger.New(os.Stdout, level, logger.WithStacktrace(zapcore.ErrorLevel))
}

func newMetricsRegistry(log *zap.Logger) (*metrics.Registry, error) {
	reg, err := metrics.New(metrics.WithNamespace("opentams"))
	if err != nil {
		log.Error("failed to initialize metrics registry", zap.Error(err),
			zap.Bool("retry_safe", false),
			zap.String("check", "duplicate metric registration"))
		return nil, fmt.Errorf("metrics: %w", err)
	}
	return reg, nil
}

// openDB returns an open pool. The caller closes it.
func openDB(ctx context.Context, cfg *config.Config, log *zap.Logger) (*pgxpool.Pool, error) {
	dsn := fmt.Sprintf("host=%s port=%d dbname=%s user=%s password=%s sslmode=%s",
		cfg.DBHost, cfg.DBPort, cfg.DBName, cfg.DBUser, cfg.DBPassword, cfg.DBSSLMode)
	poolCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		log.Error("failed to parse database DSN", zap.Error(err),
			zap.Bool("retry_safe", false),
			zap.String("check", "DB_HOST, DB_PORT, DB_NAME, DB_USER, DB_PASSWORD, DB_SSLMODE"))
		return nil, fmt.Errorf("db: parse config: %w", err)
	}
	poolCfg.MinConns = int32(cfg.DBPoolMin) //nolint:gosec // bounded above by config.Validate
	poolCfg.MaxConns = int32(cfg.DBPoolMax) //nolint:gosec // bounded above by config.Validate

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		log.Error("failed to connect to database", zap.Error(err),
			zap.Bool("retry_safe", true),
			zap.String("check", "DB_HOST, DB_PORT, DB_NAME, DB_USER, DB_PASSWORD and network"))
		return nil, fmt.Errorf("db: connect: %w", err)
	}
	return pool, nil
}

func openObjectStore(cfg *config.Config, log *zap.Logger) (*objectstore.S3Store, error) {
	obj, err := objectstore.NewS3Store(cfg)
	if err != nil {
		log.Error("failed to initialize object store", zap.Error(err),
			zap.Bool("retry_safe", false),
			zap.String("check", "OBJECT_STORE_* variables"))
		return nil, fmt.Errorf("objectstore: %w", err)
	}
	return obj, nil
}
