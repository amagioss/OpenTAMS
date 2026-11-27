package metastore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/amagioss/opentams/internal/apperror"
	"github.com/amagioss/opentams/internal/domain"
	"github.com/amagioss/opentams/internal/timerange"
)

// Store is the segments-slice metastore contract (D-32 rewrite); see
// docs/design/internal-metastore/functional-design/business-rules.md.
//
// Implementations MUST be safe for concurrent use. The Postgres backend
// satisfies this interface via *PostgresStore.
type Store interface {
	GetFlowForSegmentWrite(ctx context.Context, tx Tx, flowID uuid.UUID) (FlowGuard, error)
	GetFlowForSegmentRead(ctx context.Context, flowID uuid.UUID) (FlowGuard, error)

	InsertSegments(ctx context.Context, batch InsertBatch) (InsertResult, error)
	ListSegments(ctx context.Context, q ListQuery) (domain.SegmentPage, error)
	DeleteSegmentsByTimerange(ctx context.Context, q DeleteQuery) (DeleteResult, error)
}

// Tx is an opaque transaction handle. The Postgres backend uses pgx.Tx;
// the memory backend uses its own type. Callers (and tests) treat it as
// opaque.
type Tx interface{}

// InsertBatch is the input shape for InsertSegments. FlowID is the
// target flow; Segments are the candidate rows in caller order — input
// order is significant (REQ-META-03). A new controlled object row
// (`len(Segment.GetURLs) == 0`) takes `objects.storage_id` from its
// allocation row, not from ControlledStorageID (BR-GC-17); BYOS segments
// receive NULL. ControlledStorageID is used only for this check:
// empty is legal only for BYOS-only batches — a
// controlled segment with an empty value fails with
// ErrControlledStorageNotConfigured (BR-META-07).
type InsertBatch struct {
	FlowID              uuid.UUID
	Segments            []domain.Segment
	ControlledStorageID string
}

// InsertResult partitions [0, len(input)) into accepted vs rejected
// indices when the batch did NOT trigger ErrSegmentOverlap. On overlap
// (whole-batch reject, D-SVC-SEG-02 / BR-META-06), both slices are
// empty and the error is non-nil. A segment is rejected when its object
// group fails the allocation rules (BR-GC-12, BR-GC-14); the other
// segments are still written (BR-GC-15).
type InsertResult struct {
	AcceptedIndices []int
	RejectedIndices []int
	RejectReasons   []RejectReason
}

// RejectReason is parallel-indexed to InsertResult.RejectedIndices.
// Code is the apperror code of the rejection; the service derives the
// problem type and title from it (BR-GC-15). Type is an RFC 7807 type
// URI, used only when Code is empty. Detail is human-readable and names
// the object_id or other context.
type RejectReason struct {
	Code   apperror.ErrorCode
	Type   string
	Detail string
}

// ListQuery is the metastore-boundary input for ListSegments. The
// service constructs this from a parsed domain.ListParams; conversion
// has already mapped wire → params.
type ListQuery struct {
	FlowID                 uuid.UUID
	Timerange              *timerange.TimeRange
	ObjectID               *string
	ReverseOrder           bool
	VerboseStorage         bool
	AcceptGetURLs          []string
	AcceptStorageIDs       []string
	Presigned              *bool
	IncludeObjectTimerange bool
	Page                   string
	Limit                  int // 1..1000; 0 forbidden — caller clamps
}

// DeleteQuery is the input for DeleteSegmentsByTimerange. Timerange is
// required; the spec mandates the wire-side query param. ObjectID is
// an optional secondary filter.
type DeleteQuery struct {
	FlowID    uuid.UUID
	Timerange timerange.TimeRange
	ObjectID  *string
}

// DeleteResult carries the count for log / metric / idempotency-cache /
// test-assertion consumers. ReleasedObjects was removed (D-29) — the
// GC worker reads `objects WHERE ref_count = 0 AND deleting = false`
// directly; the service does NOT act on released-object IDs.
type DeleteResult struct {
	DeletedCount int64
}

// FlowGuard carries the segments-slice subset of flow fields:
// existence + read-only flag. Returned by GetFlowForSegmentWrite (with
// row-level lock) and GetFlowForSegmentRead (no lock).
type FlowGuard struct {
	FlowID   uuid.UUID
	Exists   bool
	ReadOnly bool
}

// Segments-slice sentinel errors. All are wrapped (not returned as-is)
// so callers can use errors.Is. Distinct from the legacy SegmentStore's
// reliance on apperror.AppError codes — the new contract uses sentinels
// at the package boundary; the service translates to AppError as needed.
var (
	ErrFlowNotFound                   = errors.New("metastore: flow not found")
	ErrFlowReadOnly                   = errors.New("metastore: flow is read-only")
	ErrInvalidCursor                  = errors.New("metastore: invalid pagination cursor")
	ErrBadInput                       = errors.New("metastore: invalid input")
	ErrSegmentOverlap                 = errors.New("metastore: batch contains overlap")
	ErrControlledStorageNotConfigured = errors.New("metastore: controlled segment registered but InsertBatch.ControlledStorageID is empty")
)

// hardLimit is the maximum LIMIT actually applied by ListSegments
// (REQ-PERF-07; INV-META-CLAMP). Callers asking for more get this.
const hardLimit = 1000

// GetFlowForSegmentWrite reads flow guard fields under a row-level lock
// (SELECT … FOR UPDATE). MUST be called inside the segment write tx.
func (s *PostgresStore) GetFlowForSegmentWrite(ctx context.Context, tx Tx, flowID uuid.UUID) (FlowGuard, error) {
	pgtx, ok := tx.(pgx.Tx)
	if !ok {
		return FlowGuard{}, fmt.Errorf("metastore: GetFlowForSegmentWrite: unexpected tx type %T", tx)
	}
	var readOnly bool
	err := pgtx.QueryRow(ctx,
		`SELECT read_only FROM flows WHERE id = $1 FOR UPDATE`, flowID,
	).Scan(&readOnly)
	if errors.Is(err, pgx.ErrNoRows) {
		return FlowGuard{FlowID: flowID, Exists: false}, nil
	}
	if err != nil {
		return FlowGuard{}, fmt.Errorf("metastore: GetFlowForSegmentWrite: %w", err)
	}
	return FlowGuard{FlowID: flowID, Exists: true, ReadOnly: readOnly}, nil
}

// GetFlowForSegmentRead reads flow guard fields without a lock.
func (s *PostgresStore) GetFlowForSegmentRead(ctx context.Context, flowID uuid.UUID) (FlowGuard, error) {
	var readOnly bool
	err := s.db.QueryRow(ctx,
		`SELECT read_only FROM flows WHERE id = $1`, flowID,
	).Scan(&readOnly)
	if errors.Is(err, pgx.ErrNoRows) {
		return FlowGuard{FlowID: flowID, Exists: false}, nil
	}
	if err != nil {
		return FlowGuard{}, fmt.Errorf("metastore: GetFlowForSegmentRead: %w", err)
	}
	return FlowGuard{FlowID: flowID, Exists: true, ReadOnly: readOnly}, nil
}

// InsertSegments performs a batch insert with overlap detection. Whole-batch
// reject on any overlap (BR-META-06). Atomic per INV-META-05.
func (s *PostgresStore) InsertSegments(ctx context.Context, batch InsertBatch) (InsertResult, error) {
	// Step 0: bounds and within-batch overlap pre-check (no DB round-trip
	// needed). Both overlap checks compare the same half-open bounds
	// (BR-META-05, ADR-0040 rule 5).
	bounds, err := segmentBounds(batch.Segments)
	if err != nil {
		return InsertResult{}, err
	}
	for i := 0; i < len(bounds); i++ {
		for j := i + 1; j < len(bounds); j++ {
			if boundsOverlap(bounds[i], bounds[j]) {
				return InsertResult{}, fmt.Errorf(
					"metastore: InsertSegments: segments %d (%s) and %d (%s) overlap: %w",
					i, batch.Segments[i].TimerangeRaw,
					j, batch.Segments[j].TimerangeRaw,
					ErrSegmentOverlap,
				)
			}
		}
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return InsertResult{}, fmt.Errorf("metastore: InsertSegments: begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op

	// Step 1: lock flow row + read read_only.
	guard, err := s.GetFlowForSegmentWrite(ctx, tx, batch.FlowID)
	if err != nil {
		return InsertResult{}, err
	}
	if !guard.Exists {
		return InsertResult{}, fmt.Errorf("metastore: InsertSegments: flow %s: %w", batch.FlowID, ErrFlowNotFound)
	}
	if guard.ReadOnly {
		return InsertResult{}, fmt.Errorf("metastore: InsertSegments: flow %s: %w", batch.FlowID, ErrFlowReadOnly)
	}

	// Step 2: against-existing overlap check.
	for i := range batch.Segments {
		var existingTR string
		err := tx.QueryRow(ctx, existingOverlapSQL,
			batch.FlowID, lowerParam(bounds[i]), upperParam(bounds[i]),
		).Scan(&existingTR)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return InsertResult{}, fmt.Errorf("metastore: InsertSegments: overlap check: %w", err)
		}
		return InsertResult{}, fmt.Errorf(
			"metastore: InsertSegments: segment %d (%s) overlaps existing %s on flow %s: %w",
			i, batch.Segments[i].TimerangeRaw, existingTR, batch.FlowID, ErrSegmentOverlap,
		)
	}

	// Step 3: register each object group against the objects and
	// allocations tables (BR-GC-11 to BR-GC-17). A rejected group writes
	// nothing; its segments are reported, not inserted.
	for i := range batch.Segments {
		if len(batch.Segments[i].GetURLs) == 0 && batch.ControlledStorageID == "" {
			return InsertResult{}, fmt.Errorf(
				"metastore: InsertSegments: object %q: %w",
				batch.Segments[i].ObjectID, ErrControlledStorageNotConfigured,
			)
		}
	}
	rejections, err := registerObjects(ctx, tx, batch)
	if err != nil {
		return InsertResult{}, err
	}
	var result InsertResult
	acceptedIdx := make([]int, 0, len(batch.Segments))
	for i := range batch.Segments {
		if code, rejected := rejections[batch.Segments[i].ObjectID]; rejected {
			result.RejectedIndices = append(result.RejectedIndices, i)
			result.RejectReasons = append(result.RejectReasons, RejectReason{
				Code:   code,
				Detail: rejectDetail(code, batch.Segments[i].ObjectID),
			})
			continue
		}
		acceptedIdx = append(acceptedIdx, i)
	}
	if len(acceptedIdx) == 0 {
		// Roll back: a commit would change only segments_updated (BR-GC-15).
		return result, nil
	}

	// Step 3.5: BR-META-19 — lazy object_timerange validation. Only when
	// BOTH stored and new are non-NULL must the new range be CONTAINED
	// within the stored range. The "stored" value is the earliest prior
	// non-NULL object_timerange across all flows for the same object_id.
	// On violation, return a wrapped apperror.ErrInvalidObjectTimerange;
	// the deferred tx.Rollback drops the in-flight ref-count upserts.
	for _, i := range acceptedIdx {
		seg := &batch.Segments[i]
		if seg.ObjectTimerange == nil {
			continue
		}
		var storedStr *string
		err := tx.QueryRow(ctx, `
			SELECT object_timerange FROM segments
			WHERE object_id = $1 AND object_timerange IS NOT NULL
			ORDER BY created_at ASC
			LIMIT 1`, seg.ObjectID,
		).Scan(&storedStr)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return InsertResult{}, fmt.Errorf("metastore: InsertSegments: object_timerange lookup (%q): %w", seg.ObjectID, err)
		}
		if storedStr == nil {
			continue
		}
		stored, err := timerange.Parse(*storedStr)
		if err != nil {
			return InsertResult{}, fmt.Errorf("metastore: InsertSegments: parse stored object_timerange %q: %w", *storedStr, err)
		}
		if !rangeContains(stored, *seg.ObjectTimerange) {
			ae := apperror.New(apperror.ErrInvalidObjectTimerange, fmt.Sprintf(
				"segment %d (%s): object_timerange %s extends beyond stored %s for object %q",
				i, seg.TimerangeRaw, seg.ObjectTimerangeRaw, *storedStr, seg.ObjectID,
			))
			return InsertResult{}, fmt.Errorf("metastore: InsertSegments: %w", ae)
		}
	}

	// Step 4: bulk INSERT segments. EXCLUDE constraint catches any race that
	// slipped past the pre-check (concurrent inserts on the same flow).
	// The time columns hold the client strings (BR-META-10). An absent
	// ts_offset is stored as '', so it reads back as absent, while an
	// explicit "0:0" or "-0:0" reads back as sent.
	for _, i := range acceptedIdx {
		seg := &batch.Segments[i]
		getURLsJSON, err := marshalGetURLs(seg.GetURLs)
		if err != nil {
			return InsertResult{}, fmt.Errorf("metastore: InsertSegments: marshal get_urls (%d): %w", i, err)
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO segments (
				flow_id, object_id, timerange, lower_ns, upper_ns,
				ts_offset, object_timerange, last_duration,
				key_frame_count, sample_offset, sample_count, get_urls
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
			batch.FlowID, seg.ObjectID, seg.TimerangeRaw, lowerParam(bounds[i]), upperParam(bounds[i]),
			seg.TSOffsetRaw, nullIfEmpty(seg.ObjectTimerangeRaw), nullIfEmpty(seg.LastDurationRaw),
			seg.KeyFrameCount, seg.SampleOffset, seg.SampleCount, getURLsJSON,
		)
		if err != nil {
			// 23P01 = exclusion_violation (no_segment_overlap on segments).
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23P01" {
				return InsertResult{}, fmt.Errorf(
					"metastore: InsertSegments: segment %d (%s) overlaps an existing row (race): %w",
					i, seg.TimerangeRaw, ErrSegmentOverlap,
				)
			}
			return InsertResult{}, fmt.Errorf("metastore: InsertSegments: insert seg %d: %w", i, err)
		}
	}

	// Step 5: refresh flow segments_updated + timerange (REQ-META-13 / BR-META-12).
	if err := refreshFlowTimerange(ctx, tx, batch.FlowID); err != nil {
		return InsertResult{}, fmt.Errorf("metastore: InsertSegments: refresh flow: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return InsertResult{}, fmt.Errorf("metastore: InsertSegments: commit: %w", err)
	}

	result.AcceptedIndices = acceptedIdx
	return result, nil
}

// objectGroup is the set of segments of one batch that share an
// object_id. It is controlled if one or more of its segments has no
// get_urls (BR-GC-10, BR-GC-11).
type objectGroup struct {
	count      int
	controlled bool
}

// registerObjects applies BR-GC-12 (controlled) or BR-GC-14 (BYOS) to
// each object group, in ascending byte order of object_id so that two
// transactions lock shared rows in one sequence (BR-GC-23). It returns
// the rejection code of each rejected object_id.
func registerObjects(ctx context.Context, tx pgx.Tx, batch InsertBatch) (map[string]apperror.ErrorCode, error) {
	groups := make(map[string]*objectGroup, len(batch.Segments))
	ids := make([]string, 0, len(batch.Segments))
	for i := range batch.Segments {
		seg := &batch.Segments[i]
		g, ok := groups[seg.ObjectID]
		if !ok {
			g = &objectGroup{}
			groups[seg.ObjectID] = g
			ids = append(ids, seg.ObjectID)
		}
		g.count++
		if len(seg.GetURLs) == 0 {
			g.controlled = true
		}
	}
	slices.Sort(ids)

	rejections := make(map[string]apperror.ErrorCode)
	for _, id := range ids {
		g := groups[id]
		var code apperror.ErrorCode
		var err error
		if g.controlled {
			code, err = registerControlledObject(ctx, tx, batch.FlowID, id, g.count)
		} else {
			code, err = registerBYOSObject(ctx, tx, id, g.count)
		}
		if err != nil {
			return nil, fmt.Errorf("metastore: InsertSegments: object %q: %w", id, err)
		}
		if code != "" {
			rejections[id] = code
		}
	}
	return rejections, nil
}

// registerControlledObject is the decision table of BR-GC-12 with the
// locking of BR-GC-13. An empty code means the n segments are accepted.
func registerControlledObject(ctx context.Context, tx pgx.Tx, flowID uuid.UUID, id string, n int) (apperror.ErrorCode, error) {
	code, found, err := addObjectRefs(ctx, tx, id, n)
	if err != nil || found {
		return code, err
	}

	var (
		allocFlowID uuid.UUID
		storageID   string
		deleting    bool
	)
	err = tx.QueryRow(ctx,
		`SELECT flow_id, storage_id, deleting FROM allocations WHERE id = $1 FOR UPDATE`, id,
	).Scan(&allocFlowID, &storageID, &deleting)
	if errors.Is(err, pgx.ErrNoRows) {
		// A concurrent registration can have used the allocation and
		// committed its object row.
		code, found, err = addObjectRefs(ctx, tx, id, n)
		if err != nil || found {
			return code, err
		}
		return apperror.ErrObjectNotAllocated, nil
	}
	if err != nil {
		return "", fmt.Errorf("lock allocation: %w", err)
	}
	switch {
	case deleting:
		return apperror.ErrObjectDeleting, nil
	case allocFlowID != flowID:
		return apperror.ErrObjectWrongFlow, nil
	}

	if _, err := tx.Exec(ctx, `DELETE FROM allocations WHERE id = $1`, id); err != nil {
		return "", fmt.Errorf("delete allocation: %w", err)
	}
	var inserted string
	err = tx.QueryRow(ctx, `
		INSERT INTO objects (id, ref_count, storage_id, deleting)
		VALUES ($1, $2, $3, false)
		ON CONFLICT (id) DO NOTHING
		RETURNING id`,
		id, n, storageID,
	).Scan(&inserted)
	if err == nil {
		return "", nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("insert object: %w", err)
	}
	code, found, err = addObjectRefs(ctx, tx, id, n)
	if err != nil {
		return "", err
	}
	if !found {
		return "", errors.New("object row conflicted on insert but cannot be read")
	}
	return code, nil
}

// addObjectRefs is rows 1 and 2 of BR-GC-12: lock the object row, and
// add n to ref_count unless the GC has claimed it. found is false if
// there is no object row.
func addObjectRefs(ctx context.Context, tx pgx.Tx, id string, n int) (code apperror.ErrorCode, found bool, err error) {
	var deleting bool
	err = tx.QueryRow(ctx, `SELECT deleting FROM objects WHERE id = $1 FOR UPDATE`, id).Scan(&deleting)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("lock object: %w", err)
	}
	if deleting {
		return apperror.ErrObjectDeleting, true, nil
	}
	if _, err := tx.Exec(ctx, `UPDATE objects SET ref_count = ref_count + $2 WHERE id = $1`, id, n); err != nil {
		return "", true, fmt.Errorf("add object refs: %w", err)
	}
	return "", true, nil
}

// registerBYOSObject is BR-GC-14. A BYOS group needs no allocation, but
// an allocation that sweep 2 has claimed blocks it, because sweep 2 is
// about to delete the bucket key of that ID. The allocation row stays.
func registerBYOSObject(ctx context.Context, tx pgx.Tx, id string, n int) (apperror.ErrorCode, error) {
	var allocDeleting bool
	err := tx.QueryRow(ctx,
		`SELECT deleting FROM allocations WHERE id = $1 FOR SHARE`, id,
	).Scan(&allocDeleting)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("lock allocation: %w", err)
	}
	if allocDeleting {
		return apperror.ErrObjectDeleting, nil
	}
	var upserted string
	err = tx.QueryRow(ctx, `
		INSERT INTO objects (id, ref_count, storage_id, deleting)
		VALUES ($1, $2, NULL, false)
		ON CONFLICT (id) DO UPDATE
			SET ref_count = objects.ref_count + EXCLUDED.ref_count
			WHERE objects.deleting = false
		RETURNING id`,
		id, n,
	).Scan(&upserted)
	if errors.Is(err, pgx.ErrNoRows) {
		return apperror.ErrObjectDeleting, nil
	}
	if err != nil {
		return "", fmt.Errorf("upsert object: %w", err)
	}
	return "", nil
}

// rejectDetail is the client-facing detail of a per-segment rejection
// (BR-GC-71 to BR-GC-73). It names the object_id and never another flow.
func rejectDetail(code apperror.ErrorCode, objectID string) string {
	switch code {
	case apperror.ErrObjectNotAllocated:
		return fmt.Sprintf("object_id %q was not allocated by POST /flows/{flowId}/storage, or its allocation expired and was deleted", objectID)
	case apperror.ErrObjectWrongFlow:
		return fmt.Sprintf("object_id %q is allocated to a different flow", objectID)
	case apperror.ErrObjectDeleting:
		return fmt.Sprintf("object_id %q is being deleted; use a new object_id", objectID)
	default:
		return fmt.Sprintf("object_id %q was rejected", objectID)
	}
}

// ListSegments returns one page matching the query. Returns wrapped
// ErrFlowNotFound for an unknown flow (deliberate divergence from REQ-BEH-16).
func (s *PostgresStore) ListSegments(ctx context.Context, q ListQuery) (domain.SegmentPage, error) {
	guard, err := s.GetFlowForSegmentRead(ctx, q.FlowID)
	if err != nil {
		return domain.SegmentPage{}, err
	}
	if !guard.Exists {
		return domain.SegmentPage{}, fmt.Errorf("metastore: ListSegments: flow %s: %w", q.FlowID, ErrFlowNotFound)
	}

	limit := q.Limit
	if limit <= 0 || limit > hardLimit {
		limit = hardLimit
	}

	ord := "ASC"
	cmp := ">"
	if q.ReverseOrder {
		ord = "DESC"
		cmp = "<"
	}

	args := []any{q.FlowID}
	where := []string{"s.flow_id = $1"}

	if q.Timerange != nil {
		lo, hi, empty, err := queryBounds(*q.Timerange)
		if err != nil {
			return domain.SegmentPage{}, fmt.Errorf("metastore: ListSegments: timerange: %w", err)
		}
		if empty {
			return domain.SegmentPage{EffectiveLimit: limit}, nil
		}
		args = append(args, lo, hi)
		where = append(where, overlapCond("s.", len(args)-1, len(args)))
	}
	if q.ObjectID != nil {
		args = append(args, *q.ObjectID)
		where = append(where, fmt.Sprintf("s.object_id = $%d", len(args)))
	}

	if q.Page != "" {
		lowerNs, segID, err := decodeSegCursor(q.Page)
		if err != nil {
			return domain.SegmentPage{}, fmt.Errorf("metastore: ListSegments: %w", ErrInvalidCursor)
		}
		args = append(args, lowerNs, segID)
		where = append(where, fmt.Sprintf("(s.lower_ns, s.id) %s ($%d, $%d)", cmp, len(args)-1, len(args)))
	}

	args = append(args, limit+1)
	// Read shape mirrors segments table. Handler classifies controlled vs
	// BYOS from `len(seg.GetURLs) == 0` — no JOIN onto objects needed
	// (BR-META-07; D-24 still governs storage_id authorship).
	q1 := fmt.Sprintf(`
		SELECT s.id, s.flow_id, s.object_id, s.timerange, s.lower_ns, s.upper_ns,
		       s.ts_offset, s.object_timerange, s.last_duration,
		       s.key_frame_count, s.sample_offset, s.sample_count, s.get_urls,
		       s.created_at
		FROM segments s
		WHERE %s
		ORDER BY s.lower_ns %s, s.id %s
		LIMIT $%d`,
		strings.Join(where, " AND "), ord, ord, len(args),
	)

	rows, err := s.db.Query(ctx, q1, args...)
	if err != nil {
		return domain.SegmentPage{}, fmt.Errorf("metastore: ListSegments: query: %w", err)
	}
	defer rows.Close()

	var items []segmentRow
	for rows.Next() {
		var it segmentRow
		var trStr, tsOff string
		var objTRStr, lastDurStr *string
		var kfc *int32
		var getURLs []byte
		if err := rows.Scan(
			&it.segID, &it.seg.FlowID, &it.seg.ObjectID, &trStr, &it.lowerNs, &it.upperNs,
			&tsOff, &objTRStr, &lastDurStr,
			&kfc, &it.seg.SampleOffset, &it.seg.SampleCount, &getURLs,
			&it.seg.CreatedAt,
		); err != nil {
			return domain.SegmentPage{}, fmt.Errorf("metastore: ListSegments: scan: %w", err)
		}
		tr, err := timerange.Parse(trStr)
		if err != nil {
			return domain.SegmentPage{}, fmt.Errorf("metastore: ListSegments: parse timerange %q: %w", trStr, err)
		}
		it.seg.Timerange = tr
		it.seg.TimerangeRaw = trStr
		if tsOff != "" {
			ts, err := timerange.ParseTimestamp(tsOff)
			if err != nil {
				return domain.SegmentPage{}, fmt.Errorf("metastore: ListSegments: parse ts_offset %q: %w", tsOff, err)
			}
			it.seg.TSOffset = &ts
			it.seg.TSOffsetRaw = tsOff
		}
		if objTRStr != nil {
			otr, err := timerange.Parse(*objTRStr)
			if err != nil {
				return domain.SegmentPage{}, fmt.Errorf("metastore: ListSegments: parse object_timerange %q: %w", *objTRStr, err)
			}
			it.seg.ObjectTimerange = &otr
			it.seg.ObjectTimerangeRaw = *objTRStr
		}
		if lastDurStr != nil {
			ld, err := timerange.ParseTimestamp(*lastDurStr)
			if err != nil {
				return domain.SegmentPage{}, fmt.Errorf("metastore: ListSegments: parse last_duration %q: %w", *lastDurStr, err)
			}
			it.seg.LastDuration = &ld
			it.seg.LastDurationRaw = *lastDurStr
		}
		if kfc != nil {
			v := int64(*kfc)
			it.seg.KeyFrameCount = &v
		}
		if len(getURLs) > 0 {
			parsed, err := unmarshalGetURLs(getURLs)
			if err != nil {
				return domain.SegmentPage{}, fmt.Errorf("metastore: ListSegments: parse get_urls: %w", err)
			}
			it.seg.GetURLs = parsed
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return domain.SegmentPage{}, fmt.Errorf("metastore: ListSegments: rows: %w", err)
	}

	page := domain.SegmentPage{EffectiveLimit: limit}
	if len(items) > limit {
		items = items[:limit]
		last := items[limit-1]
		page.NextCursor = encodeSegCursor(last.lowerNs, last.segID)
	}
	page.Items = make([]domain.Segment, len(items))
	for i := range items {
		page.Items[i] = items[i].seg
	}
	page.Count = len(page.Items)
	if len(items) > 0 {
		page.Timerange = pageSpan(items)
	}
	return page, nil
}

// DeleteSegmentsByTimerange deletes matching segments atomically and
// decrements ref-counts. GC worker reaps released objects (D-25/D-29).
func (s *PostgresStore) DeleteSegmentsByTimerange(ctx context.Context, q DeleteQuery) (DeleteResult, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return DeleteResult{}, fmt.Errorf("metastore: DeleteSegmentsByTimerange: begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op

	guard, err := s.GetFlowForSegmentWrite(ctx, tx, q.FlowID)
	if err != nil {
		return DeleteResult{}, err
	}
	if !guard.Exists {
		return DeleteResult{}, fmt.Errorf("metastore: DeleteSegmentsByTimerange: flow %s: %w", q.FlowID, ErrFlowNotFound)
	}
	if guard.ReadOnly {
		return DeleteResult{}, fmt.Errorf("metastore: DeleteSegmentsByTimerange: flow %s: %w", q.FlowID, ErrFlowReadOnly)
	}

	lo, hi, empty, err := queryBounds(q.Timerange)
	if err != nil {
		return DeleteResult{}, fmt.Errorf("metastore: DeleteSegmentsByTimerange: timerange: %w", err)
	}
	if empty {
		return DeleteResult{}, nil
	}

	args := []any{q.FlowID, lo, hi}
	objFilter := ""
	if q.ObjectID != nil {
		args = append(args, *q.ObjectID)
		objFilter = fmt.Sprintf(" AND object_id = $%d", len(args))
	}

	delSQL := fmt.Sprintf(`
		WITH deleted AS (
			DELETE FROM segments
			WHERE flow_id = $1
			  AND ($2::bigint IS NULL OR lower_ns >= $2)
			  AND ($3::bigint IS NULL OR upper_ns <= $3)%s
			RETURNING object_id
		)
		SELECT object_id, COUNT(*) FROM deleted GROUP BY object_id ORDER BY object_id COLLATE "C"`, objFilter)

	objectIDs, counts, err := deleteSegmentsCountedByObject(ctx, tx, delSQL, args...)
	if err != nil {
		return DeleteResult{}, fmt.Errorf("metastore: DeleteSegmentsByTimerange: delete: %w", err)
	}
	var totalDeleted int64
	for _, n := range counts {
		totalDeleted += n
	}

	if err := decreaseRefCounts(ctx, tx, objectIDs, counts); err != nil {
		return DeleteResult{}, fmt.Errorf("metastore: DeleteSegmentsByTimerange: %w", err)
	}

	if err := refreshFlowTimerange(ctx, tx, q.FlowID); err != nil {
		return DeleteResult{}, fmt.Errorf("metastore: DeleteSegmentsByTimerange: refresh flow: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return DeleteResult{}, fmt.Errorf("metastore: DeleteSegmentsByTimerange: commit: %w", err)
	}
	return DeleteResult{DeletedCount: totalDeleted}, nil
}

// refreshFlowTimerange sets segments_updated to now() and the flow's
// timerange to the span of its remaining segments, or NULL when it has
// none (BR-META-12). It runs inside the caller's tx.
func refreshFlowTimerange(ctx context.Context, tx pgx.Tx, flowID uuid.UUID) error {
	span, err := flowSpan(ctx, tx, flowID)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx,
		`UPDATE flows SET segments_updated = now(), timerange = $2 WHERE id = $1`,
		flowID, span)
	return err
}

// existingOverlapSQL finds a stored segment on the flow that overlaps the
// half-open bounds $2 and $3. The range expression repeats the one in the
// no_segment_overlap index, COALESCE included, so that Postgres can use
// the index for the range. upper_ns is NOT NULL, so the COALESCE has no
// effect on the result.
const existingOverlapSQL = `
	SELECT timerange FROM segments
	WHERE flow_id = $1
	  AND int8range(lower_ns, COALESCE(upper_ns, 9223372036854775807))
	      && int8range($2::bigint, $3::bigint)
	LIMIT 1`

type rowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// flowSpan joins the start of the flow's first segment to the end of its
// last segment, parsed from the stored client strings and rendered in
// canonical form (BR-META-12, BR-TR-12). It returns nil for a flow with
// no segments. Stored segments are non-empty and do not overlap, so the
// segment with the highest lower_ns also has the highest upper_ns, and
// the (flow_id, lower_ns) index serves both lookups.
func flowSpan(ctx context.Context, q rowQuerier, flowID uuid.UUID) (*string, error) {
	var firstRaw, lastRaw *string
	if err := q.QueryRow(ctx, `
		SELECT
			(SELECT timerange FROM segments WHERE flow_id = $1 ORDER BY lower_ns ASC LIMIT 1),
			(SELECT timerange FROM segments WHERE flow_id = $1 ORDER BY lower_ns DESC LIMIT 1)`,
		flowID,
	).Scan(&firstRaw, &lastRaw); err != nil {
		return nil, fmt.Errorf("flow span: %w", err)
	}
	if firstRaw == nil || lastRaw == nil {
		return nil, nil //nolint:nilnil // nil means the flow has no segments
	}
	first, err := timerange.Parse(*firstRaw)
	if err != nil {
		return nil, fmt.Errorf("flow span: parse %q: %w", *firstRaw, err)
	}
	last, err := timerange.Parse(*lastRaw)
	if err != nil {
		return nil, fmt.Errorf("flow span: parse %q: %w", *lastRaw, err)
	}
	span := joinSpan(first, last).String()
	return &span, nil
}

// joinSpan returns the range from the start of first to the end of last.
func joinSpan(first, last timerange.TimeRange) timerange.TimeRange {
	return timerange.TimeRange{
		Start:     first.Start,
		StartType: first.StartType,
		End:       last.End,
		EndType:   last.EndType,
	}
}

type segmentRow struct {
	seg     domain.Segment
	lowerNs int64
	upperNs int64
	segID   int64
}

// pageSpan is the X-Paging-Timerange value: from the start of the
// earliest item to the end of the latest, for either sort order
// (ADR-0039 rule 9).
func pageSpan(items []segmentRow) timerange.TimeRange {
	first, last := 0, 0
	for i := range items {
		if items[i].lowerNs < items[first].lowerNs {
			first = i
		}
		if items[i].upperNs > items[last].upperNs {
			last = i
		}
	}
	return joinSpan(items[first].seg.Timerange, items[last].seg.Timerange)
}

// segmentBounds converts every segment timerange with timerange.NsBounds,
// the only conversion to nanoseconds (BR-META-10). An empty or
// out-of-range timerange fails the whole batch before anything is written.
func segmentBounds(segs []domain.Segment) ([]timerange.NsBounds, error) {
	out := make([]timerange.NsBounds, len(segs))
	for i := range segs {
		if segs[i].TimerangeRaw == "" {
			return nil, fmt.Errorf("metastore: InsertSegments: segment %d has no client timerange string: %w", i, ErrBadInput)
		}
		b, err := segs[i].Timerange.NsBounds()
		if err != nil {
			return nil, fmt.Errorf("metastore: InsertSegments: segment %d (%s): %w", i, segs[i].TimerangeRaw, err)
		}
		out[i] = b
	}
	return out, nil
}

// boundsOverlap reports whether two half-open intervals share a
// nanosecond. An unbounded side is open (BR-TR-07).
func boundsOverlap(a, b timerange.NsBounds) bool {
	return (a.LowerUnbounded || b.UpperUnbounded || a.Lower < b.Upper) &&
		(b.LowerUnbounded || a.UpperUnbounded || b.Lower < a.Upper)
}

// queryBounds converts a query timerange into SQL parameters, nil for an
// unbounded side. empty is true when the range matches nothing; it is
// checked before NsBounds, so ErrEmptyRange never reaches a caller
// (BR-META-21).
func queryBounds(tr timerange.TimeRange) (lo, hi *int64, empty bool, err error) {
	if tr.IsEmpty() {
		return nil, nil, true, nil
	}
	b, err := tr.NsBounds()
	if err != nil {
		return nil, nil, false, err
	}
	return lowerParam(b), upperParam(b), false, nil
}

// overlapCond is the BR-META-21 overlap predicate on a segments row whose
// columns carry prefix, against the query bounds in args loArg and hiArg.
func overlapCond(prefix string, loArg, hiArg int) string {
	return fmt.Sprintf("($%[3]d::bigint IS NULL OR %[1]slower_ns < $%[3]d) AND ($%[2]d::bigint IS NULL OR %[1]supper_ns > $%[2]d)",
		prefix, loArg, hiArg)
}

func lowerParam(b timerange.NsBounds) *int64 {
	if b.LowerUnbounded {
		return nil
	}
	return &b.Lower
}

func upperParam(b timerange.NsBounds) *int64 {
	if b.UpperUnbounded {
		return nil
	}
	return &b.Upper
}

func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// marshalGetURLs returns the JSONB form for storage. Empty slice → NULL.
func marshalGetURLs(g []domain.GetURL) (any, error) {
	if len(g) == 0 {
		return nil, nil //nolint:nilnil // NULL JSONB requires a typed nil any
	}
	type wire struct {
		URL        string `json:"url"`
		Label      string `json:"label,omitempty"`
		Controlled bool   `json:"controlled,omitempty"`
	}
	out := make([]wire, len(g))
	for i, u := range g {
		out[i] = wire{URL: u.URL, Label: u.Label, Controlled: u.Controlled}
	}
	return json.Marshal(out)
}

// unmarshalGetURLs parses a JSONB get_urls value.
func unmarshalGetURLs(b []byte) ([]domain.GetURL, error) {
	type wire struct {
		URL        string `json:"url"`
		Label      string `json:"label,omitempty"`
		Controlled bool   `json:"controlled,omitempty"`
	}
	var ws []wire
	if err := json.Unmarshal(b, &ws); err != nil {
		return nil, err
	}
	out := make([]domain.GetURL, len(ws))
	for i, w := range ws {
		out[i] = domain.GetURL{URL: w.URL, Label: w.Label, Controlled: w.Controlled}
	}
	return out, nil
}

var _ Store = (*PostgresStore)(nil)

// IsObjectRegistered reports whether objectID is referenced by at least one segment.
func (s *PostgresStore) IsObjectRegistered(ctx context.Context, objectID string) (bool, error) {
	var exists bool
	err := s.db.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM segments WHERE object_id = $1)`, objectID,
	).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("metastore: IsObjectRegistered: %w", err)
	}
	return exists, nil
}

// rangeContains reports whether `outer` fully contains `inner` —
// every point of `inner` is also a point of `outer`. Used by BR-META-19
// (object_timerange validation): the new segment's object_timerange
// must be a sub-range of the stored value. Eternity bounds and
// inclusive/exclusive endpoint semantics follow the timerange package.
func rangeContains(outer, inner timerange.TimeRange) bool {
	if outer.IsEternity() {
		return true
	}
	if inner.IsEmpty() {
		return true
	}
	if inner.IsEternity() {
		return false
	}
	// Lower bound: outer.Start ≤ inner.Start (with bound-type rules).
	if outer.StartType != timerange.Unbounded {
		if inner.StartType == timerange.Unbounded {
			return false
		}
		if innerStartLess(outer, inner) {
			return false
		}
	}
	// Upper bound: inner.End ≤ outer.End.
	if outer.EndType != timerange.Unbounded {
		if inner.EndType == timerange.Unbounded {
			return false
		}
		if innerEndGreater(outer, inner) {
			return false
		}
	}
	return true
}

// innerStartLess reports whether inner.Start sits below outer.Start
// (i.e., a point in inner is outside outer's lower bound).
func innerStartLess(outer, inner timerange.TimeRange) bool {
	cmp := compareTS(*inner.Start, *outer.Start)
	if cmp < 0 {
		return true
	}
	if cmp > 0 {
		return false
	}
	// Equal endpoints: inner exclusive on a point that outer also
	// excludes is fine; inner inclusive on a point outer excludes is
	// out of range.
	if outer.StartType == timerange.Exclusive && inner.StartType == timerange.Inclusive {
		return true
	}
	return false
}

// innerEndGreater reports whether inner.End sits above outer.End.
func innerEndGreater(outer, inner timerange.TimeRange) bool {
	cmp := compareTS(*inner.End, *outer.End)
	if cmp > 0 {
		return true
	}
	if cmp < 0 {
		return false
	}
	if outer.EndType == timerange.Exclusive && inner.EndType == timerange.Inclusive {
		return true
	}
	return false
}

// compareTS returns -1/0/1 for a vs b on (Seconds, Nanoseconds).
func compareTS(a, b timerange.Timestamp) int {
	switch {
	case a.Seconds < b.Seconds:
		return -1
	case a.Seconds > b.Seconds:
		return 1
	case a.Nanoseconds < b.Nanoseconds:
		return -1
	case a.Nanoseconds > b.Nanoseconds:
		return 1
	}
	return 0
}
