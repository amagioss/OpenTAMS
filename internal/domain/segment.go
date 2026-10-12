// Package domain defines the wire-agnostic value types for the segments slice.
//
// This package is a pure leaf: no driver, no logger, no metrics, no I/O.
// It is imported by metastore, service, conversion, and tests; it MUST NOT
// import any of those packages back (D-32).
package domain

import (
	"time"

	"github.com/google/uuid"

	"github.com/amagioss/opentams/internal/timerange"
)

// Segment is the canonical segment value object exchanged across the
// service boundary. Field semantics are spec-aligned (TAMS v8); see
// docs/design/internal-service-segment/functional-design/functional-design.md.
type Segment struct {
	FlowID          uuid.UUID
	ObjectID        string
	Timerange       timerange.TimeRange
	TSOffset        *timerange.Timestamp
	ObjectTimerange *timerange.TimeRange
	LastDuration    *timerange.Timestamp
	KeyFrameCount   *int64
	GetURLs         []GetURL

	// The exact strings the client sent, returned byte for byte (BR-CONV-08,
	// BR-META-10). The parsed fields above are for validation and
	// timerange.NsBounds only. An empty string means the field was absent.
	TimerangeRaw       string
	TSOffsetRaw        string
	ObjectTimerangeRaw string
	LastDurationRaw    string

	// Deprecated TAMS v8 fields — stored, returned, never interpreted.
	SampleOffset *int64
	SampleCount  *int64

	CreatedAt time.Time
}

// GetURL is the uncontrolled-URL descriptor. The Controlled flag is stamped
// false by conversion on POST and true by the handler for server-managed
// entries on GET; the service does NOT touch it.
type GetURL struct {
	URL        string
	Label      string
	Controlled bool
}
