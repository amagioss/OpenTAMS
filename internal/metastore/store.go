// Package metastore provides the PostgreSQL-backed metadata store for OpenTAMS.
// It implements SourceStore, FlowStore, and SegmentStore via a single PostgresStore.
package metastore

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// dbConn is satisfied by both *pgxpool.Pool and pgx.Tx.
// Tests inject a pgx.Tx so that all store writes occur within
// a transaction that is rolled back after each test.
type dbConn interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Begin(ctx context.Context) (pgx.Tx, error)
	SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults
}

// PostgresStore implements SourceStore, FlowStore, and SegmentStore.
// It also exposes Ping for health checks; callers define their own pinger interface.
type PostgresStore struct {
	db   dbConn
	pool *pgxpool.Pool // retained for Ping; nil when running under a test tx
}

// New constructs a PostgresStore from an existing pool.
func New(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{db: pool, pool: pool}
}

// Ping verifies the database connection is alive.
func (s *PostgresStore) Ping(ctx context.Context) error {
	if s.pool != nil {
		return s.pool.Ping(ctx)
	}
	// test path: execute a cheap query through the injected tx
	_, err := s.db.Exec(ctx, "SELECT 1")
	return err
}

// begin starts a transaction (or savepoint when db is already a pgx.Tx).
func (s *PostgresStore) begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("metastore: begin tx: %w", err)
	}
	return tx, nil
}

// pageCursor is the opaque pagination token stored as base64-encoded JSON.
type pageCursor struct {
	ts time.Time
	id uuid.UUID
}

// encodeCursor serialises a (timestamp, uuid) pair into a URL-safe opaque string.
func encodeCursor(ts time.Time, id uuid.UUID) string {
	b, _ := json.Marshal(struct {
		TS time.Time `json:"ts"`
		ID uuid.UUID `json:"id"`
	}{TS: ts, ID: id})
	return base64.RawURLEncoding.EncodeToString(b)
}

// decodeCursor reverses encodeCursor.
func decodeCursor(s string) (pageCursor, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return pageCursor{}, err
	}
	var v struct {
		TS time.Time `json:"ts"`
		ID uuid.UUID `json:"id"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return pageCursor{}, err
	}
	return pageCursor{ts: v.TS, id: v.ID}, nil
}

// encodeSegCursor serialises a (lowerNs, segID) pair for segment pagination.
func encodeSegCursor(lowerNs, segID int64) string {
	b, _ := json.Marshal(struct {
		LowerNs int64 `json:"lower_ns"`
		SegID   int64 `json:"seg_id"`
	}{LowerNs: lowerNs, SegID: segID})
	return base64.RawURLEncoding.EncodeToString(b)
}

// decodeSegCursor reverses encodeSegCursor.
func decodeSegCursor(s string) (lowerNs, segID int64, err error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return 0, 0, err
	}
	var v struct {
		LowerNs int64 `json:"lower_ns"`
		SegID   int64 `json:"seg_id"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return 0, 0, err
	}
	return v.LowerNs, v.SegID, nil
}

// compile-time interface assertions
var (
	_ SourceStore = (*PostgresStore)(nil)
	_ FlowStore   = (*PostgresStore)(nil)
)
