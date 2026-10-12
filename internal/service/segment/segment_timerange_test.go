package segment_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/amagioss/opentams/internal/apperror"
	"github.com/amagioss/opentams/internal/domain"
	"github.com/amagioss/opentams/internal/metastore"
)

// recordingStore captures every call so a test can assert what reached
// the store, and what did not (ADR-0040 rule 3).
type recordingStore struct {
	batches []metastore.InsertBatch
	lists   []metastore.ListQuery
	deletes []metastore.DeleteQuery
}

func (r *recordingStore) GetFlowForSegmentWrite(_ context.Context, _ metastore.Tx, id uuid.UUID) (metastore.FlowGuard, error) {
	return metastore.FlowGuard{FlowID: id, Exists: true}, nil
}

func (r *recordingStore) GetFlowForSegmentRead(_ context.Context, id uuid.UUID) (metastore.FlowGuard, error) {
	return metastore.FlowGuard{FlowID: id, Exists: true}, nil
}

func (r *recordingStore) InsertSegments(_ context.Context, b metastore.InsertBatch) (metastore.InsertResult, error) {
	r.batches = append(r.batches, b)
	idx := make([]int, len(b.Segments))
	for i := range idx {
		idx[i] = i
	}
	return metastore.InsertResult{AcceptedIndices: idx}, nil
}

func (r *recordingStore) ListSegments(_ context.Context, q metastore.ListQuery) (domain.SegmentPage, error) {
	r.lists = append(r.lists, q)
	return domain.SegmentPage{EffectiveLimit: q.Limit}, nil
}

func (r *recordingStore) DeleteSegmentsByTimerange(_ context.Context, q metastore.DeleteQuery) (metastore.DeleteResult, error) {
	r.deletes = append(r.deletes, q)
	return metastore.DeleteResult{}, nil
}

// rawSeg builds a segment the way conversion does: the parsed value and
// the exact client string side by side.
func rawSeg(t *testing.T, objID, tr string) domain.Segment {
	t.Helper()
	return domain.Segment{ObjectID: objID, Timerange: mustTR(t, tr), TimerangeRaw: tr}
}

func invalidTimerangeType() string {
	return apperror.New(apperror.ErrInvalidTimerange, "").ToProblemDetails("", "").Type
}

// BR-SEG-02 / ADR-0040 rules 1-2: each timerange that is not bounded,
// non-empty, start-inclusive, and within int64 nanoseconds is a
// per-segment invalid-timerange failure that never reaches the store.
func Test_BR_SEG_02_InvalidSegmentTimerangeIsPerSegmentFailure(t *testing.T) {
	cases := []struct {
		name string
		tr   string
	}{
		{"empty marker pair", "()"},
		{"empty inclusive markers", "[]"},
		{"end before start", "[10:0_5:0)"},
		{"equal bounds with exclusive end", "[5:0_5:0)"},
		{"no whole nanosecond", "(0:0_0:1)"},
		{"eternity", "_"},
		{"exclusive start without end", "(5:0_"},
		{"inclusive start without end", "[5:0_"},
		{"no start", "_10:0)"},
		{"exclusive start", "(0:0_10:0)"},
		{"inclusive end past int64 max", "[9223372036:854775807]"},
		{"start below int64 min", "[-9223372036:854775809_0:0)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &recordingStore{}
			svc := newServiceV1(t, store)

			res, err := svc.RegisterBatch(context.Background(), domain.RegisterParams{
				FlowID:   uuid.New(),
				Segments: []domain.Segment{rawSeg(t, "o1", tc.tr)},
			})
			if err != nil {
				t.Fatalf("RegisterBatch(%q) err = %v, want nil (per-segment failure, not a request error)", tc.tr, err)
			}
			if len(store.batches) != 0 {
				t.Fatalf("store received %d batch(es); an invalid segment must never reach it", len(store.batches))
			}
			if res.Outcome != domain.OutcomeAllRejected || len(res.Failed) != 1 {
				t.Fatalf("Outcome=%d failed=%d, want OutcomeAllRejected with 1 failure", res.Outcome, len(res.Failed))
			}
			f := res.Failed[0]
			if f.Type != invalidTimerangeType() {
				t.Errorf("Type = %q, want %q", f.Type, invalidTimerangeType())
			}
			if f.Title != "Invalid Timerange" {
				t.Errorf("Title = %q, want %q", f.Title, "Invalid Timerange")
			}
			if f.Status != 400 {
				t.Errorf("Status = %d, want 400", f.Status)
			}
			if f.Reason == "" {
				t.Error("Reason is empty; it must name the broken rule")
			}
			if f.Segment.TimerangeRaw != tc.tr {
				t.Errorf("failed segment TimerangeRaw = %q, want %q", f.Segment.TimerangeRaw, tc.tr)
			}
		})
	}
}

// BR-SEG-02: bounded, non-empty, start-inclusive ranges are accepted,
// including the instantaneous [0:0] from the original bug and negative
// ranges inside one second. The raw string reaches the store unchanged.
func Test_BR_SEG_02_ValidSegmentTimerangeReachesStoreWithRaw(t *testing.T) {
	for _, tr := range []string{
		"[0:0]",
		"10:0",
		"[0:0_10:0]",
		"[0:0_10:0)",
		"[-1:500000000_-1:0)",
		"[-0:500000000_0:0)",
	} {
		t.Run(tr, func(t *testing.T) {
			store := &recordingStore{}
			svc := newServiceV1(t, store)

			res, err := svc.RegisterBatch(context.Background(), domain.RegisterParams{
				FlowID:   uuid.New(),
				Segments: []domain.Segment{rawSeg(t, "o1", tr)},
			})
			if err != nil {
				t.Fatalf("RegisterBatch(%q): %v", tr, err)
			}
			if res.Outcome != domain.OutcomeAllAccepted || len(res.Failed) != 0 {
				t.Fatalf("Outcome=%d failed=%+v, want OutcomeAllAccepted", res.Outcome, res.Failed)
			}
			if len(store.batches) != 1 || len(store.batches[0].Segments) != 1 {
				t.Fatalf("store batches = %+v, want one batch with one segment", store.batches)
			}
			if got := store.batches[0].Segments[0].TimerangeRaw; got != tr {
				t.Errorf("store TimerangeRaw = %q, want %q", got, tr)
			}
		})
	}
}

// BR-SEG-08: the service passes every *Raw field through unchanged.
func Test_BR_SEG_08_RawFieldsPassThrough(t *testing.T) {
	store := &recordingStore{}
	svc := newServiceV1(t, store)

	s := rawSeg(t, "o1", "0:0_10:0")
	tso := mustTimestamp(t, "-0:0")
	ld := mustTimestamp(t, "-0:500000000")
	otr := mustTR(t, "10:0")
	s.TSOffset, s.TSOffsetRaw = &tso, "-0:0"
	s.LastDuration, s.LastDurationRaw = &ld, "-0:500000000"
	s.ObjectTimerange, s.ObjectTimerangeRaw = &otr, "10:0"

	if _, err := svc.RegisterBatch(context.Background(), domain.RegisterParams{
		FlowID: uuid.New(), Segments: []domain.Segment{s},
	}); err != nil {
		t.Fatalf("RegisterBatch: %v", err)
	}
	if len(store.batches) != 1 {
		t.Fatalf("store batches = %d, want 1", len(store.batches))
	}
	got := store.batches[0].Segments[0]
	if got.TimerangeRaw != "0:0_10:0" || got.TSOffsetRaw != "-0:0" ||
		got.LastDurationRaw != "-0:500000000" || got.ObjectTimerangeRaw != "10:0" {
		t.Errorf("raw fields changed: %q %q %q %q",
			got.TimerangeRaw, got.TSOffsetRaw, got.LastDurationRaw, got.ObjectTimerangeRaw)
	}
}

// ADR-0040 rule 2: processing continues. Valid segments in the batch
// reach the store; invalid ones come back in Failed.
func Test_BR_SEG_02_MixedBatchContinues(t *testing.T) {
	store := &recordingStore{}
	svc := newServiceV1(t, store)

	res, err := svc.RegisterBatch(context.Background(), domain.RegisterParams{
		FlowID: uuid.New(),
		Segments: []domain.Segment{
			rawSeg(t, "ok-1", "[0:0]"),
			rawSeg(t, "bad-1", "_"),
			rawSeg(t, "ok-2", "[1:0_2:0)"),
			rawSeg(t, "bad-2", "(2:0_3:0)"),
		},
	})
	if err != nil {
		t.Fatalf("RegisterBatch: %v", err)
	}
	if res.Outcome != domain.OutcomePartial {
		t.Errorf("Outcome = %d, want OutcomePartial", res.Outcome)
	}
	if len(store.batches) != 1 {
		t.Fatalf("store batches = %d, want 1", len(store.batches))
	}
	var stored []string
	for _, s := range store.batches[0].Segments {
		stored = append(stored, s.ObjectID)
	}
	if len(stored) != 2 || stored[0] != "ok-1" || stored[1] != "ok-2" {
		t.Errorf("store received %v, want [ok-1 ok-2]", stored)
	}
	var failed []string
	for _, f := range res.Failed {
		failed = append(failed, f.Segment.ObjectID)
		if f.Type != invalidTimerangeType() {
			t.Errorf("failed %s Type = %q", f.Segment.ObjectID, f.Type)
		}
	}
	if len(failed) != 2 || failed[0] != "bad-1" || failed[1] != "bad-2" {
		t.Errorf("failed = %v, want [bad-1 bad-2]", failed)
	}
}

// BR-SEG-09: a query timerange whose bounds do not fit in int64
// nanoseconds is invalid-timerange (400) and never reaches the store.
func Test_BR_SEG_09_ListOutOfRangeQueryIsInvalidTimerange(t *testing.T) {
	store := &recordingStore{}
	svc := newServiceV1(t, store)
	tr := mustTR(t, "[9223372036:854775807]")

	_, err := svc.List(context.Background(), domain.ListParams{FlowID: uuid.New(), Timerange: &tr, Limit: 10})
	assertInvalidTimerange(t, err)
	if len(store.lists) != 0 {
		t.Errorf("store received %d list call(s), want 0", len(store.lists))
	}
}

// BR-SEG-09: an empty query timerange is not an error; the store decides
// it matches nothing (BR-META-21).
func Test_BR_SEG_09_ListEmptyQueryReachesStore(t *testing.T) {
	store := &recordingStore{}
	svc := newServiceV1(t, store)
	tr := mustTR(t, "[10:0_5:0)")

	if _, err := svc.List(context.Background(), domain.ListParams{FlowID: uuid.New(), Timerange: &tr, Limit: 10}); err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(store.lists) != 1 || store.lists[0].Timerange == nil || !store.lists[0].Timerange.IsEmpty() {
		t.Errorf("store lists = %+v, want one call with the empty range", store.lists)
	}
}

// BR-SEG-10 / BR-SEG-14: same query rules for DELETE.
func Test_BR_SEG_10_DeleteOutOfRangeQueryIsInvalidTimerange(t *testing.T) {
	store := &recordingStore{}
	svc := newServiceV1(t, store)

	_, err := svc.Delete(context.Background(), domain.DeleteParams{
		FlowID: uuid.New(), Timerange: mustTR(t, "[-9223372036:854775809_0:0)"),
	})
	assertInvalidTimerange(t, err)
	if len(store.deletes) != 0 {
		t.Errorf("store received %d delete call(s), want 0", len(store.deletes))
	}
}

func Test_BR_SEG_10_DeleteEmptyQueryReachesStore(t *testing.T) {
	store := &recordingStore{}
	svc := newServiceV1(t, store)

	if _, err := svc.Delete(context.Background(), domain.DeleteParams{
		FlowID: uuid.New(), Timerange: mustTR(t, "[10:0_5:0)"),
	}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(store.deletes) != 1 || !store.deletes[0].Timerange.IsEmpty() {
		t.Errorf("store deletes = %+v, want one call with the empty range", store.deletes)
	}
}

func assertInvalidTimerange(t *testing.T, err error) {
	t.Helper()
	var ae *apperror.AppError
	if !errors.As(err, &ae) || ae.Code != apperror.ErrInvalidTimerange {
		t.Fatalf("err = %v, want *apperror.AppError with code %q", err, apperror.ErrInvalidTimerange)
	}
}
