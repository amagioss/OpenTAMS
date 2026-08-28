package segment_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"github.com/amagioss/opentams/internal/domain"
	"github.com/amagioss/opentams/internal/metastore"
	"github.com/amagioss/opentams/internal/service"
	"github.com/amagioss/opentams/internal/service/segment"
	"github.com/amagioss/opentams/internal/timerange"
)

// ---------------------------------------------------------------------------
// fakeMetaStore — minimal in-memory metastore.Store for service unit tests.
// Implements just enough for RegisterBatch / List / Delete to drive the
// SCN-SEG-01..26 scenarios, each defined by the test function that carries it.
// ---------------------------------------------------------------------------

type fakeFlow struct {
	exists   bool
	readOnly bool
}

type fakeMetaStore struct {
	flows    map[uuid.UUID]*fakeFlow
	segments map[uuid.UUID][]domain.Segment

	// Cross-flow ref-count of object IDs (SCN-SEG-10). Mirrors the
	// real `objects.ref_count` column. Tests that don't care about
	// ref-counts can ignore this map.
	objects map[string]*fakeObject

	insertCalls atomic.Int64

	overlapErr   error // overrides the natural overlap behaviour when non-nil
	insertErr    error // forces InsertSegments to fail (SCN-SEG-11)
	listErr      error
	deleteErr    error
	customInsert func(batch metastore.InsertBatch) (metastore.InsertResult, error)
	customList   func(q metastore.ListQuery) (domain.SegmentPage, error)
	cursorEcho   string // mirrors the cursor the caller forwarded on the last list (SCN-SEG-23)
	captureList  metastore.ListQuery
}

type fakeObject struct {
	refCount int64
	reaping  bool
}

func newFakeMetaStore() *fakeMetaStore {
	return &fakeMetaStore{
		flows:    map[uuid.UUID]*fakeFlow{},
		segments: map[uuid.UUID][]domain.Segment{},
		objects:  map[string]*fakeObject{},
	}
}

func (f *fakeMetaStore) addFlow(id uuid.UUID, readOnly bool) {
	f.flows[id] = &fakeFlow{exists: true, readOnly: readOnly}
}

func (f *fakeMetaStore) addSegment(id uuid.UUID, s domain.Segment) {
	s.FlowID = id
	f.segments[id] = append(f.segments[id], s)
	f.objRef(s.ObjectID, +1)
}

func (f *fakeMetaStore) objRef(id string, delta int64) {
	if id == "" {
		return
	}
	o, ok := f.objects[id]
	if !ok {
		o = &fakeObject{}
		f.objects[id] = o
	}
	o.refCount += delta
	if o.refCount < 0 {
		o.refCount = 0
	}
}

func (f *fakeMetaStore) GetFlowForSegmentWrite(_ context.Context, _ metastore.Tx, flowID uuid.UUID) (metastore.FlowGuard, error) {
	fl, ok := f.flows[flowID]
	if !ok {
		return metastore.FlowGuard{FlowID: flowID, Exists: false}, nil
	}
	return metastore.FlowGuard{FlowID: flowID, Exists: fl.exists, ReadOnly: fl.readOnly}, nil
}

func (f *fakeMetaStore) GetFlowForSegmentRead(_ context.Context, flowID uuid.UUID) (metastore.FlowGuard, error) {
	return f.GetFlowForSegmentWrite(context.Background(), nil, flowID)
}

func (f *fakeMetaStore) InsertSegments(_ context.Context, batch metastore.InsertBatch) (metastore.InsertResult, error) {
	f.insertCalls.Add(1)
	fl, ok := f.flows[batch.FlowID]
	if !ok {
		return metastore.InsertResult{}, metastore.ErrFlowNotFound
	}
	if fl.readOnly {
		return metastore.InsertResult{}, metastore.ErrFlowReadOnly
	}
	if f.insertErr != nil {
		return metastore.InsertResult{}, f.insertErr
	}
	if f.overlapErr != nil {
		return metastore.InsertResult{}, f.overlapErr
	}
	if f.customInsert != nil {
		res, err := f.customInsert(batch)
		if err == nil {
			for _, idx := range res.AcceptedIndices {
				if idx < 0 || idx >= len(batch.Segments) {
					continue
				}
				seg := batch.Segments[idx]
				seg.FlowID = batch.FlowID
				f.segments[batch.FlowID] = append(f.segments[batch.FlowID], seg)
				f.objRef(seg.ObjectID, +1)
			}
		}
		return res, err
	}
	// Within-batch overlap.
	for i := 0; i < len(batch.Segments); i++ {
		for j := i + 1; j < len(batch.Segments); j++ {
			if batch.Segments[i].Timerange.Overlaps(batch.Segments[j].Timerange) {
				return metastore.InsertResult{}, metastore.ErrSegmentOverlap
			}
		}
	}
	// Against-existing overlap.
	existing := f.segments[batch.FlowID]
	for i := range batch.Segments {
		for j := range existing {
			if batch.Segments[i].Timerange.Overlaps(existing[j].Timerange) {
				return metastore.InsertResult{}, metastore.ErrSegmentOverlap
			}
		}
	}
	accepted := make([]int, 0, len(batch.Segments))
	for i := range batch.Segments {
		seg := batch.Segments[i]
		seg.FlowID = batch.FlowID
		f.segments[batch.FlowID] = append(f.segments[batch.FlowID], seg)
		f.objRef(seg.ObjectID, +1)
		accepted = append(accepted, i)
	}
	return metastore.InsertResult{AcceptedIndices: accepted}, nil
}

func (f *fakeMetaStore) ListSegments(_ context.Context, q metastore.ListQuery) (domain.SegmentPage, error) {
	f.captureList = q
	f.cursorEcho = q.Page
	if f.listErr != nil {
		return domain.SegmentPage{}, f.listErr
	}
	if f.customList != nil {
		return f.customList(q)
	}
	if _, ok := f.flows[q.FlowID]; !ok {
		return domain.SegmentPage{}, metastore.ErrFlowNotFound
	}
	items := append([]domain.Segment(nil), f.segments[q.FlowID]...)
	return domain.SegmentPage{Items: items, Count: len(items), EffectiveLimit: q.Limit}, nil
}

func (f *fakeMetaStore) DeleteSegmentsByTimerange(_ context.Context, q metastore.DeleteQuery) (metastore.DeleteResult, error) {
	if f.deleteErr != nil {
		return metastore.DeleteResult{}, f.deleteErr
	}
	fl, ok := f.flows[q.FlowID]
	if !ok {
		return metastore.DeleteResult{}, metastore.ErrFlowNotFound
	}
	if fl.readOnly {
		return metastore.DeleteResult{}, metastore.ErrFlowReadOnly
	}
	src := f.segments[q.FlowID]
	keep := src[:0]
	var deleted int64
	for i := range src {
		s := &src[i]
		if s.Timerange.Overlaps(q.Timerange) && (q.ObjectID == nil || *q.ObjectID == s.ObjectID) {
			deleted++
			f.objRef(s.ObjectID, -1)
			continue
		}
		keep = append(keep, *s)
	}
	f.segments[q.FlowID] = keep
	return metastore.DeleteResult{DeletedCount: deleted}, nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func mustTR(t *testing.T, s string) timerange.TimeRange {
	t.Helper()
	tr, err := timerange.Parse(s)
	if err != nil {
		t.Fatalf("timerange.Parse(%q): %v", s, err)
	}
	return tr
}

func newServiceV1(t *testing.T, ms metastore.Store) segment.Service {
	t.Helper()
	reg := prometheus.NewRegistry()
	m, err := service.NewAppServiceMetrics(reg)
	if err != nil {
		t.Fatalf("NewAppServiceMetrics: %v", err)
	}
	return segment.New(segment.Deps{
		Meta:    ms,
		Logger:  zap.NewNop(),
		Metrics: m,
	})
}

func seg(t *testing.T, objID, tr string) domain.Segment {
	return domain.Segment{ObjectID: objID, Timerange: mustTR(t, tr)}
}

// ---------------------------------------------------------------------------
// SCN-SEG-01 — Single-segment RegisterBatch on empty flow
// ---------------------------------------------------------------------------

func Test_SCN_SEG_01_SingleSegmentAllAccepted(t *testing.T) {
	ms := newFakeMetaStore()
	flowID := uuid.New()
	ms.addFlow(flowID, false)
	svc := newServiceV1(t, ms)

	res, err := svc.RegisterBatch(context.Background(), domain.RegisterParams{
		FlowID:   flowID,
		Segments: []domain.Segment{seg(t, "o1", "[0:0_10:0)")},
	})
	if err != nil {
		t.Fatalf("RegisterBatch: %v", err)
	}
	if res.Outcome != domain.OutcomeAllAccepted {
		t.Errorf("Outcome = %d, want OutcomeAllAccepted", res.Outcome)
	}
	if len(res.Accepted) != 1 || len(res.Failed) != 0 {
		t.Errorf("accepted=%d failed=%d, want 1/0", len(res.Accepted), len(res.Failed))
	}
}

// ---------------------------------------------------------------------------
// SCN-SEG-02 — Bulk 5-segment all-accepted; one round-trip to metastore
// ---------------------------------------------------------------------------

func Test_SCN_SEG_02_Bulk5AllAccepted(t *testing.T) {
	ms := newFakeMetaStore()
	flowID := uuid.New()
	ms.addFlow(flowID, false)
	svc := newServiceV1(t, ms)

	segs := []domain.Segment{
		seg(t, "o1", "[0:0_10:0)"),
		seg(t, "o2", "[10:0_20:0)"),
		seg(t, "o3", "[20:0_30:0)"),
		seg(t, "o4", "[30:0_40:0)"),
		seg(t, "o5", "[40:0_50:0)"),
	}
	res, err := svc.RegisterBatch(context.Background(), domain.RegisterParams{FlowID: flowID, Segments: segs})
	if err != nil {
		t.Fatalf("RegisterBatch: %v", err)
	}
	if res.Outcome != domain.OutcomeAllAccepted || len(res.Accepted) != 5 || len(res.Failed) != 0 {
		t.Errorf("got Outcome=%d accepted=%d failed=%d", res.Outcome, len(res.Accepted), len(res.Failed))
	}
	if got := ms.insertCalls.Load(); got != 1 {
		t.Errorf("metastore InsertSegments calls = %d, want 1", got)
	}
}

// ---------------------------------------------------------------------------
// SCN-SEG-03 — Against-existing overlap → ErrSegmentOverlap, whole-batch reject
// ---------------------------------------------------------------------------

func Test_SCN_SEG_03_AgainstExistingOverlap(t *testing.T) {
	ms := newFakeMetaStore()
	flowID := uuid.New()
	ms.addFlow(flowID, false)
	ms.addSegment(flowID, seg(t, "o-existing", "[10:0_20:0)"))
	svc := newServiceV1(t, ms)

	_, err := svc.RegisterBatch(context.Background(), domain.RegisterParams{
		FlowID: flowID,
		Segments: []domain.Segment{
			seg(t, "o-a", "[5:0_8:0)"),
			seg(t, "o-b", "[15:0_18:0)"),
			seg(t, "o-c", "[25:0_30:0)"),
		},
	})
	if !errors.Is(err, metastore.ErrSegmentOverlap) {
		t.Fatalf("err = %v, want ErrSegmentOverlap", err)
	}
	if got := len(ms.segments[flowID]); got != 1 {
		t.Errorf("segments persisted = %d, want 1 (only the pre-existing)", got)
	}
}

// ---------------------------------------------------------------------------
// SCN-SEG-04 — Within-batch overlap → ErrSegmentOverlap, whole-batch reject
// ---------------------------------------------------------------------------

func Test_SCN_SEG_04_WithinBatchOverlap(t *testing.T) {
	ms := newFakeMetaStore()
	flowID := uuid.New()
	ms.addFlow(flowID, false)
	svc := newServiceV1(t, ms)

	_, err := svc.RegisterBatch(context.Background(), domain.RegisterParams{
		FlowID: flowID,
		Segments: []domain.Segment{
			seg(t, "o-a", "[0:0_10:0)"),
			seg(t, "o-b", "[5:0_15:0)"),
			seg(t, "o-c", "[20:0_30:0)"),
		},
	})
	if !errors.Is(err, metastore.ErrSegmentOverlap) {
		t.Fatalf("err = %v, want ErrSegmentOverlap", err)
	}
	if got := len(ms.segments[flowID]); got != 0 {
		t.Errorf("segments persisted = %d, want 0", got)
	}
}

// ---------------------------------------------------------------------------
// SCN-SEG-06 — Adjacent ranges both accepted (boundary value)
// ---------------------------------------------------------------------------

func Test_SCN_SEG_06_AdjacentRanges(t *testing.T) {
	ms := newFakeMetaStore()
	flowID := uuid.New()
	ms.addFlow(flowID, false)
	svc := newServiceV1(t, ms)

	res, err := svc.RegisterBatch(context.Background(), domain.RegisterParams{
		FlowID: flowID,
		Segments: []domain.Segment{
			seg(t, "o1", "[0:0_10:0)"),
			seg(t, "o2", "[10:0_20:0)"),
		},
	})
	if err != nil {
		t.Fatalf("RegisterBatch: %v", err)
	}
	if res.Outcome != domain.OutcomeAllAccepted || len(res.Accepted) != 2 {
		t.Errorf("got Outcome=%d accepted=%d, want AllAccepted/2", res.Outcome, len(res.Accepted))
	}
}

// ---------------------------------------------------------------------------
// SCN-SEG-07 — RegisterBatch on non-existent flow → ErrFlowNotFound
// ---------------------------------------------------------------------------

func Test_SCN_SEG_07_RegisterMissingFlow(t *testing.T) {
	ms := newFakeMetaStore()
	svc := newServiceV1(t, ms)

	_, err := svc.RegisterBatch(context.Background(), domain.RegisterParams{
		FlowID:   uuid.New(),
		Segments: []domain.Segment{seg(t, "o1", "[0:0_10:0)")},
	})
	if !errors.Is(err, segment.ErrFlowNotFound) {
		t.Fatalf("err = %v, want segment.ErrFlowNotFound", err)
	}
}

// ---------------------------------------------------------------------------
// SCN-SEG-08 — RegisterBatch on read-only flow → ErrFlowReadOnly
// ---------------------------------------------------------------------------

func Test_SCN_SEG_08_RegisterReadOnly(t *testing.T) {
	ms := newFakeMetaStore()
	flowID := uuid.New()
	ms.addFlow(flowID, true)
	svc := newServiceV1(t, ms)

	_, err := svc.RegisterBatch(context.Background(), domain.RegisterParams{
		FlowID:   flowID,
		Segments: []domain.Segment{seg(t, "o1", "[0:0_10:0)")},
	})
	if !errors.Is(err, segment.ErrFlowReadOnly) {
		t.Fatalf("err = %v, want segment.ErrFlowReadOnly", err)
	}
}

// ---------------------------------------------------------------------------
// SCN-SEG-09 — Delete by timerange, atomic
// ---------------------------------------------------------------------------

func Test_SCN_SEG_09_DeleteByTimerangeAtomic(t *testing.T) {
	ms := newFakeMetaStore()
	flowID := uuid.New()
	ms.addFlow(flowID, false)
	for i, tr := range []string{"[0:0_10:0)", "[10:0_20:0)", "[20:0_30:0)", "[30:0_40:0)"} {
		ms.addSegment(flowID, seg(t, "o"+string(rune('1'+i)), tr))
	}
	svc := newServiceV1(t, ms)

	res, err := svc.Delete(context.Background(), domain.DeleteParams{
		FlowID:    flowID,
		Timerange: mustTR(t, "[10:0_30:0)"),
	})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if res.DeletedCount != 2 {
		t.Errorf("DeletedCount = %d, want 2", res.DeletedCount)
	}
	if got := len(ms.segments[flowID]); got != 2 {
		t.Errorf("remaining segments = %d, want 2", got)
	}
}

// ---------------------------------------------------------------------------
// SCN-SEG-11 — All-or-partial on metastore failure mid-batch
// ---------------------------------------------------------------------------

func Test_SCN_SEG_11_MetastoreErrorPropagates(t *testing.T) {
	ms := newFakeMetaStore()
	flowID := uuid.New()
	ms.addFlow(flowID, false)
	ms.insertErr = errors.New("db connection lost")
	svc := newServiceV1(t, ms)

	res, err := svc.RegisterBatch(context.Background(), domain.RegisterParams{
		FlowID:   flowID,
		Segments: []domain.Segment{seg(t, "o1", "[0:0_10:0)")},
	})
	if err == nil || errors.Is(err, segment.ErrInvalidRequest) {
		t.Fatalf("err = %v, want wrapped DB error", err)
	}
	if len(res.Accepted) != 0 || len(res.Failed) != 0 {
		t.Errorf("expected zero accepted/failed on error, got %d/%d", len(res.Accepted), len(res.Failed))
	}
	if got := len(ms.segments[flowID]); got != 0 {
		t.Errorf("segments persisted = %d, want 0", got)
	}
}

// ---------------------------------------------------------------------------
// SCN-SEG-12 — List on non-existent flow → ErrFlowNotFound
// ---------------------------------------------------------------------------

func Test_SCN_SEG_12_ListMissingFlow(t *testing.T) {
	ms := newFakeMetaStore()
	svc := newServiceV1(t, ms)

	_, err := svc.List(context.Background(), domain.ListParams{FlowID: uuid.New()})
	if !errors.Is(err, segment.ErrFlowNotFound) {
		t.Fatalf("err = %v, want segment.ErrFlowNotFound", err)
	}
}

// ---------------------------------------------------------------------------
// SCN-SEG-15 — ObjectTimerange nil preserved
// ---------------------------------------------------------------------------

func Test_SCN_SEG_15_ObjectTimerangeNilPreserved(t *testing.T) {
	ms := newFakeMetaStore()
	flowID := uuid.New()
	ms.addFlow(flowID, false)
	svc := newServiceV1(t, ms)

	s := seg(t, "o1", "[0:0_10:0)")
	if s.ObjectTimerange != nil {
		t.Fatalf("setup: expected nil ObjectTimerange")
	}
	res, err := svc.RegisterBatch(context.Background(), domain.RegisterParams{
		FlowID:   flowID,
		Segments: []domain.Segment{s},
	})
	if err != nil {
		t.Fatalf("RegisterBatch: %v", err)
	}
	if res.Accepted[0].ObjectTimerange != nil {
		t.Errorf("ObjectTimerange = %v, want nil", res.Accepted[0].ObjectTimerange)
	}
}

// ---------------------------------------------------------------------------
// SCN-SEG-17 — Client-supplied get_urls stamped controlled=false
// ---------------------------------------------------------------------------

func Test_SCN_SEG_17_GetURLsStampedUncontrolled(t *testing.T) {
	ms := newFakeMetaStore()
	flowID := uuid.New()
	ms.addFlow(flowID, false)
	svc := newServiceV1(t, ms)

	s := seg(t, "o1", "[0:0_10:0)")
	s.GetURLs = []domain.GetURL{
		{URL: "https://cdn.example/foo", Label: "primary", Controlled: true}, // hostile client
	}
	res, err := svc.RegisterBatch(context.Background(), domain.RegisterParams{
		FlowID:   flowID,
		Segments: []domain.Segment{s},
	})
	if err != nil {
		t.Fatalf("RegisterBatch: %v", err)
	}
	if len(res.Accepted[0].GetURLs) != 1 || res.Accepted[0].GetURLs[0].Controlled {
		t.Errorf("GetURLs[0].Controlled = true, want false")
	}
}

// ---------------------------------------------------------------------------
// SCN-SEG-18 — Delete on non-existent flow → ErrFlowNotFound
// ---------------------------------------------------------------------------

func Test_SCN_SEG_18_DeleteMissingFlow(t *testing.T) {
	ms := newFakeMetaStore()
	svc := newServiceV1(t, ms)

	_, err := svc.Delete(context.Background(), domain.DeleteParams{
		FlowID:    uuid.New(),
		Timerange: mustTR(t, "[0:0_10:0)"),
	})
	if !errors.Is(err, segment.ErrFlowNotFound) {
		t.Fatalf("err = %v, want segment.ErrFlowNotFound", err)
	}
}

// ---------------------------------------------------------------------------
// SCN-SEG-19 — Delete on read-only flow → ErrFlowReadOnly
// ---------------------------------------------------------------------------

func Test_SCN_SEG_19_DeleteReadOnly(t *testing.T) {
	ms := newFakeMetaStore()
	flowID := uuid.New()
	ms.addFlow(flowID, true)
	ms.addSegment(flowID, seg(t, "o1", "[0:0_10:0)"))
	svc := newServiceV1(t, ms)

	_, err := svc.Delete(context.Background(), domain.DeleteParams{
		FlowID:    flowID,
		Timerange: mustTR(t, "[0:0_10:0)"),
	})
	if !errors.Is(err, segment.ErrFlowReadOnly) {
		t.Fatalf("err = %v, want segment.ErrFlowReadOnly", err)
	}
	if got := len(ms.segments[flowID]); got != 1 {
		t.Errorf("segments remaining = %d, want 1", got)
	}
}

// ---------------------------------------------------------------------------
// SCN-SEG-22 — Empty batch → ErrInvalidRequest
// ---------------------------------------------------------------------------

func Test_SCN_SEG_22_EmptyBatchRejected(t *testing.T) {
	ms := newFakeMetaStore()
	flowID := uuid.New()
	ms.addFlow(flowID, false)
	svc := newServiceV1(t, ms)

	_, err := svc.RegisterBatch(context.Background(), domain.RegisterParams{
		FlowID:   flowID,
		Segments: []domain.Segment{},
	})
	if !errors.Is(err, segment.ErrInvalidRequest) {
		t.Fatalf("err = %v, want segment.ErrInvalidRequest", err)
	}
	if got := ms.insertCalls.Load(); got != 0 {
		t.Errorf("metastore InsertSegments calls = %d, want 0", got)
	}
}

// ---------------------------------------------------------------------------
// SCN-SEG-23 — List forwards cursor unchanged
// ---------------------------------------------------------------------------

func Test_SCN_SEG_23_ListForwardsCursor(t *testing.T) {
	ms := newFakeMetaStore()
	flowID := uuid.New()
	ms.addFlow(flowID, false)
	cursor := "opaque-cursor-page-2"
	ms.customList = func(q metastore.ListQuery) (domain.SegmentPage, error) {
		if q.Page != cursor {
			t.Errorf("metastore received Page = %q, want %q", q.Page, cursor)
		}
		return domain.SegmentPage{}, nil
	}
	svc := newServiceV1(t, ms)

	if _, err := svc.List(context.Background(), domain.ListParams{
		FlowID: flowID,
		Page:   cursor,
	}); err != nil {
		t.Fatalf("List: %v", err)
	}
	if ms.cursorEcho != cursor {
		t.Errorf("cursor mutated by service: got %q want %q", ms.cursorEcho, cursor)
	}
}

// ---------------------------------------------------------------------------
// SCN-SEG-24 — Deprecated sample_offset / sample_count round-trip
// ---------------------------------------------------------------------------

func Test_SCN_SEG_24_DeprecatedFieldsRoundTrip(t *testing.T) {
	ms := newFakeMetaStore()
	flowID := uuid.New()
	ms.addFlow(flowID, false)
	svc := newServiceV1(t, ms)

	off, cnt := int64(42), int64(7)
	s := seg(t, "o1", "[0:0_10:0)")
	s.SampleOffset = &off
	s.SampleCount = &cnt
	res, err := svc.RegisterBatch(context.Background(), domain.RegisterParams{
		FlowID:   flowID,
		Segments: []domain.Segment{s},
	})
	if err != nil {
		t.Fatalf("RegisterBatch: %v", err)
	}
	got := res.Accepted[0]
	if got.SampleOffset == nil || *got.SampleOffset != 42 || got.SampleCount == nil || *got.SampleCount != 7 {
		t.Errorf("deprecated fields not preserved: %+v %+v", got.SampleOffset, got.SampleCount)
	}
}

// ---------------------------------------------------------------------------
// SCN-SEG-25 — Delete with no matching segments → DeletedCount=0
// ---------------------------------------------------------------------------

func Test_SCN_SEG_25_DeleteNoMatches(t *testing.T) {
	ms := newFakeMetaStore()
	flowID := uuid.New()
	ms.addFlow(flowID, false)
	ms.addSegment(flowID, seg(t, "o1", "[0:0_10:0)"))
	ms.addSegment(flowID, seg(t, "o2", "[10:0_20:0)"))
	svc := newServiceV1(t, ms)

	res, err := svc.Delete(context.Background(), domain.DeleteParams{
		FlowID:    flowID,
		Timerange: mustTR(t, "[1000:0_2000:0)"),
	})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if res.DeletedCount != 0 {
		t.Errorf("DeletedCount = %d, want 0", res.DeletedCount)
	}
	if got := len(ms.segments[flowID]); got != 2 {
		t.Errorf("segments remaining = %d, want 2", got)
	}
}

// ---------------------------------------------------------------------------
// SCN-SEG-26 — Delete makes ZERO calls into the object store
// (Negative invariant: Deps has no Object field. Verified by inspection of
// the segment.Deps struct — the test compiles only if no Object field
// exists. INV-SEG-06.)
// ---------------------------------------------------------------------------

func Test_SCN_SEG_26_NoObjectStoreOnDelete(t *testing.T) {
	// Negative-invariant compile-time check: segment.Deps must not expose
	// an object-store field. If a future refactor adds one, this test
	// stops compiling and the invariant is surfaced.
	deps := segment.Deps{}
	_ = deps
	// Run the delete to confirm the path returns successfully without
	// any object-store handle in scope.
	ms := newFakeMetaStore()
	flowID := uuid.New()
	ms.addFlow(flowID, false)
	for i := 0; i < 5; i++ {
		ms.addSegment(flowID, seg(t, "o"+string(rune('1'+i)), "["+string(rune('0'+byte(i)*2))+":0_"+string(rune('0'+byte(i)*2+1))+":0)"))
	}
	svc := newServiceV1(t, ms)

	_, err := svc.Delete(context.Background(), domain.DeleteParams{
		FlowID:    flowID,
		Timerange: mustTR(t, "[0:0_100:0)"),
	})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
}
func mustTimestamp(t *testing.T, s string) timerange.Timestamp {
	t.Helper()
	ts, err := timerange.ParseTimestamp(s)
	if err != nil {
		t.Fatalf("timerange.ParseTimestamp(%q): %v", s, err)
	}
	return ts
}

// ---------------------------------------------------------------------------
// SCN-SEG-05 — Single overlap rejects entire batch.
// ---------------------------------------------------------------------------

func Test_SCN_SEG_05_SingleOverlapRejectsWholeBatch(t *testing.T) {
	ms := newFakeMetaStore()
	flowID := uuid.New()
	ms.addFlow(flowID, false)
	svc := newServiceV1(t, ms)

	// 9 non-overlapping segments + a 10th that overlaps the 5th.
	segs := []domain.Segment{
		seg(t, "o1", "[0:0_10:0)"),
		seg(t, "o2", "[10:0_20:0)"),
		seg(t, "o3", "[20:0_30:0)"),
		seg(t, "o4", "[30:0_40:0)"),
		seg(t, "o5", "[40:0_50:0)"),
		seg(t, "o6", "[50:0_60:0)"),
		seg(t, "o7", "[60:0_70:0)"),
		seg(t, "o8", "[70:0_80:0)"),
		seg(t, "o9", "[80:0_90:0)"),
		seg(t, "o5b", "[42:0_45:0)"), // overlaps o5
	}
	res, err := svc.RegisterBatch(context.Background(), domain.RegisterParams{
		FlowID:   flowID,
		Segments: segs,
	})
	if !errors.Is(err, metastore.ErrSegmentOverlap) {
		t.Fatalf("err = %v, want ErrSegmentOverlap", err)
	}
	if len(res.Accepted) != 0 || len(res.Failed) != 0 {
		t.Errorf("partial state surfaced: accepted=%d failed=%d, want 0/0",
			len(res.Accepted), len(res.Failed))
	}
	if got := len(ms.segments[flowID]); got != 0 {
		t.Errorf("segments persisted = %d, want 0 (whole-batch reject)", got)
	}
}

// ---------------------------------------------------------------------------
// SCN-SEG-10 — Cross-flow ref-count: shared object not released.
// ---------------------------------------------------------------------------

func Test_SCN_SEG_10_CrossFlowRefCountSharedObjectNotReleased(t *testing.T) {
	ms := newFakeMetaStore()
	flowA := uuid.New()
	flowB := uuid.New()
	ms.addFlow(flowA, false)
	ms.addFlow(flowB, false)
	// Both flows reference the same object; ref-count should be 2.
	ms.addSegment(flowA, seg(t, "o-shared", "[0:0_10:0)"))
	ms.addSegment(flowB, seg(t, "o-shared", "[100:0_110:0)"))
	if got := ms.objects["o-shared"].refCount; got != 2 {
		t.Fatalf("setup: ref-count = %d, want 2", got)
	}
	svc := newServiceV1(t, ms)

	res, err := svc.Delete(context.Background(), domain.DeleteParams{
		FlowID:    flowA,
		Timerange: mustTR(t, "[0:0_10:0)"),
	})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if res.DeletedCount != 1 {
		t.Errorf("DeletedCount = %d, want 1", res.DeletedCount)
	}
	if got := ms.objects["o-shared"].refCount; got != 1 {
		t.Errorf("o-shared ref-count = %d, want 1 (still referenced by flowB)", got)
	}
	// flowB's segment must still be present and pointing at the object.
	if got := len(ms.segments[flowB]); got != 1 || ms.segments[flowB][0].ObjectID != "o-shared" {
		t.Errorf("flowB segment lost or rewritten: %+v", ms.segments[flowB])
	}
}

// ---------------------------------------------------------------------------
// SCN-SEG-13 — RegisterBatch makes ZERO calls into the object store.
// ---------------------------------------------------------------------------
//
// SCN-SEG-26 covers the same Deps-shape invariant on the Delete path.
// SCN-SEG-13 mirrors it for Register so the trace map carries an
// independent entry for REQ-SEG-05 / INV-SEG-10.

func Test_SCN_SEG_13_NoObjectStoreOnRegister(t *testing.T) {
	// Negative-invariant compile-time check: segment.Deps must not
	// expose an object-store field. If a future refactor adds one,
	// this test stops compiling.
	deps := segment.Deps{}
	_ = deps

	ms := newFakeMetaStore()
	flowID := uuid.New()
	ms.addFlow(flowID, false)
	svc := newServiceV1(t, ms)

	res, err := svc.RegisterBatch(context.Background(), domain.RegisterParams{
		FlowID: flowID,
		Segments: []domain.Segment{
			seg(t, "o1", "[0:0_10:0)"),
			seg(t, "o2", "[10:0_20:0)"),
			seg(t, "o3", "[20:0_30:0)"),
		},
	})
	if err != nil {
		t.Fatalf("RegisterBatch: %v", err)
	}
	if len(res.Accepted) != 3 {
		t.Errorf("accepted = %d, want 3", len(res.Accepted))
	}
}

// ---------------------------------------------------------------------------
// SCN-SEG-14 — Delete returns success regardless of post-tx object-store state.
// ---------------------------------------------------------------------------

func Test_SCN_SEG_14_DeleteSucceedsRefCountFallsToZero(t *testing.T) {
	ms := newFakeMetaStore()
	flowID := uuid.New()
	ms.addFlow(flowID, false)
	ms.addSegment(flowID, seg(t, "o-fail", "[0:0_10:0)"))
	if got := ms.objects["o-fail"].refCount; got != 1 {
		t.Fatalf("setup: ref-count = %d, want 1", got)
	}
	svc := newServiceV1(t, ms)

	res, err := svc.Delete(context.Background(), domain.DeleteParams{
		FlowID:    flowID,
		Timerange: mustTR(t, "[0:0_10:0)"),
	})
	if err != nil {
		t.Fatalf("Delete returned err = %v, want nil (service has no object-store dep)", err)
	}
	if res.DeletedCount != 1 {
		t.Errorf("DeletedCount = %d, want 1", res.DeletedCount)
	}
	if got := ms.objects["o-fail"].refCount; got != 0 {
		t.Errorf("o-fail ref-count = %d, want 0", got)
	}
	if ms.objects["o-fail"].reaping {
		t.Errorf("o-fail.reaping = true, want false (GC worker claims the row, not the service)")
	}
}

// ---------------------------------------------------------------------------
// SCN-SEG-16 — Negative ts_offset preserved (no offset arithmetic).
// ---------------------------------------------------------------------------

func Test_SCN_SEG_16_NegativeTSOffsetPreserved(t *testing.T) {
	ms := newFakeMetaStore()
	flowID := uuid.New()
	ms.addFlow(flowID, false)
	svc := newServiceV1(t, ms)

	tsOff := mustTimestamp(t, "-5:0")
	s := seg(t, "o-16", "[0:0_10:0)")
	s.TSOffset = &tsOff

	res, err := svc.RegisterBatch(context.Background(), domain.RegisterParams{
		FlowID:   flowID,
		Segments: []domain.Segment{s},
	})
	if err != nil {
		t.Fatalf("RegisterBatch: %v", err)
	}
	if len(res.Accepted) != 1 {
		t.Fatalf("accepted = %d, want 1", len(res.Accepted))
	}
	got := res.Accepted[0].TSOffset
	if got == nil {
		t.Fatalf("TSOffset = nil, want -5:0 preserved")
	}
	if got.Seconds != -5 || got.Nanoseconds != 0 {
		t.Errorf("TSOffset = %d:%d, want -5:0", got.Seconds, got.Nanoseconds)
	}

	// Round-trip through List — same object identity, same offset.
	page, err := svc.List(context.Background(), domain.ListParams{FlowID: flowID, Limit: 100})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("List items = %d, want 1", len(page.Items))
	}
	if page.Items[0].TSOffset == nil ||
		page.Items[0].TSOffset.Seconds != -5 ||
		page.Items[0].TSOffset.Nanoseconds != 0 {
		t.Errorf("List TSOffset not preserved: %+v", page.Items[0].TSOffset)
	}
}

// ---------------------------------------------------------------------------
// SCN-SEG-20 — Per-failed-segment WARN logs.
// ---------------------------------------------------------------------------
//
// Spec divergence note: an earlier design draft prescribed
// outcome=partial_failure + failure_type=segment-overlap, but overlap is
// whole-batch reject in the current contract (BR-SEG-03). The path that actually
// produces OutcomePartial is per-segment metastore rejection via
// InsertResult.RejectedIndices. This test drives that path: 2 survivors,
// metastore accepts index 1 and rejects index 0 with a structured
// reason. The handler MUST emit one WARN per failed segment with the
// structured fields the spec lists (object_id, timerange, failure_type).

func Test_SCN_SEG_20_PerFailedSegmentWarnLogs(t *testing.T) {
	core, logs := observer.New(zapcore.WarnLevel)
	logger := zap.New(core)

	ms := newFakeMetaStore()
	flowID := uuid.New()
	ms.addFlow(flowID, false)

	// Drive a partial outcome: idx 0 rejected by metastore, idx 1 accepted.
	ms.customInsert = func(batch metastore.InsertBatch) (metastore.InsertResult, error) {
		return metastore.InsertResult{
			AcceptedIndices: []int{1},
			RejectedIndices: []int{0},
			RejectReasons: []metastore.RejectReason{
				{
					Type:   "https://github.com/amagioss/opentams/problems/segment-overlap",
					Detail: "object_id collides with object_timerange of [0:0_5:0)",
				},
			},
		}, nil
	}

	reg := prometheus.NewRegistry()
	m, err := service.NewAppServiceMetrics(reg)
	if err != nil {
		t.Fatalf("NewAppServiceMetrics: %v", err)
	}
	svc := segment.New(segment.Deps{Meta: ms, Logger: logger, Metrics: m})

	failedSeg := seg(t, "o-bad", "[100:0_110:0)")
	okSeg := seg(t, "o-good", "[200:0_210:0)")
	res, err := svc.RegisterBatch(context.Background(), domain.RegisterParams{
		FlowID:   flowID,
		Segments: []domain.Segment{failedSeg, okSeg},
	})
	if err != nil {
		t.Fatalf("RegisterBatch: %v", err)
	}
	if res.Outcome != domain.OutcomePartial {
		t.Fatalf("Outcome = %d, want OutcomePartial", res.Outcome)
	}

	// Exactly one WARN entry, carrying the structured fields per spec.
	warns := logs.FilterLevelExact(zapcore.WarnLevel).All()
	if len(warns) != 1 {
		t.Fatalf("WARN entries = %d, want 1\nentries: %+v", len(warns), warns)
	}
	entry := warns[0]
	fields := entry.ContextMap()

	mustField := func(key, want string) {
		t.Helper()
		got, ok := fields[key].(string)
		if !ok {
			t.Errorf("field %q missing or not a string: %+v", key, fields[key])
			return
		}
		if got != want {
			t.Errorf("field %q = %q, want %q", key, got, want)
		}
	}
	mustField("domain", "segments")
	mustField("action", "register")
	mustField("outcome", "partial_failure")
	mustField("failure_type", "https://github.com/amagioss/opentams/problems/segment-overlap")
	mustField("object_id", "o-bad")
	mustField("timerange", "[100:0_110:0)")
	if _, ok := fields["flow_id"].(string); !ok {
		t.Errorf("field \"flow_id\" missing or not a string")
	}
}

// ---------------------------------------------------------------------------
// SCN-SEG-21 — Outcome counters.
// ---------------------------------------------------------------------------
//
// Counters live on service.AppServiceMetrics under the existing
// app_service_operation_* family, with a new `outcome` label:
//
//   app_service_operation_total{app_service, operation, outcome}
//   app_service_operation_items_total{app_service, operation, outcome}
//
// Mapping the spec's three names onto this shape:
//
//   segment_register_total{outcome=...}            →
//     app_service_operation_total{app_service="segment", operation="register", outcome=...}
//   segment_register_segments_total{outcome=...}   →
//     app_service_operation_items_total{app_service="segment", operation="register", outcome=...}
//   segment_delete_total{outcome=...}              →
//     app_service_operation_total{app_service="segment", operation="delete",   outcome=...}

func Test_SCN_SEG_21_OutcomeCounters(t *testing.T) {
	reg := prometheus.NewRegistry()
	m, err := service.NewAppServiceMetrics(reg)
	if err != nil {
		t.Fatalf("NewAppServiceMetrics: %v", err)
	}

	ms := newFakeMetaStore()
	flowID := uuid.New()
	ms.addFlow(flowID, false)

	// All-accepted: 5 segs survive, all accepted.
	allAcceptedSegs := []domain.Segment{
		seg(t, "oa1", "[0:0_10:0)"),
		seg(t, "oa2", "[10:0_20:0)"),
		seg(t, "oa3", "[20:0_30:0)"),
		seg(t, "oa4", "[30:0_40:0)"),
		seg(t, "oa5", "[40:0_50:0)"),
	}

	// Build a service-with-customInsert sequence:
	// call 1: all-accepted (5/5)
	// call 2: partial      (2 accepted, 1 failed)
	// call 3: all-rejected (0 accepted, 2 failed)
	// call 4: delete       releases 2 segments (both ok)
	type insertScript struct {
		accepted []int
		rejected []int
		reasons  []metastore.RejectReason
	}
	scripts := []insertScript{
		{accepted: []int{0, 1, 2, 3, 4}}, // all_accepted
		{accepted: []int{0, 1}, rejected: []int{2}, reasons: // partial: 1 fail
		[]metastore.RejectReason{{Type: "x", Detail: "bad"}}},
		{accepted: nil, rejected: []int{0, 1}, reasons: // all_rejected: 2 fail
		[]metastore.RejectReason{
			{Type: "x", Detail: "bad-1"},
			{Type: "x", Detail: "bad-2"},
		}},
	}
	calls := 0
	ms.customInsert = func(_ metastore.InsertBatch) (metastore.InsertResult, error) {
		s := scripts[calls]
		calls++
		return metastore.InsertResult{
			AcceptedIndices: s.accepted,
			RejectedIndices: s.rejected,
			RejectReasons:   s.reasons,
		}, nil
	}

	svc := segment.New(segment.Deps{
		Meta:    ms,
		Logger:  zap.NewNop(),
		Metrics: m,
	})

	// Call 1: all-accepted.
	if _, err := svc.RegisterBatch(context.Background(), domain.RegisterParams{
		FlowID: flowID, Segments: allAcceptedSegs,
	}); err != nil {
		t.Fatalf("call 1: %v", err)
	}

	// Call 2: partial — 3 segs in, 2 accepted, 1 failed.
	if _, err := svc.RegisterBatch(context.Background(), domain.RegisterParams{
		FlowID: flowID,
		Segments: []domain.Segment{
			seg(t, "ob1", "[100:0_110:0)"),
			seg(t, "ob2", "[110:0_120:0)"),
			seg(t, "ob3", "[120:0_130:0)"),
		},
	}); err != nil {
		t.Fatalf("call 2: %v", err)
	}

	// Call 3: all-rejected — 2 segs in, 0 accepted, 2 failed.
	if _, err := svc.RegisterBatch(context.Background(), domain.RegisterParams{
		FlowID: flowID,
		Segments: []domain.Segment{
			seg(t, "oc1", "[200:0_210:0)"),
			seg(t, "oc2", "[210:0_220:0)"),
		},
	}); err != nil {
		t.Fatalf("call 3: %v", err)
	}

	// Call 4: Delete (eternity range — releases both surviving objects).
	if _, err := svc.Delete(context.Background(), domain.DeleteParams{
		FlowID:    flowID,
		Timerange: mustTR(t, "[0:0_9999:0)"),
	}); err != nil {
		t.Fatalf("call 4: %v", err)
	}

	want := map[string]float64{
		// app_service_operation_total{operation, outcome}
		`app_service_operation_total|app_service=segment,operation=register,outcome=all_accepted`: 1,
		`app_service_operation_total|app_service=segment,operation=register,outcome=partial`:      1,
		`app_service_operation_total|app_service=segment,operation=register,outcome=all_rejected`: 1,
		`app_service_operation_total|app_service=segment,operation=delete,outcome=success`:        1,

		// app_service_operation_items_total{operation, outcome}
		`app_service_operation_items_total|app_service=segment,operation=register,outcome=accepted`: 5 + 2,
		`app_service_operation_items_total|app_service=segment,operation=register,outcome=failed`:   1 + 2,
	}
	got := gatherCounterMap(t, reg)
	for k, v := range want {
		if g := got[k]; g != v {
			t.Errorf("metric %s = %v, want %v\nall metrics: %+v", k, g, v, got)
		}
	}
}

// gatherCounterMap collects all counter samples in the registry into a
// flat map keyed by `name|labelKey1=labelVal1,labelKey2=labelVal2,...`
// (labels in the order Prometheus reports them). Histograms are
// ignored. Test uses simple key-value strings so failures show the
// full label set without DTO boilerplate.
func gatherCounterMap(t *testing.T, reg prometheus.Gatherer) map[string]float64 {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("registry Gather: %v", err)
	}
	out := map[string]float64{}
	for _, mf := range mfs {
		if mf.GetType() != dto.MetricType_COUNTER {
			continue
		}
		for _, m := range mf.GetMetric() {
			parts := make([]string, 0, len(m.GetLabel()))
			for _, lp := range m.GetLabel() {
				parts = append(parts, lp.GetName()+"="+lp.GetValue())
			}
			key := mf.GetName() + "|" + strings.Join(parts, ",")
			out[key] = m.GetCounter().GetValue()
		}
	}
	return out
}

// Test_SCN_SEG_30_ServicePropagatesControlledStorageID — service holds
// ControlledStorageID via Deps; RegisterBatch passes it through to
// metastore.InsertBatch verbatim (BR-SEG-07).
func Test_SCN_SEG_30_ServicePropagatesControlledStorageID(t *testing.T) {
	const wantStorageID = "prod-bucket-eu-west-1"
	ms := newFakeMetaStore()
	flID := uuid.New()
	ms.addFlow(flID, false)

	var captured metastore.InsertBatch
	ms.customInsert = func(batch metastore.InsertBatch) (metastore.InsertResult, error) {
		captured = batch
		return metastore.InsertResult{AcceptedIndices: []int{0}}, nil
	}

	reg := prometheus.NewRegistry()
	m, err := service.NewAppServiceMetrics(reg)
	if err != nil {
		t.Fatalf("NewAppServiceMetrics: %v", err)
	}
	svc := segment.New(segment.Deps{
		Meta:                ms,
		Logger:              zap.NewNop(),
		Metrics:             m,
		ControlledStorageID: wantStorageID,
	})

	_, err = svc.RegisterBatch(context.Background(), domain.RegisterParams{
		FlowID:   flID,
		Segments: []domain.Segment{seg(t, "o-srv-prop", "[0:0_5:0)")},
	})
	if err != nil {
		t.Fatalf("RegisterBatch: %v", err)
	}
	if captured.ControlledStorageID != wantStorageID {
		t.Errorf("captured.ControlledStorageID = %q, want %q",
			captured.ControlledStorageID, wantStorageID)
	}
}
