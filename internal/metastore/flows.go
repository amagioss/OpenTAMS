package metastore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/amagioss/opentams/internal/apperror"
	"github.com/amagioss/opentams/internal/timerange"
)

// FlowStore reads and writes Flow records and their sub-resources.
type FlowStore interface {
	UpsertFlow(ctx context.Context, f *Flow) (created bool, err error)
	GetFlow(ctx context.Context, id uuid.UUID) (*Flow, error)
	GetFlowTimerange(ctx context.Context, id uuid.UUID) (*string, error)
	ListFlows(ctx context.Context, p ListFlowsParams) (*FlowPage, error)
	DeleteFlow(ctx context.Context, id uuid.UUID) error
	PutFlowTag(ctx context.Context, id uuid.UUID, name string, value json.RawMessage) error
	DeleteFlowTag(ctx context.Context, id uuid.UUID, name string) error
	PutFlowLabel(ctx context.Context, id uuid.UUID, label string) error
	DeleteFlowLabel(ctx context.Context, id uuid.UUID) error
	PutFlowDescription(ctx context.Context, id uuid.UUID, desc string) error
	DeleteFlowDescription(ctx context.Context, id uuid.UUID) error
	PutFlowReadOnly(ctx context.Context, id uuid.UUID, readOnly bool) error
	PutFlowCollection(ctx context.Context, id uuid.UUID, items []CollectionItem) error
	DeleteFlowCollection(ctx context.Context, id uuid.UUID) error
	PutFlowAvgBitRate(ctx context.Context, id uuid.UUID, rate int64) error
	DeleteFlowAvgBitRate(ctx context.Context, id uuid.UUID) error
	PutFlowMaxBitRate(ctx context.Context, id uuid.UUID, rate int64) error
	DeleteFlowMaxBitRate(ctx context.Context, id uuid.UUID) error
}

// Domain types

// Source is the metastore representation of a TAMS source — a logical
// origin of media essence (e.g. a camera, a microphone, a generated
// stream). Sources are created implicitly by UpsertFlow if they do not
// already exist; their format must match the flow's format.
type Source struct {
	ID          uuid.UUID
	Format      string
	Label       *string
	Description *string
	Tags        map[string]json.RawMessage
	Created     time.Time
	Updated     time.Time
}

// Flow is the metastore representation of a TAMS flow — an addressable
// time-keyed sequence of segments belonging to a source. Optional fields
// are pointer-typed so the metastore can distinguish "absent" from
// "explicit zero value" when round-tripping to the wire.
type Flow struct {
	ID                uuid.UUID
	SourceID          uuid.UUID
	Format            string
	Codec             *string
	Container         *string
	Label             *string
	Description       *string
	Tags              map[string]json.RawMessage
	EssenceParameters json.RawMessage
	ContainerMapping  json.RawMessage
	FlowCollection    []CollectionItem
	AvgBitRate        *int64
	MaxBitRate        *int64
	SegmentDuration   *string
	Generation        *int64
	MetadataVersion   *int64
	ReadOnly          bool
	Created           time.Time
	MetadataUpdated   time.Time
	SegmentsUpdated   *time.Time
	Timerange         *string
}

// CollectionItem is a single entry in a flow's flow_collection — a
// reference to a child flow (by ID) optionally tagged with a role and
// a container mapping.
type CollectionItem struct {
	ID               uuid.UUID
	Role             *string
	ContainerMapping json.RawMessage
}

// Pagination params and page results

// ListSourcesParams collects the optional filters and pagination bounds
// accepted by ListSources. Zero values are treated as "no filter".
type ListSourcesParams struct {
	Limit     int
	PageFrom  *string
	Label     *string
	Format    *string
	TagExists map[string]struct{}
	TagValues map[string]string
}

// ListFlowsParams collects the optional filters and pagination bounds
// accepted by ListFlows, including the timerange-overlap predicate.
type ListFlowsParams struct {
	SourceID    *uuid.UUID
	Format      *string
	Label       *string
	Codec       *string
	FrameWidth  *int
	FrameHeight *int
	Timerange   *timerange.TimeRange
	TagExists   map[string]struct{}
	TagValues   map[string]string
	Limit       int
	PageFrom    *string
}

// SourcePage is one page of a ListSources result; NextCursor is nil
// when no further pages exist.
type SourcePage struct {
	Items      []*Source
	NextCursor *string
}

// FlowPage is one page of a ListFlows result; NextCursor is nil when
// no further pages exist.
type FlowPage struct {
	Items      []*Flow
	NextCursor *string
}

// UpsertFlow creates or updates a flow.
// If the source does not exist it is created implicitly with the flow's format.
// Immutable fields (source_id, format) cannot change after creation.
// codec cannot change if segments exist.
func (s *PostgresStore) UpsertFlow(ctx context.Context, f *Flow) (bool, error) {
	tx, err := s.begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // tx is rolled back implicitly by Commit; an explicit Rollback after Commit is a no-op whose error is uninteresting.

	// Check whether the flow already exists — immutable field violations take priority.
	var existing struct {
		sourceID uuid.UUID
		format   string
		codec    *string
	}
	flowErr := tx.QueryRow(ctx,
		`SELECT source_id, format, codec FROM flows WHERE id = $1`, f.ID,
	).Scan(&existing.sourceID, &existing.format, &existing.codec)

	created := false
	if errors.Is(flowErr, pgx.ErrNoRows) {
		created = true

		// New flow: ensure source exists (create implicitly if not).
		var srcFormat string
		srcErr := tx.QueryRow(ctx, `SELECT format FROM sources WHERE id = $1`, f.SourceID).Scan(&srcFormat)
		if errors.Is(srcErr, pgx.ErrNoRows) {
			if _, err2 := tx.Exec(ctx,
				`INSERT INTO sources (id, format) VALUES ($1, $2)`, f.SourceID, f.Format); err2 != nil {
				return false, fmt.Errorf("metastore: UpsertFlow: insert implicit source: %w", err2)
			}
		} else if srcErr != nil {
			return false, fmt.Errorf("metastore: UpsertFlow: check source: %w", srcErr)
		} else if srcFormat != f.Format {
			return false, apperror.New(apperror.ErrFormatMismatch,
				fmt.Sprintf("source format is %q, flow format %q does not match", srcFormat, f.Format))
		}

		const ins = `
			INSERT INTO flows (
				id, source_id, format, codec, container, label, description,
				essence_parameters, container_mapping, avg_bit_rate, max_bit_rate,
				segment_duration, generation, metadata_version, read_only, timerange
			) VALUES (
				$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16
			)`
		if _, err2 := tx.Exec(ctx, ins,
			f.ID, f.SourceID, f.Format, f.Codec, f.Container, f.Label, f.Description,
			nilJSON(f.EssenceParameters), nilJSON(f.ContainerMapping),
			f.AvgBitRate, f.MaxBitRate, f.SegmentDuration,
			f.Generation, f.MetadataVersion, f.ReadOnly, f.Timerange,
		); err2 != nil {
			return false, fmt.Errorf("metastore: UpsertFlow: insert flow: %w", err2)
		}
	} else if flowErr != nil {
		return false, fmt.Errorf("metastore: UpsertFlow: check flow: %w", flowErr)
	} else {
		// Update path — validate immutable fields first.
		if existing.sourceID != f.SourceID {
			return false, apperror.New(apperror.ErrImmutableField, "source_id cannot be changed")
		}
		if existing.format != f.Format {
			return false, apperror.New(apperror.ErrImmutableField, "format cannot be changed")
		}
		// codec immutable if segments exist
		if !codecEqual(existing.codec, f.Codec) {
			var segCount int
			if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM segments WHERE flow_id = $1`, f.ID).Scan(&segCount); err != nil {
				return false, fmt.Errorf("metastore: UpsertFlow: count segments: %w", err)
			}
			if segCount > 0 {
				return false, apperror.New(apperror.ErrImmutableField, "codec cannot be changed when segments exist")
			}
		}
		const upd = `
			UPDATE flows SET
				codec=$2, container=$3, label=$4, description=$5,
				essence_parameters=$6, container_mapping=$7,
				avg_bit_rate=$8, max_bit_rate=$9,
				segment_duration=$10, generation=$11, metadata_version=$12,
				read_only=$13, timerange=$14,
				metadata_updated=now()
			WHERE id=$1`
		if _, err2 := tx.Exec(ctx, upd,
			f.ID, f.Codec, f.Container, f.Label, f.Description,
			nilJSON(f.EssenceParameters), nilJSON(f.ContainerMapping),
			f.AvgBitRate, f.MaxBitRate, f.SegmentDuration,
			f.Generation, f.MetadataVersion, f.ReadOnly, f.Timerange,
		); err2 != nil {
			return false, fmt.Errorf("metastore: UpsertFlow: update flow: %w", err2)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("metastore: UpsertFlow: commit: %w", err)
	}
	return created, nil
}

// GetFlow returns the flow with the given ID, including its
// flow_collection items in a single round trip. Returns ErrNoRows when
// the ID is unknown.
func (s *PostgresStore) GetFlow(ctx context.Context, id uuid.UUID) (*Flow, error) {
	const q = `
		SELECT f.id, f.source_id, f.format, f.codec, f.container, f.label, f.description,
		       f.essence_parameters, f.container_mapping,
		       f.avg_bit_rate, f.max_bit_rate, f.segment_duration,
		       f.generation, f.metadata_version, f.read_only,
		       f.created, f.metadata_updated, f.segments_updated, f.timerange,
		       COALESCE(
		           jsonb_object_agg(t.name, t.value) FILTER (WHERE t.name IS NOT NULL),
		           '{}'::jsonb
		       ) AS tags
		FROM flows f
		LEFT JOIN flow_tags t ON t.flow_id = f.id
		WHERE f.id = $1
		GROUP BY f.id`

	rows, err := s.db.Query(ctx, q, id)
	if err != nil {
		return nil, fmt.Errorf("metastore: GetFlow: %w", err)
	}
	fl, err := pgx.CollectOneRow(rows, scanFlowBase)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apperror.New(apperror.ErrNotFound, "flow not found")
	}
	if err != nil {
		return nil, fmt.Errorf("metastore: GetFlow: %w", err)
	}

	// load collection items ordered by sort_order
	collRows, err := s.db.Query(ctx,
		`SELECT item_id, role, container_mapping FROM flow_collection WHERE flow_id = $1 ORDER BY sort_order, item_id`,
		id)
	if err != nil {
		return nil, fmt.Errorf("metastore: GetFlow: collection: %w", err)
	}
	defer collRows.Close()
	for collRows.Next() {
		var ci CollectionItem
		var cm []byte
		if err := collRows.Scan(&ci.ID, &ci.Role, &cm); err != nil {
			return nil, fmt.Errorf("metastore: GetFlow: scan collection: %w", err)
		}
		if len(cm) > 0 {
			ci.ContainerMapping = json.RawMessage(cm)
		}
		fl.FlowCollection = append(fl.FlowCollection, ci)
	}
	if err := collRows.Err(); err != nil {
		return nil, fmt.Errorf("metastore: GetFlow: collection rows: %w", err)
	}
	return fl, nil
}

// GetFlowTimerange returns the bounding timerange of all segments for a flow,
// in TAMS bracket notation. Returns nil if the flow has no segments.
// The result is half-open [min_lower, max_upper) using second:nanosecond format.
func (s *PostgresStore) GetFlowTimerange(ctx context.Context, id uuid.UUID) (*string, error) {
	var minLowerNs *int64
	var maxUpperNs *int64
	var hasOpenEnd bool
	err := s.db.QueryRow(ctx,
		`SELECT MIN(lower_ns), MAX(upper_ns), COALESCE(bool_or(upper_ns IS NULL), false) FROM segments WHERE flow_id = $1`, id,
	).Scan(&minLowerNs, &maxUpperNs, &hasOpenEnd)
	if err != nil {
		return nil, fmt.Errorf("metastore: GetFlowTimerange: %w", err)
	}
	if minLowerNs == nil {
		return nil, nil
	}
	lowerSec := *minLowerNs / 1_000_000_000
	lowerNsFrac := *minLowerNs % 1_000_000_000
	var result string
	if hasOpenEnd || maxUpperNs == nil {
		result = fmt.Sprintf("[%d:%d_)", lowerSec, lowerNsFrac)
	} else {
		upperSec := *maxUpperNs / 1_000_000_000
		upperNsFrac := *maxUpperNs % 1_000_000_000
		result = fmt.Sprintf("[%d:%d_%d:%d)", lowerSec, lowerNsFrac, upperSec, upperNsFrac)
	}
	return &result, nil
}

// ListFlows returns one page of flows matching the optional filters in
// p. Pagination is cursor-based on (created, id); NextCursor on the
// returned FlowPage is non-nil when more pages exist.
func (s *PostgresStore) ListFlows(ctx context.Context, p ListFlowsParams) (*FlowPage, error) {
	limit := p.Limit
	if limit <= 0 {
		limit = 100
	}

	// Build dynamic WHERE clause
	args := []any{}
	conds := []string{}
	argN := 1

	if p.SourceID != nil {
		conds = append(conds, fmt.Sprintf("f.source_id = $%d", argN))
		args = append(args, *p.SourceID)
		argN++
	}
	if p.Format != nil {
		conds = append(conds, fmt.Sprintf("f.format = $%d", argN))
		args = append(args, *p.Format)
		argN++
	}
	if p.Label != nil {
		conds = append(conds, fmt.Sprintf("f.label = $%d", argN))
		args = append(args, *p.Label)
		argN++
	}
	if p.Codec != nil {
		conds = append(conds, fmt.Sprintf("f.codec = $%d", argN))
		args = append(args, *p.Codec)
		argN++
	}
	if p.FrameWidth != nil {
		conds = append(conds, fmt.Sprintf("(f.essence_parameters->>'frame_width')::int = $%d", argN))
		args = append(args, *p.FrameWidth)
		argN++
	}
	if p.FrameHeight != nil {
		conds = append(conds, fmt.Sprintf("(f.essence_parameters->>'frame_height')::int = $%d", argN))
		args = append(args, *p.FrameHeight)
		argN++
	}
	if p.Timerange != nil && !p.Timerange.IsEmpty() {
		tr := p.Timerange
		var segConds []string
		if tr.StartType != timerange.Unbounded && tr.Start != nil {
			lowerNs := tr.Start.Seconds*1_000_000_000 + int64(tr.Start.Nanoseconds)
			segConds = append(segConds, fmt.Sprintf("(upper_ns IS NULL OR upper_ns > $%d)", argN))
			args = append(args, lowerNs)
			argN++
		}
		if tr.EndType != timerange.Unbounded && tr.End != nil {
			endNs := tr.End.Seconds*1_000_000_000 + int64(tr.End.Nanoseconds)
			if tr.EndType == timerange.Inclusive {
				segConds = append(segConds, fmt.Sprintf("lower_ns <= $%d", argN))
			} else {
				segConds = append(segConds, fmt.Sprintf("lower_ns < $%d", argN))
			}
			args = append(args, endNs)
			argN++
		}
		subWhere := "flow_id = f.id"
		if len(segConds) > 0 {
			subWhere += " AND " + strings.Join(segConds, " AND ")
		}
		conds = append(conds, fmt.Sprintf("EXISTS (SELECT 1 FROM segments WHERE %s)", subWhere))
	}
	for tagName := range p.TagExists {
		conds = append(conds, fmt.Sprintf("EXISTS (SELECT 1 FROM flow_tags WHERE flow_id = f.id AND name = $%d)", argN))
		args = append(args, tagName)
		argN++
	}
	for tagName, tagVal := range p.TagValues {
		conds = append(conds, fmt.Sprintf("EXISTS (SELECT 1 FROM flow_tags WHERE flow_id = f.id AND name = $%d AND value = $%d)", argN, argN+1))
		args = append(args, tagName, tagVal)
		argN += 2
	}

	if p.PageFrom != nil {
		cursor, cerr := decodeCursor(*p.PageFrom)
		if cerr != nil {
			return nil, apperror.New(apperror.ErrInvalidJSON, "invalid page_from cursor")
		}
		conds = append(conds, fmt.Sprintf("(f.created, f.id) > ($%d, $%d)", argN, argN+1))
		args = append(args, cursor.ts, cursor.id)
		argN += 2
	}

	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}

	q := fmt.Sprintf(`
		SELECT f.id, f.source_id, f.format, f.codec, f.container, f.label, f.description,
		       f.essence_parameters, f.container_mapping,
		       f.avg_bit_rate, f.max_bit_rate, f.segment_duration,
		       f.generation, f.metadata_version, f.read_only,
		       f.created, f.metadata_updated, f.segments_updated, f.timerange,
		       COALESCE(
		           jsonb_object_agg(t.name, t.value) FILTER (WHERE t.name IS NOT NULL),
		           '{}'::jsonb
		       ) AS tags
		FROM flows f
		LEFT JOIN flow_tags t ON t.flow_id = f.id
		%s
		GROUP BY f.id
		ORDER BY f.created, f.id
		LIMIT $%d`, where, argN)
	args = append(args, limit+1)

	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("metastore: ListFlows: %w", err)
	}
	items, err := pgx.CollectRows(rows, scanFlowBase)
	if err != nil {
		return nil, fmt.Errorf("metastore: ListFlows: %w", err)
	}

	page := &FlowPage{}
	if len(items) > limit {
		items = items[:limit]
		last := items[limit-1]
		c := encodeCursor(last.Created, last.ID)
		page.NextCursor = &c
	}
	page.Items = items
	return page, nil
}

// DeleteFlow removes the flow and all its segments and decrements the
// ref counts of every distinct object the flow's segments pointed at.
//
// Object rows whose ref_count reaches zero are LEFT IN PLACE; the GC
// worker reaps them asynchronously via the `WHERE ref_count = 0 AND
// reaping = false` partial index (D-25, D-29, BR-OBJ-11). Inline S3
// cleanup from the request path is deliberately avoided: the request must
// not block on a remote object-store round trip it cannot roll back.
func (s *PostgresStore) DeleteFlow(ctx context.Context, id uuid.UUID) error {
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // tx is rolled back implicitly by Commit; an explicit Rollback after Commit is a no-op whose error is uninteresting.

	// Decrement ref counts on every distinct object referenced by this
	// flow's segments. CTE keeps the read+update inside the same tx, so
	// concurrent inserters that join after the SELECT will see the row
	// locked by ON CONFLICT in InsertSegments and serialise behind us.
	if _, err := tx.Exec(ctx, `
		WITH refs AS (
			SELECT DISTINCT object_id FROM segments WHERE flow_id = $1
		)
		UPDATE objects
		   SET ref_count = ref_count - 1
		  FROM refs
		 WHERE objects.id = refs.object_id`, id); err != nil {
		return fmt.Errorf("metastore: DeleteFlow: decrement refs: %w", err)
	}

	// Delete the flow (segments cascade via FK ON DELETE CASCADE).
	tag, err := tx.Exec(ctx, `DELETE FROM flows WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("metastore: DeleteFlow: delete: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return apperror.New(apperror.ErrNotFound, "flow not found")
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("metastore: DeleteFlow: commit: %w", err)
	}
	return nil
}

// PutFlowTag upserts a single tag on the flow. The value is stored
// verbatim; tag-name validation is the caller's responsibility.
// Returns ErrReadOnly if the flow is read-only and ErrNotFound when
// the flow ID is unknown.
func (s *PostgresStore) PutFlowTag(ctx context.Context, id uuid.UUID, name string, value json.RawMessage) error {
	if err := s.flowWritable(ctx, id); err != nil {
		return err
	}
	const q = `INSERT INTO flow_tags (flow_id, name, value) VALUES ($1, $2, $3)
	           ON CONFLICT (flow_id, name) DO UPDATE SET value = EXCLUDED.value`
	if _, err := s.db.Exec(ctx, q, id, name, value); err != nil {
		return fmt.Errorf("metastore: PutFlowTag: %w", err)
	}
	return nil
}

// DeleteFlowTag removes a single tag from the flow. Deleting a tag
// that does not exist is a no-op. Returns ErrReadOnly / ErrNotFound
// per PutFlowTag.
func (s *PostgresStore) DeleteFlowTag(ctx context.Context, id uuid.UUID, name string) error {
	if err := s.flowWritable(ctx, id); err != nil {
		return err
	}
	if _, err := s.db.Exec(ctx, `DELETE FROM flow_tags WHERE flow_id = $1 AND name = $2`, id, name); err != nil {
		return fmt.Errorf("metastore: DeleteFlowTag: %w", err)
	}
	return nil
}

// PutFlowLabel sets the flow's label. Returns ErrReadOnly /
// ErrNotFound per the shared writable-check.
func (s *PostgresStore) PutFlowLabel(ctx context.Context, id uuid.UUID, label string) error {
	return s.updateFlowField(ctx, id, "label", label)
}

// DeleteFlowLabel clears the flow's label. Returns ErrReadOnly /
// ErrNotFound per the shared writable-check.
func (s *PostgresStore) DeleteFlowLabel(ctx context.Context, id uuid.UUID) error {
	return s.updateFlowField(ctx, id, "label", nil)
}

// PutFlowDescription sets the flow's description. Returns ErrReadOnly
// / ErrNotFound per the shared writable-check.
func (s *PostgresStore) PutFlowDescription(ctx context.Context, id uuid.UUID, desc string) error {
	return s.updateFlowField(ctx, id, "description", desc)
}

// DeleteFlowDescription clears the flow's description. Returns
// ErrReadOnly / ErrNotFound per the shared writable-check.
func (s *PostgresStore) DeleteFlowDescription(ctx context.Context, id uuid.UUID) error {
	return s.updateFlowField(ctx, id, "description", nil)
}

// PutFlowReadOnly is the only flow write operation exempt from the read_only check.
func (s *PostgresStore) PutFlowReadOnly(ctx context.Context, id uuid.UUID, readOnly bool) error {
	tag, err := s.db.Exec(ctx,
		`UPDATE flows SET read_only = $1, metadata_updated = now() WHERE id = $2`, readOnly, id)
	if err != nil {
		return fmt.Errorf("metastore: PutFlowReadOnly: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return apperror.New(apperror.ErrNotFound, "flow not found")
	}
	return nil
}

// PutFlowCollection replaces the flow_collection in full. The
// existing entries are deleted and the new items inserted in the
// supplied order in a single transaction. Returns ErrReadOnly /
// ErrNotFound atomically with the writable check inside the
// transaction.
func (s *PostgresStore) PutFlowCollection(ctx context.Context, id uuid.UUID, items []CollectionItem) error {
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // tx is rolled back implicitly by Commit; an explicit Rollback after Commit is a no-op whose error is uninteresting.

	// Check writable inside the transaction to make the guard atomic.
	var readOnly bool
	err = tx.QueryRow(ctx, `SELECT read_only FROM flows WHERE id = $1`, id).Scan(&readOnly)
	if errors.Is(err, pgx.ErrNoRows) {
		return apperror.New(apperror.ErrNotFound, "flow not found")
	}
	if err != nil {
		return fmt.Errorf("metastore: PutFlowCollection: check: %w", err)
	}
	if readOnly {
		return apperror.New(apperror.ErrReadOnly, "flow is read-only")
	}

	if _, err := tx.Exec(ctx, `DELETE FROM flow_collection WHERE flow_id = $1`, id); err != nil {
		return fmt.Errorf("metastore: PutFlowCollection: delete: %w", err)
	}
	for i, ci := range items {
		if _, err := tx.Exec(ctx,
			`INSERT INTO flow_collection (flow_id, item_id, role, container_mapping, sort_order) VALUES ($1,$2,$3,$4,$5)`,
			id, ci.ID, ci.Role, nilJSON(ci.ContainerMapping), i,
		); err != nil {
			return fmt.Errorf("metastore: PutFlowCollection: insert: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("metastore: PutFlowCollection: commit: %w", err)
	}
	return nil
}

// DeleteFlowCollection clears the flow_collection in full. Returns
// ErrReadOnly / ErrNotFound per the shared writable-check.
func (s *PostgresStore) DeleteFlowCollection(ctx context.Context, id uuid.UUID) error {
	if err := s.flowWritable(ctx, id); err != nil {
		return err
	}
	if _, err := s.db.Exec(ctx, `DELETE FROM flow_collection WHERE flow_id = $1`, id); err != nil {
		return fmt.Errorf("metastore: DeleteFlowCollection: %w", err)
	}
	return nil
}

// PutFlowAvgBitRate sets the flow's avg_bit_rate. Returns ErrReadOnly
// / ErrNotFound per the shared writable-check.
func (s *PostgresStore) PutFlowAvgBitRate(ctx context.Context, id uuid.UUID, rate int64) error {
	return s.updateFlowField(ctx, id, "avg_bit_rate", rate)
}

// DeleteFlowAvgBitRate clears the flow's avg_bit_rate. Returns
// ErrReadOnly / ErrNotFound per the shared writable-check.
func (s *PostgresStore) DeleteFlowAvgBitRate(ctx context.Context, id uuid.UUID) error {
	return s.updateFlowField(ctx, id, "avg_bit_rate", nil)
}

// PutFlowMaxBitRate sets the flow's max_bit_rate. Returns ErrReadOnly
// / ErrNotFound per the shared writable-check.
func (s *PostgresStore) PutFlowMaxBitRate(ctx context.Context, id uuid.UUID, rate int64) error {
	return s.updateFlowField(ctx, id, "max_bit_rate", rate)
}

// DeleteFlowMaxBitRate clears the flow's max_bit_rate. Returns
// ErrReadOnly / ErrNotFound per the shared writable-check.
func (s *PostgresStore) DeleteFlowMaxBitRate(ctx context.Context, id uuid.UUID) error {
	return s.updateFlowField(ctx, id, "max_bit_rate", nil)
}

// updateFlowField runs a targeted UPDATE on a single nullable column with a read_only guard.
func (s *PostgresStore) updateFlowField(ctx context.Context, id uuid.UUID, col string, val any) error {
	if err := s.flowWritable(ctx, id); err != nil {
		return err
	}
	tag, err := s.db.Exec(ctx,
		fmt.Sprintf(`UPDATE flows SET %s = $1, metadata_updated = now() WHERE id = $2`, col), val, id)
	if err != nil {
		return fmt.Errorf("metastore: update flow %s: %w", col, err)
	}
	if tag.RowsAffected() == 0 {
		return apperror.New(apperror.ErrNotFound, "flow not found")
	}
	return nil
}

// flowWritable returns ErrNotFound if the flow doesn't exist and ErrReadOnly if it is read_only.
func (s *PostgresStore) flowWritable(ctx context.Context, id uuid.UUID) error {
	var readOnly bool
	err := s.db.QueryRow(ctx, `SELECT read_only FROM flows WHERE id = $1`, id).Scan(&readOnly)
	if errors.Is(err, pgx.ErrNoRows) {
		return apperror.New(apperror.ErrNotFound, "flow not found")
	}
	if err != nil {
		return fmt.Errorf("metastore: flowWritable: %w", err)
	}
	if readOnly {
		return apperror.New(apperror.ErrReadOnly, "flow is read-only")
	}
	return nil
}

func scanFlowBase(row pgx.CollectableRow) (*Flow, error) {
	var fl Flow
	var tagsJSON []byte
	err := row.Scan(
		&fl.ID, &fl.SourceID, &fl.Format, &fl.Codec, &fl.Container,
		&fl.Label, &fl.Description,
		&fl.EssenceParameters, &fl.ContainerMapping,
		&fl.AvgBitRate, &fl.MaxBitRate, &fl.SegmentDuration,
		&fl.Generation, &fl.MetadataVersion, &fl.ReadOnly,
		&fl.Created, &fl.MetadataUpdated, &fl.SegmentsUpdated, &fl.Timerange,
		&tagsJSON,
	)
	if err != nil {
		return nil, err
	}
	fl.Tags = make(map[string]json.RawMessage)
	if len(tagsJSON) > 0 {
		if err := json.Unmarshal(tagsJSON, &fl.Tags); err != nil {
			return nil, fmt.Errorf("unmarshal flow tags: %w", err)
		}
	}
	return &fl, nil
}

// nilJSON returns nil if b is empty, otherwise returns b.
// Prevents inserting empty byte slices as null-like JSON.
func nilJSON(b json.RawMessage) any {
	if len(b) == 0 {
		return nil
	}
	return b
}

// codecEqual compares two *string codec values.
func codecEqual(a, b *string) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}
