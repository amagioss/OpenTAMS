package domain

import (
	"github.com/google/uuid"

	"github.com/amagioss/opentams/internal/timerange"
)

// RegisterParams is the input to Service.RegisterBatch. Carries the parsed
// body plus the flow identity. Service does not enforce upper bound on
// Segments — handler does (REQ-SEC-10 / openapi maxItems).
type RegisterParams struct {
	FlowID   uuid.UUID
	Segments []Segment
}

// RegisterOutcome encodes the three-state outcome of a non-overlap
// RegisterBatch. The overlap path bypasses this enum entirely (returns
// metastore.ErrSegmentOverlap with an empty RegisterResult).
type RegisterOutcome int

// RegisterOutcome values. iota+1 so the zero value is invalid (defends
// against forgetting to set Outcome).
const (
	OutcomeAllAccepted RegisterOutcome = iota + 1 // 201
	OutcomePartial                                // 200 + failed list
	OutcomeAllRejected                            // 400 (validation only)
)

// RegisterResult is the non-overlap outcome of RegisterBatch. Accepted +
// Failed are in input order. Sum of lengths equals len(input segments)
// (INV-SEG-01).
type RegisterResult struct {
	Outcome  RegisterOutcome
	Accepted []Segment
	Failed   []FailedSegment
}

// FailedSegment carries the original payload plus the embedded RFC 9457
// subset (Type, Title, Reason → error.detail). The handler renders these
// verbatim — no re-classification (INV-HTTP-08).
type FailedSegment struct {
	Segment Segment
	Reason  string
	Type    string
	Title   string
	Status  int
}

// ListParams is the input to Service.List. Limit semantics: 1..1000;
// 0 means "use server default (100)" — clamping happens in conversion
// (CONV-SEG-08), the service never sees an out-of-range value.
type ListParams struct {
	FlowID                 uuid.UUID
	Timerange              *timerange.TimeRange
	ObjectID               *string
	ReverseOrder           bool
	VerboseStorage         bool
	AcceptGetURLs          []string
	AcceptStorageIDs       []string
	Presigned              *bool
	IncludeObjectTimerange bool
	Page                   string
	Limit                  int
}

// SegmentPage is the canonical paged-list result. Same struct used by
// metastore (ListSegments), service (List), and conversion
// (SegmentPageToAPI).
type SegmentPage struct {
	Items          []Segment
	NextCursor     string
	EffectiveLimit int
	Timerange      timerange.TimeRange
	Count          int
}

// DeleteParams is the input to Service.Delete. Timerange is required
// (REQ-BEH-32 / openapi marks the query param required).
type DeleteParams struct {
	FlowID    uuid.UUID
	Timerange timerange.TimeRange
	ObjectID  *string
}

// DeleteResult carries the count for log / metric / idempotency-cache /
// test-assertion consumers. ReleasedObjects was removed (D-29) — GC
// owns reaping.
type DeleteResult struct {
	DeletedCount int64
}
