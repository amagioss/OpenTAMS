//go:build integration || perf

package metastore

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	"github.com/amagioss/opentams/internal/apperror"
	"github.com/amagioss/opentams/internal/timerange"
)

// simpleFlow returns a minimal valid Flow for upsert tests.
func simpleFlow(sourceID, flowID uuid.UUID, format string) *Flow {
	return &Flow{
		ID:       flowID,
		SourceID: sourceID,
		Format:   format,
	}
}

// TC-META-FL-01: UpsertFlow creates source implicitly and returns created=true.
func TestUpsertFlow_CreatesSourceAndFlow(t *testing.T) {
	store, _ := withTx(t)
	srcID := uuid.New()
	flID := uuid.New()

	created, err := store.UpsertFlow(context.Background(), simpleFlow(srcID, flID, "urn:x-nmos:format:video"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !created {
		t.Error("created: want true")
	}

	// source must exist implicitly
	src, err := store.GetSource(context.Background(), srcID)
	if err != nil {
		t.Fatalf("GetSource: %v", err)
	}
	if src.Format != "urn:x-nmos:format:video" {
		t.Errorf("implicit source format: got %q", src.Format)
	}
}

// TC-META-FL-02: UpsertFlow on existing flow returns created=false.
func TestUpsertFlow_UpdateReturnsCreatedFalse(t *testing.T) {
	store, _ := withTx(t)
	srcID, flID := uuid.New(), uuid.New()
	f := simpleFlow(srcID, flID, "urn:x-nmos:format:audio")

	if _, err := store.UpsertFlow(context.Background(), f); err != nil {
		t.Fatalf("first upsert: %v", err)
	}

	label := "updated"
	f.Label = &label
	created, err := store.UpsertFlow(context.Background(), f)
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if created {
		t.Error("created: want false on update")
	}
}

// TC-META-FL-03: UpsertFlow returns ErrFormatMismatch when format differs from existing source.
func TestUpsertFlow_FormatMismatch(t *testing.T) {
	store, tx := withTx(t)
	srcID := uuid.New()
	execTx(t, tx, `INSERT INTO sources (id, format) VALUES ($1, $2)`, srcID, "urn:x-nmos:format:video")

	_, err := store.UpsertFlow(context.Background(), simpleFlow(srcID, uuid.New(), "urn:x-nmos:format:audio"))
	requireErrCode(t, err, apperror.ErrFormatMismatch)
}

// TC-META-FL-04: UpsertFlow returns ErrImmutableField when source_id changes.
func TestUpsertFlow_SourceIDImmutable(t *testing.T) {
	store, _ := withTx(t)
	flID := uuid.New()
	f := simpleFlow(uuid.New(), flID, "urn:x-nmos:format:video")
	if _, err := store.UpsertFlow(context.Background(), f); err != nil {
		t.Fatalf("first upsert: %v", err)
	}

	f.SourceID = uuid.New() // different source
	_, err := store.UpsertFlow(context.Background(), f)
	requireErrCode(t, err, apperror.ErrImmutableField)
}

// TC-META-FL-05: UpsertFlow returns ErrImmutableField when format changes.
func TestUpsertFlow_FormatImmutable(t *testing.T) {
	store, _ := withTx(t)
	srcID, flID := uuid.New(), uuid.New()
	f := simpleFlow(srcID, flID, "urn:x-nmos:format:video")
	if _, err := store.UpsertFlow(context.Background(), f); err != nil {
		t.Fatalf("first upsert: %v", err)
	}

	f.Format = "urn:x-nmos:format:audio"
	_, err := store.UpsertFlow(context.Background(), f)
	requireErrCode(t, err, apperror.ErrImmutableField)
}

// TC-META-FL-06: UpsertFlow returns ErrImmutableField when codec changes on a flow that has segments.
func TestUpsertFlow_CodecImmutableWhenSegmentsExist(t *testing.T) {
	store, tx := withTx(t)
	srcID, flID, objID := uuid.New(), uuid.New(), "obj-001"
	codec := "video/mp4"
	f := simpleFlow(srcID, flID, "urn:x-nmos:format:video")
	f.Codec = &codec
	if _, err := store.UpsertFlow(context.Background(), f); err != nil {
		t.Fatalf("first upsert: %v", err)
	}

	// insert a segment via raw SQL
	execTx(t, tx, `INSERT INTO objects (id, ref_count) VALUES ($1, 1)`, objID)
	execTx(t, tx, `INSERT INTO segments (flow_id, object_id, timerange, lower_ns, upper_ns) VALUES ($1,$2,$3,$4,$5)`,
		flID, objID, "[0:0_10:0)", int64(0), int64(10_000_000_000))

	newCodec := "video/h264"
	f.Codec = &newCodec
	_, err := store.UpsertFlow(context.Background(), f)
	requireErrCode(t, err, apperror.ErrImmutableField)
}

// TC-META-FL-07: GetFlow returns ErrNotFound for unknown ID.
func TestGetFlow_NotFound(t *testing.T) {
	store, _ := withTx(t)
	_, err := store.GetFlow(context.Background(), uuid.New())
	requireErrCode(t, err, apperror.ErrNotFound)
}

// TC-META-FL-08: GetFlow returns flow with tags and collection.
func TestGetFlow_WithTagsAndCollection(t *testing.T) {
	store, tx := withTx(t)
	srcID, flID := uuid.New(), uuid.New()
	execTx(t, tx, `INSERT INTO sources (id, format) VALUES ($1, $2)`, srcID, "urn:x-nmos:format:video")
	execTx(t, tx, `INSERT INTO flows (id, source_id, format, label) VALUES ($1, $2, $3, $4)`,
		flID, srcID, "urn:x-nmos:format:video", "test-flow")
	execTx(t, tx, `INSERT INTO flow_tags (flow_id, name, value) VALUES ($1, $2, $3)`,
		flID, "env", json.RawMessage(`"staging"`))
	itemID := uuid.New()
	execTx(t, tx, `INSERT INTO flow_collection (flow_id, item_id, role, sort_order) VALUES ($1, $2, $3, $4)`,
		flID, itemID, "member", 0)

	fl, err := store.GetFlow(context.Background(), flID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fl.ID != flID {
		t.Errorf("ID: got %v", fl.ID)
	}
	if fl.Label == nil || *fl.Label != "test-flow" {
		t.Errorf("Label: got %v", fl.Label)
	}
	if len(fl.Tags) != 1 || string(fl.Tags["env"]) != `"staging"` {
		t.Errorf("Tags: got %v", fl.Tags)
	}
	if len(fl.FlowCollection) != 1 || fl.FlowCollection[0].ID != itemID {
		t.Errorf("FlowCollection: got %v", fl.FlowCollection)
	}
}

// TC-META-FL-09: DeleteFlow returns ErrNotFound for unknown ID.
func TestDeleteFlow_NotFound(t *testing.T) {
	store, _ := withTx(t)
	err := store.DeleteFlow(context.Background(), uuid.New())
	requireErrCode(t, err, apperror.ErrNotFound)
}

// TC-META-FL-10: DeleteFlow removes the flow and decrements ref counts.
// The GC worker (D-25 / D-29) is the sole reaper of zero-ref rows; this
// test asserts the metastore leaves them intact with `ref_count = 0` and
// `reaping = false` so the GC sweep can claim them.
func TestDeleteFlow_DecrementsRefCounts(t *testing.T) {
	store, tx := withTx(t)
	srcID, flID := uuid.New(), uuid.New()
	sharedObj, exclusiveObj := "obj-shared", "obj-exclusive"

	execTx(t, tx, `INSERT INTO sources (id, format) VALUES ($1, $2)`, srcID, "urn:x-nmos:format:video")
	execTx(t, tx, `INSERT INTO flows (id, source_id, format) VALUES ($1, $2, $3)`, flID, srcID, "urn:x-nmos:format:video")
	// sharedObj has ref_count 2 — not eligible for reaping after this flow's segments are removed
	execTx(t, tx, `INSERT INTO objects (id, ref_count) VALUES ($1, 2)`, sharedObj)
	// exclusiveObj has ref_count 1 — reaches zero (GC will reap it)
	execTx(t, tx, `INSERT INTO objects (id, ref_count) VALUES ($1, 1)`, exclusiveObj)
	execTx(t, tx, `INSERT INTO segments (flow_id, object_id, timerange, lower_ns, upper_ns) VALUES ($1,$2,$3,$4,$5)`,
		flID, sharedObj, "[0:0_5:0)", int64(0), int64(5_000_000_000))
	execTx(t, tx, `INSERT INTO segments (flow_id, object_id, timerange, lower_ns, upper_ns) VALUES ($1,$2,$3,$4,$5)`,
		flID, exclusiveObj, "[5:0_10:0)", int64(5_000_000_000), int64(10_000_000_000))

	if err := store.DeleteFlow(context.Background(), flID); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// flow must be gone
	_, err := store.GetFlow(context.Background(), flID)
	requireErrCode(t, err, apperror.ErrNotFound)

	// shared object still references count 1, exclusive sits at 0 awaiting GC
	var sharedRC, exclusiveRC int64
	var exclusiveReaping bool
	if err := tx.QueryRow(context.Background(),
		`SELECT ref_count FROM objects WHERE id = $1`, sharedObj).Scan(&sharedRC); err != nil {
		t.Fatalf("shared ref_count: %v", err)
	}
	if err := tx.QueryRow(context.Background(),
		`SELECT ref_count, reaping FROM objects WHERE id = $1`, exclusiveObj).Scan(&exclusiveRC, &exclusiveReaping); err != nil {
		t.Fatalf("exclusive ref_count: %v", err)
	}
	if sharedRC != 1 {
		t.Errorf("shared ref_count: got %d, want 1", sharedRC)
	}
	if exclusiveRC != 0 {
		t.Errorf("exclusive ref_count: got %d, want 0", exclusiveRC)
	}
	if exclusiveReaping {
		t.Errorf("exclusive reaping: got true, want false (GC, not metastore, flips reaping=true)")
	}
}

// TC-META-FL-11: Write operations on a read_only flow return ErrReadOnly.
func TestFlowWriteOps_ReadOnly(t *testing.T) {
	store, tx := withTx(t)
	srcID, flID := uuid.New(), uuid.New()
	execTx(t, tx, `INSERT INTO sources (id, format) VALUES ($1, $2)`, srcID, "urn:x-nmos:format:video")
	execTx(t, tx, `INSERT INTO flows (id, source_id, format, read_only) VALUES ($1, $2, $3, true)`,
		flID, srcID, "urn:x-nmos:format:video")

	ctx := context.Background()
	requireErrCode(t, store.PutFlowTag(ctx, flID, "k", json.RawMessage(`"v"`)), apperror.ErrReadOnly)
	requireErrCode(t, store.DeleteFlowTag(ctx, flID, "k"), apperror.ErrReadOnly)
	requireErrCode(t, store.PutFlowLabel(ctx, flID, "x"), apperror.ErrReadOnly)
	requireErrCode(t, store.DeleteFlowLabel(ctx, flID), apperror.ErrReadOnly)
	requireErrCode(t, store.PutFlowDescription(ctx, flID, "x"), apperror.ErrReadOnly)
	requireErrCode(t, store.DeleteFlowDescription(ctx, flID), apperror.ErrReadOnly)
	requireErrCode(t, store.PutFlowCollection(ctx, flID, nil), apperror.ErrReadOnly)
	requireErrCode(t, store.DeleteFlowCollection(ctx, flID), apperror.ErrReadOnly)
	requireErrCode(t, store.PutFlowAvgBitRate(ctx, flID, 1000), apperror.ErrReadOnly)
	requireErrCode(t, store.DeleteFlowAvgBitRate(ctx, flID), apperror.ErrReadOnly)
	requireErrCode(t, store.PutFlowMaxBitRate(ctx, flID, 2000), apperror.ErrReadOnly)
	requireErrCode(t, store.DeleteFlowMaxBitRate(ctx, flID), apperror.ErrReadOnly)
}

// TC-META-FL-12: PutFlowReadOnly succeeds even on a read_only flow (exempt from check).
func TestPutFlowReadOnly_ExemptFromReadOnlyCheck(t *testing.T) {
	store, tx := withTx(t)
	srcID, flID := uuid.New(), uuid.New()
	execTx(t, tx, `INSERT INTO sources (id, format) VALUES ($1, $2)`, srcID, "urn:x-nmos:format:video")
	execTx(t, tx, `INSERT INTO flows (id, source_id, format, read_only) VALUES ($1, $2, $3, true)`,
		flID, srcID, "urn:x-nmos:format:video")

	if err := store.PutFlowReadOnly(context.Background(), flID, false); err != nil {
		t.Fatalf("PutFlowReadOnly: %v", err)
	}
	fl, _ := store.GetFlow(context.Background(), flID)
	if fl.ReadOnly {
		t.Error("ReadOnly: want false after update")
	}
}

// TC-META-FL-13: PutFlowCollection replaces the collection atomically.
func TestPutFlowCollection_Replaces(t *testing.T) {
	store, tx := withTx(t)
	srcID, flID := uuid.New(), uuid.New()
	execTx(t, tx, `INSERT INTO sources (id, format) VALUES ($1, $2)`, srcID, "urn:x-nmos:format:video")
	execTx(t, tx, `INSERT INTO flows (id, source_id, format) VALUES ($1, $2, $3)`, flID, srcID, "urn:x-nmos:format:video")

	item1, item2 := uuid.New(), uuid.New()
	if err := store.PutFlowCollection(context.Background(), flID, []CollectionItem{
		{ID: item1}, {ID: item2},
	}); err != nil {
		t.Fatalf("PutFlowCollection: %v", err)
	}
	fl, _ := store.GetFlow(context.Background(), flID)
	if len(fl.FlowCollection) != 2 {
		t.Errorf("FlowCollection len: got %d, want 2", len(fl.FlowCollection))
	}

	// replace with one item
	if err := store.PutFlowCollection(context.Background(), flID, []CollectionItem{{ID: item1}}); err != nil {
		t.Fatalf("PutFlowCollection replace: %v", err)
	}
	fl, _ = store.GetFlow(context.Background(), flID)
	if len(fl.FlowCollection) != 1 {
		t.Errorf("FlowCollection after replace: got %d, want 1", len(fl.FlowCollection))
	}
}

// TC-META-FL-14: ListFlows with FrameWidth filter returns only matching flows.
func TestListFlows_FrameWidthFilter(t *testing.T) {
	store, tx := withTx(t)
	ctx := context.Background()
	srcID := uuid.New()
	execTx(t, tx, `INSERT INTO sources (id, format) VALUES ($1, $2)`, srcID, "urn:x-nmos:format:video")

	fl1 := uuid.New()
	execTx(t, tx, `INSERT INTO flows (id, source_id, format, essence_parameters) VALUES ($1, $2, $3, $4)`,
		fl1, srcID, "urn:x-nmos:format:video", json.RawMessage(`{"frame_width":1920,"frame_height":1080}`))
	fl2 := uuid.New()
	execTx(t, tx, `INSERT INTO flows (id, source_id, format, essence_parameters) VALUES ($1, $2, $3, $4)`,
		fl2, srcID, "urn:x-nmos:format:video", json.RawMessage(`{"frame_width":1280,"frame_height":720}`))

	w := 1920
	page, err := store.ListFlows(ctx, ListFlowsParams{FrameWidth: &w, Limit: 100})
	if err != nil {
		t.Fatalf("ListFlows: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != fl1 {
		t.Errorf("ListFlows FrameWidth=1920: got %d items, want 1 matching fl1", len(page.Items))
	}
}

// TC-META-FL-15: ListFlows with FrameHeight filter returns only matching flows.
func TestListFlows_FrameHeightFilter(t *testing.T) {
	store, tx := withTx(t)
	ctx := context.Background()
	srcID := uuid.New()
	execTx(t, tx, `INSERT INTO sources (id, format) VALUES ($1, $2)`, srcID, "urn:x-nmos:format:video")

	fl1 := uuid.New()
	execTx(t, tx, `INSERT INTO flows (id, source_id, format, essence_parameters) VALUES ($1, $2, $3, $4)`,
		fl1, srcID, "urn:x-nmos:format:video", json.RawMessage(`{"frame_width":1920,"frame_height":1080}`))
	fl2 := uuid.New()
	execTx(t, tx, `INSERT INTO flows (id, source_id, format, essence_parameters) VALUES ($1, $2, $3, $4)`,
		fl2, srcID, "urn:x-nmos:format:video", json.RawMessage(`{"frame_width":1280,"frame_height":720}`))

	h := 720
	page, err := store.ListFlows(ctx, ListFlowsParams{FrameHeight: &h, Limit: 100})
	if err != nil {
		t.Fatalf("ListFlows: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != fl2 {
		t.Errorf("ListFlows FrameHeight=720: got %d items, want 1 matching fl2", len(page.Items))
	}
}

// TC-META-FL-16: ListFlows with Timerange filter returns flows with overlapping segments.
func TestListFlows_TimerangeFilter(t *testing.T) {
	store, tx := withTx(t)
	ctx := context.Background()
	srcID := uuid.New()
	execTx(t, tx, `INSERT INTO sources (id, format) VALUES ($1, $2)`, srcID, "urn:x-nmos:format:video")

	// fl1 has a segment at [0:0_10:0)
	fl1 := uuid.New()
	execTx(t, tx, `INSERT INTO flows (id, source_id, format) VALUES ($1, $2, $3)`, fl1, srcID, "urn:x-nmos:format:video")
	obj1 := "obj-" + fl1.String()
	execTx(t, tx, `INSERT INTO objects (id, ref_count) VALUES ($1, 1)`, obj1)
	execTx(t, tx, `INSERT INTO segments (flow_id, object_id, timerange, lower_ns, upper_ns) VALUES ($1, $2, $3, $4, $5)`,
		fl1, obj1, "[0:0_10:0)", int64(0), int64(10_000_000_000))

	// fl2 has a segment at [20:0_30:0) — does not overlap [5:0_15:0)
	fl2 := uuid.New()
	execTx(t, tx, `INSERT INTO flows (id, source_id, format) VALUES ($1, $2, $3)`, fl2, srcID, "urn:x-nmos:format:video")
	obj2 := "obj-" + fl2.String()
	execTx(t, tx, `INSERT INTO objects (id, ref_count) VALUES ($1, 1)`, obj2)
	execTx(t, tx, `INSERT INTO segments (flow_id, object_id, timerange, lower_ns, upper_ns) VALUES ($1, $2, $3, $4, $5)`,
		fl2, obj2, "[20:0_30:0)", int64(20_000_000_000), int64(30_000_000_000))

	tr, _ := timerange.Parse("[5:0_15:0)")
	page, err := store.ListFlows(ctx, ListFlowsParams{Timerange: &tr, Limit: 100})
	if err != nil {
		t.Fatalf("ListFlows: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != fl1 {
		t.Errorf("ListFlows Timerange=[5:0_15:0): got %d items, want 1 matching fl1", len(page.Items))
	}
}

// TC-META-FL-17: GetFlowTimerange returns nil when flow has no segments.
func TestGetFlowTimerange_NoSegments(t *testing.T) {
	store, tx := withTx(t)
	ctx := context.Background()
	srcID, flID := uuid.New(), uuid.New()
	execTx(t, tx, `INSERT INTO sources (id, format) VALUES ($1, $2)`, srcID, "urn:x-nmos:format:video")
	execTx(t, tx, `INSERT INTO flows (id, source_id, format) VALUES ($1, $2, $3)`, flID, srcID, "urn:x-nmos:format:video")

	tr, err := store.GetFlowTimerange(ctx, flID)
	if err != nil {
		t.Fatalf("GetFlowTimerange: %v", err)
	}
	if tr != nil {
		t.Errorf("GetFlowTimerange: want nil for flow with no segments, got %q", *tr)
	}
}

// TC-META-FL-18: GetFlowTimerange returns the span of all segments.
func TestGetFlowTimerange_WithSegments(t *testing.T) {
	store, tx := withTx(t)
	ctx := context.Background()
	srcID, flID := uuid.New(), uuid.New()
	execTx(t, tx, `INSERT INTO sources (id, format) VALUES ($1, $2)`, srcID, "urn:x-nmos:format:video")
	execTx(t, tx, `INSERT INTO flows (id, source_id, format) VALUES ($1, $2, $3)`, flID, srcID, "urn:x-nmos:format:video")
	obj1, obj2 := "obj-tr-1", "obj-tr-2"
	execTx(t, tx, `INSERT INTO objects (id, ref_count) VALUES ($1, 1), ($2, 1)`, obj1, obj2)
	// Two segments: [0:0_5:0) and [10:0_20:0)
	execTx(t, tx, `INSERT INTO segments (flow_id, object_id, timerange, lower_ns, upper_ns) VALUES ($1, $2, $3, $4, $5)`,
		flID, obj1, "[0:0_5:0)", int64(0), int64(5_000_000_000))
	execTx(t, tx, `INSERT INTO segments (flow_id, object_id, timerange, lower_ns, upper_ns) VALUES ($1, $2, $3, $4, $5)`,
		flID, obj2, "[10:0_20:0)", int64(10_000_000_000), int64(20_000_000_000))

	tr, err := store.GetFlowTimerange(ctx, flID)
	if err != nil {
		t.Fatalf("GetFlowTimerange: %v", err)
	}
	if tr == nil {
		t.Fatal("GetFlowTimerange: want non-nil")
	}
	// Expect span [0:0_20:0)
	if *tr != "[0:0_20:0)" {
		t.Errorf("GetFlowTimerange: got %q, want %q", *tr, "[0:0_20:0)")
	}
}
