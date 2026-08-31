package apperror_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/amagioss/opentams/internal/apperror"
)

// TC-ERR-01: every catalogued code maps to the correct HTTP status
func TestNew_StatusMapping(t *testing.T) {
	cases := []struct {
		code   apperror.ErrorCode
		status int
	}{
		{apperror.ErrSegmentOverlap, 422},
		{apperror.ErrInvalidTimerange, 400},
		{apperror.ErrInvalidUUID, 400},
		{apperror.ErrInvalidJSON, 400},
		{apperror.ErrSchemaValidation, 400},
		{apperror.ErrImmutableField, 400},
		{apperror.ErrFormatMismatch, 400},
		{apperror.ErrMissingIdempotencyKey, 400},
		{apperror.ErrStorageModeConflict, 400},
		{apperror.ErrObjectIDExists, 400},
		{apperror.ErrVFRFrameRateConflict, 400},
		{apperror.ErrBatchTooLarge, 400},
		{apperror.ErrUnauthorized, 401},
		{apperror.ErrForbidden, 403},
		{apperror.ErrTokenExpired, 401},
		{apperror.ErrReadOnly, 403},
		{apperror.ErrNotFound, 404},
		{apperror.ErrNotAcceptable, 406},
		{apperror.ErrRateLimited, 429},
		{apperror.ErrDependencyUnavailable, 503},
	}
	for _, tc := range cases {
		e := apperror.New(tc.code, "detail")
		if e.Status != tc.status {
			t.Errorf("New(%q).Status = %d, want %d", tc.code, e.Status, tc.status)
		}
	}
}

// TC-ERR-02: ToProblemDetails for a catalogued error — type URI, status, instance, request_id, detail all set
func TestToProblemDetails_Catalogued(t *testing.T) {
	e := apperror.New(apperror.ErrNotFound, "flow abc not found")
	pd := e.ToProblemDetails("/flows/abc", "req-123")

	const wantType = "https://github.com/amagioss/opentams/problems/not-found"
	if pd.Type != wantType {
		t.Errorf("Type = %q, want %q", pd.Type, wantType)
	}
	if pd.Status != 404 {
		t.Errorf("Status = %d, want 404", pd.Status)
	}
	if pd.Instance != "/flows/abc" {
		t.Errorf("Instance = %q, want /flows/abc", pd.Instance)
	}
	if pd.RequestID != "req-123" {
		t.Errorf("RequestID = %q, want req-123", pd.RequestID)
	}
	if pd.Detail != "flow abc not found" {
		t.Errorf("Detail = %q, want %q", pd.Detail, "flow abc not found")
	}
}

// TC-ERR-03: ToProblemDetails for Generic (uncatalogued) — type field is empty
func TestToProblemDetails_GenericNoType(t *testing.T) {
	e := apperror.Generic(409, "concurrent modification")
	pd := e.ToProblemDetails("/flows/abc", "req-456")
	if pd.Type != "" {
		t.Errorf("Type = %q, want empty for uncatalogued error", pd.Type)
	}
	if pd.Status != 409 {
		t.Errorf("Status = %d, want 409", pd.Status)
	}
}

// TC-ERR-04: JSON marshal of Generic ProblemDetails omits the "type" key entirely (omitempty)
func TestProblemDetails_JSONOmitsTypeWhenEmpty(t *testing.T) {
	e := apperror.Generic(409, "conflict")
	pd := e.ToProblemDetails("/flows/abc", "rid")
	b, err := json.Marshal(pd)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if strings.Contains(string(b), `"type"`) {
		t.Errorf("JSON output contains \"type\" key, want it omitted: %s", b)
	}
}

// TC-ERR-05: all 19 catalogued codes produce the correct title
func TestToProblemDetails_Titles(t *testing.T) {
	cases := []struct {
		code      apperror.ErrorCode
		wantTitle string
	}{
		{apperror.ErrSegmentOverlap, "Segment Overlap"},
		{apperror.ErrInvalidTimerange, "Invalid Timerange"},
		{apperror.ErrInvalidUUID, "Invalid UUID"},
		{apperror.ErrInvalidJSON, "Invalid JSON"},
		{apperror.ErrSchemaValidation, "Schema Validation Failed"},
		{apperror.ErrImmutableField, "Immutable Field"},
		{apperror.ErrFormatMismatch, "Format Mismatch"},
		{apperror.ErrMissingIdempotencyKey, "Missing Idempotency Key"},
		{apperror.ErrStorageModeConflict, "Storage Mode Conflict"},
		{apperror.ErrObjectIDExists, "Object ID Exists"},
		{apperror.ErrVFRFrameRateConflict, "VFR Frame Rate Conflict"},
		{apperror.ErrBatchTooLarge, "Batch Too Large"},
		{apperror.ErrUnauthorized, "Unauthorized"},
		{apperror.ErrForbidden, "Forbidden"},
		{apperror.ErrTokenExpired, "Token Expired"},
		{apperror.ErrReadOnly, "Read Only"},
		{apperror.ErrNotFound, "Not Found"},
		{apperror.ErrNotAcceptable, "Not Acceptable"},
		{apperror.ErrRateLimited, "Rate Limited"},
		{apperror.ErrDependencyUnavailable, "Dependency Unavailable"},
	}
	for _, tc := range cases {
		pd := apperror.New(tc.code, "detail").ToProblemDetails("/path", "rid")
		if pd.Title != tc.wantTitle {
			t.Errorf("New(%q) title = %q, want %q", tc.code, pd.Title, tc.wantTitle)
		}
	}
}

// TC-ERR-06: Error() is non-empty and contains both the code slug and the detail
func TestError_String(t *testing.T) {
	e := apperror.New(apperror.ErrNotFound, "missing resource")
	s := e.Error()
	if s == "" {
		t.Fatal("Error() returned empty string")
	}
	if !strings.Contains(s, string(apperror.ErrNotFound)) {
		t.Errorf("Error() = %q, want it to contain %q", s, apperror.ErrNotFound)
	}
	if !strings.Contains(s, "missing resource") {
		t.Errorf("Error() = %q, want it to contain detail", s)
	}
}

// TC-ERR-07: Generic AppError Error() contains the status code and detail
func TestGeneric_ErrorString(t *testing.T) {
	e := apperror.Generic(503, "db down")
	s := e.Error()
	if s == "" {
		t.Fatal("Generic.Error() returned empty string")
	}
	if !strings.Contains(s, "503") {
		t.Errorf("Generic.Error() = %q, want it to contain status 503", s)
	}
	if !strings.Contains(s, "db down") {
		t.Errorf("Generic.Error() = %q, want it to contain detail", s)
	}
}

// TC-ERR-09: object-reaping (409) and request-too-large (413) catalogued
func TestNew_NewlyCataloguedCodes(t *testing.T) {
	cases := []struct {
		code      apperror.ErrorCode
		status    int
		wantTitle string
	}{
		{apperror.ErrObjectReaping, 409, "Object Reaping"},
		{apperror.ErrRequestTooLarge, 413, "Request Too Large"},
	}
	for _, tc := range cases {
		e := apperror.New(tc.code, "detail")
		if e.Status != tc.status {
			t.Errorf("New(%q).Status = %d, want %d", tc.code, e.Status, tc.status)
		}
		pd := e.ToProblemDetails("/path", "rid")
		if pd.Title != tc.wantTitle {
			t.Errorf("New(%q) title = %q, want %q", tc.code, pd.Title, tc.wantTitle)
		}
		wantType := "https://github.com/amagioss/opentams/problems/" + string(tc.code)
		if pd.Type != wantType {
			t.Errorf("New(%q) type = %q, want %q", tc.code, pd.Type, wantType)
		}
	}
}

// TC-ERR-08: New with an unknown code returns 500 with "Internal Server Error" title
func TestNew_UnknownCodeFallsBackTo500(t *testing.T) {
	e := apperror.New("no-such-code", "detail")
	if e.Status != 500 {
		t.Errorf("unknown code status = %d, want 500", e.Status)
	}
	pd := e.ToProblemDetails("/path", "rid")
	if pd.Type != "" {
		t.Errorf("unknown code type = %q, want empty", pd.Type)
	}
	if pd.Title != "Internal Server Error" {
		t.Errorf("unknown code title = %q, want %q", pd.Title, "Internal Server Error")
	}
}
