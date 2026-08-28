//go:build integration || perf

package metastore

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	"github.com/amagioss/opentams/internal/apperror"
)

// TC-META-SRC-01: GetSource returns ErrNotFound for unknown ID.
func TestGetSource_NotFound(t *testing.T) {
	store, _ := withTx(t)
	_, err := store.GetSource(context.Background(), uuid.New())
	requireErrCode(t, err, apperror.ErrNotFound)
}

// TC-META-SRC-02: GetSource returns the source with correct fields.
func TestGetSource_Found(t *testing.T) {
	store, tx := withTx(t)
	id := uuid.New()
	execTx(t, tx, `INSERT INTO sources (id, format, label, description) VALUES ($1, $2, $3, $4)`,
		id, "urn:x-nmos:format:video", "my label", "my desc")

	src, err := store.GetSource(context.Background(), id)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if src.ID != id {
		t.Errorf("ID: got %v, want %v", src.ID, id)
	}
	if src.Format != "urn:x-nmos:format:video" {
		t.Errorf("Format: got %q", src.Format)
	}
	if src.Label == nil || *src.Label != "my label" {
		t.Errorf("Label: got %v", src.Label)
	}
	if src.Description == nil || *src.Description != "my desc" {
		t.Errorf("Description: got %v", src.Description)
	}
}

// TC-META-SRC-03: GetSource returns tags.
func TestGetSource_WithTags(t *testing.T) {
	store, tx := withTx(t)
	id := uuid.New()
	execTx(t, tx, `INSERT INTO sources (id, format) VALUES ($1, $2)`, id, "urn:x-nmos:format:audio")
	execTx(t, tx, `INSERT INTO source_tags (source_id, name, value) VALUES ($1, $2, $3)`,
		id, "env", json.RawMessage(`"production"`))

	src, err := store.GetSource(context.Background(), id)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(src.Tags) != 1 {
		t.Fatalf("Tags: got %d entries, want 1", len(src.Tags))
	}
	if string(src.Tags["env"]) != `"production"` {
		t.Errorf("Tags[env]: got %s", src.Tags["env"])
	}
}

// TC-META-SRC-04: ListSources returns empty page when no sources exist.
func TestListSources_Empty(t *testing.T) {
	store, _ := withTx(t)
	page, err := store.ListSources(context.Background(), ListSourcesParams{Limit: 10})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(page.Items) != 0 {
		t.Errorf("Items: got %d, want 0", len(page.Items))
	}
	if page.NextCursor != nil {
		t.Errorf("NextCursor: want nil, got %v", *page.NextCursor)
	}
}

// TC-META-SRC-05: ListSources returns sources and cursor when more exist.
func TestListSources_Pagination(t *testing.T) {
	store, tx := withTx(t)
	for range 3 {
		execTx(t, tx, `INSERT INTO sources (id, format) VALUES ($1, $2)`, uuid.New(), "urn:x-nmos:format:video")
	}

	page, err := store.ListSources(context.Background(), ListSourcesParams{Limit: 2})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("Items: got %d, want 2", len(page.Items))
	}
	if page.NextCursor == nil {
		t.Fatal("NextCursor: want non-nil")
	}

	page2, err := store.ListSources(context.Background(), ListSourcesParams{Limit: 2, PageFrom: page.NextCursor})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(page2.Items) != 1 {
		t.Errorf("page2 Items: got %d, want 1", len(page2.Items))
	}
	if page2.NextCursor != nil {
		t.Errorf("page2 NextCursor: want nil")
	}
}

// TC-META-SRC-06: PutSourceTag inserts and updates a tag.
func TestPutSourceTag(t *testing.T) {
	store, tx := withTx(t)
	id := uuid.New()
	execTx(t, tx, `INSERT INTO sources (id, format) VALUES ($1, $2)`, id, "urn:x-nmos:format:video")

	if err := store.PutSourceTag(context.Background(), id, "region", json.RawMessage(`"eu-west"`)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	src, _ := store.GetSource(context.Background(), id)
	if string(src.Tags["region"]) != `"eu-west"` {
		t.Errorf("tag value: got %s", src.Tags["region"])
	}

	// update
	if err := store.PutSourceTag(context.Background(), id, "region", json.RawMessage(`"us-east"`)); err != nil {
		t.Fatalf("unexpected error on update: %v", err)
	}
	src, _ = store.GetSource(context.Background(), id)
	if string(src.Tags["region"]) != `"us-east"` {
		t.Errorf("tag updated value: got %s", src.Tags["region"])
	}
}

// TC-META-SRC-07: PutSourceTag returns ErrNotFound for unknown source.
func TestPutSourceTag_NotFound(t *testing.T) {
	store, _ := withTx(t)
	err := store.PutSourceTag(context.Background(), uuid.New(), "k", json.RawMessage(`"v"`))
	requireErrCode(t, err, apperror.ErrNotFound)
}

// TC-META-SRC-08: DeleteSourceTag removes a tag.
func TestDeleteSourceTag(t *testing.T) {
	store, tx := withTx(t)
	id := uuid.New()
	execTx(t, tx, `INSERT INTO sources (id, format) VALUES ($1, $2)`, id, "urn:x-nmos:format:video")
	execTx(t, tx, `INSERT INTO source_tags (source_id, name, value) VALUES ($1, $2, $3)`,
		id, "env", json.RawMessage(`"test"`))

	if err := store.DeleteSourceTag(context.Background(), id, "env"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	src, _ := store.GetSource(context.Background(), id)
	if _, ok := src.Tags["env"]; ok {
		t.Error("tag still present after delete")
	}
}

// TC-META-SRC-09: DeleteSourceTag returns ErrNotFound when source does not exist.
func TestDeleteSourceTag_NotFound(t *testing.T) {
	store, _ := withTx(t)
	err := store.DeleteSourceTag(context.Background(), uuid.New(), "k")
	requireErrCode(t, err, apperror.ErrNotFound)
}

// TC-META-SRC-10: PutSourceLabel sets and PutSourceDescription sets their fields.
func TestPutSourceLabel(t *testing.T) {
	store, tx := withTx(t)
	id := uuid.New()
	execTx(t, tx, `INSERT INTO sources (id, format) VALUES ($1, $2)`, id, "urn:x-nmos:format:video")

	if err := store.PutSourceLabel(context.Background(), id, "my-label"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	src, _ := store.GetSource(context.Background(), id)
	if src.Label == nil || *src.Label != "my-label" {
		t.Errorf("Label: got %v", src.Label)
	}
}

// TC-META-SRC-11: DeleteSourceLabel clears the label field.
func TestDeleteSourceLabel(t *testing.T) {
	store, tx := withTx(t)
	id := uuid.New()
	execTx(t, tx, `INSERT INTO sources (id, format, label) VALUES ($1, $2, $3)`, id, "urn:x-nmos:format:video", "old")

	if err := store.DeleteSourceLabel(context.Background(), id); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	src, _ := store.GetSource(context.Background(), id)
	if src.Label != nil {
		t.Errorf("Label: want nil, got %q", *src.Label)
	}
}

// TC-META-SRC-12: PutSourceDescription sets the description field.
func TestPutSourceDescription(t *testing.T) {
	store, tx := withTx(t)
	id := uuid.New()
	execTx(t, tx, `INSERT INTO sources (id, format) VALUES ($1, $2)`, id, "urn:x-nmos:format:video")

	if err := store.PutSourceDescription(context.Background(), id, "a description"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	src, _ := store.GetSource(context.Background(), id)
	if src.Description == nil || *src.Description != "a description" {
		t.Errorf("Description: got %v", src.Description)
	}
}

// TC-META-SRC-13: DeleteSourceDescription clears the description field.
func TestDeleteSourceDescription(t *testing.T) {
	store, tx := withTx(t)
	id := uuid.New()
	execTx(t, tx, `INSERT INTO sources (id, format, description) VALUES ($1, $2, $3)`, id, "urn:x-nmos:format:video", "old desc")

	if err := store.DeleteSourceDescription(context.Background(), id); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	src, _ := store.GetSource(context.Background(), id)
	if src.Description != nil {
		t.Errorf("Description: want nil, got %q", *src.Description)
	}
}

// TC-META-SRC-14: ListSources filters by label.
func TestListSources_FilterLabel(t *testing.T) {
	store, tx := withTx(t)
	execTx(t, tx, `INSERT INTO sources (id, format, label) VALUES ($1, $2, $3)`, uuid.New(), "urn:x-nmos:format:video", "match")
	execTx(t, tx, `INSERT INTO sources (id, format, label) VALUES ($1, $2, $3)`, uuid.New(), "urn:x-nmos:format:audio", "other")

	lbl := "match"
	page, err := store.ListSources(context.Background(), ListSourcesParams{Limit: 10, Label: &lbl})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("Items: got %d, want 1", len(page.Items))
	}
	if page.Items[0].Label == nil || *page.Items[0].Label != "match" {
		t.Errorf("Label: got %v", page.Items[0].Label)
	}
}

// TC-META-SRC-15: ListSources filters by format.
func TestListSources_FilterFormat(t *testing.T) {
	store, tx := withTx(t)
	execTx(t, tx, `INSERT INTO sources (id, format) VALUES ($1, $2)`, uuid.New(), "urn:x-nmos:format:video")
	execTx(t, tx, `INSERT INTO sources (id, format) VALUES ($1, $2)`, uuid.New(), "urn:x-nmos:format:audio")

	fmt := "urn:x-nmos:format:video"
	page, err := store.ListSources(context.Background(), ListSourcesParams{Limit: 10, Format: &fmt})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("Items: got %d, want 1", len(page.Items))
	}
	if page.Items[0].Format != "urn:x-nmos:format:video" {
		t.Errorf("Format: got %s", page.Items[0].Format)
	}
}

// TC-META-SRC-16: ListSources filters by tag presence.
func TestListSources_FilterTagExists(t *testing.T) {
	store, tx := withTx(t)
	idWith := uuid.New()
	idWithout := uuid.New()
	execTx(t, tx, `INSERT INTO sources (id, format) VALUES ($1, $2)`, idWith, "urn:x-nmos:format:video")
	execTx(t, tx, `INSERT INTO sources (id, format) VALUES ($1, $2)`, idWithout, "urn:x-nmos:format:video")
	execTx(t, tx, `INSERT INTO source_tags (source_id, name, value) VALUES ($1, $2, $3)`, idWith, "env", `"prod"`)

	page, err := store.ListSources(context.Background(), ListSourcesParams{
		Limit:     10,
		TagExists: map[string]struct{}{"env": {}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != idWith {
		t.Errorf("Items: got %d items, want 1 with id %s", len(page.Items), idWith)
	}
}

// TC-META-SRC-17: ListSources filters by tag value.
func TestListSources_FilterTagValue(t *testing.T) {
	store, tx := withTx(t)
	idMatch := uuid.New()
	idOther := uuid.New()
	execTx(t, tx, `INSERT INTO sources (id, format) VALUES ($1, $2)`, idMatch, "urn:x-nmos:format:video")
	execTx(t, tx, `INSERT INTO sources (id, format) VALUES ($1, $2)`, idOther, "urn:x-nmos:format:video")
	execTx(t, tx, `INSERT INTO source_tags (source_id, name, value) VALUES ($1, $2, $3)`, idMatch, "region", `"eu-west"`)
	execTx(t, tx, `INSERT INTO source_tags (source_id, name, value) VALUES ($1, $2, $3)`, idOther, "region", `"us-east"`)

	page, err := store.ListSources(context.Background(), ListSourcesParams{
		Limit:     10,
		TagValues: map[string]string{"region": `"eu-west"`},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != idMatch {
		t.Errorf("Items: got %d items, want 1 with id %s", len(page.Items), idMatch)
	}
}
