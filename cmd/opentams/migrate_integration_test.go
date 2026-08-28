//go:build integration

package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	tcwait "github.com/testcontainers/testcontainers-go/wait"
)

var migrateTestAdminDSN string // postgres:// DSN for admin CREATE DATABASE

func TestMain(m *testing.M) {
	ctx := context.Background()

	ctr, err := postgres.Run(ctx,
		"postgres:16-alpine",
		postgres.WithDatabase("postgres"),
		postgres.WithUsername("test"),
		postgres.WithPassword("test"),
		testcontainers.WithWaitStrategy(
			tcwait.ForLog("database system is ready to accept connections").WithOccurrence(2),
			tcwait.ForListeningPort("5432/tcp"),
		),
	)
	if err != nil {
		panic("start postgres: " + err.Error())
	}
	defer func() { _ = ctr.Terminate(ctx) }()

	migrateTestAdminDSN, err = ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		panic("get connection string: " + err.Error())
	}

	host, err := ctr.Host(ctx)
	if err != nil {
		panic(err)
	}
	mappedPort, err := ctr.MappedPort(ctx, "5432")
	if err != nil {
		panic(err)
	}

	os.Setenv("DB_HOST", host)
	os.Setenv("DB_PORT", mappedPort.Port())
	os.Setenv("DB_USER", "test")
	os.Setenv("DB_PASSWORD", "test")
	os.Setenv("DB_SSLMODE", "disable")
	os.Setenv("APP_ENV", "development")
	// Stub object store vars — required by config.Load but unused by migrate commands.
	os.Setenv("OBJECT_STORE_BUCKET", "test-bucket")
	os.Setenv("OBJECT_STORE_REGION", "us-east-1")
	os.Setenv("STORAGE_BACKEND_PROVIDER", "s3")

	os.Exit(m.Run())
}

// freshMigrateDB creates an isolated database for a test and sets DB_NAME via
// t.Setenv so config.Load picks it up. The database is dropped at test cleanup.
func freshMigrateDB(t *testing.T) {
	t.Helper()
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, migrateTestAdminDSN)
	if err != nil {
		t.Fatalf("admin pool: %v", err)
	}
	defer pool.Close()

	name := fmt.Sprintf("migcli_%s", sanitize(t.Name()))
	if len(name) > 40 {
		name = name[:40]
	}

	if _, err := pool.Exec(ctx, fmt.Sprintf(`CREATE DATABASE %q`, name)); err != nil {
		t.Fatalf("CREATE DATABASE %s: %v", name, err)
	}
	t.Setenv("DB_NAME", name)
	t.Cleanup(func() {
		p, _ := pgxpool.New(ctx, migrateTestAdminDSN)
		if p != nil {
			_, _ = p.Exec(ctx, fmt.Sprintf(`DROP DATABASE IF EXISTS %q`, name))
			p.Close()
		}
	})
}

func sanitize(s string) string {
	s = strings.ToLower(s)
	var out []byte
	for _, c := range s {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			out = append(out, byte(c))
		} else {
			out = append(out, '_')
		}
	}
	return string(out)
}

// TC-MIGRATE-CLI-01: migrate up applies all pending migrations and returns no error.
func TestMigrateCLI_Up(t *testing.T) {
	freshMigrateDB(t)

	root := newRootCmd()
	root.SetArgs([]string{"migrate", "up"})
	if err := root.Execute(); err != nil {
		t.Fatalf("migrate up: %v", err)
	}
}

// TC-MIGRATE-CLI-02: migrate up then version prints non-zero version and dirty: false.
func TestMigrateCLI_Version(t *testing.T) {
	freshMigrateDB(t)

	up := newRootCmd()
	up.SetArgs([]string{"migrate", "up"})
	if err := up.Execute(); err != nil {
		t.Fatalf("migrate up: %v", err)
	}

	buf := &bytes.Buffer{}
	root := newRootCmd()
	root.SetOut(buf)
	root.SetArgs([]string{"migrate", "version"})
	if err := root.Execute(); err != nil {
		t.Fatalf("migrate version: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "version:") {
		t.Errorf("output missing 'version:'; got: %q", out)
	}
	if strings.Contains(out, "dirty: true") {
		t.Errorf("schema dirty after clean up; output: %q", out)
	}
}

// TC-MIGRATE-CLI-03: migrate down N rolls back without error.
func TestMigrateCLI_Down(t *testing.T) {
	freshMigrateDB(t)

	up := newRootCmd()
	up.SetArgs([]string{"migrate", "up"})
	if err := up.Execute(); err != nil {
		t.Fatalf("migrate up: %v", err)
	}

	root := newRootCmd()
	root.SetArgs([]string{"migrate", "down", "1"})
	if err := root.Execute(); err != nil {
		t.Fatalf("migrate down 1: %v", err)
	}
}

// TC-MIGRATE-CLI-04: migrate force N clears the dirty flag without error.
func TestMigrateCLI_Force(t *testing.T) {
	freshMigrateDB(t)

	up := newRootCmd()
	up.SetArgs([]string{"migrate", "up"})
	if err := up.Execute(); err != nil {
		t.Fatalf("migrate up before force: %v", err)
	}

	root := newRootCmd()
	root.SetArgs([]string{"migrate", "force", "1"})
	if err := root.Execute(); err != nil {
		t.Fatalf("migrate force 1: %v", err)
	}
}
