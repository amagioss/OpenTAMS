// Package apperror defines the domain error type and RFC 9457 Problem Details
// serialization for OpenTAMS API responses.
package apperror

import "fmt"

const typeURIBase = "https://github.com/amagioss/opentams/problems/"

// ErrorCode is a catalogued error slug from §6.3.1 of the requirements spec.
type ErrorCode string

// Catalogued error codes — slugs from §6.3.1 of the requirements
// spec. Each code maps to a fixed HTTP status and Problem Details
// type URI (see catalogue() for the mapping). Add new codes here
// only when extending the requirements spec.
const (
	ErrSegmentOverlap         ErrorCode = "segment-overlap"
	ErrInvalidObjectTimerange ErrorCode = "invalid-object-timerange"
	ErrInvalidTimerange       ErrorCode = "invalid-timerange"
	ErrInvalidUUID            ErrorCode = "invalid-uuid"
	ErrInvalidJSON            ErrorCode = "invalid-json"
	ErrSchemaValidation       ErrorCode = "schema-validation"
	ErrImmutableField         ErrorCode = "immutable-field"
	ErrFormatMismatch         ErrorCode = "format-mismatch"
	ErrMissingIdempotencyKey  ErrorCode = "missing-idempotency-key"
	ErrStorageModeConflict    ErrorCode = "storage-mode-conflict"
	ErrObjectIDExists         ErrorCode = "object-id-exists"
	ErrVFRFrameRateConflict   ErrorCode = "vfr-frame-rate-conflict"
	ErrBatchTooLarge          ErrorCode = "batch-too-large"
	ErrUnauthorized           ErrorCode = "unauthorized"
	ErrForbidden              ErrorCode = "forbidden"
	ErrTokenExpired           ErrorCode = "token-expired"
	ErrReadOnly               ErrorCode = "read-only"
	ErrObjectReaping          ErrorCode = "object-reaping"
	ErrNotFound               ErrorCode = "not-found"
	ErrNotAcceptable          ErrorCode = "not-acceptable"
	ErrRequestTooLarge        ErrorCode = "request-too-large"
	ErrRateLimited            ErrorCode = "rate-limited"
	ErrDependencyUnavailable  ErrorCode = "dependency-unavailable"
)

type catalogueEntry struct {
	status int
	title  string
}

// catalogue is read-only after package init — never mutate.
var catalogue = map[ErrorCode]catalogueEntry{
	ErrSegmentOverlap:         {422, "Segment Overlap"},
	ErrInvalidObjectTimerange: {400, "Invalid Object Timerange"},
	ErrInvalidTimerange:       {400, "Invalid Timerange"},
	ErrInvalidUUID:            {400, "Invalid UUID"},
	ErrInvalidJSON:            {400, "Invalid JSON"},
	ErrSchemaValidation:       {400, "Schema Validation Failed"},
	ErrImmutableField:         {400, "Immutable Field"},
	ErrFormatMismatch:         {400, "Format Mismatch"},
	ErrMissingIdempotencyKey:  {400, "Missing Idempotency Key"},
	ErrStorageModeConflict:    {400, "Storage Mode Conflict"},
	ErrObjectIDExists:         {400, "Object ID Exists"},
	ErrVFRFrameRateConflict:   {400, "VFR Frame Rate Conflict"},
	ErrBatchTooLarge:          {400, "Batch Too Large"},
	ErrUnauthorized:           {401, "Unauthorized"},
	ErrForbidden:              {403, "Forbidden"},
	ErrTokenExpired:           {401, "Token Expired"},
	ErrReadOnly:               {403, "Read Only"},
	ErrObjectReaping:          {409, "Object Reaping"},
	ErrNotFound:               {404, "Not Found"},
	ErrNotAcceptable:          {406, "Not Acceptable"},
	ErrRequestTooLarge:        {413, "Request Too Large"},
	ErrRateLimited:            {429, "Rate Limited"},
	ErrDependencyUnavailable:  {503, "Dependency Unavailable"},
}

// AppError is a domain error carrying an optional catalogued code and HTTP status.
type AppError struct {
	Code   ErrorCode
	Status int
	Detail string
}

func (e *AppError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("apperror: %s: %s", e.Code, e.Detail)
	}
	return fmt.Sprintf("apperror: %d: %s", e.Status, e.Detail)
}

// New creates a catalogued AppError. If code is not in the catalogue (programming
// error), returns a Generic 500 so the caller gets a visible failure without a panic.
func New(code ErrorCode, detail string) *AppError {
	entry, ok := catalogue[code]
	if !ok {
		return &AppError{Status: 500, Detail: fmt.Sprintf("unknown error code %q: %s", code, detail)}
	}
	return &AppError{Code: code, Status: entry.status, Detail: detail}
}

// Generic creates an uncatalogued AppError. The type URI is omitted in ProblemDetails
// per RFC 9457 §3.1.
func Generic(status int, detail string) *AppError {
	return &AppError{Status: status, Detail: detail}
}

// ProblemDetails is the RFC 9457 JSON body for non-2xx responses.
type ProblemDetails struct {
	Type      string `json:"type,omitempty"`
	Title     string `json:"title"`
	Status    int    `json:"status"`
	Detail    string `json:"detail,omitempty"`
	Instance  string `json:"instance"`
	RequestID string `json:"request_id"`
}

// ToProblemDetails converts the AppError into a ProblemDetails value ready for
// JSON serialization. Instance must be a URI reference per RFC 9457 §3.5;
// requestID is injected by the HTTP handler.
func (e *AppError) ToProblemDetails(instance, requestID string) ProblemDetails {
	pd := ProblemDetails{
		Status:    e.Status,
		Detail:    e.Detail,
		Instance:  instance,
		RequestID: requestID,
	}
	if entry, ok := catalogue[e.Code]; ok {
		pd.Type = typeURIBase + string(e.Code)
		pd.Title = entry.title
	} else {
		pd.Title = "Internal Server Error"
	}
	return pd
}
