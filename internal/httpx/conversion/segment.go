package conversion

import (
	api "github.com/amagioss/opentams/gen/api"
	"github.com/amagioss/opentams/internal/apperror"
	"github.com/amagioss/opentams/internal/domain"
	"github.com/amagioss/opentams/internal/timerange"
)

// getUrlEntry mirrors the anonymous struct that oapi-codegen emits as
// the element type of api.FlowSegment.GetUrls. Field names match the
// generated code verbatim.
//
//nolint:revive // field names mirror oapi-codegen output verbatim
type getUrlEntry = struct {
	AvailabilityZone *string                          `json:"availability_zone,omitempty"`
	Controlled       *bool                            `json:"controlled,omitempty"`
	Label            *string                          `json:"label,omitempty"`
	Presigned        *bool                            `json:"presigned,omitempty"`
	Provider         *string                          `json:"provider,omitempty"`
	Region           *string                          `json:"region,omitempty"`
	StorageId        *api.Uuid                        `json:"storage_id,omitempty"`
	StoreProduct     *string                          `json:"store_product,omitempty"`
	StoreType        *api.FlowSegmentGetUrlsStoreType `json:"store_type,omitempty"`
	Url              string                           `json:"url"`
}

// SegmentToAPI is a pure field copy from domain to wire. FlowID and the
// `Controlled` projection are intentionally lossy on this direction —
// the wire shape does not carry them; the handler stamps Controlled at
// GET time when projecting controlled get_urls.
func SegmentToAPI(s domain.Segment) api.FlowSegment {
	out := api.FlowSegment{
		ObjectId:  s.ObjectID,
		Timerange: s.Timerange.String(),
	}
	if s.TSOffset != nil {
		v := s.TSOffset.String()
		out.TsOffset = &v
	}
	if s.ObjectTimerange != nil {
		v := s.ObjectTimerange.String()
		out.ObjectTimerange = &v
	}
	if s.LastDuration != nil {
		v := s.LastDuration.String()
		out.LastDuration = &v
	}
	if s.KeyFrameCount != nil {
		v := int(*s.KeyFrameCount)
		out.KeyFrameCount = &v
	}
	if s.SampleOffset != nil {
		v := int(*s.SampleOffset)
		out.SampleOffset = &v
	}
	if s.SampleCount != nil {
		v := int(*s.SampleCount)
		out.SampleCount = &v
	}
	if len(s.GetURLs) > 0 {
		urls := make([]getUrlEntry, len(s.GetURLs))
		for i := range s.GetURLs {
			g := &s.GetURLs[i]
			label := g.Label
			ctrl := g.Controlled
			urls[i] = getUrlEntry{
				Url:        g.URL,
				Label:      &label,
				Controlled: &ctrl,
			}
		}
		out.GetUrls = &urls
	}
	return out
}

// SegmentFromAPI is the read-back path. Returns *apperror.AppError
// (wrapping ErrSchemaValidation) on malformed `timerange` strings.
// FlowID is path-derived upstream; GetURLs are server-projected on read,
// so neither is reconstructed here.
func SegmentFromAPI(a api.FlowSegment) (domain.Segment, error) {
	tr, err := timerange.Parse(a.Timerange)
	if err != nil {
		return domain.Segment{}, apperror.New(apperror.ErrSchemaValidation, "timerange: "+err.Error())
	}
	out := domain.Segment{
		ObjectID:  a.ObjectId,
		Timerange: tr,
	}
	if a.TsOffset != nil {
		ts, err := timerange.ParseTimestamp(*a.TsOffset)
		if err != nil {
			return domain.Segment{}, apperror.New(apperror.ErrSchemaValidation, "ts_offset: "+err.Error())
		}
		out.TSOffset = &ts
	}
	if a.ObjectTimerange != nil {
		otr, err := timerange.Parse(*a.ObjectTimerange)
		if err != nil {
			return domain.Segment{}, apperror.New(apperror.ErrSchemaValidation, "object_timerange: "+err.Error())
		}
		out.ObjectTimerange = &otr
	}
	if a.LastDuration != nil {
		ld, err := timerange.ParseTimestamp(*a.LastDuration)
		if err != nil {
			return domain.Segment{}, apperror.New(apperror.ErrSchemaValidation, "last_duration: "+err.Error())
		}
		out.LastDuration = &ld
	}
	if a.KeyFrameCount != nil {
		v := int64(*a.KeyFrameCount)
		out.KeyFrameCount = &v
	}
	if a.SampleOffset != nil {
		v := int64(*a.SampleOffset)
		out.SampleOffset = &v
	}
	if a.SampleCount != nil {
		v := int64(*a.SampleCount)
		out.SampleCount = &v
	}
	return out, nil
}

// RegisterAcceptedToAPI maps the accepted slice of a RegisterResult to
// a wire-shape segment list. Pure field-copy; preserves order; does NOT
// pick a status code (handler's concern).
func RegisterAcceptedToAPI(segs []domain.Segment) []api.FlowSegment {
	out := make([]api.FlowSegment, len(segs))
	for i := range segs {
		out[i] = SegmentToAPI(segs[i])
	}
	return out
}

// failedEntry mirrors the anonymous struct that oapi-codegen emits as
// the element type of api.FlowSegmentBulkFailure.FailedSegments. Field
// names match the generated code verbatim — Go's structural typing
// requires it — so the revive var-naming rule is suppressed at the
// type level.
//
//nolint:revive // field names mirror oapi-codegen output verbatim
type failedEntry = struct {
	Error struct {
		Detail string `json:"detail"`
		Title  string `json:"title"`
		Type   string `json:"type"`
	} `json:"error"`
	ObjectId  string         `json:"object_id"`
	Timerange *api.Timerange `json:"timerange,omitempty"`
}

// RegisterFailureToAPI maps the failed slice of a RegisterResult to a
// FlowSegmentBulkFailure body. Empty input yields a zero-length (non-nil)
// FailedSegments slice so JSON marshal renders `"failed_segments":[]`,
// not `"failed_segments":null` — which RFC 9457 / TAMS clients expect.
func RegisterFailureToAPI(failed []domain.FailedSegment) api.FlowSegmentBulkFailure {
	entries := make([]failedEntry, len(failed))
	for i := range failed {
		f := &failed[i]
		tr := f.Segment.Timerange.String()
		entries[i] = failedEntry{
			ObjectId:  f.Segment.ObjectID,
			Timerange: &tr,
		}
		entries[i].Error.Type = f.Type
		entries[i].Error.Title = f.Title
		entries[i].Error.Detail = f.Reason
	}
	return api.FlowSegmentBulkFailure{FailedSegments: entries}
}

// SegmentPageToAPI maps a paged list to a flat `[]api.FlowSegment` body.
// HTTP headers (Link, X-Paging-*) are the handler's concern; this
// function returns body bytes only.
func SegmentPageToAPI(p domain.SegmentPage) []api.FlowSegment {
	return RegisterAcceptedToAPI(p.Items)
}
