// Package dbtest provides a shared Postgres testcontainer helper for
// integration / concurrency / performance tests that need a real
// metastore. The metastore package has its own TestMain (which exports
// `metastore.SharedTestPool()` to its `metastore_test` external package),
// but other packages — most notably `internal/service/segment` — cannot
// reach those `*_test.go` symbols. Setup() spins up a fresh container,
// applies the migration tree, and returns a connected pool + teardown.
//
// Callers wire it from their own TestMain:
//
//	func TestMain(m *testing.M) {
//	    pool, cleanup, err := dbtest.Setup(context.Background(), dbtest.DefaultMigrationsPath())
//	    if err != nil { panic(err) }
//	    sharedPool = pool
//	    code := m.Run()
//	    cleanup()
//	    os.Exit(code)
//	}
//
// Each test binary gets its own container — this matches the metastore
// pattern and keeps test packages independent.
package dbtest

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5" // pgx5 driver for migrate
	_ "github.com/golang-migrate/migrate/v4/source/file"     // file:// source for migrate
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	tcwait "github.com/testcontainers/testcontainers-go/wait"
)

// Setup boots a postgres:16-alpine container, applies the migrations at
// migrationsDir, and returns a *pgxpool.Pool. cleanup() closes the pool
// and terminates the container; callers should defer it.
func Setup(ctx context.Context, migrationsDir string) (*pgxpool.Pool, func(), error) {
	ctr, err := postgres.Run(ctx,
		"postgres:16-alpine",
		postgres.WithDatabase("opentams_test"),
		postgres.WithUsername("test"),
		postgres.WithPassword("test"),
		testcontainers.WithWaitStrategy(
			tcwait.ForLog("database system is ready to accept connections").WithOccurrence(2),
			tcwait.ForListeningPort("5432/tcp"),
		),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("dbtest: start postgres: %w", err)
	}
	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		_ = ctr.Terminate(ctx)
		return nil, nil, fmt.Errorf("dbtest: connection string: %w", err)
	}
	if err := runMigrations(dsn, migrationsDir); err != nil {
		_ = ctr.Terminate(ctx)
		return nil, nil, fmt.Errorf("dbtest: migrations: %w", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		_ = ctr.Terminate(ctx)
		return nil, nil, fmt.Errorf("dbtest: pool: %w", err)
	}
	cleanup := func() {
		pool.Close()
		_ = ctr.Terminate(context.Background())
	}
	return pool, cleanup, nil
}

func runMigrations(dsn, migrationsDir string) error {
	src := "file://" + migrationsDir
	// testcontainer versions return either "postgres://..." or
	// "postgresql://..."; strip whichever prefix matches before stamping
	// the pgx5 scheme. Slicing by a hardcoded prefix length would corrupt
	// the DSN if the prefix differs.
	stripped, ok := strings.CutPrefix(dsn, "postgres://")
	if !ok {
		stripped, ok = strings.CutPrefix(dsn, "postgresql://")
	}
	if !ok {
		return fmt.Errorf("dbtest: unrecognised DSN scheme: %q", dsn)
	}
	mig, err := migrate.New(src, "pgx5://"+stripped)
	if err != nil {
		return err
	}
	if err := mig.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return err
	}
	return nil
}

// DefaultMigrationsPath returns the absolute path to <repo>/migrations,
// computed relative to this source file. Robust to a caller's CWD —
// `go test` typically runs in the test package's directory.
func DefaultMigrationsPath() string {
	_, thisFile, _, _ := runtime.Caller(0)
	// thisFile = <repo>/internal/dbtest/dbtest.go → ../../../migrations
	return filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", "..", "migrations"))
}
