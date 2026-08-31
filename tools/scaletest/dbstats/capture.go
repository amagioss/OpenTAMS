package dbstats

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// queryTextMax bounds the stored pg_stat_statements query text.
const queryTextMax = 200

// Capture takes a snapshot of the database counters. The core
// pg_stat_database read must succeed (it signals the pool is usable);
// everything else is best-effort, so a missing view, missing column, or
// absent extension (e.g. pg_stat_statements) or a Postgres-version
// difference (pg_stat_checkpointer vs pg_stat_bgwriter, pg_stat_wal)
// degrades gracefully to a zero field rather than failing the capture.
// `tables` are the unqualified relnames to track (e.g. segments, objects).
func Capture(ctx context.Context, pool *pgxpool.Pool, tables []string) (Snapshot, error) {
	s := Snapshot{At: time.Now()}

	// Core: per-database cumulative counters (all supported versions).
	err := pool.QueryRow(ctx, `
		SELECT xact_commit, xact_rollback, blks_hit, blks_read,
		       deadlocks, temp_files, temp_bytes
		FROM pg_stat_database
		WHERE datname = current_database()`,
	).Scan(&s.Commits, &s.Rollbacks, &s.BlksHit, &s.BlksRead, &s.Deadlocks, &s.TempFiles, &s.TempBytes)
	if err != nil {
		return Snapshot{}, fmt.Errorf("dbstats: pg_stat_database: %w", err)
	}

	// Best-effort: WAL bytes (PG14+).
	_ = pool.QueryRow(ctx, `SELECT wal_bytes::bigint FROM pg_stat_wal`).Scan(&s.WALBytes)

	// Best-effort: checkpoints — PG17+ moved these to pg_stat_checkpointer.
	if err := pool.QueryRow(ctx,
		`SELECT num_timed + num_requested FROM pg_stat_checkpointer`).Scan(&s.Checkpoints); err != nil {
		_ = pool.QueryRow(ctx,
			`SELECT checkpoints_timed + checkpoints_req FROM pg_stat_bgwriter`).Scan(&s.Checkpoints)
	}

	s.Tables = captureTables(ctx, pool, tables)
	s.Queries = captureQueries(ctx, pool)
	return s, nil
}

func captureTables(ctx context.Context, pool *pgxpool.Pool, tables []string) map[string]TableSnap {
	if len(tables) == 0 {
		return nil
	}
	rows, err := pool.Query(ctx, `
		SELECT relname, n_live_tup, n_dead_tup, autovacuum_count, autoanalyze_count,
		       pg_total_relation_size(relid)
		FROM pg_stat_user_tables
		WHERE relname = ANY($1)`, tables)
	if err != nil {
		return nil
	}
	defer rows.Close()

	out := make(map[string]TableSnap, len(tables))
	for rows.Next() {
		var name string
		var t TableSnap
		if err := rows.Scan(&name, &t.LiveTup, &t.DeadTup, &t.AutovacuumCount, &t.AutoanalyzeCount, &t.SizeBytes); err != nil {
			return out
		}
		out[name] = t
	}
	return out
}

func captureQueries(ctx context.Context, pool *pgxpool.Pool) map[int64]QuerySnap {
	// pg_stat_statements is optional; absence (relation does not exist)
	// just yields no top-query data.
	rows, err := pool.Query(ctx, `
		SELECT queryid, query, calls, total_exec_time
		FROM pg_stat_statements
		WHERE queryid IS NOT NULL
		ORDER BY total_exec_time DESC
		LIMIT 200`)
	if err != nil {
		return nil
	}
	defer rows.Close()

	out := make(map[int64]QuerySnap)
	for rows.Next() {
		var id int64
		var q QuerySnap
		if err := rows.Scan(&id, &q.Text, &q.Calls, &q.TotalMs); err != nil {
			return out
		}
		if len(q.Text) > queryTextMax {
			q.Text = q.Text[:queryTextMax]
		}
		out[id] = q
	}
	return out
}
