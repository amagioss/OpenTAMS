package metastore

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/amagioss/opentams/internal/gc"
)

var _ gc.Store = (*PostgresStore)(nil)

// ClaimReleasedObjects claims up to limit released objects of storageID
// for sweep 1 in one transaction (BR-GC-30 to BR-GC-34).
func (s *PostgresStore) ClaimReleasedObjects(ctx context.Context, storageID string, limit int) (gc.ClaimResult, error) {
	tx, err := s.begin(ctx)
	if err != nil {
		return gc.ClaimResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx, `
		SELECT id, storage_id FROM objects
		WHERE ref_count = 0 AND deleting = false
		  AND (storage_id IS NULL OR storage_id = $1)
		ORDER BY id
		LIMIT $2
		FOR UPDATE SKIP LOCKED`, storageID, limit)
	if err != nil {
		return gc.ClaimResult{}, fmt.Errorf("metastore: select released objects: %w", err)
	}
	candidates, err := pgx.CollectRows(rows, pgx.RowToStructByPos[releasedCandidate])
	if err != nil {
		return gc.ClaimResult{}, fmt.Errorf("metastore: scan released objects: %w", err)
	}

	result := gc.ClaimResult{Selected: len(candidates)}
	if len(candidates) == 0 {
		return result, nil
	}

	result.Drift, err = repairReleasedDrift(ctx, tx, candidates)
	if err != nil {
		return gc.ClaimResult{}, err
	}
	for _, c := range candidates {
		if slices.ContainsFunc(result.Drift, func(d gc.DriftRecord) bool { return d.ObjectID == c.ID }) {
			continue
		}
		if c.StorageID == nil {
			result.RowOnly = append(result.RowOnly, c.ID)
		} else {
			result.Claimed = append(result.Claimed, c.ID)
		}
	}

	if len(result.RowOnly) > 0 {
		if _, err := tx.Exec(ctx, `DELETE FROM objects WHERE id = ANY($1::text[])`, result.RowOnly); err != nil {
			return gc.ClaimResult{}, fmt.Errorf("metastore: delete released BYOS objects: %w", err)
		}
	}
	if len(result.Claimed) > 0 {
		if _, err := tx.Exec(ctx,
			`UPDATE objects SET deleting = true, claimed_at = now() WHERE id = ANY($1::text[])`,
			result.Claimed); err != nil {
			return gc.ClaimResult{}, fmt.Errorf("metastore: claim released objects: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return gc.ClaimResult{}, fmt.Errorf("metastore: commit released claim: %w", err)
	}
	slices.Sort(result.Claimed)
	slices.Sort(result.RowOnly)
	return result, nil
}

type releasedCandidate struct {
	ID        string
	StorageID *string
}

// repairReleasedDrift counts the segments of each candidate and sets
// ref_count to the count where it is more than 0 (BR-GC-33).
func repairReleasedDrift(ctx context.Context, tx pgx.Tx, candidates []releasedCandidate) ([]gc.DriftRecord, error) {
	ids := make([]string, len(candidates))
	for i, c := range candidates {
		ids[i] = c.ID
	}
	rows, err := tx.Query(ctx, `
		SELECT object_id, count(*) FROM segments
		WHERE object_id = ANY($1::text[])
		GROUP BY object_id`, ids)
	if err != nil {
		return nil, fmt.Errorf("metastore: count segments of released objects: %w", err)
	}
	drift, err := pgx.CollectRows(rows, pgx.RowToStructByPos[gc.DriftRecord])
	if err != nil {
		return nil, fmt.Errorf("metastore: scan segment counts: %w", err)
	}
	if len(drift) == 0 {
		return nil, nil
	}
	slices.SortFunc(drift, func(a, b gc.DriftRecord) int { return strings.Compare(a.ObjectID, b.ObjectID) })

	driftIDs := make([]string, len(drift))
	counts := make([]int64, len(drift))
	for i, d := range drift {
		driftIDs[i] = d.ObjectID
		counts[i] = d.Actual
	}
	if _, err := tx.Exec(ctx, `
		UPDATE objects AS o SET ref_count = d.n
		  FROM unnest($1::text[], $2::int[]) AS d(id, n)
		 WHERE o.id = d.id`, driftIDs, counts); err != nil {
		return nil, fmt.Errorf("metastore: repair ref_count drift: %w", err)
	}
	return drift, nil
}

// ReclaimRetryObjects renews the claim of up to limit retry rows of
// sweep 1 whose lease expired before cycleStart (BR-GC-64, BR-GC-65).
func (s *PostgresStore) ReclaimRetryObjects(ctx context.Context, storageID string, lease time.Duration, cycleStart time.Time, limit int) ([]string, error) {
	return s.reclaimRetryRows(ctx, "objects", storageID, lease, cycleStart, limit)
}

func (s *PostgresStore) reclaimRetryRows(ctx context.Context, table, storageID string, lease time.Duration, cycleStart time.Time, limit int) ([]string, error) {
	rows, err := s.db.Query(ctx, `
		UPDATE `+table+` SET claimed_at = now()
		WHERE id IN (
		    SELECT id FROM `+table+`
		    WHERE deleting = true
		      AND storage_id = $1
		      AND claimed_at < now() - make_interval(secs => $2)
		      AND claimed_at < $3
		    ORDER BY id
		    LIMIT $4
		    FOR UPDATE SKIP LOCKED)
		RETURNING id`, storageID, lease.Seconds(), cycleStart, limit)
	if err != nil {
		return nil, fmt.Errorf("metastore: reclaim retry %s: %w", table, err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("metastore: reclaim retry %s: %w", table, err)
	}
	slices.Sort(ids)
	return ids, nil
}

// ReleasedBacklog returns the gauge counts of sweep 1.
func (s *PostgresStore) ReleasedBacklog(ctx context.Context, storageID string) (gc.Backlog, error) {
	var b gc.Backlog
	err := s.db.QueryRow(ctx, `
		SELECT
		  (SELECT count(*) FROM objects WHERE ref_count = 0 AND deleting = false
		      AND (storage_id IS NULL OR storage_id = $1))                     AS pending,
		  (SELECT count(*) FROM objects WHERE deleting = true)                 AS retrying,
		  (SELECT count(*) FROM objects WHERE ref_count = 0 AND deleting = false
		      AND storage_id IS NOT NULL AND storage_id <> $1)                 AS unknown_storage`,
		storageID).Scan(&b.Pending, &b.Retrying, &b.UnknownStorage)
	if err != nil {
		return gc.Backlog{}, fmt.Errorf("metastore: released backlog: %w", err)
	}
	return b, nil
}

// DeleteClaimedObjects deletes the object rows of ids that have
// deleting = true, and returns the deleted IDs (BR-GC-36).
func (s *PostgresStore) DeleteClaimedObjects(ctx context.Context, ids []string) ([]string, error) {
	return s.deleteClaimedRows(ctx, "objects", ids)
}

// ClaimExpiredAllocations claims up to limit allocations of storageID
// that expired more than grace ago, for sweep 2 in one transaction
// (BR-GC-40 to BR-GC-42).
func (s *PostgresStore) ClaimExpiredAllocations(ctx context.Context, storageID string, grace time.Duration, limit int) (gc.ClaimResult, error) {
	tx, err := s.begin(ctx)
	if err != nil {
		return gc.ClaimResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx, `
		SELECT id FROM allocations
		WHERE deleting = false
		  AND storage_id = $1
		  AND expires_at < now() - make_interval(secs => $2)
		ORDER BY expires_at, id
		LIMIT $3
		FOR UPDATE SKIP LOCKED`, storageID, grace.Seconds(), limit)
	if err != nil {
		return gc.ClaimResult{}, fmt.Errorf("metastore: select expired allocations: %w", err)
	}
	candidates, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return gc.ClaimResult{}, fmt.Errorf("metastore: scan expired allocations: %w", err)
	}

	result := gc.ClaimResult{Selected: len(candidates)}
	if len(candidates) == 0 {
		return result, nil
	}

	rows, err = tx.Query(ctx, `SELECT id FROM objects WHERE id = ANY($1::text[])`, candidates)
	if err != nil {
		return gc.ClaimResult{}, fmt.Errorf("metastore: find registered allocations: %w", err)
	}
	result.RowOnly, err = pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return gc.ClaimResult{}, fmt.Errorf("metastore: scan registered allocations: %w", err)
	}
	for _, id := range candidates {
		if !slices.Contains(result.RowOnly, id) {
			result.Claimed = append(result.Claimed, id)
		}
	}

	if len(result.RowOnly) > 0 {
		if _, err := tx.Exec(ctx, `DELETE FROM allocations WHERE id = ANY($1::text[])`, result.RowOnly); err != nil {
			return gc.ClaimResult{}, fmt.Errorf("metastore: delete leftover allocations: %w", err)
		}
	}
	if len(result.Claimed) > 0 {
		if _, err := tx.Exec(ctx,
			`UPDATE allocations SET deleting = true, claimed_at = now() WHERE id = ANY($1::text[])`,
			result.Claimed); err != nil {
			return gc.ClaimResult{}, fmt.Errorf("metastore: claim expired allocations: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return gc.ClaimResult{}, fmt.Errorf("metastore: commit allocation claim: %w", err)
	}
	slices.Sort(result.Claimed)
	slices.Sort(result.RowOnly)
	return result, nil
}

// ReclaimRetryAllocations renews the claim of up to limit retry rows of
// sweep 2 whose lease expired before cycleStart (BR-GC-64, BR-GC-65).
func (s *PostgresStore) ReclaimRetryAllocations(ctx context.Context, storageID string, lease time.Duration, cycleStart time.Time, limit int) ([]string, error) {
	return s.reclaimRetryRows(ctx, "allocations", storageID, lease, cycleStart, limit)
}

// AllocationBacklog returns the gauge counts of sweep 2.
func (s *PostgresStore) AllocationBacklog(ctx context.Context, storageID string, grace time.Duration) (gc.Backlog, error) {
	var b gc.Backlog
	err := s.db.QueryRow(ctx, `
		SELECT
		  (SELECT count(*) FROM allocations WHERE deleting = false AND storage_id = $1
		      AND expires_at < now() - make_interval(secs => $2))               AS pending,
		  (SELECT count(*) FROM allocations WHERE deleting = true)              AS retrying,
		  (SELECT count(*) FROM allocations WHERE deleting = false AND storage_id <> $1
		      AND expires_at < now() - make_interval(secs => $2))               AS unknown_storage,
		  (SELECT count(*) FROM allocations)                                    AS rows,
		  (SELECT COALESCE(EXTRACT(EPOCH FROM (now() - make_interval(secs => $2) - min(expires_at))), 0)
		     FROM allocations WHERE deleting = false AND storage_id = $1
		      AND expires_at < now() - make_interval(secs => $2))               AS oldest_overdue`,
		storageID, grace.Seconds()).Scan(&b.Pending, &b.Retrying, &b.UnknownStorage, &b.Rows, &b.OldestOverdue)
	if err != nil {
		return gc.Backlog{}, fmt.Errorf("metastore: allocation backlog: %w", err)
	}
	return b, nil
}

// DeleteClaimedAllocations deletes the allocation rows of ids that have
// deleting = true, and returns the deleted IDs (BR-GC-44).
func (s *PostgresStore) DeleteClaimedAllocations(ctx context.Context, ids []string) ([]string, error) {
	return s.deleteClaimedRows(ctx, "allocations", ids)
}

// deleteClaimedRows locks the claimed rows of table in ascending byte
// order (BR-GC-23) and deletes them in one transaction.
func (s *PostgresStore) deleteClaimedRows(ctx context.Context, table string, ids []string) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
		SELECT id FROM `+table+` WHERE id = ANY($1::text[]) AND deleting = true
		ORDER BY id COLLATE "C" FOR UPDATE`, ids); err != nil {
		return nil, fmt.Errorf("metastore: lock claimed %s: %w", table, err)
	}
	rows, err := tx.Query(ctx, `
		DELETE FROM `+table+` WHERE id = ANY($1::text[]) AND deleting = true
		RETURNING id`, ids)
	if err != nil {
		return nil, fmt.Errorf("metastore: delete claimed %s: %w", table, err)
	}
	deleted, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("metastore: delete claimed %s: %w", table, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("metastore: commit claimed %s delete: %w", table, err)
	}
	slices.Sort(deleted)
	return deleted, nil
}
