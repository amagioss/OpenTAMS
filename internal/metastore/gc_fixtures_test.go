//go:build integration || perf

package metastore

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Fixtures of the GC tests (test plan section 0): flows F and G, and
// plain-SQL rows on a given pool (testPool or the collation database).

var (
	gcFlowF = uuid.MustParse("11111111-1111-1111-1111-111111111111")
	gcFlowG = uuid.MustParse("22222222-2222-2222-2222-222222222222")
)

func gcExec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

func gcQueryInt(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int64 {
	t.Helper()
	var v int64
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		t.Fatalf("query %q: %v", sql, err)
	}
	return v
}

func gcSeedFlow(t *testing.T, pool *pgxpool.Pool, flowID uuid.UUID, readOnly bool) {
	t.Helper()
	srcID := uuid.New()
	gcExec(t, pool, `INSERT INTO sources (id, format) VALUES ($1, 'urn:x-nmos:format:video')`, srcID)
	gcExec(t, pool, `INSERT INTO flows (id, source_id, format, codec, read_only)
		VALUES ($1, $2, 'urn:x-nmos:format:video', 'video/h264', $3)`, flowID, srcID, readOnly)
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM flows WHERE id = $1`, flowID)
		_, _ = pool.Exec(ctx, `DELETE FROM sources WHERE id = $1`, srcID)
	})
}
