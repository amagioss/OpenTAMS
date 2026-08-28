//go:build integration || perf

package metastore_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/amagioss/opentams/internal/apperror"
	"github.com/amagioss/opentams/internal/domain"
	"github.com/amagioss/opentams/internal/metastore"
	"github.com/amagioss/opentams/internal/timerange"
)

// External test package: forces using exported names only — the same
// surface the service-layer caller will see.

// -- helpers (file-local; do not collide with internal-test helpers) --

func mustTR(t *testing.T, s string) timerange.TimeRange {
	t.Helper()
	tr, err := timerange.Parse(s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return tr
}

// seedFlow inserts a sources/flows pair via the test pool. flow row is
// in its own tx so visible across the test's read-only inspections.
// Returns flowID. ReadOnly is set when readOnly is true.
func seedFlow(t *testing.T, readOnly bool) uuid.UUID {
	t.Helper()
	srcID := uuid.New()
	flID := uuid.New()
	exec(t, `INSERT INTO sources (id, format) VALUES ($1, $2)`, srcID, "urn:x-nmos:format:video")
	exec(t, `INSERT INTO flows (id, source_id, format, read_only) VALUES ($1, $2, $3, $4)`,
		flID, srcID, "urn:x-nmos:format:video", readOnly)
	t.Cleanup(func() {
		// 1. Capture the object_ids referenced from this flow.
		// 2. DELETE the flow → segments cascade away via FK.
		// 3. Drop any objects rows whose ref_count is zero AND are no longer
		//    referenced by any other flow's segments (test isolation: each
		//    test seeds its own object_ids; if shared between two seedFlow
		//    calls in one test, the other flow's cleanup also touches them).
		var orphans []string
		rows, err := metastore.SharedTestPool().Query(context.Background(),
			`SELECT object_id FROM segments WHERE flow_id = $1`, flID)
		if err == nil {
			for rows.Next() {
				var oid string
				_ = rows.Scan(&oid)
				orphans = append(orphans, oid)
			}
			rows.Close()
		}
		exec(t, `DELETE FROM flows WHERE id = $1`, flID)
		exec(t, `DELETE FROM sources WHERE id = $1`, srcID)
		for _, oid := range orphans {
			// Only drop if no remaining segments reference it (cross-flow share).
			n := queryInt(t, `SELECT COUNT(*) FROM segments WHERE object_id = $1`, oid)
			if n == 0 {
				exec(t, `DELETE FROM objects WHERE id = $1`, oid)
			}
		}
	})
	return flID
}

// exec runs raw SQL against the package-level test pool. Used for
// fixture seeding outside the store-under-test.
func exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := metastore.SharedTestPool().Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

// queryInt runs a SELECT and returns the first int column value.
func queryInt(t *testing.T, sql string, args ...any) int64 {
	t.Helper()
	var v int64
	if err := metastore.SharedTestPool().QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("queryInt %q: no rows", sql)
		}
		t.Fatalf("queryInt %q: %v", sql, err)
	}
	return v
}

// queryBool runs a SELECT returning a bool column.
func queryBool(t *testing.T, sql string, args ...any) bool {
	t.Helper()
	var v bool
	if err := metastore.SharedTestPool().QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		t.Fatalf("queryBool %q: %v", sql, err)
	}
	return v
}

// queryStrPtr returns *string (nil if NULL).
func queryStrPtr(t *testing.T, sql string, args ...any) *string {
	t.Helper()
	var v *string
	if err := metastore.SharedTestPool().QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		t.Fatalf("queryStrPtr %q: %v", sql, err)
	}
	return v
}

// newStore returns a *metastore.PostgresStore bound to the package pool.
// Each test runs against the shared pool; tests clean up their own
// flow + segments + objects via t.Cleanup in seedFlow.
func newStore(t *testing.T) *metastore.PostgresStore {
	t.Helper()
	return metastore.New(metastore.SharedTestPool())
}

// makeSegment builds a domain.Segment with timerange parsed.
func makeSegment(t *testing.T, objectID, tr string) domain.Segment {
	t.Helper()
	return domain.Segment{
		ObjectID:  objectID,
		Timerange: mustTR(t, tr),
	}
}

// =============================================================================
// SCN-META-01 — Single insert accepted
// =============================================================================
func Test_SCN_META_01_SingleInsertAccepted(t *testing.T) {
	flID := seedFlow(t, false)
	store := newStore(t)

	res, err := store.InsertSegments(context.Background(), metastore.InsertBatch{
		FlowID:              flID,
		Segments:            []domain.Segment{makeSegment(t, "o1", "[0:0_10:0)")},
		ControlledStorageID: "default",
	})
	if err != nil {
		t.Fatalf("InsertSegments: %v", err)
	}
	if got, want := res.AcceptedIndices, []int{0}; !equalInts(got, want) {
		t.Errorf("AcceptedIndices: got %v, want %v", got, want)
	}
	if len(res.RejectedIndices) != 0 {
		t.Errorf("RejectedIndices: got %v, want empty", res.RejectedIndices)
	}

	// Visible via List.
	page, err := store.ListSegments(context.Background(), metastore.ListQuery{FlowID: flID, Limit: 100})
	if err != nil {
		t.Fatalf("ListSegments: %v", err)
	}
	if len(page.Items) != 1 {
		t.Errorf("page.Items: got %d, want 1", len(page.Items))
	}
}

// =============================================================================
// SCN-META-02 — Bulk 5 non-overlapping all accepted
// =============================================================================
func Test_SCN_META_02_BulkFiveNonOverlapping(t *testing.T) {
	flID := seedFlow(t, false)
	store := newStore(t)

	segs := make([]domain.Segment, 5)
	for i := range segs {
		segs[i] = makeSegment(t, fmt.Sprintf("o%d", i), fmt.Sprintf("[%d:0_%d:0)", i*10, (i+1)*10))
	}
	res, err := store.InsertSegments(context.Background(), metastore.InsertBatch{FlowID: flID, Segments: segs, ControlledStorageID: "default"})
	if err != nil {
		t.Fatalf("InsertSegments: %v", err)
	}
	if got, want := res.AcceptedIndices, []int{0, 1, 2, 3, 4}; !equalInts(got, want) {
		t.Errorf("AcceptedIndices: got %v, want %v", got, want)
	}
	if len(res.RejectedIndices) != 0 {
		t.Errorf("RejectedIndices: got %v, want empty", res.RejectedIndices)
	}
}

// =============================================================================
// SCN-META-03 — Against-existing overlap → whole-batch reject
// =============================================================================
func Test_SCN_META_03_AgainstExistingOverlapWholeBatchReject(t *testing.T) {
	flID := seedFlow(t, false)
	store := newStore(t)

	// pre-existing row at [10:0_20:0)
	if _, err := store.InsertSegments(context.Background(), metastore.InsertBatch{
		FlowID:              flID,
		Segments:            []domain.Segment{makeSegment(t, "o-existing", "[10:0_20:0)")},
		ControlledStorageID: "default",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	res, err := store.InsertSegments(context.Background(), metastore.InsertBatch{
		FlowID: flID,
		Segments: []domain.Segment{
			makeSegment(t, "o-a", "[5:0_8:0)"),
			makeSegment(t, "o-b", "[15:0_18:0)"), // overlaps existing
			makeSegment(t, "o-c", "[25:0_30:0)"),
		},
	})
	if !errors.Is(err, metastore.ErrSegmentOverlap) {
		t.Fatalf("err: got %v, want ErrSegmentOverlap", err)
	}
	if len(res.AcceptedIndices) != 0 || len(res.RejectedIndices) != 0 {
		t.Errorf("InsertResult must be zero on overlap: got %+v", res)
	}

	// only the seeded row remains — none of the batch persisted.
	count := queryInt(t, `SELECT COUNT(*) FROM segments WHERE flow_id = $1`, flID)
	if count != 1 {
		t.Errorf("segments count: got %d, want 1 (seed only)", count)
	}
}

// =============================================================================
// SCN-META-04 — Within-batch overlap → whole-batch reject
// =============================================================================
func Test_SCN_META_04_WithinBatchOverlapWholeBatchReject(t *testing.T) {
	flID := seedFlow(t, false)
	store := newStore(t)

	res, err := store.InsertSegments(context.Background(), metastore.InsertBatch{
		FlowID: flID,
		Segments: []domain.Segment{
			makeSegment(t, "o0", "[0:0_10:0)"),
			makeSegment(t, "o1", "[5:0_15:0)"), // overlaps i=0
			makeSegment(t, "o2", "[20:0_30:0)"),
		},
	})
	if !errors.Is(err, metastore.ErrSegmentOverlap) {
		t.Fatalf("err: got %v, want ErrSegmentOverlap", err)
	}
	if len(res.AcceptedIndices) != 0 || len(res.RejectedIndices) != 0 {
		t.Errorf("InsertResult must be zero on overlap: got %+v", res)
	}
	count := queryInt(t, `SELECT COUNT(*) FROM segments WHERE flow_id = $1`, flID)
	if count != 0 {
		t.Errorf("segments must be empty on within-batch overlap: count=%d", count)
	}
}

// =============================================================================
// SCN-META-05 — Three colliding rows → whole-batch reject (D-SVC-SEG-02)
// =============================================================================
func Test_SCN_META_05_ThreeCollidingRowsWholeBatchReject(t *testing.T) {
	flID := seedFlow(t, false)
	store := newStore(t)
	_, err := store.InsertSegments(context.Background(), metastore.InsertBatch{
		FlowID: flID,
		Segments: []domain.Segment{
			makeSegment(t, "o0", "[0:0_10:0)"),
			makeSegment(t, "o1", "[5:0_15:0)"),
			makeSegment(t, "o2", "[8:0_12:0)"),
		},
	})
	if !errors.Is(err, metastore.ErrSegmentOverlap) {
		t.Fatalf("err: got %v, want ErrSegmentOverlap", err)
	}
	count := queryInt(t, `SELECT COUNT(*) FROM segments WHERE flow_id = $1`, flID)
	if count != 0 {
		t.Errorf("segments must be empty: count=%d", count)
	}
}

// =============================================================================
// SCN-META-06 — Adjacent ranges accepted
// =============================================================================
func Test_SCN_META_06_AdjacentRangesAccepted(t *testing.T) {
	flID := seedFlow(t, false)
	store := newStore(t)
	res, err := store.InsertSegments(context.Background(), metastore.InsertBatch{
		FlowID: flID,
		Segments: []domain.Segment{
			makeSegment(t, "o0", "[0:0_10:0)"),
			makeSegment(t, "o1", "[10:0_20:0)"),
		},
		ControlledStorageID: "default",
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got, want := res.AcceptedIndices, []int{0, 1}; !equalInts(got, want) {
		t.Errorf("AcceptedIndices: got %v, want %v", got, want)
	}
}

// =============================================================================
// SCN-META-07 — Read-only flow → ErrFlowReadOnly
// =============================================================================
func Test_SCN_META_07_ReadOnlyFlow(t *testing.T) {
	flID := seedFlow(t, true)
	store := newStore(t)
	_, err := store.InsertSegments(context.Background(), metastore.InsertBatch{
		FlowID:   flID,
		Segments: []domain.Segment{makeSegment(t, "o0", "[0:0_5:0)")},
	})
	if !errors.Is(err, metastore.ErrFlowReadOnly) {
		t.Errorf("err: got %v, want ErrFlowReadOnly", err)
	}
}

// =============================================================================
// SCN-META-08 — Round-trip preserves all fields
// =============================================================================
func Test_SCN_META_08_RoundTripAllFields(t *testing.T) {
	flID := seedFlow(t, false)
	store := newStore(t)
	tsOff := mustTS(t, "-5:0")
	sampleOff := int64(42)
	sampleCount := int64(7)
	keyFrames := int64(3)
	in := domain.Segment{
		ObjectID:      "o-rt",
		Timerange:     mustTR(t, "[0:0_10:0)"),
		TSOffset:      &tsOff,
		KeyFrameCount: &keyFrames,
		SampleOffset:  &sampleOff,
		SampleCount:   &sampleCount,
		GetURLs: []domain.GetURL{
			{URL: "https://x", Label: "alt", Controlled: false},
		},
	}
	if _, err := store.InsertSegments(context.Background(), metastore.InsertBatch{
		FlowID: flID, Segments: []domain.Segment{in},
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	page, err := store.ListSegments(context.Background(), metastore.ListQuery{FlowID: flID, Limit: 100})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("Items: %d, want 1", len(page.Items))
	}
	out := page.Items[0]
	if out.ObjectID != in.ObjectID {
		t.Errorf("ObjectID: %q != %q", out.ObjectID, in.ObjectID)
	}
	if out.TSOffset == nil || out.TSOffset.String() != tsOff.String() {
		t.Errorf("TSOffset: %v != %v", out.TSOffset, &tsOff)
	}
	if out.KeyFrameCount == nil || *out.KeyFrameCount != keyFrames {
		t.Errorf("KeyFrameCount: %v != %d", out.KeyFrameCount, keyFrames)
	}
	if out.SampleOffset == nil || *out.SampleOffset != sampleOff {
		t.Errorf("SampleOffset: %v != %d", out.SampleOffset, sampleOff)
	}
	if out.SampleCount == nil || *out.SampleCount != sampleCount {
		t.Errorf("SampleCount: %v != %d", out.SampleCount, sampleCount)
	}
	if len(out.GetURLs) != 1 || out.GetURLs[0].URL != "https://x" {
		t.Errorf("GetURLs: %+v", out.GetURLs)
	}
	if out.CreatedAt.IsZero() {
		t.Error("CreatedAt: server should populate")
	}
}

func mustTS(t *testing.T, s string) timerange.Timestamp {
	t.Helper()
	ts, err := timerange.ParseTimestamp(s)
	if err != nil {
		t.Fatalf("ParseTimestamp %q: %v", s, err)
	}
	return ts
}

// =============================================================================
// SCN-META-10 — DeleteSegmentsByTimerange removes all matching, atomic
// =============================================================================
func Test_SCN_META_10_DeleteAtomicMatches(t *testing.T) {
	flID := seedFlow(t, false)
	store := newStore(t)
	segs := []domain.Segment{
		makeSegment(t, "oA", "[0:0_10:0)"),
		makeSegment(t, "oB", "[10:0_20:0)"),
		makeSegment(t, "oC", "[20:0_30:0)"),
		makeSegment(t, "oD", "[30:0_40:0)"),
	}
	if _, err := store.InsertSegments(context.Background(), metastore.InsertBatch{FlowID: flID, Segments: segs, ControlledStorageID: "default"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	res, err := store.DeleteSegmentsByTimerange(context.Background(), metastore.DeleteQuery{
		FlowID: flID, Timerange: mustTR(t, "[10:0_30:0)"),
	})
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if res.DeletedCount != 2 {
		t.Errorf("DeletedCount: got %d, want 2", res.DeletedCount)
	}

	page, _ := store.ListSegments(context.Background(), metastore.ListQuery{FlowID: flID, Limit: 100})
	if len(page.Items) != 2 {
		t.Errorf("remaining: %d, want 2", len(page.Items))
	}

	// objects oB, oC have ref_count 0 but still present (no reaping by metastore).
	for _, oid := range []string{"oB", "oC"} {
		rc := queryInt(t, `SELECT ref_count FROM objects WHERE id = $1`, oid)
		if rc != 0 {
			t.Errorf("object %s ref_count: got %d, want 0", oid, rc)
		}
		reaping := queryBool(t, `SELECT reaping FROM objects WHERE id = $1`, oid)
		if reaping {
			t.Errorf("object %s reaping: must remain false post-delete", oid)
		}
	}
}

// =============================================================================
// SCN-META-11 — Ref-count decrement; cross-flow shared object's count not zero
// =============================================================================
func Test_SCN_META_11_RefCountDecrementCrossFlow(t *testing.T) {
	flA := seedFlow(t, false)
	flB := seedFlow(t, false)
	store := newStore(t)

	if _, err := store.InsertSegments(context.Background(), metastore.InsertBatch{
		FlowID: flA, Segments: []domain.Segment{
			makeSegment(t, "o-A", "[0:0_10:0)"),
			makeSegment(t, "o-B", "[10:0_20:0)"),
			makeSegment(t, "o-C", "[20:0_30:0)"),
		},
		ControlledStorageID: "default",
	}); err != nil {
		t.Fatalf("seed flA: %v", err)
	}
	if _, err := store.InsertSegments(context.Background(), metastore.InsertBatch{
		FlowID:              flB,
		Segments:            []domain.Segment{makeSegment(t, "o-B", "[0:0_10:0)")},
		ControlledStorageID: "default",
	}); err != nil {
		t.Fatalf("seed flB: %v", err)
	}
	// o-B ref_count == 2 now
	if rc := queryInt(t, `SELECT ref_count FROM objects WHERE id = 'o-B'`); rc != 2 {
		t.Fatalf("o-B ref_count: got %d, want 2", rc)
	}

	res, err := store.DeleteSegmentsByTimerange(context.Background(), metastore.DeleteQuery{
		FlowID: flA, Timerange: mustTR(t, "[0:0_30:0)"),
	})
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if res.DeletedCount != 3 {
		t.Errorf("DeletedCount: got %d, want 3", res.DeletedCount)
	}
	// o-A ref_count = 0; o-B ref_count = 1; o-C ref_count = 0.
	if rc := queryInt(t, `SELECT ref_count FROM objects WHERE id = 'o-A'`); rc != 0 {
		t.Errorf("o-A ref_count: %d, want 0", rc)
	}
	if rc := queryInt(t, `SELECT ref_count FROM objects WHERE id = 'o-B'`); rc != 1 {
		t.Errorf("o-B ref_count: %d, want 1 (shared with flB)", rc)
	}
	if rc := queryInt(t, `SELECT ref_count FROM objects WHERE id = 'o-C'`); rc != 0 {
		t.Errorf("o-C ref_count: %d, want 0", rc)
	}
}

// =============================================================================
// SCN-META-13 — Insert/Delete refresh flow segments_updated and timerange
// =============================================================================
func Test_SCN_META_13_FlowTimestampsRefresh(t *testing.T) {
	flID := seedFlow(t, false)
	store := newStore(t)

	// Anchor an older segments_updated. The flow seed left it NULL.
	exec(t, `UPDATE flows SET segments_updated = now() - interval '1 hour' WHERE id = $1`, flID)
	t0 := queryTime(t, `SELECT segments_updated FROM flows WHERE id = $1`, flID)

	if _, err := store.InsertSegments(context.Background(), metastore.InsertBatch{
		FlowID:              flID,
		Segments:            []domain.Segment{makeSegment(t, "o0", "[100:0_200:0)")},
		ControlledStorageID: "default",
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	t1 := queryTime(t, `SELECT segments_updated FROM flows WHERE id = $1`, flID)
	if !t1.After(t0) {
		t.Errorf("segments_updated did not advance: t0=%v t1=%v", t0, t1)
	}
	tr := queryStrPtr(t, `SELECT timerange FROM flows WHERE id = $1`, flID)
	if tr == nil || *tr != "[100:0_200:0)" {
		t.Errorf("flow.timerange: got %v, want [100:0_200:0)", tr)
	}

	if _, err := store.DeleteSegmentsByTimerange(context.Background(), metastore.DeleteQuery{
		FlowID: flID, Timerange: mustTR(t, "[100:0_200:0)"),
	}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	t2 := queryTime(t, `SELECT segments_updated FROM flows WHERE id = $1`, flID)
	if !t2.After(t1) {
		t.Errorf("segments_updated did not advance after delete: t1=%v t2=%v", t1, t2)
	}
}

func queryTime(t *testing.T, sql string, args ...any) time.Time {
	t.Helper()
	var v time.Time
	if err := metastore.SharedTestPool().QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		t.Fatalf("queryTime %q: %v", sql, err)
	}
	return v
}

// =============================================================================
// SCN-META-14a — List on missing flow → ErrFlowNotFound
// =============================================================================
func Test_SCN_META_14a_ListOnMissingFlow(t *testing.T) {
	store := newStore(t)
	_, err := store.ListSegments(context.Background(), metastore.ListQuery{FlowID: uuid.New(), Limit: 100})
	if !errors.Is(err, metastore.ErrFlowNotFound) {
		t.Errorf("err: got %v, want ErrFlowNotFound", err)
	}
}

// =============================================================================
// SCN-META-14b — List on existing flow with no rows → empty page, nil err
// =============================================================================
func Test_SCN_META_14b_ListOnExistingFlowEmpty(t *testing.T) {
	flID := seedFlow(t, false)
	store := newStore(t)
	page, err := store.ListSegments(context.Background(), metastore.ListQuery{
		FlowID: flID, Limit: 100, Timerange: trPtr(mustTR(t, "[1000:0_2000:0)")),
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(page.Items) != 0 || page.NextCursor != "" || page.Count != 0 {
		t.Errorf("empty page expected: %+v", page)
	}
}

func trPtr(tr timerange.TimeRange) *timerange.TimeRange { return &tr }

// =============================================================================
// SCN-META-19 — Insert on missing flow → ErrFlowNotFound
// =============================================================================
func Test_SCN_META_19_InsertOnMissingFlow(t *testing.T) {
	store := newStore(t)
	_, err := store.InsertSegments(context.Background(), metastore.InsertBatch{
		FlowID:   uuid.New(),
		Segments: []domain.Segment{makeSegment(t, "o0", "[0:0_10:0)")},
	})
	if !errors.Is(err, metastore.ErrFlowNotFound) {
		t.Errorf("err: got %v, want ErrFlowNotFound", err)
	}
}

// =============================================================================
// SCN-META-20 — Delete on missing flow → ErrFlowNotFound
// =============================================================================
func Test_SCN_META_20_DeleteOnMissingFlow(t *testing.T) {
	store := newStore(t)
	_, err := store.DeleteSegmentsByTimerange(context.Background(), metastore.DeleteQuery{
		FlowID: uuid.New(), Timerange: mustTR(t, "[0:0_10:0)"),
	})
	if !errors.Is(err, metastore.ErrFlowNotFound) {
		t.Errorf("err: got %v, want ErrFlowNotFound", err)
	}
}

// =============================================================================
// SCN-META-21 — Delete on read-only flow → ErrFlowReadOnly
// =============================================================================
func Test_SCN_META_21_DeleteOnReadOnlyFlow(t *testing.T) {
	flID := seedFlow(t, false)
	store := newStore(t)
	if _, err := store.InsertSegments(context.Background(), metastore.InsertBatch{
		FlowID:              flID,
		Segments:            []domain.Segment{makeSegment(t, "o0", "[0:0_10:0)")},
		ControlledStorageID: "default",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	exec(t, `UPDATE flows SET read_only = true WHERE id = $1`, flID)
	_, err := store.DeleteSegmentsByTimerange(context.Background(), metastore.DeleteQuery{
		FlowID: flID, Timerange: mustTR(t, "[0:0_10:0)"),
	})
	if !errors.Is(err, metastore.ErrFlowReadOnly) {
		t.Errorf("err: got %v, want ErrFlowReadOnly", err)
	}
}

// =============================================================================
// SCN-META-22 — Delete with object_id filter
// =============================================================================
func Test_SCN_META_22_DeleteWithObjectIDFilter(t *testing.T) {
	flID := seedFlow(t, false)
	store := newStore(t)
	if _, err := store.InsertSegments(context.Background(), metastore.InsertBatch{
		FlowID: flID, Segments: []domain.Segment{
			makeSegment(t, "o-X", "[0:0_10:0)"),
			makeSegment(t, "o-Y", "[10:0_20:0)"),
			makeSegment(t, "o-X", "[20:0_30:0)"),
		},
		ControlledStorageID: "default",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	objX := "o-X"
	res, err := store.DeleteSegmentsByTimerange(context.Background(), metastore.DeleteQuery{
		FlowID: flID, Timerange: mustTR(t, "[0:0_30:0)"), ObjectID: &objX,
	})
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if res.DeletedCount != 2 {
		t.Errorf("DeletedCount: %d, want 2", res.DeletedCount)
	}
	page, _ := store.ListSegments(context.Background(), metastore.ListQuery{FlowID: flID, Limit: 100})
	if len(page.Items) != 1 || page.Items[0].ObjectID != "o-Y" {
		t.Errorf("remaining: %+v, want [o-Y]", page.Items)
	}
}

// =============================================================================
// SCN-META-23 — Invalid cursor → ErrInvalidCursor
// =============================================================================
func Test_SCN_META_23_InvalidCursor(t *testing.T) {
	flID := seedFlow(t, false)
	store := newStore(t)
	if _, err := store.InsertSegments(context.Background(), metastore.InsertBatch{
		FlowID:              flID,
		Segments:            []domain.Segment{makeSegment(t, "o0", "[0:0_10:0)")},
		ControlledStorageID: "default",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, err := store.ListSegments(context.Background(), metastore.ListQuery{
		FlowID: flID, Limit: 10, Page: "not-a-valid-cursor",
	})
	if !errors.Is(err, metastore.ErrInvalidCursor) {
		t.Errorf("err: got %v, want ErrInvalidCursor", err)
	}
}

// =============================================================================
// SCN-META-24 — List filters: timerange, object_id, reverse_order
// =============================================================================
func Test_SCN_META_24_ListFilters(t *testing.T) {
	flID := seedFlow(t, false)
	store := newStore(t)

	// 10 segments alternating o-A/o-B at [i*10, (i+1)*10).
	segs := make([]domain.Segment, 10)
	for i := range segs {
		obj := "o-A"
		if i%2 == 1 {
			obj = "o-B"
		}
		segs[i] = makeSegment(t, obj, fmt.Sprintf("[%d:0_%d:0)", i*10, (i+1)*10))
	}
	if _, err := store.InsertSegments(context.Background(), metastore.InsertBatch{FlowID: flID, Segments: segs, ControlledStorageID: "default"}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// (a) timerange filter [0:0_50:0) → 5 rows
	page, err := store.ListSegments(context.Background(), metastore.ListQuery{
		FlowID: flID, Limit: 100, Timerange: trPtr(mustTR(t, "[0:0_50:0)")),
	})
	if err != nil {
		t.Fatalf("(a): %v", err)
	}
	if len(page.Items) != 5 {
		t.Errorf("(a) timerange filter: got %d items, want 5", len(page.Items))
	}

	// (b) object_id filter
	objA := "o-A"
	page, err = store.ListSegments(context.Background(), metastore.ListQuery{
		FlowID: flID, Limit: 100, ObjectID: &objA,
	})
	if err != nil {
		t.Fatalf("(b): %v", err)
	}
	if len(page.Items) != 5 {
		t.Errorf("(b) object filter: got %d, want 5", len(page.Items))
	}

	// (c) reverse_order
	page, err = store.ListSegments(context.Background(), metastore.ListQuery{
		FlowID: flID, Limit: 100, ReverseOrder: true,
	})
	if err != nil {
		t.Fatalf("(c): %v", err)
	}
	if len(page.Items) != 10 {
		t.Fatalf("(c) all: got %d, want 10", len(page.Items))
	}
	first := page.Items[0].Timerange.String()
	if first != "[90:0_100:0)" {
		t.Errorf("(c) first item under reverse order: got %q, want [90:0_100:0)", first)
	}
}

// =============================================================================
// SCN-META-25 — Limit clamp (>1000 → 1000)
// =============================================================================
func Test_SCN_META_25_LimitClamp(t *testing.T) {
	flID := seedFlow(t, false)
	store := newStore(t)
	// Seed 1500 segments — small enough to fit in CI tx, large enough to exercise clamp.
	const N = 1500
	segs := make([]domain.Segment, N)
	for i := range segs {
		segs[i] = makeSegment(t, fmt.Sprintf("o%d", i), fmt.Sprintf("[%d:0_%d:0)", i, i+1))
	}
	if _, err := store.InsertSegments(context.Background(), metastore.InsertBatch{FlowID: flID, Segments: segs, ControlledStorageID: "default"}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	page, err := store.ListSegments(context.Background(), metastore.ListQuery{FlowID: flID, Limit: 5000})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(page.Items) > 1000 {
		t.Errorf("len(Items) = %d, want ≤ 1000", len(page.Items))
	}
	if page.EffectiveLimit != 1000 {
		t.Errorf("EffectiveLimit: got %d, want 1000", page.EffectiveLimit)
	}
}

// =============================================================================
// SCN-META-26 — Eternity timerange removes all
// =============================================================================
func Test_SCN_META_26_DeleteEternityTimerange(t *testing.T) {
	flID := seedFlow(t, false)
	store := newStore(t)
	segs := []domain.Segment{
		makeSegment(t, "oA", "[0:0_10:0)"),
		makeSegment(t, "oB", "[100:0_200:0)"),
		makeSegment(t, "oC", "[500:0_600:0)"),
	}
	if _, err := store.InsertSegments(context.Background(), metastore.InsertBatch{FlowID: flID, Segments: segs, ControlledStorageID: "default"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	res, err := store.DeleteSegmentsByTimerange(context.Background(), metastore.DeleteQuery{
		FlowID: flID, Timerange: mustTR(t, "_"),
	})
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if res.DeletedCount != 3 {
		t.Errorf("DeletedCount: got %d, want 3", res.DeletedCount)
	}
	page, _ := store.ListSegments(context.Background(), metastore.ListQuery{FlowID: flID, Limit: 100})
	if len(page.Items) != 0 {
		t.Errorf("residual segments: %d", len(page.Items))
	}
}

// =============================================================================
// SCN-META-27 — Insert races GC reap → ErrSegmentObjectReaping, batch rolls back
// =============================================================================
func Test_SCN_META_27_InsertRacesGCReap(t *testing.T) {
	flID := seedFlow(t, false)
	store := newStore(t)
	// Pre-create object with reaping=true (GC has claimed it).
	exec(t, `INSERT INTO objects (id, ref_count, reaping) VALUES ($1, 0, true)`, "o-reaping")
	t.Cleanup(func() { exec(t, `DELETE FROM objects WHERE id = 'o-reaping'`) })

	res, err := store.InsertSegments(context.Background(), metastore.InsertBatch{
		FlowID:              flID,
		Segments:            []domain.Segment{makeSegment(t, "o-reaping", "[0:0_10:0)")},
		ControlledStorageID: "default",
	})
	if !errors.Is(err, metastore.ErrSegmentObjectReaping) {
		t.Fatalf("err: got %v, want ErrSegmentObjectReaping", err)
	}
	if len(res.AcceptedIndices) != 0 {
		t.Errorf("must not accept any when batch races a reap")
	}
	count := queryInt(t, `SELECT COUNT(*) FROM segments WHERE flow_id = $1`, flID)
	if count != 0 {
		t.Errorf("segments persisted despite reap race: count=%d", count)
	}
	// objects row untouched
	rc := queryInt(t, `SELECT ref_count FROM objects WHERE id = 'o-reaping'`)
	if rc != 0 {
		t.Errorf("o-reaping ref_count: got %d, want 0", rc)
	}
}

// =============================================================================
// SCN-META-28 — BYOS object insert sets storage_id NULL
// =============================================================================
func Test_SCN_META_28_BYOSStorageIDNull(t *testing.T) {
	flID := seedFlow(t, false)
	store := newStore(t)
	in := domain.Segment{
		ObjectID:  "o-byos",
		Timerange: mustTR(t, "[0:0_10:0)"),
		GetURLs: []domain.GetURL{
			{URL: "https://byos.example/x", Label: "primary", Controlled: false},
		},
	}
	if _, err := store.InsertSegments(context.Background(), metastore.InsertBatch{
		FlowID: flID, Segments: []domain.Segment{in},
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	sid := queryStrPtr(t, `SELECT storage_id FROM objects WHERE id = 'o-byos'`)
	if sid != nil {
		t.Errorf("storage_id: got %v, want NULL (BYOS)", sid)
	}
}

// =============================================================================
// SCN-META-29 — Controlled insert sets storage_id once; immutable thereafter
// =============================================================================
func Test_SCN_META_29_ControlledStorageIDSetOnce(t *testing.T) {
	flA := seedFlow(t, false)
	flB := seedFlow(t, false)
	store := newStore(t)
	const storageID = "primary"
	in := domain.Segment{
		ObjectID:  "o-ctrl",
		Timerange: mustTR(t, "[0:0_10:0)"),
		// controlled ⇒ no GetURLs
	}
	if _, err := store.InsertSegments(context.Background(), metastore.InsertBatch{
		FlowID: flA, Segments: []domain.Segment{in}, ControlledStorageID: storageID,
	}); err != nil {
		t.Fatalf("insert flA: %v", err)
	}
	sid := queryStrPtr(t, `SELECT storage_id FROM objects WHERE id = 'o-ctrl'`)
	if sid == nil || *sid != storageID {
		t.Fatalf("storage_id: %v, want %q after controlled insert", sid, storageID)
	}

	// Cross-flow share — caller passes a DIFFERENT ControlledStorageID;
	// stored value must NOT mutate (INV-META-14 immutability).
	in2 := domain.Segment{
		ObjectID:  "o-ctrl",
		Timerange: mustTR(t, "[0:0_10:0)"),
	}
	if _, err := store.InsertSegments(context.Background(), metastore.InsertBatch{
		FlowID: flB, Segments: []domain.Segment{in2}, ControlledStorageID: "secondary",
	}); err != nil {
		t.Fatalf("insert flB: %v", err)
	}
	sid2 := queryStrPtr(t, `SELECT storage_id FROM objects WHERE id = 'o-ctrl'`)
	if sid2 == nil || *sid2 != storageID {
		t.Errorf("storage_id mutated: got %v, want %q (immutable)", sid2, storageID)
	}
	rc := queryInt(t, `SELECT ref_count FROM objects WHERE id = 'o-ctrl'`)
	if rc != 2 {
		t.Errorf("ref_count: %d, want 2 (cross-flow share)", rc)
	}
}

// =============================================================================
// SCN-META-30 — ListSegments round-trips classifier shape (GetURLs)
// =============================================================================
//
// The handler's URL projection (BR-HTTP-05, SCN-HTTP-15a–e) classifies
// each returned segment as controlled (`len(seg.GetURLs) == 0`) or BYOS
// (`len(seg.GetURLs) > 0`). ListSegments MUST preserve the stored shape:
// controlled rows return with empty GetURLs; BYOS rows return with the
// stored entries verbatim. The metastore stamps `objects.storage_id`
// from the same classifier on insert (BR-META-07).
func Test_SCN_META_30_ListPreservesGetURLsClassifier(t *testing.T) {
	flID := seedFlow(t, false)
	store := newStore(t)

	ctrl := domain.Segment{
		ObjectID:  "o-ctrl-30",
		Timerange: mustTR(t, "[0:0_5:0)"),
	}
	byos := domain.Segment{
		ObjectID:  "o-byos-30",
		Timerange: mustTR(t, "[5:0_10:0)"),
		GetURLs: []domain.GetURL{
			{URL: "https://byos.example/x", Label: "primary", Controlled: false},
		},
	}
	if _, err := store.InsertSegments(context.Background(), metastore.InsertBatch{
		FlowID: flID, Segments: []domain.Segment{ctrl, byos}, ControlledStorageID: "primary",
	}); err != nil {
		t.Fatalf("InsertSegments: %v", err)
	}

	page, err := store.ListSegments(context.Background(), metastore.ListQuery{FlowID: flID, Limit: 100})
	if err != nil {
		t.Fatalf("ListSegments: %v", err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("page.Items: got %d, want 2", len(page.Items))
	}
	byID := map[string]domain.Segment{}
	for _, s := range page.Items {
		byID[s.ObjectID] = s
	}
	if got, ok := byID["o-ctrl-30"]; !ok || len(got.GetURLs) != 0 {
		t.Errorf("o-ctrl-30: GetURLs=%v (ok=%v), want empty (controlled classifier)", got.GetURLs, ok)
	}
	if got, ok := byID["o-byos-30"]; !ok || len(got.GetURLs) != 1 {
		t.Errorf("o-byos-30: GetURLs=%v (ok=%v), want 1 entry (BYOS classifier)", got.GetURLs, ok)
	}
	// Storage row classifier matches: controlled has storage_id, BYOS has NULL.
	sidCtrl := queryStrPtr(t, `SELECT storage_id FROM objects WHERE id = 'o-ctrl-30'`)
	if sidCtrl == nil || *sidCtrl != "primary" {
		t.Errorf("o-ctrl-30 storage_id: got %v, want %q", sidCtrl, "primary")
	}
	sidByos := queryStrPtr(t, `SELECT storage_id FROM objects WHERE id = 'o-byos-30'`)
	if sidByos != nil {
		t.Errorf("o-byos-30 storage_id: got %v, want NULL", sidByos)
	}
}

// =============================================================================
// SCN-META-CONC-01 — Concurrent inserts on colliding range — exactly one wins
// =============================================================================
func Test_SCN_META_CONC_01_ConcurrentCollidingInserts(t *testing.T) {
	if testing.Short() {
		t.Skip("concurrency test")
	}
	flID := seedFlow(t, false)
	store := newStore(t)

	const N = 20
	var wg sync.WaitGroup
	var mu sync.Mutex
	var wins, overlapErrs int
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, err := store.InsertSegments(context.Background(), metastore.InsertBatch{
				FlowID:              flID,
				Segments:            []domain.Segment{makeSegment(t, fmt.Sprintf("o-%d", idx), "[0:0_10:0)")},
				ControlledStorageID: "default",
			})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				wins++
			case errors.Is(err, metastore.ErrSegmentOverlap):
				overlapErrs++
			default:
				t.Errorf("unexpected err: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if wins != 1 {
		t.Errorf("wins: got %d, want exactly 1", wins)
	}
	if overlapErrs != N-1 {
		t.Errorf("overlap errors: got %d, want %d", overlapErrs, N-1)
	}
	count := queryInt(t, `SELECT COUNT(*) FROM segments WHERE flow_id = $1`, flID)
	if count != 1 {
		t.Errorf("final segment count: got %d, want 1", count)
	}
}

// =============================================================================
// helpers
// =============================================================================

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// segWithObjTR builds a domain.Segment with object_timerange set.
func segWithObjTR(t *testing.T, objectID, tr, objTR string) domain.Segment {
	t.Helper()
	otr := mustTR(t, objTR)
	return domain.Segment{
		ObjectID:        objectID,
		Timerange:       mustTR(t, tr),
		ObjectTimerange: &otr,
	}
}

// =============================================================================
// SCN-META-09 — Mid-batch error rolls back the whole tx
// =============================================================================
//
// Trigger (chosen): BR-META-19. First segment carries a stored
// object_timerange [0:0_60:0); second segment for the same object
// extends to [50:0_70:0) which is NOT contained — must reject and
// roll back the whole tx (atomicity, INV-META-05).
func Test_SCN_META_09_MidBatchErrorRollback(t *testing.T) {
	flID := seedFlow(t, false)
	store := newStore(t)

	// Seed: object o-T9 with stored object_timerange [0:0_60:0).
	if _, err := store.InsertSegments(context.Background(), metastore.InsertBatch{
		FlowID: flID,
		Segments: []domain.Segment{
			segWithObjTR(t, "o-T9", "[0:0_5:0)", "[0:0_60:0)"),
		},
		ControlledStorageID: "default",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Pin segments_updated to a known value so the post-call assertion
	// can detect any (unexpected) advance via queryTime (non-nullable).
	exec(t, `UPDATE flows SET segments_updated = now() - interval '1 minute' WHERE id = $1`, flID)
	t0 := queryTime(t, `SELECT segments_updated FROM flows WHERE id = $1`, flID)

	// Three-segment batch: idx 1 violates BR-META-19 (extends beyond stored).
	batch := []domain.Segment{
		makeSegment(t, "o-other-09", "[100:0_110:0)"),
		segWithObjTR(t, "o-T9", "[200:0_210:0)", "[50:0_70:0)"), // BAD
		makeSegment(t, "o-tail-09", "[300:0_310:0)"),
	}
	res, err := store.InsertSegments(context.Background(), metastore.InsertBatch{
		FlowID:              flID,
		Segments:            batch,
		ControlledStorageID: "default",
	})
	if err == nil {
		t.Fatalf("err = nil; want apperror.ErrInvalidObjectTimerange")
	}
	var ae *apperror.AppError
	if !errors.As(err, &ae) || ae.Code != apperror.ErrInvalidObjectTimerange {
		t.Fatalf("err = %v; want AppError code=%q", err, apperror.ErrInvalidObjectTimerange)
	}
	if len(res.AcceptedIndices) != 0 || len(res.RejectedIndices) != 0 {
		t.Errorf("InsertResult must be zero on whole-batch rollback: got %+v", res)
	}

	// Whole tx rolled back: only the seed survives.
	count := queryInt(t, `SELECT COUNT(*) FROM segments WHERE flow_id = $1`, flID)
	if count != 1 {
		t.Errorf("segments count = %d, want 1 (seed only)", count)
	}
	for _, oid := range []string{"o-other-09", "o-tail-09"} {
		exists := queryInt(t, `SELECT COUNT(*) FROM objects WHERE id = $1`, oid)
		if exists != 0 {
			t.Errorf("objects row %q persisted across rollback", oid)
		}
	}
	// segments_updated unchanged.
	t1 := queryTime(t, `SELECT segments_updated FROM flows WHERE id = $1`, flID)
	if !t1.Equal(t0) {
		t.Errorf("segments_updated advanced across rollback: t0=%v t1=%v", t0, t1)
	}
}

// =============================================================================
// SCN-META-12 — Pre-cancelled ctx → context.Canceled, no persisted state
// =============================================================================
func Test_SCN_META_12_PreCancelledContext(t *testing.T) {
	flID := seedFlow(t, false)
	store := newStore(t)
	// segments_updated starts NULL on a fresh flow — anchor it so the
	// post-call comparison can use queryTime (non-nullable scan).
	exec(t, `UPDATE flows SET segments_updated = now() - interval '1 hour' WHERE id = $1`, flID)
	t0 := queryTime(t, `SELECT segments_updated FROM flows WHERE id = $1`, flID)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel BEFORE the call

	segs := make([]domain.Segment, 5)
	for i := range segs {
		segs[i] = makeSegment(t, fmt.Sprintf("o-12-%d", i), fmt.Sprintf("[%d:0_%d:0)", i*10, (i+1)*10))
	}
	_, err := store.InsertSegments(ctx, metastore.InsertBatch{FlowID: flID, Segments: segs, ControlledStorageID: "default"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v; want context.Canceled", err)
	}

	// No segments persisted.
	count := queryInt(t, `SELECT COUNT(*) FROM segments WHERE flow_id = $1`, flID)
	if count != 0 {
		t.Errorf("segments persisted on cancelled ctx: count=%d", count)
	}
	// segments_updated untouched.
	t1 := queryTime(t, `SELECT segments_updated FROM flows WHERE id = $1`, flID)
	if !t1.Equal(t0) {
		t.Errorf("segments_updated advanced on cancelled ctx: t0=%v t1=%v", t0, t1)
	}
}

// =============================================================================
// SCN-META-15 — Stable pagination under concurrent writes
// =============================================================================
//
// Walk a 200-segment flow with Limit=50. After page 1, delete a row in
// the *already-visited* range [5:0_10:0) and re-insert it (gets a new
// segment id, same lower_ns). The cursor uses (lower_ns, id) tuple
// > comparison; the re-inserted row has lower_ns BELOW the cursor's
// lower_ns and so MUST NOT re-appear. Walk visits 200 distinct segs.
func Test_SCN_META_15_StablePaginationUnderWrites(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: 200-row seed + cursor walk")
	}
	flID := seedFlow(t, false)
	store := newStore(t)

	const N = 200
	segs := make([]domain.Segment, N)
	for i := range segs {
		segs[i] = makeSegment(t, fmt.Sprintf("o-15-%d", i), fmt.Sprintf("[%d:0_%d:0)", i*10, (i+1)*10))
	}
	if _, err := store.InsertSegments(context.Background(), metastore.InsertBatch{FlowID: flID, Segments: segs, ControlledStorageID: "default"}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	const limit = 50
	visited := map[string]int{}
	addPage := func(items []domain.Segment) {
		for _, it := range items {
			visited[it.Timerange.String()]++
		}
	}

	// Page 1.
	page, err := store.ListSegments(context.Background(), metastore.ListQuery{FlowID: flID, Limit: limit})
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	if len(page.Items) != limit {
		t.Fatalf("page 1 items = %d, want %d", len(page.Items), limit)
	}
	addPage(page.Items)
	cursor := page.NextCursor
	if cursor == "" {
		t.Fatal("page 1: NextCursor empty")
	}

	// Concurrent write: delete + re-insert a row in the already-visited range.
	if _, err := store.DeleteSegmentsByTimerange(context.Background(), metastore.DeleteQuery{
		FlowID: flID, Timerange: mustTR(t, "[5:0_10:0)"),
	}); err != nil {
		t.Fatalf("delete during walk: %v", err)
	}
	if _, err := store.InsertSegments(context.Background(), metastore.InsertBatch{
		FlowID:              flID,
		Segments:            []domain.Segment{makeSegment(t, "o-15-0-replay", "[5:0_10:0)")},
		ControlledStorageID: "default",
	}); err != nil {
		t.Fatalf("re-insert during walk: %v", err)
	}

	// Walk pages 2..4.
	for p := 2; p <= 4; p++ {
		page, err = store.ListSegments(context.Background(), metastore.ListQuery{
			FlowID: flID, Limit: limit, Page: cursor,
		})
		if err != nil {
			t.Fatalf("page %d: %v", p, err)
		}
		if len(page.Items) != limit {
			t.Errorf("page %d items = %d, want %d", p, len(page.Items), limit)
		}
		addPage(page.Items)
		cursor = page.NextCursor
	}

	// Total visited ≥ 200 distinct. Original [5:0_10:0) was visited on
	// page 1; the replayed row's timerange is identical so its presence
	// would *not* show via timerange-uniqueness alone — guard explicitly:
	// no timerange visited twice.
	for tr, n := range visited {
		if n != 1 {
			t.Errorf("timerange %q visited %d times, want 1", tr, n)
		}
	}
	if len(visited) < N {
		t.Errorf("distinct timeranges visited = %d, want ≥ %d", len(visited), N)
	}
}

// =============================================================================
// SCN-META-16 — object_timerange validation (BR-META-19)
// =============================================================================

func Test_SCN_META_16a_ObjectTimerangeContainedAccepted(t *testing.T) {
	flID := seedFlow(t, false)
	store := newStore(t)

	if _, err := store.InsertSegments(context.Background(), metastore.InsertBatch{
		FlowID:              flID,
		Segments:            []domain.Segment{segWithObjTR(t, "o-T16a", "[0:0_5:0)", "[0:0_60:0)")},
		ControlledStorageID: "default",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// New segment, contained inside stored object_timerange.
	if _, err := store.InsertSegments(context.Background(), metastore.InsertBatch{
		FlowID:              flID,
		Segments:            []domain.Segment{segWithObjTR(t, "o-T16a", "[10:0_20:0)", "[10:0_20:0)")},
		ControlledStorageID: "default",
	}); err != nil {
		t.Errorf("contained object_timerange must be accepted, got %v", err)
	}
}

func Test_SCN_META_16b_ObjectTimerangeExtendsRejected(t *testing.T) {
	flID := seedFlow(t, false)
	store := newStore(t)

	if _, err := store.InsertSegments(context.Background(), metastore.InsertBatch{
		FlowID:              flID,
		Segments:            []domain.Segment{segWithObjTR(t, "o-T16b", "[0:0_5:0)", "[0:0_60:0)")},
		ControlledStorageID: "default",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, err := store.InsertSegments(context.Background(), metastore.InsertBatch{
		FlowID:              flID,
		Segments:            []domain.Segment{segWithObjTR(t, "o-T16b", "[100:0_110:0)", "[50:0_70:0)")},
		ControlledStorageID: "default",
	})
	if err == nil {
		t.Fatalf("err = nil; want apperror.ErrInvalidObjectTimerange")
	}
	var ae *apperror.AppError
	if !errors.As(err, &ae) || ae.Code != apperror.ErrInvalidObjectTimerange {
		t.Errorf("err = %v; want AppError code=%q", err, apperror.ErrInvalidObjectTimerange)
	}
}

func Test_SCN_META_16c_NewObjectTimerangeNilAccepted(t *testing.T) {
	flID := seedFlow(t, false)
	store := newStore(t)

	if _, err := store.InsertSegments(context.Background(), metastore.InsertBatch{
		FlowID:              flID,
		Segments:            []domain.Segment{segWithObjTR(t, "o-T16c", "[0:0_5:0)", "[0:0_60:0)")},
		ControlledStorageID: "default",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Nil object_timerange on new segment — must be accepted; stored unchanged.
	if _, err := store.InsertSegments(context.Background(), metastore.InsertBatch{
		FlowID:              flID,
		Segments:            []domain.Segment{makeSegment(t, "o-T16c", "[100:0_110:0)")},
		ControlledStorageID: "default",
	}); err != nil {
		t.Errorf("nil new object_timerange must be accepted, got %v", err)
	}
	// The earliest non-NULL stored value persists for future validation.
	stored := queryStrPtr(t,
		`SELECT object_timerange FROM segments
		   WHERE object_id = $1 AND object_timerange IS NOT NULL
		   ORDER BY created_at ASC LIMIT 1`, "o-T16c")
	if stored == nil || *stored != "[0:0_60:0)" {
		t.Errorf("stored object_timerange = %v, want [0:0_60:0)", stored)
	}
}

func Test_SCN_META_16d_StoredNilThenSetBecomesStored(t *testing.T) {
	flID := seedFlow(t, false)
	store := newStore(t)

	// First insert: object_timerange = NULL.
	if _, err := store.InsertSegments(context.Background(), metastore.InsertBatch{
		FlowID:              flID,
		Segments:            []domain.Segment{makeSegment(t, "o-T16d", "[0:0_5:0)")},
		ControlledStorageID: "default",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Second insert: object_timerange = [0:0_10:0). Must accept (no
	// stored non-NULL to compare against) and become the stored value.
	if _, err := store.InsertSegments(context.Background(), metastore.InsertBatch{
		FlowID:              flID,
		Segments:            []domain.Segment{segWithObjTR(t, "o-T16d", "[10:0_15:0)", "[0:0_10:0)")},
		ControlledStorageID: "default",
	}); err != nil {
		t.Fatalf("second insert: %v", err)
	}
	// Third insert with extending object_timerange would fail now.
	_, err := store.InsertSegments(context.Background(), metastore.InsertBatch{
		FlowID:              flID,
		Segments:            []domain.Segment{segWithObjTR(t, "o-T16d", "[100:0_110:0)", "[0:0_20:0)")},
		ControlledStorageID: "default",
	})
	var ae *apperror.AppError
	if !errors.As(err, &ae) || ae.Code != apperror.ErrInvalidObjectTimerange {
		t.Errorf("third insert err = %v; want AppError code=%q after stored became non-NULL",
			err, apperror.ErrInvalidObjectTimerange)
	}
}

// =============================================================================
// SCN-META-17 — InsertSegments creates `objects` row when missing
// =============================================================================
func Test_SCN_META_17_ObjectsRowCreatedWhenMissing(t *testing.T) {
	flID := seedFlow(t, false)
	store := newStore(t)

	pre := queryInt(t, `SELECT COUNT(*) FROM objects WHERE id = $1`, "o-new-17")
	if pre != 0 {
		t.Fatalf("preflight: o-new-17 already exists")
	}

	// BYOS-shaped segment (carries GetURLs) so the objects row is created
	// with storage_id NULL — no ControlledStorageID needed.
	byos := domain.Segment{
		ObjectID:  "o-new-17",
		Timerange: mustTR(t, "[0:0_10:0)"),
		GetURLs:   []domain.GetURL{{URL: "https://byos.example/x", Label: "primary", Controlled: false}},
	}
	if _, err := store.InsertSegments(context.Background(), metastore.InsertBatch{
		FlowID:   flID,
		Segments: []domain.Segment{byos},
	}); err != nil {
		t.Fatalf("InsertSegments: %v", err)
	}

	rc := queryInt(t, `SELECT ref_count FROM objects WHERE id = $1`, "o-new-17")
	if rc != 1 {
		t.Errorf("ref_count = %d, want 1", rc)
	}
	reaping := queryBool(t, `SELECT reaping FROM objects WHERE id = $1`, "o-new-17")
	if reaping {
		t.Errorf("reaping = true, want false")
	}
	// BYOS-shaped segment ⇒ storage_id NULL.
	sid := queryStrPtr(t, `SELECT storage_id FROM objects WHERE id = $1`, "o-new-17")
	if sid != nil {
		t.Errorf("storage_id = %v, want NULL (BYOS-shaped)", sid)
	}
}

// =============================================================================
// SCN-META-18 — Ref-count increments on existing object
// =============================================================================
func Test_SCN_META_18_RefCountIncrementOnExistingObject(t *testing.T) {
	flA := seedFlow(t, false)
	flB := seedFlow(t, false)
	store := newStore(t)

	// Pre-populate via flow A.
	if _, err := store.InsertSegments(context.Background(), metastore.InsertBatch{
		FlowID:              flA,
		Segments:            []domain.Segment{makeSegment(t, "o-pre-18", "[0:0_10:0)")},
		ControlledStorageID: "default",
	}); err != nil {
		t.Fatalf("seed flA: %v", err)
	}
	if rc := queryInt(t, `SELECT ref_count FROM objects WHERE id = $1`, "o-pre-18"); rc != 1 {
		t.Fatalf("after flA: ref_count = %d, want 1", rc)
	}

	if _, err := store.InsertSegments(context.Background(), metastore.InsertBatch{
		FlowID:              flB,
		Segments:            []domain.Segment{makeSegment(t, "o-pre-18", "[0:0_10:0)")},
		ControlledStorageID: "default",
	}); err != nil {
		t.Fatalf("insert flB: %v", err)
	}
	if rc := queryInt(t, `SELECT ref_count FROM objects WHERE id = $1`, "o-pre-18"); rc != 2 {
		t.Errorf("after flB: ref_count = %d, want 2 (cross-flow share)", rc)
	}
}

// =============================================================================
// SCN-META-CONC-02 — Read-only flip during concurrent inserts
// =============================================================================
//
// Trace: REQ-META-09, INV-META-12, NFR-META-REL-05.
// 20 inserter goroutines + 20 toggler goroutines on the same flow.
// Invariant: every InsertSegments that returned nil left exactly its
// segments persisted; every call observing read_only=true returned
// ErrFlowReadOnly with no rows persisted (no half-commit).
func Test_SCN_META_CONC_02_ReadOnlyFlipDuringInsert(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: concurrency")
	}
	flID := seedFlow(t, false)
	store := newStore(t)

	const inserters = 20
	const togglers = 20
	const togglesPerWorker = 10

	type insertOutcome struct {
		idx int
		err error
	}
	out := make(chan insertOutcome, inserters)

	var wg sync.WaitGroup
	for i := 0; i < inserters; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			lo := idx * 1000
			seg := makeSegment(t, fmt.Sprintf("o-conc2-%d", idx),
				fmt.Sprintf("[%d:0_%d:0)", lo, lo+10))
			_, err := store.InsertSegments(context.Background(), metastore.InsertBatch{
				FlowID:              flID,
				Segments:            []domain.Segment{seg},
				ControlledStorageID: "default",
			})
			out <- insertOutcome{idx: idx, err: err}
		}(i)
	}
	for i := 0; i < togglers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for k := 0; k < togglesPerWorker; k++ {
				v := k%2 == 0
				exec(t, `UPDATE flows SET read_only = $1 WHERE id = $2`, v, flID)
			}
		}()
	}
	wg.Wait()
	close(out)

	// Restore writeable state for cleanup.
	exec(t, `UPDATE flows SET read_only = false WHERE id = $1`, flID)

	successIDs := map[string]struct{}{}
	for o := range out {
		switch {
		case o.err == nil:
			successIDs[fmt.Sprintf("o-conc2-%d", o.idx)] = struct{}{}
		case errors.Is(o.err, metastore.ErrFlowReadOnly):
			// Expected when worker raced with read_only=true.
		default:
			t.Errorf("unexpected err for inserter %d: %v", o.idx, o.err)
		}
	}

	// Every successful insert MUST have persisted its segment.
	rows, err := metastore.SharedTestPool().Query(context.Background(),
		`SELECT object_id FROM segments WHERE flow_id = $1`, flID)
	if err != nil {
		t.Fatalf("post-state query: %v", err)
	}
	defer rows.Close()
	persisted := map[string]struct{}{}
	for rows.Next() {
		var oid string
		if err := rows.Scan(&oid); err != nil {
			t.Fatalf("scan: %v", err)
		}
		persisted[oid] = struct{}{}
	}
	if len(persisted) != len(successIDs) {
		t.Errorf("persisted=%d successes=%d (mismatch implies half-commit)", len(persisted), len(successIDs))
	}
	for id := range successIDs {
		if _, ok := persisted[id]; !ok {
			t.Errorf("nil-error insert %q has no row (half-commit)", id)
		}
	}
}

// =============================================================================
// SCN-META-CONC-03 — 50 concurrent disjoint-range inserts
// =============================================================================
func Test_SCN_META_CONC_03_FiftyConcurrentDisjointInserts(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: concurrency")
	}
	flID := seedFlow(t, false)
	store := newStore(t)

	const N = 50
	var wg sync.WaitGroup
	errs := make(chan error, N)
	start := time.Now()
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			lo := idx*100 + 100000
			seg := makeSegment(t, fmt.Sprintf("o-conc3-%d", idx),
				fmt.Sprintf("[%d:0_%d:0)", lo, lo+50))
			_, err := store.InsertSegments(context.Background(), metastore.InsertBatch{
				FlowID:              flID,
				Segments:            []domain.Segment{seg},
				ControlledStorageID: "default",
			})
			errs <- err
		}(i)
	}
	wg.Wait()
	elapsed := time.Since(start)
	close(errs)

	for err := range errs {
		if err != nil {
			t.Errorf("disjoint insert err = %v, want nil", err)
		}
	}
	count := queryInt(t, `SELECT COUNT(*) FROM segments WHERE flow_id = $1`, flID)
	if count != N {
		t.Errorf("segments count = %d, want %d", count, N)
	}
	// NFR-META-PERF-05: 50 disjoint inserts in ≤ 5s.
	const budget = 5 * time.Second
	if elapsed > budget {
		t.Errorf("elapsed = %v, want ≤ %v", elapsed, budget)
	}
}

// =============================================================================
// SCN-META-29 (extension) — Controlled storage_id stamped from InsertBatch
// =============================================================================
//
// Companion to SCN-META-29 above. The original asserts immutability across
// cross-flow upserts; this variant asserts the value comes from the caller
// (InsertBatch.ControlledStorageID), not a package constant. Classifier
// is `len(GetURLs) == 0` per BR-META-07.
func Test_SCN_META_29_ControlledStorageIDFromInsertBatch(t *testing.T) {
	flID := seedFlow(t, false)
	store := newStore(t)
	const wantStorageID = "test-backend-xyz"
	seg := domain.Segment{
		ObjectID:  "o-ctrl-from-batch",
		Timerange: mustTR(t, "[0:0_10:0)"),
		// no GetURLs => controlled
	}
	if _, err := store.InsertSegments(context.Background(), metastore.InsertBatch{
		FlowID:              flID,
		Segments:            []domain.Segment{seg},
		ControlledStorageID: wantStorageID,
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	sid := queryStrPtr(t, `SELECT storage_id FROM objects WHERE id = 'o-ctrl-from-batch'`)
	if sid == nil || *sid != wantStorageID {
		t.Errorf("storage_id: got %v, want %q", sid, wantStorageID)
	}
}

// =============================================================================
// SCN-META-31 — Controlled segment + empty ControlledStorageID fails loud
// =============================================================================
func Test_SCN_META_31_ErrControlledStorageNotConfigured(t *testing.T) {
	flID := seedFlow(t, false)
	store := newStore(t)
	seg := domain.Segment{
		ObjectID:  "o-ctrl-missing-cfg",
		Timerange: mustTR(t, "[0:0_10:0)"),
	}
	_, err := store.InsertSegments(context.Background(), metastore.InsertBatch{
		FlowID:              flID,
		Segments:            []domain.Segment{seg},
		ControlledStorageID: "",
	})
	if !errors.Is(err, metastore.ErrControlledStorageNotConfigured) {
		t.Fatalf("err: %v, want ErrControlledStorageNotConfigured", err)
	}
	cnt := queryInt(t, `SELECT COUNT(*) FROM objects WHERE id = 'o-ctrl-missing-cfg'`)
	if cnt != 0 {
		t.Errorf("objects row leaked on error: got %d rows, want 0 (rolled back)", cnt)
	}
}

// =============================================================================
// SCN-META-32 — BYOS-only batch tolerates empty ControlledStorageID
// =============================================================================
func Test_SCN_META_32_BYOSOnlyAllowsEmptyControlledStorageID(t *testing.T) {
	flID := seedFlow(t, false)
	store := newStore(t)
	seg := domain.Segment{
		ObjectID:  "o-byos-no-cfg",
		Timerange: mustTR(t, "[0:0_10:0)"),
		GetURLs: []domain.GetURL{
			{URL: "https://byos.example/x", Label: "primary"},
		},
	}
	if _, err := store.InsertSegments(context.Background(), metastore.InsertBatch{
		FlowID:              flID,
		Segments:            []domain.Segment{seg},
		ControlledStorageID: "",
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	sid := queryStrPtr(t, `SELECT storage_id FROM objects WHERE id = 'o-byos-no-cfg'`)
	if sid != nil {
		t.Errorf("storage_id: got %v, want NULL (BYOS)", sid)
	}
}

// TestIsObjectRegistered covers the object-existence probe used by the
// storage service to reject re-allocation of an already-registered object.
func TestIsObjectRegistered(t *testing.T) {
	flowID := seedFlow(t, false)
	store := newStore(t)
	ctx := context.Background()

	if _, err := store.InsertSegments(ctx, metastore.InsertBatch{
		FlowID:              flowID,
		Segments:            []domain.Segment{makeSegment(t, "known-obj", "[0:0_1:0)")},
		ControlledStorageID: "default",
	}); err != nil {
		t.Fatalf("InsertSegments: %v", err)
	}

	registered, err := store.IsObjectRegistered(ctx, "known-obj")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !registered {
		t.Error("known-obj: want registered=true")
	}

	registered, err = store.IsObjectRegistered(ctx, "unknown-obj")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if registered {
		t.Error("unknown-obj: want registered=false")
	}
}
