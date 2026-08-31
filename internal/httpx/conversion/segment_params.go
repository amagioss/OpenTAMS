package conversion

import (
	api "github.com/amagioss/opentams/gen/api"
	"github.com/amagioss/opentams/internal/apperror"
	"github.com/amagioss/opentams/internal/domain"
	"github.com/amagioss/opentams/internal/timerange"
)

const (
	defaultListLimit = 100
	maxListLimit     = 1000
)

// RegisterParamsFromAPI decodes a POST /flows/{id}/segments body. The
// body is a oneOf union of `[FlowSegmentPost]` (array) and
// `FlowSegmentPost` (single). Tries the array branch first via
// `AsFlowSegmentPostBody1` (oapi-codegen's generated name for the
// array variant); falls back to `AsFlowSegmentPost` on failure. If
// neither shape parses, returns wrapped `apperror.ErrSchemaValidation`
// with "request body" in the detail.
//
// FlowID is set by the handler from the path; this decoder leaves it
// zero-valued. Per INV-CONV-08, GetURL.Controlled is stamped `false`
// regardless of the wire's value.
func RegisterParamsFromAPI(body api.PostFlowSegmentsJSONRequestBody) (domain.RegisterParams, error) {
	if arr, err := body.AsFlowSegmentPostBody1(); err == nil && len(arr) > 0 {
		return registerParamsFromPostArray(arr)
	}
	if single, err := body.AsFlowSegmentPost(); err == nil && single.ObjectId != "" {
		segs, err := registerParamsFromPostArray([]api.FlowSegmentPost{single})
		if err != nil {
			return domain.RegisterParams{}, err
		}
		return segs, nil
	}
	return domain.RegisterParams{}, apperror.New(
		apperror.ErrSchemaValidation,
		"request body: expected FlowSegmentPost or [FlowSegmentPost]",
	)
}

func registerParamsFromPostArray(in []api.FlowSegmentPost) (domain.RegisterParams, error) {
	out := domain.RegisterParams{Segments: make([]domain.Segment, 0, len(in))}
	for i, p := range in {
		seg, err := postToSegment(p)
		if err != nil {
			return domain.RegisterParams{}, apperror.New(
				apperror.ErrSchemaValidation,
				fieldIdx("segments", i)+": "+err.Error(),
			)
		}
		out.Segments = append(out.Segments, seg)
	}
	return out, nil
}

func postToSegment(p api.FlowSegmentPost) (domain.Segment, error) {
	tr, err := timerange.Parse(p.Timerange)
	if err != nil {
		return domain.Segment{}, err
	}
	seg := domain.Segment{
		ObjectID:  p.ObjectId,
		Timerange: tr,
	}
	if p.TsOffset != nil {
		ts, err := timerange.ParseTimestamp(*p.TsOffset)
		if err != nil {
			return domain.Segment{}, err
		}
		seg.TSOffset = &ts
	}
	if p.ObjectTimerange != nil {
		otr, err := timerange.Parse(*p.ObjectTimerange)
		if err != nil {
			return domain.Segment{}, err
		}
		seg.ObjectTimerange = &otr
	}
	if p.LastDuration != nil {
		ld, err := timerange.ParseTimestamp(*p.LastDuration)
		if err != nil {
			return domain.Segment{}, err
		}
		seg.LastDuration = &ld
	}
	if p.KeyFrameCount != nil {
		v := int64(*p.KeyFrameCount)
		seg.KeyFrameCount = &v
	}
	if p.SampleOffset != nil {
		v := int64(*p.SampleOffset)
		seg.SampleOffset = &v
	}
	if p.SampleCount != nil {
		v := int64(*p.SampleCount)
		seg.SampleCount = &v
	}
	if p.GetUrls != nil {
		seg.GetURLs = make([]domain.GetURL, len(*p.GetUrls))
		for i, g := range *p.GetUrls {
			// Controlled is stamped false unconditionally — INV-CONV-08.
			// The wire payload may carry a `controlled` field (hostile
			// client); we ignore it.
			seg.GetURLs[i] = domain.GetURL{
				URL:        g.Url,
				Label:      g.Label,
				Controlled: false,
			}
		}
	}
	return seg, nil
}

// ListParamsFromAPI decodes a GET /flows/{id}/segments query. The
// `limit` query mirrors the spec's `minimum:1, default:100`:
//   - Absent (`p.Limit == nil`) → server default (100).
//   - Explicit value ≤ 0 → rejected as schema-validation 400. The
//     OpenAPI validator usually catches this first, but defence in
//     depth (SCN-HTTP-39) requires the conversion layer to refuse
//     hostile values when the validator is bypassed.
//   - In-range value (1..1000) passes through.
//   - Above ceiling (>1000) clamps to 1000.
//
// Returns wrapped `apperror.ErrSchemaValidation` on a malformed
// `timerange` query string or out-of-range `limit`.
func ListParamsFromAPI(p api.GetFlowSegmentsParams) (domain.ListParams, error) {
	out := domain.ListParams{}
	if p.Timerange != nil {
		tr, err := timerange.Parse(*p.Timerange)
		if err != nil {
			return domain.ListParams{}, apperror.New(apperror.ErrSchemaValidation, "timerange: "+err.Error())
		}
		out.Timerange = &tr
	}
	if p.ObjectId != nil {
		v := *p.ObjectId
		out.ObjectID = &v
	}
	if p.ReverseOrder != nil {
		out.ReverseOrder = *p.ReverseOrder
	}
	if p.VerboseStorage != nil {
		out.VerboseStorage = *p.VerboseStorage
	}
	if p.AcceptGetUrls != nil {
		out.AcceptGetURLs = csvSplit(*p.AcceptGetUrls)
	}
	if p.AcceptStorageIds != nil {
		out.AcceptStorageIDs = csvSplit(*p.AcceptStorageIds)
	}
	if p.Presigned != nil {
		v := *p.Presigned
		out.Presigned = &v
	}
	if p.IncludeObjectTimerange != nil {
		out.IncludeObjectTimerange = *p.IncludeObjectTimerange
	}
	if p.Page != nil {
		out.Page = *p.Page
	}

	if p.Limit == nil {
		out.Limit = defaultListLimit
	} else {
		limit := *p.Limit
		if limit <= 0 {
			return domain.ListParams{}, apperror.New(apperror.ErrSchemaValidation, "limit must be >= 1")
		}
		if limit > maxListLimit {
			out.Limit = maxListLimit
		} else {
			out.Limit = limit
		}
	}
	return out, nil
}

// DeleteParamsFromAPI decodes a DELETE /flows/{id}/segments query.
// Timerange is required by the OpenAPI spec; if nil arrives here it's
// treated as the eternity ("_") default. Returns wrapped
// `apperror.ErrSchemaValidation` on a malformed `timerange` string.
func DeleteParamsFromAPI(p api.DeleteFlowSegmentsParams) (domain.DeleteParams, error) {
	out := domain.DeleteParams{}
	trStr := "_"
	if p.Timerange != nil {
		trStr = *p.Timerange
	}
	tr, err := timerange.Parse(trStr)
	if err != nil {
		return domain.DeleteParams{}, apperror.New(apperror.ErrSchemaValidation, "timerange: "+err.Error())
	}
	out.Timerange = tr
	if p.ObjectId != nil {
		v := *p.ObjectId
		out.ObjectID = &v
	}
	return out, nil
}

func csvSplit(s string) []string {
	if s == "" {
		return []string{}
	}
	parts := []string{}
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == ',' {
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	parts = append(parts, s[start:])
	return parts
}

func fieldIdx(field string, i int) string {
	return field + "[" + itoa(i) + "]"
}

// itoa is a tiny stack-only int-to-string for small i; avoids strconv
// import to keep the package's import set minimal (visible to the
// dependency assertion test).
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	n := len(buf)
	for i > 0 {
		n--
		buf[n] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		n--
		buf[n] = '-'
	}
	return string(buf[n:])
}
