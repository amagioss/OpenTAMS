package metastore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/amagioss/opentams/internal/apperror"
)

// SourceStore reads and writes Source records and their sub-resources.
type SourceStore interface {
	GetSource(ctx context.Context, id uuid.UUID) (*Source, error)
	ListSources(ctx context.Context, p ListSourcesParams) (*SourcePage, error)
	PutSourceTag(ctx context.Context, id uuid.UUID, name string, value json.RawMessage) error
	DeleteSourceTag(ctx context.Context, id uuid.UUID, name string) error
	PutSourceLabel(ctx context.Context, id uuid.UUID, label string) error
	DeleteSourceLabel(ctx context.Context, id uuid.UUID) error
	PutSourceDescription(ctx context.Context, id uuid.UUID, desc string) error
	DeleteSourceDescription(ctx context.Context, id uuid.UUID) error
}

// GetSource returns the source with the given ID, including its
// tags in a single round trip. Returns ErrNotFound when the ID is
// unknown.
func (s *PostgresStore) GetSource(ctx context.Context, id uuid.UUID) (*Source, error) {
	const q = `
		SELECT s.id, s.format, s.label, s.description, s.created, s.updated,
		       COALESCE(
		           jsonb_object_agg(t.name, t.value) FILTER (WHERE t.name IS NOT NULL),
		           '{}'::jsonb
		       ) AS tags
		FROM sources s
		LEFT JOIN source_tags t ON t.source_id = s.id
		WHERE s.id = $1
		GROUP BY s.id`

	rows, err := s.db.Query(ctx, q, id)
	if err != nil {
		return nil, fmt.Errorf("metastore: GetSource: %w", err)
	}
	src, err := pgx.CollectOneRow(rows, scanSource)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apperror.New(apperror.ErrNotFound, "source not found")
	}
	if err != nil {
		return nil, fmt.Errorf("metastore: GetSource: %w", err)
	}
	return src, nil
}

// ListSources returns one page of sources matching the optional
// filters in p. Pagination is cursor-based on (created, id);
// NextCursor on the returned SourcePage is non-nil when more pages
// exist.
func (s *PostgresStore) ListSources(ctx context.Context, p ListSourcesParams) (*SourcePage, error) {
	limit := p.Limit
	if limit <= 0 {
		limit = 100
	}

	args := []any{}
	conds := []string{}
	argN := 1

	if p.Label != nil {
		conds = append(conds, fmt.Sprintf("s.label = $%d", argN))
		args = append(args, *p.Label)
		argN++
	}
	if p.Format != nil {
		conds = append(conds, fmt.Sprintf("s.format = $%d", argN))
		args = append(args, *p.Format)
		argN++
	}
	for tagName := range p.TagExists {
		conds = append(conds, fmt.Sprintf("EXISTS (SELECT 1 FROM source_tags WHERE source_id = s.id AND name = $%d)", argN))
		args = append(args, tagName)
		argN++
	}
	for tagName, tagVal := range p.TagValues {
		conds = append(conds, fmt.Sprintf("EXISTS (SELECT 1 FROM source_tags WHERE source_id = s.id AND name = $%d AND value = $%d)", argN, argN+1))
		args = append(args, tagName, tagVal)
		argN += 2
	}
	if p.PageFrom != nil {
		cursor, cerr := decodeCursor(*p.PageFrom)
		if cerr != nil {
			return nil, apperror.New(apperror.ErrInvalidJSON, "invalid page_from cursor")
		}
		conds = append(conds, fmt.Sprintf("(s.created, s.id) > ($%d, $%d)", argN, argN+1))
		args = append(args, cursor.ts, cursor.id)
		argN += 2
	}

	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}

	q := fmt.Sprintf(`
		SELECT s.id, s.format, s.label, s.description, s.created, s.updated,
		       COALESCE(jsonb_object_agg(t.name, t.value) FILTER (WHERE t.name IS NOT NULL), '{}'::jsonb) AS tags
		FROM sources s
		LEFT JOIN source_tags t ON t.source_id = s.id
		%s
		GROUP BY s.id
		ORDER BY s.created, s.id
		LIMIT $%d`, where, argN)
	args = append(args, limit+1)

	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("metastore: ListSources: %w", err)
	}

	items, err := pgx.CollectRows(rows, scanSource)
	if err != nil {
		return nil, fmt.Errorf("metastore: ListSources: %w", err)
	}

	page := &SourcePage{}
	if len(items) > limit {
		items = items[:limit]
		last := items[limit-1]
		c := encodeCursor(last.Created, last.ID)
		page.NextCursor = &c
	}
	page.Items = items
	return page, nil
}

// PutSourceTag upserts a single tag on the source. Returns
// ErrNotFound when the source ID is unknown.
func (s *PostgresStore) PutSourceTag(ctx context.Context, id uuid.UUID, name string, value json.RawMessage) error {
	if err := s.sourceExists(ctx, id); err != nil {
		return err
	}
	const q = `INSERT INTO source_tags (source_id, name, value) VALUES ($1, $2, $3)
	           ON CONFLICT (source_id, name) DO UPDATE SET value = EXCLUDED.value`
	if _, err := s.db.Exec(ctx, q, id, name, value); err != nil {
		return fmt.Errorf("metastore: PutSourceTag: %w", err)
	}
	return nil
}

// DeleteSourceTag removes a single tag from the source. Deleting an
// absent tag is a no-op.
func (s *PostgresStore) DeleteSourceTag(ctx context.Context, id uuid.UUID, name string) error {
	if err := s.sourceExists(ctx, id); err != nil {
		return err
	}
	if _, err := s.db.Exec(ctx, `DELETE FROM source_tags WHERE source_id = $1 AND name = $2`, id, name); err != nil {
		return fmt.Errorf("metastore: DeleteSourceTag: %w", err)
	}
	return nil
}

// PutSourceLabel sets the source's label.
func (s *PostgresStore) PutSourceLabel(ctx context.Context, id uuid.UUID, label string) error {
	return s.updateSourceField(ctx, id, "label", label)
}

// DeleteSourceLabel clears the source's label.
func (s *PostgresStore) DeleteSourceLabel(ctx context.Context, id uuid.UUID) error {
	return s.updateSourceField(ctx, id, "label", nil)
}

// PutSourceDescription sets the source's description.
func (s *PostgresStore) PutSourceDescription(ctx context.Context, id uuid.UUID, desc string) error {
	return s.updateSourceField(ctx, id, "description", desc)
}

// DeleteSourceDescription clears the source's description.
func (s *PostgresStore) DeleteSourceDescription(ctx context.Context, id uuid.UUID) error {
	return s.updateSourceField(ctx, id, "description", nil)
}

// updateSourceField runs a targeted UPDATE on a single nullable column.
func (s *PostgresStore) updateSourceField(ctx context.Context, id uuid.UUID, col string, val any) error {
	tag, err := s.db.Exec(ctx,
		fmt.Sprintf(`UPDATE sources SET %s = $1, updated = now() WHERE id = $2`, col), val, id)
	if err != nil {
		return fmt.Errorf("metastore: update source %s: %w", col, err)
	}
	if tag.RowsAffected() == 0 {
		return apperror.New(apperror.ErrNotFound, "source not found")
	}
	return nil
}

func (s *PostgresStore) sourceExists(ctx context.Context, id uuid.UUID) error {
	var exists bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM sources WHERE id = $1)`, id).Scan(&exists)
	if err != nil {
		return fmt.Errorf("metastore: sourceExists: %w", err)
	}
	if !exists {
		return apperror.New(apperror.ErrNotFound, "source not found")
	}
	return nil
}

func scanSource(row pgx.CollectableRow) (*Source, error) {
	var src Source
	var tagsJSON []byte
	err := row.Scan(&src.ID, &src.Format, &src.Label, &src.Description,
		&src.Created, &src.Updated, &tagsJSON)
	if err != nil {
		return nil, err
	}
	src.Tags = make(map[string]json.RawMessage)
	if len(tagsJSON) > 0 {
		if err := json.Unmarshal(tagsJSON, &src.Tags); err != nil {
			return nil, fmt.Errorf("unmarshal source tags: %w", err)
		}
	}
	return &src, nil
}
