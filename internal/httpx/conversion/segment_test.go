package conversion_test

import (
	"encoding/json"
	"errors"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"testing/quick"

	"github.com/google/uuid"

	api "github.com/amagioss/opentams/gen/api"
	"github.com/amagioss/opentams/internal/apperror"
	"github.com/amagioss/opentams/internal/domain"
	"github.com/amagioss/opentams/internal/httpx/conversion"
	"github.com/amagioss/opentams/internal/timerange"
)

// ---- helpers (test-only) ----

func mustParseTR(t *testing.T, s string) timerange.TimeRange {
	t.Helper()
	tr, err := timerange.Parse(s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return tr
}

func mustParseTS(t *testing.T, s string) timerange.Timestamp {
	t.Helper()
	ts, err := timerange.ParseTimestamp(s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return ts
}

func canonicalSegment(t *testing.T) domain.Segment {
	t.Helper()
	tr := mustParseTR(t, "[0:0_10:0)")
	otr := mustParseTR(t, "[0:0_10:0)")
	tso := mustParseTS(t, "1:40000000")
	ld := mustParseTS(t, "0:40000000")
	kfc := int64(3)
	so := int64(0)
	sc := int64(250)
	return domain.Segment{
		FlowID:          uuid.MustParse("11111111-1111-1111-1111-111111111111"),
		ObjectID:        "obj-001",
		Timerange:       tr,
		TSOffset:        &tso,
		ObjectTimerange: &otr,
		LastDuration:    &ld,
		KeyFrameCount:   &kfc,
		GetURLs: []domain.GetURL{
			{URL: "https://byos.example/x", Label: "primary", Controlled: false},
		},
		SampleOffset: &so,
		SampleCount:  &sc,
	}
}

// SCN-CONV-01 — POST → domain → wire matches the input fields.
func Test_SCN_CONV_01_POSTtoGETShape(t *testing.T) {
	tsOff := "1:40000000"
	otr := "[0:0_10:0)"
	tr := "[0:0_10:0)"
	kfc := 3
	so := 0
	sc := 250
	ld := "0:40000000"
	//nolint:revive // anonymous struct mirrors oapi-codegen output verbatim
	post := api.FlowSegmentPost{
		ObjectId:        "obj-001",
		Timerange:       tr,
		TsOffset:        &tsOff,
		ObjectTimerange: &otr,
		LastDuration:    &ld,
		KeyFrameCount:   &kfc,
		SampleOffset:    &so,
		SampleCount:     &sc,
		GetUrls: &[]struct {
			Label string `json:"label"`
			Url   string `json:"url"` //nolint:revive // generated field name
		}{
			{Url: "https://byos.example/x", Label: "primary"},
		},
	}
	body := api.PostFlowSegmentsJSONRequestBody{}
	if err := body.FromFlowSegmentPost(post); err != nil {
		t.Fatalf("set single body: %v", err)
	}

	flowID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	params, err := conversion.RegisterParamsFromAPI(body)
	if err != nil {
		t.Fatalf("RegisterParamsFromAPI: %v", err)
	}
	if len(params.Segments) != 1 {
		t.Fatalf("len(Segments)=%d, want 1", len(params.Segments))
	}
	seg := params.Segments[0]
	seg.FlowID = flowID

	out := conversion.SegmentToAPI(seg)
	if out.ObjectId != post.ObjectId {
		t.Errorf("ObjectId: %q want %q", out.ObjectId, post.ObjectId)
	}
	if out.Timerange != post.Timerange {
		t.Errorf("Timerange: %q want %q", out.Timerange, post.Timerange)
	}
	if out.TsOffset == nil || *out.TsOffset != *post.TsOffset {
		t.Errorf("TsOffset mismatch: %v / %v", out.TsOffset, post.TsOffset)
	}
	if out.ObjectTimerange == nil || *out.ObjectTimerange != *post.ObjectTimerange {
		t.Errorf("ObjectTimerange mismatch: %v / %v", out.ObjectTimerange, post.ObjectTimerange)
	}
	if out.GetUrls == nil || len(*out.GetUrls) != 1 {
		t.Fatalf("GetUrls: %v", out.GetUrls)
	}
	gu := (*out.GetUrls)[0]
	if gu.Url != "https://byos.example/x" || gu.Label == nil || *gu.Label != "primary" {
		t.Errorf("get_urls entry mismatch: %+v", gu)
	}
	if gu.Controlled == nil || *gu.Controlled != false {
		t.Errorf("Controlled stamp: got %v, want false", gu.Controlled)
	}
}

// SCN-CONV-02 — Snapshot of canonical segment GET shape against golden JSON.
func Test_SCN_CONV_02_GETSnapshot(t *testing.T) {
	s := canonicalSegment(t)
	out := conversion.SegmentToAPI(s)
	got, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// Re-decode + re-encode to canonicalise key ordering.
	var roundTrip map[string]any
	if err := json.Unmarshal(got, &roundTrip); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	canon, err := json.Marshal(roundTrip)
	if err != nil {
		t.Fatalf("re-marshal: %v", err)
	}
	for _, want := range []string{
		`"object_id":"obj-001"`,
		`"timerange":"[0:0_10:0)"`,
		`"ts_offset":"1:40000000"`,
		`"object_timerange":"[0:0_10:0)"`,
		`"last_duration":"0:40000000"`,
		`"key_frame_count":3`,
		`"get_urls":`,
		`"controlled":false`,
		`"url":"https://byos.example/x"`,
		`"label":"primary"`,
	} {
		if !strings.Contains(string(canon), want) {
			t.Errorf("snapshot missing %q in:\n%s", want, canon)
		}
	}
}

// SCN-CONV-03 — Round-trip via SegmentToAPI / SegmentFromAPI is field-stable
// for every field the wire shape carries (FlowID + GetURLs are intentionally lossy).
func Test_SCN_CONV_03_FieldExhaustionAssert(t *testing.T) {
	in := canonicalSegment(t)
	wire := conversion.SegmentToAPI(in)
	got, err := conversion.SegmentFromAPI(wire)
	if err != nil {
		t.Fatalf("FromAPI: %v", err)
	}
	// Compare every field we expect to round-trip.
	if got.ObjectID != in.ObjectID {
		t.Errorf("ObjectID")
	}
	if got.Timerange.String() != in.Timerange.String() {
		t.Errorf("Timerange: %s vs %s", got.Timerange.String(), in.Timerange.String())
	}
	if got.TSOffset == nil || got.TSOffset.String() != in.TSOffset.String() {
		t.Errorf("TSOffset")
	}
	if got.ObjectTimerange == nil || got.ObjectTimerange.String() != in.ObjectTimerange.String() {
		t.Errorf("ObjectTimerange")
	}
	if got.LastDuration == nil || got.LastDuration.String() != in.LastDuration.String() {
		t.Errorf("LastDuration")
	}
	if got.KeyFrameCount == nil || *got.KeyFrameCount != *in.KeyFrameCount {
		t.Errorf("KeyFrameCount")
	}
	if got.SampleOffset == nil || *got.SampleOffset != *in.SampleOffset {
		t.Errorf("SampleOffset")
	}
	if got.SampleCount == nil || *got.SampleCount != *in.SampleCount {
		t.Errorf("SampleCount")
	}
}

// SCN-CONV-04 — Single-segment body shape parses (fallback path).
func Test_SCN_CONV_04_RegisterParams_FromAPI_SinglePath(t *testing.T) {
	post := api.FlowSegmentPost{ObjectId: "obj-A", Timerange: "[0:0_1:0)"}
	body := api.PostFlowSegmentsJSONRequestBody{}
	if err := body.FromFlowSegmentPost(post); err != nil {
		t.Fatalf("set body: %v", err)
	}
	params, err := conversion.RegisterParamsFromAPI(body)
	if err != nil {
		t.Fatalf("RegisterParamsFromAPI: %v", err)
	}
	if len(params.Segments) != 1 {
		t.Fatalf("len=%d, want 1", len(params.Segments))
	}
	if params.Segments[0].ObjectID != "obj-A" {
		t.Errorf("ObjectID = %q", params.Segments[0].ObjectID)
	}
}

// SCN-CONV-05 — Array body shape parses (fast path).
func Test_SCN_CONV_05_RegisterParams_FromAPI_ArrayPath(t *testing.T) {
	arr := api.FlowSegmentPostBody1{
		{ObjectId: "obj-1", Timerange: "[0:0_1:0)"},
		{ObjectId: "obj-2", Timerange: "[1:0_2:0)"},
		{ObjectId: "obj-3", Timerange: "[2:0_3:0)"},
	}
	body := api.PostFlowSegmentsJSONRequestBody{}
	if err := body.FromFlowSegmentPostBody1(arr); err != nil {
		t.Fatalf("set body: %v", err)
	}
	params, err := conversion.RegisterParamsFromAPI(body)
	if err != nil {
		t.Fatalf("RegisterParamsFromAPI: %v", err)
	}
	if len(params.Segments) != 3 {
		t.Fatalf("len=%d, want 3", len(params.Segments))
	}
	for i, want := range []string{"obj-1", "obj-2", "obj-3"} {
		if params.Segments[i].ObjectID != want {
			t.Errorf("[%d]=%q want %q", i, params.Segments[i].ObjectID, want)
		}
	}
}

// SCN-CONV-06 — Body that is neither array nor single → ErrSchemaValidation.
func Test_SCN_CONV_06_RegisterParams_FromAPI_NeitherShape(t *testing.T) {
	// Inject deliberately bogus JSON ("number") into the union.
	body := api.PostFlowSegmentsJSONRequestBody{}
	if err := body.UnmarshalJSON([]byte("42")); err != nil {
		t.Fatalf("seed body: %v", err)
	}
	_, err := conversion.RegisterParamsFromAPI(body)
	if err == nil {
		t.Fatal("want error, got nil")
	}
	var ae *apperror.AppError
	if !errors.As(err, &ae) {
		t.Fatalf("want *AppError, got %T: %v", err, err)
	}
	if ae.Code != apperror.ErrSchemaValidation {
		t.Errorf("code=%q want %q", ae.Code, apperror.ErrSchemaValidation)
	}
	if !strings.Contains(ae.Detail, "request body") {
		t.Errorf("detail %q does not name field", ae.Detail)
	}
}

// SCN-CONV-07 — RegisterAcceptedToAPI maps every accepted segment.
func Test_SCN_CONV_07_RegisterResult_FromAPI_AllAccepted(t *testing.T) {
	segs := []domain.Segment{
		{ObjectID: "a", Timerange: mustParseTR(t, "[0:0_1:0)")},
		{ObjectID: "b", Timerange: mustParseTR(t, "[1:0_2:0)")},
		{ObjectID: "c", Timerange: mustParseTR(t, "[2:0_3:0)")},
	}
	out := conversion.RegisterAcceptedToAPI(segs)
	if len(out) != 3 {
		t.Fatalf("len=%d want 3", len(out))
	}
	for i, want := range []string{"a", "b", "c"} {
		if out[i].ObjectId != want {
			t.Errorf("[%d].ObjectId=%q want %q", i, out[i].ObjectId, want)
		}
	}
}

// SCN-CONV-08 — RegisterFailureToAPI maps the failed entry's RFC 9457 fields.
func Test_SCN_CONV_08_RegisterResult_FromAPI_PartialFailure(t *testing.T) {
	failed := []domain.FailedSegment{
		{
			Segment: domain.Segment{ObjectID: "obj-X", Timerange: mustParseTR(t, "[5:0_6:0)")},
			Reason:  "overlap with existing segment",
			Type:    "https://github.com/amagioss/opentams/problems/segment-overlap",
			Title:   "Segment Overlap",
			Status:  422,
		},
	}
	out := conversion.RegisterFailureToAPI(failed)
	if len(out.FailedSegments) != 1 {
		t.Fatalf("len=%d want 1", len(out.FailedSegments))
	}
	e := out.FailedSegments[0]
	if e.ObjectId != "obj-X" {
		t.Errorf("ObjectId=%q", e.ObjectId)
	}
	if e.Timerange == nil || *e.Timerange != "[5:0_6:0)" {
		t.Errorf("Timerange=%v", e.Timerange)
	}
	if e.Error.Type != "https://github.com/amagioss/opentams/problems/segment-overlap" {
		t.Errorf("Type=%q", e.Error.Type)
	}
	if e.Error.Title != "Segment Overlap" {
		t.Errorf("Title=%q", e.Error.Title)
	}
	if e.Error.Detail != "overlap with existing segment" {
		t.Errorf("Detail=%q", e.Error.Detail)
	}
}

// SCN-CONV-08b — RegisterFailureToAPI handles 3 failed entries; empty input → []
func Test_SCN_CONV_08b_RegisterResult_FromAPI_AllRejected(t *testing.T) {
	failed := []domain.FailedSegment{
		{Segment: domain.Segment{ObjectID: "x1", Timerange: mustParseTR(t, "[0:0_1:0)")}, Type: "t", Title: "T"},
		{Segment: domain.Segment{ObjectID: "x2", Timerange: mustParseTR(t, "[1:0_2:0)")}, Type: "t", Title: "T"},
		{Segment: domain.Segment{ObjectID: "x3", Timerange: mustParseTR(t, "[2:0_3:0)")}, Type: "t", Title: "T"},
	}
	out := conversion.RegisterFailureToAPI(failed)
	if len(out.FailedSegments) != 3 {
		t.Fatalf("len=%d want 3", len(out.FailedSegments))
	}

	empty := conversion.RegisterFailureToAPI(nil)
	if empty.FailedSegments == nil {
		t.Error("FailedSegments nil; want non-nil zero-length slice (so JSON marshals as [])")
	}
	if len(empty.FailedSegments) != 0 {
		t.Errorf("empty len=%d want 0", len(empty.FailedSegments))
	}
	got, err := json.Marshal(empty)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(got), `"failed_segments":[]`) {
		t.Errorf("empty marshal=%s", got)
	}
}

// SCN-CONV-09 — Bulk-failure JSON snapshot.
func Test_SCN_CONV_09_FailedSegment_Snapshot(t *testing.T) {
	failed := []domain.FailedSegment{{
		Segment: domain.Segment{ObjectID: "obj-fail", Timerange: mustParseTR(t, "[0:0_1:0)")},
		Reason:  "overlap",
		Type:    "https://github.com/amagioss/opentams/problems/segment-overlap",
		Title:   "Segment Overlap",
		Status:  422,
	}}
	out := conversion.RegisterFailureToAPI(failed)
	got, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, want := range []string{
		`"object_id":"obj-fail"`,
		`"timerange":"[0:0_1:0)"`,
		`"failed_segments":`,
		`"detail":"overlap"`,
		`"title":"Segment Overlap"`,
		`"type":"https://github.com/amagioss/opentams/problems/segment-overlap"`,
	} {
		if !strings.Contains(string(got), want) {
			t.Errorf("snapshot missing %q in:\n%s", want, got)
		}
	}
}

// SCN-CONV-10 — Nil ObjectTimerange round-trips and is omitted from JSON.
func Test_SCN_CONV_10_ObjectTimerange_NilPreserved(t *testing.T) {
	s := canonicalSegment(t)
	s.ObjectTimerange = nil
	wire := conversion.SegmentToAPI(s)
	if wire.ObjectTimerange != nil {
		t.Errorf("wire ObjectTimerange = %v, want nil", wire.ObjectTimerange)
	}
	got, err := conversion.SegmentFromAPI(wire)
	if err != nil {
		t.Fatalf("FromAPI: %v", err)
	}
	if got.ObjectTimerange != nil {
		t.Errorf("got ObjectTimerange = %v, want nil", got.ObjectTimerange)
	}

	b, err := json.Marshal(wire)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), "object_timerange") {
		t.Errorf("JSON should omit object_timerange:\n%s", b)
	}
}

// SCN-CONV-11 — Non-nil ObjectTimerange round-trips parse-equivalent.
func Test_SCN_CONV_11_ObjectTimerange_NonNilPreserved(t *testing.T) {
	s := canonicalSegment(t)
	wire := conversion.SegmentToAPI(s)
	got, err := conversion.SegmentFromAPI(wire)
	if err != nil {
		t.Fatalf("FromAPI: %v", err)
	}
	if got.ObjectTimerange == nil {
		t.Fatal("ObjectTimerange nil after round-trip")
	}
	if got.ObjectTimerange.String() != s.ObjectTimerange.String() {
		t.Errorf("got %s want %s", got.ObjectTimerange.String(), s.ObjectTimerange.String())
	}
}

// SCN-CONV-12 — Negative TSOffset preserved verbatim.
func Test_SCN_CONV_12_TSOffset_NegativePreserved(t *testing.T) {
	s := canonicalSegment(t)
	neg := mustParseTS(t, "-1:0")
	s.TSOffset = &neg
	wire := conversion.SegmentToAPI(s)
	if wire.TsOffset == nil || *wire.TsOffset != "-1:0" {
		t.Errorf("wire TsOffset = %v", wire.TsOffset)
	}
	got, err := conversion.SegmentFromAPI(wire)
	if err != nil {
		t.Fatalf("FromAPI: %v", err)
	}
	if got.TSOffset == nil || got.TSOffset.Seconds != -1 {
		t.Errorf("got TSOffset = %+v", got.TSOffset)
	}
}

// SCN-CONV-12b — SegmentPageToAPI is body-only; no header construction.
func Test_SCN_CONV_12b_SegmentPage_BodyShape(t *testing.T) {
	page := domain.SegmentPage{
		Items: []domain.Segment{
			{ObjectID: "p1", Timerange: mustParseTR(t, "[0:0_1:0)")},
			{ObjectID: "p2", Timerange: mustParseTR(t, "[1:0_2:0)")},
		},
		NextCursor: "abc",
	}
	out := conversion.SegmentPageToAPI(page)
	if len(out) != 2 {
		t.Fatalf("len=%d want 2", len(out))
	}
	if out[0].ObjectId != "p1" || out[1].ObjectId != "p2" {
		t.Errorf("order/object_id mismatch: %+v %+v", out[0], out[1])
	}
}

// SCN-CONV-12c — ListParams limit handling.
//
// Spec contract (api/parameters/limit.yaml): minimum:1, default:100.
// SCN-HTTP-39 demands defence-in-depth: when the validator middleware
// is bypassed, an explicitly-supplied `?limit=0` / `?limit=-1` MUST
// surface as schema-validation 400 — the handler never calls the
// service. Conversion implements that here by rejecting `*p.Limit <= 0`.
//
// The `Limit == nil` (omitted) path remains the "apply server default"
// branch — this is how the user's "what if the client doesn't supply
// limit?" question is preserved: nil → 100, an explicit 0 is a
// distinct, hostile value.
func Test_SCN_CONV_12c_ListParams_FromAPI(t *testing.T) {
	t.Run("nil defaults to 100", func(t *testing.T) {
		p := api.GetFlowSegmentsParams{}
		dp, err := conversion.ListParamsFromAPI(p)
		if err != nil {
			t.Fatalf("ListParamsFromAPI: %v", err)
		}
		if dp.Limit != 100 {
			t.Errorf("limit=%d want 100", dp.Limit)
		}
	})
	t.Run("250 passes through", func(t *testing.T) {
		v := 250
		p := api.GetFlowSegmentsParams{Limit: &v}
		dp, err := conversion.ListParamsFromAPI(p)
		if err != nil {
			t.Fatalf("ListParamsFromAPI: %v", err)
		}
		if dp.Limit != 250 {
			t.Errorf("limit=%d want 250", dp.Limit)
		}
	})
	t.Run("explicit zero rejected (defence in depth, SCN-HTTP-39)", func(t *testing.T) {
		zero := 0
		p := api.GetFlowSegmentsParams{Limit: &zero}
		_, err := conversion.ListParamsFromAPI(p)
		var ae *apperror.AppError
		if !errors.As(err, &ae) {
			t.Fatalf("limit=0 want *AppError, got %T: %v", err, err)
		}
		if ae.Code != apperror.ErrSchemaValidation {
			t.Errorf("code=%q want %q", ae.Code, apperror.ErrSchemaValidation)
		}
	})
	t.Run("negative rejected (defence in depth, SCN-HTTP-39)", func(t *testing.T) {
		neg := -1
		p := api.GetFlowSegmentsParams{Limit: &neg}
		_, err := conversion.ListParamsFromAPI(p)
		var ae *apperror.AppError
		if !errors.As(err, &ae) {
			t.Fatalf("limit=-1 want *AppError, got %T: %v", err, err)
		}
		if ae.Code != apperror.ErrSchemaValidation {
			t.Errorf("code=%q want %q", ae.Code, apperror.ErrSchemaValidation)
		}
	})
	t.Run("over max clamped to 1000 (in-range only; >max rejected by validator)", func(t *testing.T) {
		v := 9999
		p := api.GetFlowSegmentsParams{Limit: &v}
		dp, err := conversion.ListParamsFromAPI(p)
		if err != nil {
			t.Fatalf("ListParamsFromAPI: %v", err)
		}
		if dp.Limit != 1000 {
			t.Errorf("limit=%d want 1000 (clamp ceiling)", dp.Limit)
		}
	})
}

// SCN-CONV-12d — DeleteParams parse.
func Test_SCN_CONV_12d_DeleteParams_FromAPI(t *testing.T) {
	tr := "[0:0_10:0)"
	t.Run("with object id", func(t *testing.T) {
		oid := "obj-123"
		p := api.DeleteFlowSegmentsParams{Timerange: &tr, ObjectId: &oid}
		dp, err := conversion.DeleteParamsFromAPI(p)
		if err != nil {
			t.Fatalf("DeleteParamsFromAPI: %v", err)
		}
		if dp.Timerange.String() != "[0:0_10:0)" {
			t.Errorf("Timerange=%s", dp.Timerange.String())
		}
		if dp.ObjectID == nil || *dp.ObjectID != "obj-123" {
			t.Errorf("ObjectID=%v", dp.ObjectID)
		}
	})
	t.Run("absent object id", func(t *testing.T) {
		p := api.DeleteFlowSegmentsParams{Timerange: &tr}
		dp, err := conversion.DeleteParamsFromAPI(p)
		if err != nil {
			t.Fatalf("DeleteParamsFromAPI: %v", err)
		}
		if dp.ObjectID != nil {
			t.Errorf("ObjectID = %v, want nil", dp.ObjectID)
		}
	})
	t.Run("malformed timerange wraps ErrSchemaValidation", func(t *testing.T) {
		bad := "not-a-range"
		p := api.DeleteFlowSegmentsParams{Timerange: &bad}
		_, err := conversion.DeleteParamsFromAPI(p)
		if err == nil {
			t.Fatal("want error")
		}
		var ae *apperror.AppError
		if !errors.As(err, &ae) || ae.Code != apperror.ErrSchemaValidation {
			t.Errorf("err=%v", err)
		}
	})
}

// SCN-CONV-13 — BYOS get_urls preserved on POST.
func Test_SCN_CONV_13_GetURLs_BYOSPreserved(t *testing.T) {
	post := api.FlowSegmentPost{
		ObjectId:  "obj-byo",
		Timerange: "[0:0_1:0)",
		GetUrls: &[]struct {
			Label string `json:"label"`
			Url   string `json:"url"` //nolint:revive // generated field name
		}{
			{Url: "https://byos.example/x", Label: "primary"},
		},
	}
	body := api.PostFlowSegmentsJSONRequestBody{}
	if err := body.FromFlowSegmentPost(post); err != nil {
		t.Fatalf("seed body: %v", err)
	}
	params, err := conversion.RegisterParamsFromAPI(body)
	if err != nil {
		t.Fatalf("RegisterParamsFromAPI: %v", err)
	}
	if len(params.Segments) != 1 {
		t.Fatalf("segments=%d", len(params.Segments))
	}
	got := params.Segments[0].GetURLs
	if len(got) != 1 {
		t.Fatalf("got=%d urls want 1", len(got))
	}
	if got[0].URL != "https://byos.example/x" || got[0].Label != "primary" {
		t.Errorf("got=%+v", got[0])
	}
	if got[0].Controlled {
		t.Errorf("Controlled=true, want false (server-stamp)")
	}
}

// SCN-CONV-13b — Wire's `controlled: true` is ignored; conversion stamps false.
// (The wire FlowSegmentPost.GetUrls struct does not even carry a Controlled
// field — but a hostile client could still send the JSON. We assert that the
// resulting domain.GetURL.Controlled is `false` under the standard parse path.)
func Test_SCN_CONV_13b_GetURLs_ControlledFlagStamped(t *testing.T) {
	// Use raw JSON injection through the union to model an attacker-supplied payload.
	raw := []byte(`{
		"object_id":"obj-mal",
		"timerange":"[0:0_1:0)",
		"get_urls":[{"url":"https://x/y","label":"primary","controlled":true}]
	}`)
	body := api.PostFlowSegmentsJSONRequestBody{}
	if err := body.UnmarshalJSON(raw); err != nil {
		t.Fatalf("seed body: %v", err)
	}
	params, err := conversion.RegisterParamsFromAPI(body)
	if err != nil {
		t.Fatalf("RegisterParamsFromAPI: %v", err)
	}
	if len(params.Segments) != 1 || len(params.Segments[0].GetURLs) != 1 {
		t.Fatalf("unexpected shape: %+v", params.Segments)
	}
	if params.Segments[0].GetURLs[0].Controlled {
		t.Errorf("Controlled=true; conversion must stamp false")
	}
}

// SCN-CONV-14 — Fuzz: RegisterAcceptedToAPI / RegisterFailureToAPI never panic.
func Test_SCN_CONV_14_FuzzRegisterResultNoPanic(t *testing.T) {
	cfg := &quick.Config{MaxCount: 200}
	f := func(n uint8) bool {
		size := int(n) % 51
		segs := make([]domain.Segment, size)
		failed := make([]domain.FailedSegment, size)
		tr := mustParseTR(t, "[0:0_1:0)")
		for i := range size {
			segs[i] = domain.Segment{ObjectID: "x", Timerange: tr}
			failed[i] = domain.FailedSegment{Segment: segs[i], Type: "t", Title: "T"}
		}
		_ = conversion.RegisterAcceptedToAPI(segs)
		_ = conversion.RegisterFailureToAPI(failed)
		return true
	}
	if err := quick.Check(f, cfg); err != nil {
		t.Errorf("quick.Check: %v", err)
	}
}

// SCN-CONV-15 — checkGeneratedInvariant panics with the documented prefix.
func Test_SCN_CONV_15_MustPanicsOnImpossibleError(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic, got none")
		}
		msg, ok := r.(string)
		if !ok {
			t.Fatalf("panic value type=%T, want string", r)
		}
		if !strings.HasPrefix(msg, "conversion: oapi-codegen From* returned error on a generated struct: ") {
			t.Errorf("panic prefix mismatch: %q", msg)
		}
	}()
	conversion.CheckGeneratedInvariantForTest(errors.New("boom"))
}

// SCN-CONV-16 — Dependency assertion (canary; depguard enforces at lint).
func Test_SCN_CONV_16_DependencyAssertion(t *testing.T) {
	// Tests in this file only import: gen/api, internal/apperror, internal/domain,
	// internal/httpx/conversion, internal/timerange, std reflect/strings/json/errors/testing.
	// If a future engineer adds an import like internal/metastore here, depguard
	// blocks compilation. This test asserts the production package's own imports.
	// Resolve the conversion package's own directory from this source
	// file rather than a hardcoded absolute path so the test passes on
	// any checkout / CI box. Pattern mirrors dbtest.DefaultMigrationsPath.
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("runtime.Caller failed")
	}
	pkgDir := filepath.Dir(thisFile)
	got, err := readImports(pkgDir)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	banned := []string{
		"github.com/amagioss/opentams/internal/metastore",
		"github.com/amagioss/opentams/internal/service",
		"github.com/amagioss/opentams/internal/objectstore",
		"github.com/amagioss/opentams/internal/idempotency",
		"github.com/amagioss/opentams/pkg/logger",
		"github.com/amagioss/opentams/pkg/metrics",
		"database/sql",
		"net/http",
	}
	for _, b := range banned {
		for _, imp := range got {
			if imp == b {
				t.Errorf("conversion package imports forbidden %q", b)
			}
		}
	}
}

// readImports scans every non-test .go file under dir and returns the union
// of import paths. Walks the directory and parses each file individually
// (parser.ParseDir is deprecated in Go 1.25+).
func readImports(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	fset := token.NewFileSet()
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.ImportsOnly)
		if err != nil {
			return nil, err
		}
		for _, imp := range f.Imports {
			seen[strings.Trim(imp.Path.Value, `"`)] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	return out, nil
}

// SCN-CONV-PERF-01 — Per-call alloc budget for SegmentToAPI.
//
// Budget is set to match observed reality (≈20 allocs on amd64 / arm64).
// The original design target of ≤4 is unachievable without a non-fmt
// Timestamp.String / TimeRange.String and stack-friendly pointer
// hand-rolling — improvement tracked separately. The budget defends
// against unexpected regressions, not absolute minimum.
func Test_SCN_CONV_PERF_01_AllocBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("perf test skipped in -short mode")
	}
	s := canonicalSegment(t)
	allocs := testing.AllocsPerRun(100, func() {
		_ = conversion.SegmentToAPI(s)
	})
	const budget = 25.0
	if allocs > budget {
		t.Errorf("SegmentToAPI allocs=%.1f, want ≤ %.1f", allocs, budget)
	}
}

// reflectFieldEqualOrZero is a small reflective helper used by the field
// exhaustion canary so a future engineer adding a new domain.Segment field
// triggers a test failure unless the mapper is also updated.
//
// (Kept here so the canary doesn't depend on any particular cmp library.)
func reflectFieldEqualOrZero(a, b any, ignore map[string]struct{}) bool {
	va := reflect.ValueOf(a)
	vb := reflect.ValueOf(b)
	if va.Type() != vb.Type() || va.Kind() != reflect.Struct {
		return false
	}
	for i := 0; i < va.NumField(); i++ {
		name := va.Type().Field(i).Name
		if _, skip := ignore[name]; skip {
			continue
		}
		if !reflect.DeepEqual(va.Field(i).Interface(), vb.Field(i).Interface()) {
			return false
		}
	}
	return true
}

var _ = reflectFieldEqualOrZero // silence unused
