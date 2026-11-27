package metastore

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// AllocateParams is the input of AllocateObjects (BR-GC-01 to BR-GC-06).
type AllocateParams struct {
	FlowID    uuid.UUID
	StorageID string
	TTL       time.Duration
	IDs       []string
	Requested bool // true: mode object_ids (BR-GC-03); false: mode limit (BR-GC-02)
}

// Allocation errors. AllocateObjects wraps each one in an *ObjectError.
var (
	ErrObjectExists    = errors.New("metastore: object already exists")
	ErrObjectDeleting  = errors.New("metastore: object is being deleted")
	ErrObjectWrongFlow = errors.New("metastore: object is allocated to a different flow")
)

// ObjectError carries the object ID of an allocation error.
type ObjectError struct {
	ID  string
	Err error // one of the three sentinels
}

func (e *ObjectError) Error() string { return fmt.Sprintf("metastore: object %q: %v", e.ID, e.Err) }
func (e *ObjectError) Unwrap() error { return e.Err }

// AllocateObjects writes one allocation row for each ID in one
// transaction. In mode object_ids it applies the decision table of
// BR-GC-03 to each ID, and the first rejection rolls back the full
// request (BR-GC-04).
func (s *PostgresStore) AllocateObjects(ctx context.Context, p AllocateParams) error {
	ids := slices.Clone(p.IDs)
	slices.Sort(ids)

	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	ttl := p.TTL.Seconds()
	if p.Requested {
		for _, id := range ids {
			if err := allocateRequested(ctx, tx, p, id, ttl); err != nil {
				return err
			}
		}
	} else if _, err := tx.Exec(ctx, `
		INSERT INTO allocations (id, flow_id, storage_id, expires_at)
		SELECT unnest($1::text[]), $2, $3, now() + make_interval(secs => $4)`,
		ids, p.FlowID, p.StorageID, ttl); err != nil {
		return fmt.Errorf("metastore: insert allocations: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("metastore: commit allocations: %w", err)
	}
	return nil
}

// allocateRequested applies the decision table of BR-GC-03 to one ID.
func allocateRequested(ctx context.Context, tx pgx.Tx, p AllocateParams, id string, ttl float64) error {
	var objectDeleting bool
	err := tx.QueryRow(ctx, `SELECT deleting FROM objects WHERE id = $1 FOR SHARE`, id).Scan(&objectDeleting)
	switch {
	case err == nil && objectDeleting:
		return &ObjectError{ID: id, Err: ErrObjectDeleting}
	case err == nil:
		return &ObjectError{ID: id, Err: ErrObjectExists}
	case !errors.Is(err, pgx.ErrNoRows):
		return fmt.Errorf("metastore: read object %q: %w", id, err)
	}

	var written string
	err = tx.QueryRow(ctx, `
		INSERT INTO allocations (id, flow_id, storage_id, expires_at)
		VALUES ($1, $2, $3, now() + make_interval(secs => $4))
		ON CONFLICT (id) DO UPDATE
		    SET expires_at = EXCLUDED.expires_at
		    WHERE allocations.flow_id = EXCLUDED.flow_id
		      AND allocations.deleting = false
		RETURNING id`,
		id, p.FlowID, p.StorageID, ttl).Scan(&written)
	switch {
	case err == nil:
		return nil
	case !errors.Is(err, pgx.ErrNoRows):
		return fmt.Errorf("metastore: write allocation %q: %w", id, err)
	}

	var allocationDeleting bool
	if err := tx.QueryRow(ctx, `SELECT deleting FROM allocations WHERE id = $1`, id).Scan(&allocationDeleting); err != nil {
		return fmt.Errorf("metastore: read allocation %q: %w", id, err)
	}
	if allocationDeleting {
		return &ObjectError{ID: id, Err: ErrObjectDeleting}
	}
	return &ObjectError{ID: id, Err: ErrObjectWrongFlow}
}
