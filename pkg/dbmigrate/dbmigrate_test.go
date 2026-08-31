//go:build integration

package dbmigrate_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	tcwait "github.com/testcontainers/testcontainers-go/wait"
	"go.uber.org/zap"

	"github.com/amagioss/opentams/internal/metastore"
	openmigrations "github.com/amagioss/opentams/migrations"
	"github.com/amagioss/opentams/pkg/dbmigrate"
)

var testConnStr string // postgres:// DSN for admin ops

func TestMain(m *testing.M) {
	// Indirect through runTestMain so defers fire before os.Exit (which would
	// otherwise skip them — see gocritic exitAfterDefer).
	os.Exit(runTestMain(m))
}

func runTestMain(m *testing.M) int {
	ctx := context.Background()

	ctr, err := postgres.Run(ctx,
		"postgres:16-alpine",
		postgres.WithDatabase("opentams_dbmigrate_test"),
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

	testConnStr, err = ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		panic("get connection string: " + err.Error())
	}

	return m.Run()
}

// TC-DBM-UP-01: New + Up on fresh DB applies all migrations.
func TestNew_Up_FreshDB(t *testing.T) {
	dsn := freshDB(t)
	m, err := dbmigrate.New(openmigrations.FS, dsn, zap.NewNop(), false)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer dbmigrate.Close(m, zap.NewNop())

	if err := dbmigrate.Up(m, 0); err != nil {
		t.Fatalf("Up: %v", err)
	}

	ver, dirty, err := dbmigrate.Version(m)
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if dirty {
		t.Error("schema dirty after Up")
	}
	wantVer := uint(metastore.ExpectedSchemaVersion)
	if ver != wantVer {
		t.Errorf("version = %d, want %d", ver, wantVer)
	}
}

// TC-DBM-UP-02: Up when already at head returns no error.
func TestUp_AlreadyAtHead(t *testing.T) {
	dsn := migratedDB(t)
	m, err := dbmigrate.New(openmigrations.FS, dsn, zap.NewNop(), false)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer dbmigrate.Close(m, zap.NewNop())

	if err := dbmigrate.Up(m, 0); err != nil {
		t.Fatalf("Up (already at head) returned error: %v", err)
	}
}

// TC-DBM-DOWN-01: Down(1) rolls back one migration.
func TestDown_OneStep(t *testing.T) {
	dsn := migratedDB(t)
	m, err := dbmigrate.New(openmigrations.FS, dsn, zap.NewNop(), false)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer dbmigrate.Close(m, zap.NewNop())

	if err := dbmigrate.Down(m, 1); err != nil {
		t.Fatalf("Down(1): %v", err)
	}

	ver, dirty, err := dbmigrate.Version(m)
	if err != nil {
		t.Fatalf("Version after down: %v", err)
	}
	if dirty {
		t.Error("dirty after Down")
	}
	// Derived from the head version rather than hard-coded: this
	// assertion read "want 3" and silently went stale when migration
	// 000005 landed. metastore.ExpectedSchemaVersion is kept in step with
	// the migration set by TC-META-SCH-01, which fails if a migration is
	// added without bumping it.
	wantVer := uint(metastore.ExpectedSchemaVersion) - 1
	if ver != wantVer {
		t.Errorf("version = %d, want %d (head - 1)", ver, wantVer)
	}
}

// TC-DBM-FORCE-01: Force clears dirty flag.
func TestForce_ClearsDirty(t *testing.T) {
	dsn := migratedDB(t)

	pool, err := pgxpool.New(context.Background(), adminDSN(dsn))
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()

	head := int(metastore.ExpectedSchemaVersion)
	if _, err := pool.Exec(context.Background(),
		fmt.Sprintf(`UPDATE schema_migrations SET dirty = true WHERE version = %d`, head)); err != nil {
		t.Fatalf("set dirty: %v", err)
	}

	m, err := dbmigrate.New(openmigrations.FS, dsn, zap.NewNop(), false)
	if err != nil {
		t.Fatalf("New after dirty set: %v", err)
	}
	defer dbmigrate.Close(m, zap.NewNop())

	if err := dbmigrate.Force(m, head); err != nil {
		t.Fatalf("Force(%d): %v", head, err)
	}

	_, dirty, err := dbmigrate.Version(m)
	if err != nil {
		t.Fatalf("Version after force: %v", err)
	}
	if dirty {
		t.Error("still dirty after Force")
	}
}

// TC-DBM-PATH-01: NewFromPath applies migrations from filesystem path.
func TestNewFromPath_Up(t *testing.T) {
	dsn := freshDB(t)
	m, err := dbmigrate.NewFromPath("../../migrations", dsn, zap.NewNop(), false)
	if err != nil {
		t.Fatalf("NewFromPath: %v", err)
	}
	defer dbmigrate.Close(m, zap.NewNop())

	if err := dbmigrate.Up(m, 0); err != nil {
		t.Fatalf("Up via path: %v", err)
	}

	ver, dirty, err := dbmigrate.Version(m)
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if dirty {
		t.Error("dirty after path Up")
	}
	wantVer := uint(metastore.ExpectedSchemaVersion)
	if ver != wantVer {
		t.Errorf("version = %d, want %d", ver, wantVer)
	}
}

// --- helpers ---

func pgx5DSN(connStr string) string {
	// testConnStr is postgres://user:pass@host:port/db?...
	// golang-migrate pgx5 driver needs pgx5:// scheme
	return "pgx5://" + connStr[len("postgres://"):]
}

func adminDSN(pgx5dsn string) string {
	// convert pgx5:// back to postgres:// for pgxpool
	return "postgres://" + pgx5dsn[len("pgx5://"):]
}

func freshDB(t *testing.T) string {
	t.Helper()
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, testConnStr)
	if err != nil {
		t.Fatalf("admin pool: %v", err)
	}
	defer pool.Close()

	name := dbName(t)
	if _, err := pool.Exec(ctx, fmt.Sprintf(`CREATE DATABASE %q`, name)); err != nil {
		t.Fatalf("CREATE DATABASE: %v", err)
	}
	t.Cleanup(func() {
		p, _ := pgxpool.New(ctx, testConnStr)
		if p != nil {
			_, _ = p.Exec(ctx, fmt.Sprintf(`DROP DATABASE IF EXISTS %q`, name))
			p.Close()
		}
	})

	base := testConnStr[:strings.LastIndex(testConnStr, "/")]
	return pgx5DSN(base + "/" + name + "?sslmode=disable")
}

func migratedDB(t *testing.T) string {
	t.Helper()
	dsn := freshDB(t)
	m, err := dbmigrate.New(openmigrations.FS, dsn, zap.NewNop(), false)
	if err != nil {
		t.Fatalf("migratedDB New: %v", err)
	}
	defer dbmigrate.Close(m, zap.NewNop())
	if err := dbmigrate.Up(m, 0); err != nil {
		t.Fatalf("migratedDB Up: %v", err)
	}
	return dsn
}

func dbName(t *testing.T) string {
	name := strings.ToLower(t.Name())
	var out []byte
	for _, c := range name {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			out = append(out, byte(c))
		} else {
			out = append(out, '_')
		}
	}
	if len(out) > 40 {
		out = out[:40]
	}
	return string(out)
}
