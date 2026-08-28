//go:build integration || perf

package metastore

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	tcwait "github.com/testcontainers/testcontainers-go/wait"

	"github.com/amagioss/opentams/internal/apperror"
)

var testPool *pgxpool.Pool

// SharedTestPool exposes the package-level test pool to external test
// packages (e.g. `metastore_test`). Defined in a *_test.go file so it
// is NOT compiled into the production binary. Named to avoid the
// `Test*` prefix, which `go test` would otherwise treat as a test.
func SharedTestPool() *pgxpool.Pool { return testPool }

func TestMain(m *testing.M) {
	// Indirect through runTestMain so defers fire before os.Exit (which would
	// otherwise skip them — see gocritic exitAfterDefer).
	os.Exit(runTestMain(m))
}

func runTestMain(m *testing.M) int {
	ctx := context.Background()

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
		panic("start postgres container: " + err.Error())
	}
	defer func() { _ = ctr.Terminate(ctx) }()

	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		panic("get connection string: " + err.Error())
	}

	if err := runMigrations(dsn); err != nil {
		panic("run migrations: " + err.Error())
	}

	testPool, err = pgxpool.New(ctx, dsn)
	if err != nil {
		panic("create pool: " + err.Error())
	}
	defer testPool.Close()

	return m.Run()
}

func runMigrations(dsn string) error {
	mig, err := migrate.New("file://../../migrations", "pgx5://"+dsn[len("postgres://"):])
	if err != nil {
		return err
	}
	if err := mig.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return err
	}
	return nil
}

// withTx starts a transaction from testPool, builds a test store backed by it,
// and registers a rollback on t.Cleanup. All store writes go through the tx
// and are undone after the test. The raw tx is returned for raw SQL fixture setup.
// newTestStore constructs a PostgresStore backed by a pgx.Tx. The caller
// begins a transaction, passes it here, and rolls it back after the test,
// so every store write is undone. It lives here rather than in store.go:
// it is only ever built by tests, and keeping it in the production file
// meant `unused` could not see it was dead outside the test build.
func newTestStore(tx pgx.Tx) *PostgresStore {
	return &PostgresStore{db: tx}
}

func withTx(t *testing.T) (*PostgresStore, pgx.Tx) {
	t.Helper()
	tx, err := testPool.Begin(context.Background())
	if err != nil {
		t.Fatalf("begin test tx: %v", err)
	}
	t.Cleanup(func() {
		_ = tx.Rollback(context.Background())
	})
	return newTestStore(tx), tx
}

// execTx runs raw SQL within a test transaction — used for fixture setup only.
func execTx(t *testing.T, tx pgx.Tx, sql string, args ...any) {
	t.Helper()
	if _, err := tx.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("execTx %q: %v", sql, err)
	}
}

// requireErrCode asserts that err is an *apperror.AppError with the given code.
func requireErrCode(t *testing.T, err error, code apperror.ErrorCode) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error with code %q, got nil", code)
	}
	var ae *apperror.AppError
	if !errors.As(err, &ae) {
		t.Fatalf("expected *apperror.AppError, got %T: %v", err, err)
	}
	if ae.Code != code {
		t.Errorf("error code: got %q, want %q", ae.Code, code)
	}
}
