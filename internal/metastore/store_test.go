//go:build integration || perf

package metastore

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	tcwait "github.com/testcontainers/testcontainers-go/wait"

	"github.com/amagioss/opentams/internal/apperror"
)

var (
	testPool *pgxpool.Pool
	testDSN  string
)

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
	testDSN = dsn

	if err := runMigrations(dsn); err != nil {
		panic("run migrations: " + err.Error())
	}

	testPool, err = pgxpool.New(ctx, dsn)
	if err != nil {
		panic("create pool: " + err.Error())
	}
	defer testPool.Close()
	defer closeICUPool()

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

// resetGCTables empties the tables that the GC sweeps read in full.
// Other tests of the package leave committed rows (for example objects
// at ref_count = 0), and a sweep test must see only its own fixtures.
func resetGCTables(t *testing.T) {
	t.Helper()
	if _, err := testPool.Exec(context.Background(),
		`TRUNCATE objects, allocations, segments, flows CASCADE`); err != nil {
		t.Fatalf("reset GC tables: %v", err)
	}
}

// seedAllocation writes one allocation row for each ID, as
// POST /flows/{flowId}/storage does: storage_id 'default' and an expiry
// one hour later. A controlled registration needs it (BR-GC-12).
func seedAllocation(t *testing.T, flowID uuid.UUID, ids ...string) {
	t.Helper()
	ctx := context.Background()
	for _, id := range ids {
		if _, err := testPool.Exec(ctx, `
			INSERT INTO allocations (id, flow_id, storage_id, expires_at)
			VALUES ($1, $2, 'default', now() + interval '1 hour')`, id, flowID); err != nil {
			t.Fatalf("seed allocation %q: %v", id, err)
		}
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM allocations WHERE id = ANY($1)`, ids)
	})
}

// ResetGCTables exposes resetGCTables to the external test package.
func ResetGCTables(t *testing.T) { t.Helper(); resetGCTables(t) }

// SeedAllocation exposes seedAllocation to the external test package.
func SeedAllocation(t *testing.T, flowID uuid.UUID, ids ...string) {
	t.Helper()
	seedAllocation(t, flowID, ids...)
}

var (
	icuOnce sync.Once
	icuPool *pgxpool.Pool
	icuErr  error
)

// icuTestPool returns a pool on a second database of the test container
// whose default collation is ICU en-US. In that collation 'a1' sorts
// before 'B1', and in byte order 'B1' sorts before 'a1'. The default
// database of postgres:16-alpine sorts in byte order, so only this
// database can show a lock-order deadlock that ORDER BY id alone causes
// (BR-GC-23).
func icuTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	icuOnce.Do(func() {
		ctx := context.Background()
		if _, err := testPool.Exec(ctx,
			`CREATE DATABASE gc_icu TEMPLATE template0 LOCALE_PROVIDER icu ICU_LOCALE 'en-US'`); err != nil {
			icuErr = err
			return
		}
		base, _, _ := strings.Cut(testDSN, "?")
		dsn := base[:strings.LastIndex(base, "/")] + "/gc_icu?sslmode=disable"
		if err := runMigrations(dsn); err != nil {
			icuErr = err
			return
		}
		icuPool, icuErr = pgxpool.New(ctx, dsn)
	})
	if icuErr != nil {
		t.Fatalf("collation database: %v", icuErr)
	}
	var lessThan bool
	if err := icuPool.QueryRow(context.Background(), `SELECT 'a1' < 'B1'`).Scan(&lessThan); err != nil {
		t.Fatalf("collation probe: %v", err)
	}
	if !lessThan {
		t.Fatal("collation database sorts 'B1' before 'a1'; it cannot show a lock-order deadlock")
	}
	return icuPool
}

// ICUTestStore exposes a store on the collation database to the
// external test package.
func ICUTestStore(t *testing.T) *PostgresStore {
	t.Helper()
	return New(icuTestPool(t))
}

// ICUTestPool exposes the collation database pool for raw fixtures.
func ICUTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	return icuTestPool(t)
}

func closeICUPool() {
	if icuPool != nil {
		icuPool.Close()
	}
}
