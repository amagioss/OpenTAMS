//go:build integration

package idempotency

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

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

func TestMain(m *testing.M) {
	// Indirect through runTestMain so defers fire before os.Exit (which would
	// otherwise skip them — see gocritic exitAfterDefer).
	os.Exit(runTestMain(m))
}

func runTestMain(m *testing.M) int {
	ctx := context.Background()

	ctr, err := postgres.Run(ctx,
		"postgres:16-alpine",
		postgres.WithDatabase("opentams_idmp_test"),
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

	mig, err := migrate.New("file://../../migrations", "pgx5://"+dsn[len("postgres://"):])
	if err != nil {
		panic("migrate.New: " + err.Error())
	}
	if err := mig.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		panic("mig.Up: " + err.Error())
	}

	testPool, err = pgxpool.New(ctx, dsn)
	if err != nil {
		panic("create pool: " + err.Error())
	}
	defer testPool.Close()

	return m.Run()
}

func withTx(t *testing.T) (*PostgresStore, pgx.Tx) {
	t.Helper()
	tx, err := testPool.Begin(context.Background())
	if err != nil {
		t.Fatalf("begin test tx: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	return NewStore(tx), tx
}

func execTx(t *testing.T, tx pgx.Tx, sql string, args ...any) {
	t.Helper()
	if _, err := tx.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("execTx %q: %v", sql, err)
	}
}

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

// TC-IDMP-01: Acquire on a new key returns StatusAcquired.
func TestAcquire_NewKey(t *testing.T) {
	store, _ := withTx(t)
	result, err := store.Acquire(context.Background(), "key-01", "hash-a", 24*time.Hour)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != StatusAcquired {
		t.Errorf("status: got %d, want StatusAcquired", result.Status)
	}
	if result.Cached != nil {
		t.Errorf("Cached: want nil for new key")
	}
}

// TC-IDMP-02: Acquire with same key+hash while in-flight returns StatusInFlight.
func TestAcquire_InFlight(t *testing.T) {
	store, _ := withTx(t)
	ctx := context.Background()
	if _, err := store.Acquire(ctx, "key-02", "hash-a", 24*time.Hour); err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	result, err := store.Acquire(ctx, "key-02", "hash-a", 24*time.Hour)
	if err != nil {
		t.Fatalf("second acquire: %v", err)
	}
	if result.Status != StatusInFlight {
		t.Errorf("status: got %d, want StatusInFlight", result.Status)
	}
}

// TC-IDMP-03: After Complete, same key+hash returns StatusCached with original response.
func TestAcquire_Cached(t *testing.T) {
	store, _ := withTx(t)
	ctx := context.Background()
	if _, err := store.Acquire(ctx, "key-03", "hash-a", 24*time.Hour); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if err := store.Complete(ctx, "key-03", 201, json.RawMessage(`{"ok":true}`)); err != nil {
		t.Fatalf("complete: %v", err)
	}
	result, err := store.Acquire(ctx, "key-03", "hash-a", 24*time.Hour)
	if err != nil {
		t.Fatalf("replay acquire: %v", err)
	}
	if result.Status != StatusCached {
		t.Fatalf("status: got %d, want StatusCached", result.Status)
	}
	if result.Cached == nil {
		t.Fatal("Cached: want non-nil")
	}
	if result.Cached.Status != 201 {
		t.Errorf("Cached.Status: got %d, want 201", result.Cached.Status)
	}
	// JSONB normalizes whitespace; compare unmarshalled value.
	var got map[string]any
	if err := json.Unmarshal(result.Cached.Body, &got); err != nil {
		t.Fatalf("Cached.Body not valid JSON: %v", err)
	}
	if v, ok := got["ok"]; !ok || v != true {
		t.Errorf("Cached.Body: got %s", result.Cached.Body)
	}
}

// TC-IDMP-04: Acquire with same key but different body hash returns StatusConflict.
func TestAcquire_Conflict(t *testing.T) {
	store, _ := withTx(t)
	ctx := context.Background()
	if _, err := store.Acquire(ctx, "key-04", "hash-a", 24*time.Hour); err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	result, err := store.Acquire(ctx, "key-04", "hash-b", 24*time.Hour)
	if err != nil {
		t.Fatalf("second acquire: %v", err)
	}
	if result.Status != StatusConflict {
		t.Errorf("status: got %d, want StatusConflict", result.Status)
	}
}

// TC-IDMP-05: Acquire on an expired key returns StatusAcquired (expired treated as new).
func TestAcquire_ExpiredKeyReacquired(t *testing.T) {
	store, tx := withTx(t)
	ctx := context.Background()
	execTx(t, tx, `INSERT INTO idempotency_keys (key, body_hash, expires_at) VALUES ($1, $2, $3)`,
		"key-05", "hash-old", time.Now().Add(-time.Minute))

	result, err := store.Acquire(ctx, "key-05", "hash-new", 24*time.Hour)
	if err != nil {
		t.Fatalf("acquire on expired key: %v", err)
	}
	if result.Status != StatusAcquired {
		t.Errorf("status: got %d, want StatusAcquired", result.Status)
	}
}

// TC-IDMP-06: Prune deletes expired records and returns the count; live records survive.
func TestPrune(t *testing.T) {
	store, tx := withTx(t)
	ctx := context.Background()
	execTx(t, tx, `INSERT INTO idempotency_keys (key, body_hash, expires_at) VALUES ($1, $2, $3)`,
		"key-prune-expired", "hash", time.Now().Add(-time.Minute))
	execTx(t, tx, `INSERT INTO idempotency_keys (key, body_hash, expires_at) VALUES ($1, $2, $3)`,
		"key-prune-live", "hash", time.Now().Add(24*time.Hour))

	n, err := store.Prune(ctx)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if n != 1 {
		t.Errorf("Prune: got %d rows deleted, want 1", n)
	}

	var count int
	_ = tx.QueryRow(ctx, `SELECT COUNT(*) FROM idempotency_keys WHERE key = 'key-prune-live'`).Scan(&count)
	if count != 1 {
		t.Errorf("key-prune-live count: got %d, want 1", count)
	}
}

// TC-IDMP-07: Complete on an unknown/expired key is a no-op (no error).
func TestComplete_UnknownKey(t *testing.T) {
	store, _ := withTx(t)
	err := store.Complete(context.Background(), "key-unknown", 201, json.RawMessage(`{}`))
	if err != nil {
		t.Errorf("Complete on unknown key: want no error, got %v", err)
	}
}

// TC-IDMP-08: Acquire with key longer than 255 chars returns ErrSchemaValidation.
func TestAcquire_KeyTooLong(t *testing.T) {
	store, _ := withTx(t)
	_, err := store.Acquire(context.Background(), strings.Repeat("x", 256), "hash-a", 24*time.Hour)
	requireErrCode(t, err, apperror.ErrSchemaValidation)
}

// TC-IDMP-09: Acquire with empty key returns ErrSchemaValidation.
func TestAcquire_EmptyKey(t *testing.T) {
	store, _ := withTx(t)
	_, err := store.Acquire(context.Background(), "", "hash-a", 24*time.Hour)
	requireErrCode(t, err, apperror.ErrSchemaValidation)
}

// TC-IDMP-10: Complete with key longer than 255 chars returns ErrSchemaValidation.
func TestComplete_KeyTooLong(t *testing.T) {
	store, _ := withTx(t)
	err := store.Complete(context.Background(), strings.Repeat("x", 256), 200, json.RawMessage(`{}`))
	requireErrCode(t, err, apperror.ErrSchemaValidation)
}

// TC-IDMP-11: Release deletes an in-flight row so a fresh Acquire succeeds as new.
//
// Models the transient-failure path of POST /flows/{flowId}/segments: handler
// hit Acquire, RegisterSegments returned a non-deterministic error (e.g. 5xx),
// handler called Release, client retries with the same key.
func TestRelease_InFlightRowDeleted(t *testing.T) {
	store, tx := withTx(t)
	ctx := context.Background()
	if _, err := store.Acquire(ctx, "key-rel-01", "hash-a", 24*time.Hour); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if err := store.Release(ctx, "key-rel-01"); err != nil {
		t.Fatalf("release: %v", err)
	}

	var count int
	_ = tx.QueryRow(ctx, `SELECT COUNT(*) FROM idempotency_keys WHERE key = $1`, "key-rel-01").Scan(&count)
	if count != 0 {
		t.Errorf("row count after Release: got %d, want 0", count)
	}

	// Subsequent Acquire with the same (or different) hash must behave as new.
	result, err := store.Acquire(ctx, "key-rel-01", "hash-different", 24*time.Hour)
	if err != nil {
		t.Fatalf("re-acquire after Release: %v", err)
	}
	if result.Status != StatusAcquired {
		t.Errorf("status after Release: got %d, want StatusAcquired", result.Status)
	}
}

// TC-IDMP-12: Release on a completed (cached) row removes the cache entirely.
//
// Defensive: Release should not be called after Complete in the normal flow,
// but if it is (e.g. by a misordered defer), it must not panic and must leave
// the row gone — preferring "client can retry" over "client gets stale cache".
func TestRelease_CompletedRowDeleted(t *testing.T) {
	store, _ := withTx(t)
	ctx := context.Background()
	if _, err := store.Acquire(ctx, "key-rel-02", "hash-a", 24*time.Hour); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if err := store.Complete(ctx, "key-rel-02", 201, json.RawMessage(`{"ok":true}`)); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if err := store.Release(ctx, "key-rel-02"); err != nil {
		t.Fatalf("release after complete: %v", err)
	}

	result, err := store.Acquire(ctx, "key-rel-02", "hash-a", 24*time.Hour)
	if err != nil {
		t.Fatalf("re-acquire after Release: %v", err)
	}
	if result.Status != StatusAcquired {
		t.Errorf("status: got %d, want StatusAcquired (Release should erase the cache)", result.Status)
	}
}

// TC-IDMP-13: Release on an unknown key is a no-op (no error).
func TestRelease_UnknownKey(t *testing.T) {
	store, _ := withTx(t)
	err := store.Release(context.Background(), "key-rel-unknown")
	if err != nil {
		t.Errorf("Release on unknown key: want no error, got %v", err)
	}
}

// TC-IDMP-14: Release with an invalid key returns ErrSchemaValidation.
//
// Mirrors Acquire/Complete validation so misuse is caught early instead of
// silently issuing a no-op DELETE on a malformed key.
func TestRelease_KeyTooLong(t *testing.T) {
	store, _ := withTx(t)
	err := store.Release(context.Background(), strings.Repeat("x", 256))
	requireErrCode(t, err, apperror.ErrSchemaValidation)
}

// TC-IDMP-15: Release with empty key returns ErrSchemaValidation.
func TestRelease_EmptyKey(t *testing.T) {
	store, _ := withTx(t)
	err := store.Release(context.Background(), "")
	requireErrCode(t, err, apperror.ErrSchemaValidation)
}

// TC-IDMP-16: ReapStale deletes only in_flight rows older than the threshold.
//
// Models the crash-safety backstop: a process died between Acquire and
// Complete/Release, leaving an orphaned in_flight row. ReapStale clears it
// once the row is older than the threshold (which the serve loop clamps
// to >= http.Server.WriteTimeout so we never reap a genuinely-running
// request).
func TestReapStale_DeletesOldInFlightRows(t *testing.T) {
	store, tx := withTx(t)
	ctx := context.Background()

	// Stale in-flight: acquired_at is older than the 30s threshold we'll use.
	execTx(t, tx, `INSERT INTO idempotency_keys (key, body_hash, acquired_at, expires_at)
	               VALUES ($1, $2, $3, $4)`,
		"key-stale-inflight", "hash", time.Now().Add(-2*time.Minute), time.Now().Add(time.Hour))

	// Fresh in-flight: acquired_at is within the threshold; must NOT be reaped.
	execTx(t, tx, `INSERT INTO idempotency_keys (key, body_hash, acquired_at, expires_at)
	               VALUES ($1, $2, $3, $4)`,
		"key-fresh-inflight", "hash", time.Now().Add(-5*time.Second), time.Now().Add(time.Hour))

	// Old completed: in_flight=false, must NOT be reaped (Prune handles this via expires_at).
	execTx(t, tx, `INSERT INTO idempotency_keys (key, body_hash, in_flight, acquired_at, expires_at)
	               VALUES ($1, $2, false, $3, $4)`,
		"key-old-completed", "hash", time.Now().Add(-2*time.Minute), time.Now().Add(time.Hour))

	n, err := store.ReapStale(ctx, 30*time.Second)
	if err != nil {
		t.Fatalf("ReapStale: %v", err)
	}
	if n != 1 {
		t.Errorf("ReapStale: got %d rows deleted, want 1", n)
	}

	survived := func(key string) int {
		var c int
		_ = tx.QueryRow(ctx, `SELECT COUNT(*) FROM idempotency_keys WHERE key = $1`, key).Scan(&c)
		return c
	}
	if survived("key-stale-inflight") != 0 {
		t.Errorf("stale in_flight row should have been reaped")
	}
	if survived("key-fresh-inflight") != 1 {
		t.Errorf("fresh in_flight row must survive reaping")
	}
	if survived("key-old-completed") != 1 {
		t.Errorf("completed row must survive reaping (Prune is responsible for it)")
	}
}

// TC-IDMP-17: ReapStale does not delete a row whose acquired_at sits exactly
// on the threshold boundary (uses <= against now() - threshold; rows must be
// strictly older to be reaped — but boundary equality DOES delete, so we test
// just-under-threshold to ensure no false positives).
func TestReapStale_FreshInFlightUntouched(t *testing.T) {
	store, tx := withTx(t)
	ctx := context.Background()

	execTx(t, tx, `INSERT INTO idempotency_keys (key, body_hash, acquired_at, expires_at)
	               VALUES ($1, $2, $3, $4)`,
		"key-just-fresh", "hash", time.Now().Add(-10*time.Second), time.Now().Add(time.Hour))

	n, err := store.ReapStale(ctx, 60*time.Second)
	if err != nil {
		t.Fatalf("ReapStale: %v", err)
	}
	if n != 0 {
		t.Errorf("ReapStale: got %d rows deleted, want 0", n)
	}

	var c int
	_ = tx.QueryRow(ctx, `SELECT COUNT(*) FROM idempotency_keys WHERE key = $1`, "key-just-fresh").Scan(&c)
	if c != 1 {
		t.Errorf("fresh row count: got %d, want 1", c)
	}
}

// TC-IDMP-18: ReapStale on an empty table is a no-op (returns 0).
func TestReapStale_EmptyTable(t *testing.T) {
	store, _ := withTx(t)
	n, err := store.ReapStale(context.Background(), time.Minute)
	if err != nil {
		t.Fatalf("ReapStale on empty: %v", err)
	}
	if n != 0 {
		t.Errorf("ReapStale on empty: got %d, want 0", n)
	}
}

// TC-IDMP-19: Acquire stamps acquired_at on insert, so a freshly-acquired key
// is shielded from a same-tick ReapStale call with a non-zero threshold.
//
// Guards against a regression where the migration's DEFAULT now() were
// removed without the application-side default also being added.
func TestAcquire_StampsAcquiredAt(t *testing.T) {
	store, _ := withTx(t)
	ctx := context.Background()
	if _, err := store.Acquire(ctx, "key-acq-stamp", "hash", 24*time.Hour); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	n, err := store.ReapStale(ctx, time.Minute)
	if err != nil {
		t.Fatalf("ReapStale: %v", err)
	}
	if n != 0 {
		t.Errorf("ReapStale immediately after Acquire: got %d, want 0", n)
	}
}
