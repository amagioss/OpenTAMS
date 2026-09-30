package conversion_test

import (
	"errors"
	"testing"

	api "github.com/amagioss/opentams/gen/api"
	"github.com/amagioss/opentams/internal/apperror"
	"github.com/amagioss/opentams/internal/domain"
	"github.com/amagioss/opentams/internal/httpx/conversion"
)

func strp(s string) *string { return &s }

// wireTimes is the set of time strings one wire segment carries.
type wireTimes struct {
	timerange, tsOffset, objectTimerange, lastDuration string
}

func (w wireTimes) segment() api.FlowSegment {
	out := api.FlowSegment{ObjectId: "obj", Timerange: w.timerange}
	if w.tsOffset != "" {
		out.TsOffset = strp(w.tsOffset)
	}
	if w.objectTimerange != "" {
		out.ObjectTimerange = strp(w.objectTimerange)
	}
	if w.lastDuration != "" {
		out.LastDuration = strp(w.lastDuration)
	}
	return out
}

func (w wireTimes) post() api.FlowSegmentPost {
	out := api.FlowSegmentPost{ObjectId: "obj", Timerange: w.timerange}
	if w.tsOffset != "" {
		out.TsOffset = strp(w.tsOffset)
	}
	if w.objectTimerange != "" {
		out.ObjectTimerange = strp(w.objectTimerange)
	}
	if w.lastDuration != "" {
		out.LastDuration = strp(w.lastDuration)
	}
	return out
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// Each string is valid and has a canonical form that differs from it, so
// a re-render through String() shows up as a mismatch.
var rawRoundTripCases = []wireTimes{
	{timerange: "10:0"},
	{timerange: "[10:0]"},
	{timerange: "0:0_10:0"},
	{timerange: "[10:0_10:0]"},
	{timerange: "[0:0_1:0)", tsOffset: "-0:500000000"},
	{timerange: "[0:0_1:0)", tsOffset: "-0:0"},
	{timerange: "[0:0_1:0)", objectTimerange: "0:0_10:0"},
	{timerange: "[0:0_1:0)", objectTimerange: "5:0"},
	{timerange: "[0:0_1:0)", lastDuration: "-0:0"},
	{timerange: "[0:0_1:0)", tsOffset: "-1:500000000", objectTimerange: "[1:0_1:0]", lastDuration: "-0:500000000"},
}

// BR-CONV-08 round-trip contract: each time string in
// SegmentToAPI(SegmentFromAPI(w)) is byte for byte the string in w.
func Test_BR_CONV_08_RawRoundTripByteForByte(t *testing.T) {
	for _, w := range rawRoundTripCases {
		t.Run(w.timerange+"|"+w.tsOffset+"|"+w.objectTimerange+"|"+w.lastDuration, func(t *testing.T) {
			seg, err := conversion.SegmentFromAPI(w.segment())
			if err != nil {
				t.Fatalf("SegmentFromAPI: %v", err)
			}
			if seg.TimerangeRaw != w.timerange || seg.TSOffsetRaw != w.tsOffset ||
				seg.ObjectTimerangeRaw != w.objectTimerange || seg.LastDurationRaw != w.lastDuration {
				t.Errorf("raw fields = %q %q %q %q, want %q %q %q %q",
					seg.TimerangeRaw, seg.TSOffsetRaw, seg.ObjectTimerangeRaw, seg.LastDurationRaw,
					w.timerange, w.tsOffset, w.objectTimerange, w.lastDuration)
			}
			assertWireTimes(t, conversion.SegmentToAPI(seg), w)
		})
	}
}

// BR-CONV-08 on the POST path: the decoder keeps the client string, so the
// accepted segment renders back exactly as posted.
func Test_BR_CONV_08_PostKeepsRawStrings(t *testing.T) {
	for _, w := range rawRoundTripCases {
		t.Run(w.timerange+"|"+w.tsOffset+"|"+w.objectTimerange+"|"+w.lastDuration, func(t *testing.T) {
			var body api.PostFlowSegmentsJSONRequestBody
			if err := body.FromFlowSegmentPost(w.post()); err != nil {
				t.Fatalf("FromFlowSegmentPost: %v", err)
			}
			params, err := conversion.RegisterParamsFromAPI(body)
			if err != nil {
				t.Fatalf("RegisterParamsFromAPI: %v", err)
			}
			assertWireTimes(t, conversion.SegmentToAPI(params.Segments[0]), w)
		})
	}
}

func assertWireTimes(t *testing.T, got api.FlowSegment, want wireTimes) {
	t.Helper()
	if got.Timerange != want.timerange {
		t.Errorf("timerange = %q, want %q", got.Timerange, want.timerange)
	}
	if deref(got.TsOffset) != want.tsOffset {
		t.Errorf("ts_offset = %q, want %q", deref(got.TsOffset), want.tsOffset)
	}
	if deref(got.ObjectTimerange) != want.objectTimerange {
		t.Errorf("object_timerange = %q, want %q", deref(got.ObjectTimerange), want.objectTimerange)
	}
	if deref(got.LastDuration) != want.lastDuration {
		t.Errorf("last_duration = %q, want %q", deref(got.LastDuration), want.lastDuration)
	}
}

// BR-CONV-08: the failure body also returns the client's string.
func Test_BR_CONV_08_FailureEntryUsesRawTimerange(t *testing.T) {
	out := conversion.RegisterFailureToAPI([]domain.FailedSegment{{
		Segment: domain.Segment{ObjectID: "o", Timerange: mustParseTR(t, "[10:0]"), TimerangeRaw: "10:0"},
		Type:    "t", Title: "T", Reason: "r",
	}})
	if len(out.FailedSegments) != 1 || out.FailedSegments[0].Timerange == nil {
		t.Fatalf("failed_segments = %+v", out.FailedSegments)
	}
	if got := *out.FailedSegments[0].Timerange; got != "10:0" {
		t.Errorf("failed timerange = %q, want %q", got, "10:0")
	}
}

// Instantaneous ranges with exclusive markers match the schema regex but
// do not parse (BR-TR-05).
var unparseableTimeranges = []string{"(10:0)", "(10:0]", "[10:0)"}

// BR-CONV-09: a string that fails to parse fails the whole request with
// schema-validation, even when other segments in the batch are valid.
func Test_BR_CONV_09_UnparseableBodyTimerangeIsRequestError(t *testing.T) {
	for _, tr := range unparseableTimeranges {
		t.Run(tr, func(t *testing.T) {
			var body api.PostFlowSegmentsJSONRequestBody
			if err := body.FromFlowSegmentPostBody1([]api.FlowSegmentPost{
				{ObjectId: "ok", Timerange: "[0:0_1:0)"},
				{ObjectId: "bad", Timerange: tr},
			}); err != nil {
				t.Fatalf("FromFlowSegmentPostBody1: %v", err)
			}
			_, err := conversion.RegisterParamsFromAPI(body)
			assertSchemaValidation(t, err)
		})
	}
}

// BR-CONV-09: an empty range parses, so conversion passes it to the
// service, which reports it per segment (BR-SEG-02).
func Test_BR_CONV_09_EmptyBodyTimerangePassesThrough(t *testing.T) {
	for _, tr := range []string{"()", "[10:0_5:0)", "(0:0_0:1)"} {
		t.Run(tr, func(t *testing.T) {
			var body api.PostFlowSegmentsJSONRequestBody
			if err := body.FromFlowSegmentPost(api.FlowSegmentPost{ObjectId: "o", Timerange: tr}); err != nil {
				t.Fatalf("FromFlowSegmentPost: %v", err)
			}
			params, err := conversion.RegisterParamsFromAPI(body)
			if err != nil {
				t.Fatalf("RegisterParamsFromAPI(%q): %v", tr, err)
			}
			if got := params.Segments[0]; !got.Timerange.IsEmpty() || got.TimerangeRaw != tr {
				t.Errorf("segment = %+v, want empty range with raw %q", got, tr)
			}
		})
	}
}

// BR-CONV-09: the same split applies to query parameters.
func Test_BR_CONV_09_QueryTimerange(t *testing.T) {
	for _, tr := range unparseableTimeranges {
		t.Run("unparseable "+tr, func(t *testing.T) {
			v := tr
			_, err := conversion.ListParamsFromAPI(api.GetFlowSegmentsParams{Timerange: &v})
			assertSchemaValidation(t, err)
			_, err = conversion.DeleteParamsFromAPI(api.DeleteFlowSegmentsParams{Timerange: &v})
			assertSchemaValidation(t, err)
		})
	}
	t.Run("empty range passes", func(t *testing.T) {
		v := "[10:0_5:0)"
		lp, err := conversion.ListParamsFromAPI(api.GetFlowSegmentsParams{Timerange: &v})
		if err != nil || lp.Timerange == nil || !lp.Timerange.IsEmpty() {
			t.Errorf("ListParamsFromAPI = %+v, %v; want empty range, nil", lp.Timerange, err)
		}
		dp, err := conversion.DeleteParamsFromAPI(api.DeleteFlowSegmentsParams{Timerange: &v})
		if err != nil || !dp.Timerange.IsEmpty() {
			t.Errorf("DeleteParamsFromAPI = %+v, %v; want empty range, nil", dp.Timerange, err)
		}
	})
}

func assertSchemaValidation(t *testing.T, err error) {
	t.Helper()
	var ae *apperror.AppError
	if !errors.As(err, &ae) || ae.Code != apperror.ErrSchemaValidation {
		t.Fatalf("err = %v, want *apperror.AppError with code %q", err, apperror.ErrSchemaValidation)
	}
}
