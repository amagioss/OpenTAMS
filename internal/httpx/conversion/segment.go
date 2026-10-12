package conversion

import (
	"fmt"

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
//
// Time fields are the client's exact strings (BR-CONV-08); an empty raw
// string means the client omitted the field. The parsed values are never
// rendered back.
func SegmentToAPI(s domain.Segment) api.FlowSegment {
	out := api.FlowSegment{
		ObjectId:        s.ObjectID,
		Timerange:       s.TimerangeRaw,
		TsOffset:        optionalRaw(s.TSOffsetRaw),
		ObjectTimerange: optionalRaw(s.ObjectTimerangeRaw),
		LastDuration:    optionalRaw(s.LastDurationRaw),
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
// (wrapping ErrSchemaValidation) on a time string that does not parse.
// FlowID is path-derived upstream; GetURLs are server-projected on read,
// so neither is reconstructed here.
func SegmentFromAPI(a api.FlowSegment) (domain.Segment, error) {
	out := domain.Segment{ObjectID: a.ObjectId}
	if err := setTimeFields(&out, a.Timerange, a.TsOffset, a.ObjectTimerange, a.LastDuration); err != nil {
		return domain.Segment{}, apperror.New(apperror.ErrSchemaValidation, err.Error())
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

// setTimeFields parses the four client time strings onto seg and keeps
// each exact string next to its parsed value (BR-CONV-08). A string that
// does not parse is an error naming the field; a parsed empty range is a
// valid value (BR-CONV-09).
func setTimeFields(seg *domain.Segment, tr string, tsOffset, objectTimerange, lastDuration *string) error {
	parsed, err := timerange.Parse(tr)
	if err != nil {
		return fmt.Errorf("timerange: %w", err)
	}
	seg.Timerange, seg.TimerangeRaw = parsed, tr
	if tsOffset != nil {
		ts, err := timerange.ParseTimestamp(*tsOffset)
		if err != nil {
			return fmt.Errorf("ts_offset: %w", err)
		}
		seg.TSOffset, seg.TSOffsetRaw = &ts, *tsOffset
	}
	if objectTimerange != nil {
		otr, err := timerange.Parse(*objectTimerange)
		if err != nil {
			return fmt.Errorf("object_timerange: %w", err)
		}
		seg.ObjectTimerange, seg.ObjectTimerangeRaw = &otr, *objectTimerange
	}
	if lastDuration != nil {
		ld, err := timerange.ParseTimestamp(*lastDuration)
		if err != nil {
			return fmt.Errorf("last_duration: %w", err)
		}
		seg.LastDuration, seg.LastDurationRaw = &ld, *lastDuration
	}
	return nil
}

// optionalRaw maps an absent raw string ("") to an omitted wire field.
func optionalRaw(raw string) *string {
	if raw == "" {
		return nil
	}
	v := raw
	return &v
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
		entries[i] = failedEntry{
			ObjectId:  f.Segment.ObjectID,
			Timerange: optionalRaw(f.Segment.TimerangeRaw),
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
