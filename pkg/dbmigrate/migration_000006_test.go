//go:build integration

package dbmigrate_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"

	"github.com/amagioss/opentams/internal/metastore"
	openmigrations "github.com/amagioss/opentams/migrations"
	"github.com/amagioss/opentams/pkg/dbmigrate"
)

// TC-GC-170: after migration 6, the claim flag is named deleting, the
// partial GC indexes use it, and the allocations table and its indexes
// exist (AC-GC-17).
func TestMigration000006_SchemaAtHead(t *testing.T) {
	pool := adminPool(t, migratedDB(t))
	ctx := context.Background()

	if !columnExists(t, pool, "objects", "deleting") {
		t.Error("objects.deleting is missing")
	}
	if columnExists(t, pool, "objects", "reaping") {
		t.Error("objects.reaping still exists")
	}
	if !columnExists(t, pool, "objects", "claimed_at") {
		t.Error("objects.claimed_at is missing")
	}

	for idx, want := range map[string]string{
		"objects_gc_claim_idx": "deleting = false",
		"objects_gc_retry_idx": "deleting = true",
	} {
		var def string
		if err := pool.QueryRow(ctx, `SELECT indexdef FROM pg_indexes WHERE indexname = $1`, idx).Scan(&def); err != nil {
			t.Fatalf("read %s: %v", idx, err)
		}
		if !strings.Contains(def, want) {
			t.Errorf("%s = %q, want a predicate with %q", idx, def, want)
		}
	}

	for _, col := range []string{"id", "flow_id", "storage_id", "expires_at", "created_at", "deleting", "claimed_at"} {
		if !columnExists(t, pool, "allocations", col) {
			t.Errorf("allocations.%s is missing", col)
		}
	}
	for _, idx := range []string{"allocations_gc_claim_idx", "allocations_gc_retry_idx", "segments_object_id_idx"} {
		if !indexExists(t, pool, idx) {
			t.Errorf("index %s is missing", idx)
		}
	}
}

// TC-GC-171: migrate down 1 returns the schema of version 5.
func TestMigration000006_DownRestoresVersion5(t *testing.T) {
	dsn := migratedDB(t)
	m := newMigrator(t, dsn)
	if err := dbmigrate.Down(m, 1); err != nil {
		t.Fatalf("Down(1): %v", err)
	}
	pool := adminPool(t, dsn)

	if tableExists(t, pool, "allocations") {
		t.Error("allocations still exists after down")
	}
	if !columnExists(t, pool, "objects", "reaping") {
		t.Error("objects.reaping is missing after down")
	}
	if columnExists(t, pool, "objects", "claimed_at") {
		t.Error("objects.claimed_at still exists after down")
	}
	if indexExists(t, pool, "segments_object_id_idx") {
		t.Error("segments_object_id_idx still exists after down")
	}
}

// TC-GC-174: an object that an old GC claimed (reaping = true) is still
// claimed after the rename, and gets a claimed_at, so the claim lease
// applies to it (BR-GC-31, BR-GC-50).
func TestMigration000006_RenameKeepsClaims(t *testing.T) {
	dsn := freshDB(t)
	m := newMigrator(t, dsn)
	if err := dbmigrate.Up(m, 5); err != nil {
		t.Fatalf("Up to 5: %v", err)
	}
	pool := adminPool(t, dsn)
	ctx := context.Background()
	if _, err := pool.Exec(ctx,
		`INSERT INTO objects (id, ref_count, storage_id, reaping) VALUES ('old-claim', 0, 'default', true)`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := dbmigrate.Up(m, 1); err != nil {
		t.Fatalf("Up to 6: %v", err)
	}

	var deleting, hasClaim bool
	if err := pool.QueryRow(ctx,
		`SELECT deleting, claimed_at IS NOT NULL FROM objects WHERE id = 'old-claim'`).Scan(&deleting, &hasClaim); err != nil {
		t.Fatalf("read: %v", err)
	}
	if !deleting {
		t.Error("deleting = false, want true (the claim must survive the rename)")
	}
	if !hasClaim {
		t.Error("claimed_at IS NULL, want a value for each claimed row")
	}
}

// TC-GC-175: the binary needs schema 6. A database at version 5 is too
// old (BR-GC-52).
func TestMigration000006_Version5IsTooOld(t *testing.T) {
	if metastore.ExpectedSchemaVersion != 6 {
		t.Fatalf("ExpectedSchemaVersion = %d, want 6", metastore.ExpectedSchemaVersion)
	}
	dsn := freshDB(t)
	if err := dbmigrate.Up(newMigrator(t, dsn), 5); err != nil {
		t.Fatalf("Up to 5: %v", err)
	}

	_, err := metastore.New(adminPool(t, dsn)).VerifySchema(context.Background())
	if !errors.Is(err, metastore.ErrSchemaTooOld) {
		t.Fatalf("VerifySchema = %v, want ErrSchemaTooOld", err)
	}
}

func newMigrator(t *testing.T, dsn string) *migrate.Migrate {
	t.Helper()
	m, err := dbmigrate.New(openmigrations.FS, dsn, zap.NewNop(), false)
	if err != nil {
		t.Fatalf("dbmigrate.New: %v", err)
	}
	t.Cleanup(func() { dbmigrate.Close(m, zap.NewNop()) })
	return m
}

func adminPool(t *testing.T, dsn string) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), adminDSN(dsn))
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func columnExists(t *testing.T, pool *pgxpool.Pool, table, column string) bool {
	t.Helper()
	return exists(t, pool,
		`SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = $1 AND column_name = $2)`,
		table, column)
}

func tableExists(t *testing.T, pool *pgxpool.Pool, table string) bool {
	t.Helper()
	return exists(t, pool, `SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = $1)`, table)
}

func indexExists(t *testing.T, pool *pgxpool.Pool, index string) bool {
	t.Helper()
	return exists(t, pool, `SELECT EXISTS (SELECT 1 FROM pg_indexes WHERE indexname = $1)`, index)
}

func exists(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) bool {
	t.Helper()
	var ok bool
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&ok); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return ok
}
