//go:build integration || perf

package metastore_test

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"testing"

	"github.com/google/uuid"

	"github.com/amagioss/opentams/internal/domain"
	"github.com/amagioss/opentams/internal/metastore"
	"github.com/amagioss/opentams/internal/timerange"
)

// Edge cases from the [0:0] 500 bug: markers, negative bounds, stored
// client strings, and the predicate of each timerange query
// (ADR-0039, ADR-0040, BR-META-05/08/10/12/21).

func insertSegs(t *testing.T, store *metastore.PostgresStore, flID uuid.UUID, trs ...string) error {
	t.Helper()
	segs := make([]domain.Segment, len(trs))
	for i, tr := range trs {
		segs[i] = makeSegment(t, fmt.Sprintf("o-%s-%d", flID, uniqueObj()), tr)
	}
	_, err := store.InsertSegments(context.Background(), metastore.InsertBatch{
		FlowID: flID, Segments: segs, ControlledStorageID: "default",
	})
	return err
}

var objCounter int

func uniqueObj() int {
	objCounter++
	return objCounter
}

func mustInsert(t *testing.T, store *metastore.PostgresStore, flID uuid.UUID, trs ...string) {
	t.Helper()
	if err := insertSegs(t, store, flID, trs...); err != nil {
		t.Fatalf("insert %v: %v", trs, err)
	}
}

func listRaw(t *testing.T, store *metastore.PostgresStore, flID uuid.UUID, q *timerange.TimeRange) []string {
	t.Helper()
	page, err := store.ListSegments(context.Background(), metastore.ListQuery{FlowID: flID, Limit: 100, Timerange: q})
	if err != nil {
		t.Fatalf("ListSegments: %v", err)
	}
	out := make([]string, len(page.Items))
	for i := range page.Items {
		out[i] = page.Items[i].TimerangeRaw
	}
	return out
}

func segmentCount(t *testing.T, flID uuid.UUID) int64 {
	t.Helper()
	return queryInt(t, `SELECT COUNT(*) FROM segments WHERE flow_id = $1`, flID)
}

// Regression for the bug report: [0:0] became lower_ns = upper_ns = 0 and
// failed the CHECK constraint with a 500.
func TestTimerange_InsertInstantAtZero(t *testing.T) {
	flID := seedFlow(t, false)
	store := newStore(t)
	mustInsert(t, store, flID, "[0:0]")

	if lo := queryInt(t, `SELECT lower_ns FROM segments WHERE flow_id = $1`, flID); lo != 0 {
		t.Errorf("lower_ns = %d, want 0", lo)
	}
	if hi := queryInt(t, `SELECT upper_ns FROM segments WHERE flow_id = $1`, flID); hi != 1 {
		t.Errorf("upper_ns = %d, want 1", hi)
	}
}

// One overlap rule for both checks (ADR-0040 rule 5): the outcome must not
// depend on whether the client sends the two segments in one batch or two.
func TestTimerange_OverlapSameWithinAndAcrossBatches(t *testing.T) {
	cases := []struct {
		a, b    string
		overlap bool
	}{
		{"[1:0_2:0)", "[2:0]", false},                // TAMS example
		{"[0:0_10:0]", "[10:0_20:0)", true},          // inclusive end meets inclusive start
		{"[0:0_10:0)", "[10:0_20:0)", false},         // adjacent
		{"[-1:500000000_-1:0)", "[-1:0_0:0)", false}, // negative, inside one second
		{"[-1:500000000_-0:500000000)", "[-1:0_0:0)", true},
		{"10:0", "[10:0_10:0]", true}, // two spellings of one instant
		{"[0:0]", "[0:0_1:0)", true},
		{"[0:0]", "[0:1_1:0)", false},
	}
	for _, tc := range cases {
		name := tc.a + " vs " + tc.b
		t.Run("within/"+name, func(t *testing.T) {
			flID := seedFlow(t, false)
			store := newStore(t)
			err := insertSegs(t, store, flID, tc.a, tc.b)
			checkOverlap(t, err, tc.overlap)
		})
		t.Run("across/"+name, func(t *testing.T) {
			flID := seedFlow(t, false)
			store := newStore(t)
			mustInsert(t, store, flID, tc.a)
			err := insertSegs(t, store, flID, tc.b)
			checkOverlap(t, err, tc.overlap)
		})
	}
}

func checkOverlap(t *testing.T, err error, want bool) {
	t.Helper()
	if want && !errors.Is(err, metastore.ErrSegmentOverlap) {
		t.Errorf("err = %v, want ErrSegmentOverlap", err)
	}
	if !want && err != nil {
		t.Errorf("err = %v, want nil", err)
	}
}

// BR-META-10: the four client strings come back byte for byte, with the
// parsed values alongside.
func TestTimerange_RawStringsRoundTrip(t *testing.T) {
	cases := []struct {
		name                            string
		tr, tsOffset, objTR, lastDurRaw string
	}{
		{"bare instant", "10:0", "-0:500000000", "0:0_10:0", "0:40000000"},
		{"bracketed instant", "[10:0]", "-0:0", "[10:0]", "-0:0"},
		{"no markers", "0:0_10:0", "0:0", "[10:0_10:0]", "1:0"},
		{"closed instant", "[10:0_10:0]", "5:0", "(0:0_20:0]", "-1:500000000"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			flID := seedFlow(t, false)
			store := newStore(t)
			ts := mustTS(t, tc.tsOffset)
			otr := mustTR(t, tc.objTR)
			ld := mustTS(t, tc.lastDurRaw)
			in := domain.Segment{
				ObjectID:           "o-raw-" + flID.String(),
				Timerange:          mustTR(t, tc.tr),
				TimerangeRaw:       tc.tr,
				TSOffset:           &ts,
				TSOffsetRaw:        tc.tsOffset,
				ObjectTimerange:    &otr,
				ObjectTimerangeRaw: tc.objTR,
				LastDuration:       &ld,
				LastDurationRaw:    tc.lastDurRaw,
			}
			if _, err := store.InsertSegments(context.Background(), metastore.InsertBatch{
				FlowID: flID, Segments: []domain.Segment{in}, ControlledStorageID: "default",
			}); err != nil {
				t.Fatalf("insert: %v", err)
			}

			var dbTR, dbTS string
			var dbObj, dbLD *string
			if err := metastore.SharedTestPool().QueryRow(context.Background(),
				`SELECT timerange, ts_offset, object_timerange, last_duration FROM segments WHERE flow_id = $1`, flID,
			).Scan(&dbTR, &dbTS, &dbObj, &dbLD); err != nil {
				t.Fatalf("select: %v", err)
			}
			if dbTR != tc.tr || dbTS != tc.tsOffset || dbObj == nil || *dbObj != tc.objTR || dbLD == nil || *dbLD != tc.lastDurRaw {
				t.Errorf("stored = (%q, %q, %v, %v), want (%q, %q, %q, %q)",
					dbTR, dbTS, deref(dbObj), deref(dbLD), tc.tr, tc.tsOffset, tc.objTR, tc.lastDurRaw)
			}

			page, err := store.ListSegments(context.Background(), metastore.ListQuery{FlowID: flID, Limit: 10})
			if err != nil || len(page.Items) != 1 {
				t.Fatalf("list: %v, %d items", err, len(page.Items))
			}
			out := page.Items[0]
			if out.TimerangeRaw != tc.tr || out.TSOffsetRaw != tc.tsOffset ||
				out.ObjectTimerangeRaw != tc.objTR || out.LastDurationRaw != tc.lastDurRaw {
				t.Errorf("read raw = (%q, %q, %q, %q), want (%q, %q, %q, %q)",
					out.TimerangeRaw, out.TSOffsetRaw, out.ObjectTimerangeRaw, out.LastDurationRaw,
					tc.tr, tc.tsOffset, tc.objTR, tc.lastDurRaw)
			}
			if out.Timerange.String() != in.Timerange.String() {
				t.Errorf("parsed Timerange = %s, want %s", out.Timerange, in.Timerange)
			}
			if out.TSOffset == nil || *out.TSOffset != ts {
				t.Errorf("parsed TSOffset = %v, want %v", out.TSOffset, ts)
			}
			if out.ObjectTimerange == nil || out.ObjectTimerange.String() != otr.String() {
				t.Errorf("parsed ObjectTimerange = %v, want %s", out.ObjectTimerange, otr)
			}
			if out.LastDuration == nil || *out.LastDuration != ld {
				t.Errorf("parsed LastDuration = %v, want %v", out.LastDuration, ld)
			}
		})
	}
}

func deref(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

// BR-META-10: absent optional strings stay absent.
func TestTimerange_AbsentOptionalStringsStayAbsent(t *testing.T) {
	flID := seedFlow(t, false)
	store := newStore(t)
	mustInsert(t, store, flID, "[0:0_1:0)")
	page, err := store.ListSegments(context.Background(), metastore.ListQuery{FlowID: flID, Limit: 10})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("list: %v", err)
	}
	out := page.Items[0]
	if out.TSOffsetRaw != "" || out.TSOffset != nil {
		t.Errorf("ts_offset = (%q, %v), want absent", out.TSOffsetRaw, out.TSOffset)
	}
	if out.ObjectTimerangeRaw != "" || out.ObjectTimerange != nil {
		t.Errorf("object_timerange = (%q, %v), want absent", out.ObjectTimerangeRaw, out.ObjectTimerange)
	}
	if out.LastDurationRaw != "" || out.LastDuration != nil {
		t.Errorf("last_duration = (%q, %v), want absent", out.LastDurationRaw, out.LastDuration)
	}
}

// BR-META-21, ListSegments: overlap with half-open bounds; an unbounded
// side has no condition; an empty query matches nothing.
func TestTimerange_ListSegmentsOverlapPredicate(t *testing.T) {
	cases := []struct {
		segment, query string
		match          bool
	}{
		{"[0:0_10:0)", "[10:0]", false},
		{"[0:0_10:0]", "[10:0]", true},
		{"[0:0_10:0]", "(10:0_20:0)", false},
		{"[0:0_10:0]", "[10:0_20:0)", true},
		{"[1:0_2:0)", "[2:0]", false},
		{"[2:0]", "[2:0]", true},
		{"[2:0]", "2:0", true},
		{"[0:0_10:0)", "(5:0_", true},
		{"[0:0_5:0]", "(5:0_", false},
		{"[0:0_5:0]", "[5:0_", true},
		{"[5:0_10:0)", "_5:0)", false},
		{"[5:0_10:0)", "_5:0]", true},
		{"[-1:500000000_-1:0)", "[-1:0_0:0)", false},
		{"[-1:500000000_-1:0)", "_-1:0]", true},
		{"[0:0_10:0)", "_", true},
		{"[0:0_10:0)", "()", false},
		{"[0:0_10:0)", "[10:0_5:0)", false},
		{"[0:0_10:0)", "(0:0_0:1)", false},
		{"[0:0_10:0)", "[5:0_5:0)", false},
	}
	for _, tc := range cases {
		t.Run(tc.segment+" q="+tc.query, func(t *testing.T) {
			flID := seedFlow(t, false)
			store := newStore(t)
			mustInsert(t, store, flID, tc.segment)
			got := listRaw(t, store, flID, trPtr(mustTR(t, tc.query)))
			if tc.match && (len(got) != 1 || got[0] != tc.segment) {
				t.Errorf("got %v, want [%s]", got, tc.segment)
			}
			if !tc.match && len(got) != 0 {
				t.Errorf("got %v, want none", got)
			}
		})
	}
}

// BR-META-21: a query bound outside int64 nanoseconds is an error the
// caller maps to 400 invalid-timerange.
func TestTimerange_QueryOutOfRange(t *testing.T) {
	flID := seedFlow(t, false)
	store := newStore(t)
	mustInsert(t, store, flID, "[0:0_10:0)")
	q := mustTR(t, "[0:0_9300000000:0)")

	_, err := store.ListSegments(context.Background(), metastore.ListQuery{FlowID: flID, Limit: 10, Timerange: &q})
	if !errors.Is(err, timerange.ErrOutOfRange) {
		t.Errorf("ListSegments err = %v, want ErrOutOfRange", err)
	}
	_, err = store.DeleteSegmentsByTimerange(context.Background(), metastore.DeleteQuery{FlowID: flID, Timerange: q})
	if !errors.Is(err, timerange.ErrOutOfRange) {
		t.Errorf("DeleteSegmentsByTimerange err = %v, want ErrOutOfRange", err)
	}
	if n := segmentCount(t, flID); n != 1 {
		t.Errorf("segments after failed delete = %d, want 1", n)
	}
}

// BR-META-08: DELETE removes only segments completely covered by the query.
func TestTimerange_DeleteContainment(t *testing.T) {
	all := []string{"[0:0_10:0)", "[10:0_20:0)", "[20:0_30:0)"}
	cases := []struct {
		query  string
		remain []string
	}{
		{"[5:0_25:0)", []string{"[0:0_10:0)", "[20:0_30:0)"}},
		{"[10:0_20:0)", []string{"[0:0_10:0)", "[20:0_30:0)"}},
		{"[10:0_19:0]", all},
		{"(10:0_20:0)", all},
		{"[10:0_", []string{"[0:0_10:0)"}},
		{"_20:0)", []string{"[20:0_30:0)"}},
		{"()", all},
		{"[20:0_10:0)", all},
		{"(0:0_0:1)", all},
		{"_", nil},
	}
	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			flID := seedFlow(t, false)
			store := newStore(t)
			mustInsert(t, store, flID, all...)
			res, err := store.DeleteSegmentsByTimerange(context.Background(), metastore.DeleteQuery{
				FlowID: flID, Timerange: mustTR(t, tc.query),
			})
			if err != nil {
				t.Fatalf("delete: %v", err)
			}
			if want := int64(len(all) - len(tc.remain)); res.DeletedCount != want {
				t.Errorf("DeletedCount = %d, want %d", res.DeletedCount, want)
			}
			got := listRaw(t, store, flID, nil)
			if fmt.Sprint(got) != fmt.Sprint(tc.remain) {
				t.Errorf("remaining = %v, want %v", got, tc.remain)
			}
		})
	}
}

// BR-META-05/ADR-0040: the store defends itself against a timerange that
// has no valid bounds. Nothing is stored, not even the objects row.
func TestTimerange_InsertRejectsInvalidBounds(t *testing.T) {
	cases := []struct {
		name, tr string
		want     error
	}{
		{"empty exclusive end", "[5:0_5:0)", timerange.ErrEmptyRange},
		{"empty inverted", "[10:0_5:0)", timerange.ErrEmptyRange},
		{"empty sub-nanosecond", "(0:0_0:1)", timerange.ErrEmptyRange},
		{"inclusive end at int64 max", "[9223372036:854775807]", timerange.ErrOutOfRange},
		{"beyond int64", "[9300000000:0_9300000001:0)", timerange.ErrOutOfRange},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			flID := seedFlow(t, false)
			store := newStore(t)
			objID := "o-bad-" + flID.String()
			good := makeSegment(t, "o-good-"+flID.String(), "[100:0_101:0)")
			bad := makeSegment(t, objID, tc.tr)
			_, err := store.InsertSegments(context.Background(), metastore.InsertBatch{
				FlowID: flID, Segments: []domain.Segment{good, bad}, ControlledStorageID: "default",
			})
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if n := segmentCount(t, flID); n != 0 {
				t.Errorf("segments stored = %d, want 0", n)
			}
			if n := queryInt(t, `SELECT COUNT(*) FROM objects WHERE id = ANY($1)`,
				[]string{objID, "o-good-" + flID.String()}); n != 0 {
				t.Errorf("objects stored = %d, want 0", n)
			}
		})
	}
}

// BR-META-10: the store never invents the client string, so a segment
// without TimerangeRaw is bad input.
func TestTimerange_InsertRequiresTimerangeRaw(t *testing.T) {
	flID := seedFlow(t, false)
	store := newStore(t)
	seg := domain.Segment{ObjectID: "o-noraw-" + flID.String(), Timerange: mustTR(t, "[0:0_1:0)")}
	_, err := store.InsertSegments(context.Background(), metastore.InsertBatch{
		FlowID: flID, Segments: []domain.Segment{seg}, ControlledStorageID: "default",
	})
	if !errors.Is(err, metastore.ErrBadInput) {
		t.Errorf("err = %v, want ErrBadInput", err)
	}
	if n := segmentCount(t, flID); n != 0 {
		t.Errorf("segments stored = %d, want 0", n)
	}
}

// BR-META-12: the flow timerange joins the first segment's start to the
// last segment's end, parsed from the stored strings and rendered
// canonically. It is never built from the integer bounds.
func TestTimerange_FlowTimerangeDerived(t *testing.T) {
	cases := []struct {
		name string
		segs []string
		want string
	}{
		{"negative start", []string{"[-1:500000000_-1:0)", "[0:0_1:0)"}, "[-1:500000000_1:0)"},
		{"single instant", []string{"10:0"}, "[10:0]"},
		{"inclusive last end", []string{"[0:0_1:0)", "[5:0_10:0]"}, "[0:0_10:0]"},
		{"unordered batch", []string{"[20:0_30:0)", "0:0_10:0"}, "[0:0_30:0)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			flID := seedFlow(t, false)
			store := newStore(t)
			mustInsert(t, store, flID, tc.segs...)

			if got := queryStrPtr(t, `SELECT timerange FROM flows WHERE id = $1`, flID); got == nil || *got != tc.want {
				t.Errorf("flows.timerange = %s, want %s", deref(got), tc.want)
			}
			got, err := store.GetFlowTimerange(context.Background(), flID)
			if err != nil {
				t.Fatalf("GetFlowTimerange: %v", err)
			}
			if got == nil || *got != tc.want {
				t.Errorf("GetFlowTimerange = %s, want %s", deref(got), tc.want)
			}
		})
	}
}

func TestTimerange_FlowTimerangeAfterDelete(t *testing.T) {
	flID := seedFlow(t, false)
	store := newStore(t)
	mustInsert(t, store, flID, "[-2:0_-1:500000000)", "[0:0_1:0]")

	if _, err := store.DeleteSegmentsByTimerange(context.Background(), metastore.DeleteQuery{
		FlowID: flID, Timerange: mustTR(t, "[0:0_"),
	}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if got := queryStrPtr(t, `SELECT timerange FROM flows WHERE id = $1`, flID); got == nil || *got != "[-2:0_-1:500000000)" {
		t.Errorf("flows.timerange = %s, want [-2:0_-1:500000000)", deref(got))
	}

	if _, err := store.DeleteSegmentsByTimerange(context.Background(), metastore.DeleteQuery{
		FlowID: flID, Timerange: mustTR(t, "_"),
	}); err != nil {
		t.Fatalf("delete all: %v", err)
	}
	if got := queryStrPtr(t, `SELECT timerange FROM flows WHERE id = $1`, flID); got != nil {
		t.Errorf("flows.timerange = %q, want NULL", *got)
	}
}

// ADR-0039 rule 9: the page timerange (X-Paging-Timerange) spans the
// earliest and the latest items on the page, for either sort order.
func TestTimerange_PageTimerange(t *testing.T) {
	flID := seedFlow(t, false)
	store := newStore(t)
	mustInsert(t, store, flID, "[-1:500000000_-1:0)", "[0:0_1:0]")
	for _, reverse := range []bool{false, true} {
		page, err := store.ListSegments(context.Background(), metastore.ListQuery{
			FlowID: flID, Limit: 10, ReverseOrder: reverse,
		})
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if got := page.Timerange.String(); got != "[-1:500000000_1:0]" {
			t.Errorf("reverse=%v: page timerange = %s, want [-1:500000000_1:0]", reverse, got)
		}
	}
}

// BR-META-21, ListFlows: a flow matches when one of its segments overlaps
// the query; an empty query matches only flows with no segments; "_"
// applies no condition.
func TestTimerange_ListFlowsFilter(t *testing.T) {
	store := newStore(t)
	gappy := seedFlow(t, false)
	mustInsert(t, store, gappy, "[0:0_10:0)", "[20:0_30:0)")
	empty := seedFlow(t, false)
	closed := seedFlow(t, false)
	mustInsert(t, store, closed, "[100:0_110:0]")
	mine := map[uuid.UUID]string{gappy: "gappy", empty: "empty", closed: "closed"}

	cases := []struct {
		query string
		want  []string
	}{
		{"[12:0_15:0)", nil},
		{"[5:0_6:0)", []string{"gappy"}},
		{"[10:0]", nil},
		{"[110:0]", []string{"closed"}},
		{"(110:0_", nil},
		{"_0:0]", []string{"gappy"}},
		{"()", []string{"empty"}},
		{"[30:0_20:0)", []string{"empty"}},
		{"_", []string{"closed", "empty", "gappy"}},
	}
	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			q := mustTR(t, tc.query)
			page, err := store.ListFlows(context.Background(), metastore.ListFlowsParams{Timerange: &q, Limit: 1000})
			if err != nil {
				t.Fatalf("ListFlows: %v", err)
			}
			var got []string
			for _, f := range page.Items {
				if name, ok := mine[f.ID]; ok {
					got = append(got, name)
				}
			}
			sort.Strings(got)
			if fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}

	q := mustTR(t, "[0:0_9300000000:0)")
	if _, err := store.ListFlows(context.Background(), metastore.ListFlowsParams{Timerange: &q, Limit: 10}); !errors.Is(err, timerange.ErrOutOfRange) {
		t.Errorf("out-of-range err = %v, want ErrOutOfRange", err)
	}
}
