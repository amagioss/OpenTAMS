---
unit: M2 internal/apperror
stage: Functional Design
status: Complete (retroactive backfill)
---

# Business Rules — internal/apperror

## BR-ERR-01: 19 catalogued error codes
The 19 stable type URIs from §6.3.1 of the requirements spec are the only catalogued codes. Each maps to exactly one HTTP status and one human-readable title. Base URI: `https://github.com/amagioss/opentams/problems/`.

## BR-ERR-02: segment-overlap is 422, not 400
Overlapping segment submissions return 422 Unprocessable Entity. Rationale: the request is syntactically valid and schema-valid — the rejection is a business invariant (no overlapping timeranges per flow), not a malformed request. 400 conflates parse errors with domain violations.

> *Decision: segment-overlap → 422. Updated in requirements spec REQ-BEH-15, §6.3.1 catalogue, acceptance criterion #14, and opentams-api-v1.yaml. Rejected: 400 (too broad, conflates format errors with business rule violations).*

## BR-ERR-03: Type URI omitted for uncatalogued errors
When no catalogued code applies (e.g. 409 idempotency conflict), the `type` field is absent from the JSON response body — not set to `about:blank`. Per RFC 9457 §3.1, absent `type` is equivalent to `about:blank`. The `omitempty` JSON tag enforces this.

## BR-ERR-04: Titles stored explicitly, not derived from slug
Titles are stored in the catalogue map alongside statuses. Derivation from slug (e.g. title-casing hyphen-separated words) produces wrong results for `UUID`, `JSON`, `VFR`, `ID` — these require non-mechanical casing.

## BR-ERR-05: Unknown code returns 500, no panic
`New` with an unrecognised `ErrorCode` returns a Generic 500 AppError rather than panicking. Rationale: consistent with the project-wide no-panics policy. The 500 response makes the programming error visible at runtime without crashing the process.

## BR-ERR-06: ProblemDetails is a pure data struct
The `apperror` package does not write HTTP responses. It produces a `ProblemDetails` value; the HTTP handler layer serialises it and sets `Content-Type: application/problem+json`. Separation of concerns: error domain logic is independent of the HTTP layer.

## BR-ERR-07: instance must be a URI reference
`ToProblemDetails` accepts `instance` as a string per RFC 9457 §3.5. Validation that it is a URI reference is the caller's responsibility — documented in the function comment, not enforced at runtime in this package.
