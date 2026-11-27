package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/spf13/cobra"
	"go.uber.org/zap"

	"github.com/amagioss/opentams/internal/config"
	"github.com/amagioss/opentams/internal/gc"
	"github.com/amagioss/opentams/internal/httpx/health"
	metricsep "github.com/amagioss/opentams/internal/httpx/metrics"
	"github.com/amagioss/opentams/internal/metastore"
)

const (
	adminReadHeaderTimeout = 10 * time.Second
	adminShutdownTimeout   = 5 * time.Second
)

var errGCFailed = errors.New("gc: stopped with an error; see the log")

func newGCCmd() *cobra.Command {
	var once bool
	cmd := &cobra.Command{
		Use:   "gc",
		Short: "Run the object garbage collector",
		Long: `Deletes media objects that no segment uses any more.

Sweep 1 deletes released objects: objects with no segment (ref_count = 0).
Sweep 2 deletes the bytes of expired allocations: object IDs that
POST /flows/{flowId}/storage issued but no segment registered.

Without --once, the two sweeps run as loops until SIGTERM or SIGINT. The
process serves /healthz, /readyz, and /metrics over plain HTTP on
SERVER_PORT. On the signal, /readyz returns 503, no new batch starts, and
the current batch has SERVER_GRACEFUL_SHUTDOWN_PERIOD to finish.

With --once, the command does one cycle of each sweep and stops. It opens no
listener. Exit code 0: no cycle stopped because of a batch-level error.
Exit code 1: a cycle stopped because of a batch-level error, or the start
failed (configuration, database, schema version).

Settings:
  GC_POLL_INTERVAL          time between two cycles of a sweep (default 15m)
  GC_BATCH_SIZE             rows in one batch, 1 to 1000 (default 1000)
  GC_ALLOCATION_GRACE       time after expires_at before sweep 2 deletes (default 5m)
  OPENTAMS_OBJECT_STORE_ID  storage ID of this object store; must equal the
                            value of 'opentams serve' (default "default")

The database (DB_*) and object-store (OBJECT_STORE_*, STORAGE_BACKEND_PROVIDER)
settings are the same as for 'opentams serve'.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			log, _ := newProcessLogger(cfg)
			defer log.Sync() //nolint:errcheck // process is exiting; an unflushed final-log error is not actionable.

			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()
			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
			defer signal.Stop(sigCh)
			stop := make(chan struct{})
			defer forwardShutdown(sigCh, stop, cancel, cfg.ServerGracefulShutdownPeriod)()

			if runGC(ctx, cfg, log, once, stop) != 0 {
				return errGCFailed
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&once, "once", false, "do one cycle of each sweep, then stop")
	return cmd
}

// forwardShutdown closes stop at the first signal and calls cancel after
// grace (BR-GC-67). The returned function stops the forwarding.
func forwardShutdown(sig <-chan os.Signal, stop chan<- struct{}, cancel context.CancelFunc, grace time.Duration) func() {
	done := make(chan struct{})
	var wg sync.WaitGroup
	var timer *time.Timer
	wg.Go(func() {
		select {
		case <-sig:
			close(stop)
			timer = time.AfterFunc(grace, cancel)
		case <-done:
		}
	})
	return func() {
		close(done)
		wg.Wait()
		if timer != nil {
			timer.Stop()
		}
	}
}

// runGC returns the exit code. stop closes on SIGTERM or SIGINT.
func runGC(ctx context.Context, cfg *config.Config, log *zap.Logger, once bool, stop <-chan struct{}) int {
	reg, err := newMetricsRegistry(log)
	if err != nil {
		return 1
	}
	pool, err := openDB(ctx, cfg, log)
	if err != nil {
		return 1
	}
	defer pool.Close()
	obj, err := openObjectStore(cfg, log)
	if err != nil {
		return 1
	}

	ms := metastore.New(pool)
	if _, err := ms.VerifySchema(ctx); err != nil {
		log.Error("database schema not usable", zap.Error(err),
			zap.Bool("retry_safe", false),
			zap.String("check", "run 'opentams migrate up'"))
		return 1
	}

	m, err := gc.NewMetrics(reg.NamespacedRegisterer())
	if err != nil {
		log.Error("failed to register gc metrics", zap.Error(err))
		return 1
	}
	worker, err := gc.New(ms, obj, gc.Config{
		PollInterval:    cfg.GCPollInterval,
		BatchSize:       cfg.GCBatchSize,
		AllocationGrace: cfg.GCAllocationGrace,
		StorageID:       cfg.ObjectStoreID,
	}, m, log)
	if err != nil {
		log.Error("invalid gc configuration", zap.Error(err))
		return 1
	}

	if once {
		return runOnce(ctx, worker, log, stop)
	}

	checker, err := health.New(health.Options{DB: ms, Object: obj})
	if err != nil {
		log.Error("failed to initialize health checker", zap.Error(err))
		return 1
	}
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", fmt.Sprintf(":%d", cfg.ServerPort))
	if err != nil {
		log.Error("failed to open the admin listener", zap.Error(err),
			zap.Bool("retry_safe", true),
			zap.String("check", "SERVER_PORT"))
		return 1
	}
	srv := &http.Server{
		Handler:           newAdminHandler(checker, reg.Gatherer(), stop),
		ReadHeaderTimeout: adminReadHeaderTimeout,
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()
	log.Info("gc started", zap.String("admin_addr", ln.Addr().String()))

	_ = worker.Run(ctx, stop)

	shutCtx, cancel := context.WithTimeout(context.Background(), adminShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		log.Warn("admin listener shutdown", zap.Error(err))
	}
	if err := <-serveErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Warn("admin listener stopped", zap.Error(err))
	}
	log.Info("gc stopped")
	return 0
}

func runOnce(ctx context.Context, worker *gc.Worker, log *zap.Logger, stop <-chan struct{}) int {
	start := time.Now()
	report, _ := worker.RunOnce(ctx, stop)
	log.Info("gc once completed",
		zap.Int("released_deleted", report.Released.Deleted),
		zap.Int("released_row_only", report.Released.RowOnly),
		zap.Int("released_failed", report.Released.Failed),
		zap.Int("released_drift", report.Released.Drift),
		zap.Bool("released_batch_error", report.Released.BatchError),
		zap.Int("allocation_deleted", report.Allocation.Deleted),
		zap.Int("allocation_row_only", report.Allocation.RowOnly),
		zap.Int("allocation_failed", report.Allocation.Failed),
		zap.Int("allocation_drift", report.Allocation.Drift),
		zap.Bool("allocation_batch_error", report.Allocation.BatchError),
		zap.Int64("duration_ms", time.Since(start).Milliseconds()),
	)
	if report.Released.BatchError || report.Allocation.BatchError {
		return 1
	}
	return 0
}

// newAdminHandler serves /healthz, /readyz, and /metrics. /readyz is 503
// once stop is closed (BR-GC-67 step 1).
func newAdminHandler(checker health.Checker, gatherer prometheus.Gatherer, stop <-chan struct{}) http.Handler {
	// Debug mode prints route lines that are not JSON on stdout.
	gin.SetMode(gin.ReleaseMode)
	engine := gin.New()
	engine.GET("/healthz", health.HealthzHandler())
	ready := health.ReadyzHandler(checker)
	engine.GET("/readyz", func(c *gin.Context) {
		select {
		case <-stop:
			c.Status(http.StatusServiceUnavailable)
		default:
			ready(c)
		}
	})
	engine.GET("/metrics", gin.WrapH(metricsep.Handler(gatherer)))
	return engine
}
