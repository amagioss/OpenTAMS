package idempotency

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/amagioss/opentams/internal/apperror"
)

// Status is the outcome of an Acquire call: it tells the caller
// whether they own the key, must wait for an in-flight peer, can
// replay a cached response, or hit a body-hash conflict on the same
// key.
type Status int

// Outcomes of Acquire. See the Acquire godoc for the contract each
// status implies for the caller.
const (
	// StatusAcquired means this caller now owns the key and must
	// either Complete or Release before the TTL expires.
	StatusAcquired Status = iota
	// StatusInFlight means another caller is currently processing
	// the same key; this caller should retry (typically with 409 to
	// the client).
	StatusInFlight
	// StatusCached means a previous successful response for this
	// key+bodyHash is available in AcquireResult.Cached and should
	// be replayed verbatim.
	StatusCached
	// StatusConflict means the key was reused with a different
	// body hash than the in-flight or cached request; the caller
	// should reject the request.
	StatusConflict
)

// CachedResponse is the cached response to replay for a
// StatusCached outcome. Body is the raw bytes the original handler
// returned and must be replayed verbatim — the handler is
// responsible for canonicalising before hashing.
type CachedResponse struct {
	Status int
	Body   json.RawMessage
}

// AcquireResult is the outcome of an Acquire call. Cached is non-nil
// exactly when Status is StatusCached.
type AcquireResult struct {
	Status Status
	Cached *CachedResponse
}

// dbConn is satisfied by *pgxpool.Pool and pgx.Tx.
// On a pgx.Tx, Begin creates a savepoint (nested transaction semantics).
type dbConn interface {
	Begin(ctx context.Context) (pgx.Tx, error)
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// PostgresStore persists idempotency records in a PostgreSQL table.
// The store is safe for concurrent use; serialisation is enforced
// per-key with SELECT FOR UPDATE inside a transaction.
type PostgresStore struct {
	db dbConn
}

// NewStore returns a PostgresStore backed by the supplied connection
// (or pool). The schema is owned by the migrations package and must
// be present before the first Acquire call.
func NewStore(db dbConn) *PostgresStore {
	return &PostgresStore{db: db}
}

func validateKey(key string) error {
	if key == "" {
		return apperror.New(apperror.ErrSchemaValidation, "idempotency key must not be empty")
	}
	if len(key) > 255 {
		return apperror.New(apperror.ErrSchemaValidation, "idempotency key exceeds 255 characters")
	}
	return nil
}

// Acquire claims an idempotency key for the caller.
//
// Any expired record for the key is deleted first. A SELECT FOR UPDATE inside
// a transaction prevents two concurrent callers from both inserting a new row.
// Returns StatusAcquired, StatusInFlight, StatusCached, or StatusConflict.
func (s *PostgresStore) Acquire(ctx context.Context, key, bodyHash string, ttl time.Duration) (AcquireResult, error) {
	if err := validateKey(key); err != nil {
		return AcquireResult{}, err
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return AcquireResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Delete expired record before locking so an expired key is treated as new.
	if _, err := tx.Exec(ctx,
		`DELETE FROM idempotency_keys WHERE key = $1 AND expires_at <= now()`,
		key,
	); err != nil {
		return AcquireResult{}, err
	}

	var (
		storedHash     string
		inFlight       bool
		responseStatus int
		responseBody   json.RawMessage
	)

	err = tx.QueryRow(ctx,
		`SELECT body_hash, in_flight, response_status, response_body
		   FROM idempotency_keys
		  WHERE key = $1
		    FOR UPDATE`,
		key,
	).Scan(&storedHash, &inFlight, &responseStatus, &responseBody)

	if errors.Is(err, pgx.ErrNoRows) {
		if _, err := tx.Exec(ctx,
			`INSERT INTO idempotency_keys (key, body_hash, expires_at) VALUES ($1, $2, $3)`,
			key, bodyHash, time.Now().Add(ttl),
		); err != nil {
			return AcquireResult{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return AcquireResult{}, err
		}
		return AcquireResult{Status: StatusAcquired}, nil
	}
	if err != nil {
		return AcquireResult{}, err
	}

	if storedHash != bodyHash {
		return AcquireResult{Status: StatusConflict}, nil
	}
	if inFlight {
		return AcquireResult{Status: StatusInFlight}, nil
	}
	return AcquireResult{
		Status: StatusCached,
		Cached: &CachedResponse{Status: responseStatus, Body: responseBody},
	}, nil
}

// Complete records the HTTP response for a completed operation and marks the
// key as no longer in-flight. It is a no-op if the key is unknown or expired.
//
// Use Complete for deterministic outcomes whose result should be replayed on
// retries within the TTL window (2xx success, and 4xx client errors that are
// stable for the same input — overlap, read-only, schema validation, etc.).
// Use Release for transient outcomes (5xx, infra failures) so the client can
// retry successfully once the underlying issue resolves.
func (s *PostgresStore) Complete(ctx context.Context, key string, statusCode int, body json.RawMessage) error {
	if err := validateKey(key); err != nil {
		return err
	}
	_, err := s.db.Exec(ctx,
		`UPDATE idempotency_keys
		    SET in_flight = false, response_status = $2, response_body = $3
		  WHERE key = $1`,
		key, statusCode, body,
	)
	return err
}

// Release deletes the idempotency row for key, allowing a subsequent retry
// with the same key to proceed as a fresh request. Use this for transient
// failures (5xx, context cancellation, panics caught by the handler's defer
// safety net) where the client should be able to retry.
//
// Release is also the appropriate response to a process crash or unhandled
// exit between Acquire and Complete, where the row would otherwise remain
// in_flight=true until TTL expiry, locking the client out of legitimate
// retries for IDEMPOTENCY_KEY_TTL.
//
// No-op if the key is unknown.
func (s *PostgresStore) Release(ctx context.Context, key string) error {
	if err := validateKey(key); err != nil {
		return err
	}
	_, err := s.db.Exec(ctx,
		`DELETE FROM idempotency_keys WHERE key = $1`,
		key,
	)
	return err
}

// Prune deletes all expired idempotency keys and returns the count deleted.
func (s *PostgresStore) Prune(ctx context.Context) (int64, error) {
	tag, err := s.db.Exec(ctx,
		`DELETE FROM idempotency_keys WHERE expires_at <= now()`,
	)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// ReapStale deletes idempotency rows that are still in_flight after threshold
// has elapsed since acquisition, and returns the count deleted.
//
// Recovers keys orphaned by a process crash, OOM kill, or unhandled exit
// between Acquire and Complete/Release — paths that bypass the handler's
// defer safety net. Without this reaper such rows would remain in_flight
// until expires_at (IDEMPOTENCY_KEY_TTL), locking the client out of
// legitimate retries for that whole window.
//
// CALLER CONTRACT: threshold MUST be >= the server's HTTP WriteTimeout.
// A genuinely-running request can hold the row in_flight for up to
// WriteTimeout; reaping faster than that races against in-progress
// segment registrations and would silently drop the in-memory work
// before the handler can finalise. The serve loop enforces this clamp
// at startup; passing a smaller value here is a programming error.
//
// Concurrent ReapStale calls across HA replicas are race-safe: the DELETE
// is atomic, and a row reaped by replica A is simply absent for replica B.
func (s *PostgresStore) ReapStale(ctx context.Context, threshold time.Duration) (int64, error) {
	// Compare server-side now() against acquired_at to avoid coupling the
	// reaper's correctness to clock skew between the app process and the DB.
	tag, err := s.db.Exec(ctx,
		`DELETE FROM idempotency_keys
		  WHERE in_flight = true
		    AND acquired_at <= now() - ($1 * INTERVAL '1 second')`,
		threshold.Seconds(),
	)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
