---
unit: M2 internal/apperror
stage: Functional Design
status: Complete (retroactive backfill)
---

# Business Logic Model — internal/apperror

## Purpose

Domain error type and RFC 9457 Problem Details serialization for all non-2xx OpenTAMS API responses. Single source of truth for the 19 catalogued error codes, their HTTP statuses, type URIs, and human-readable titles.

## Core Concepts

### AppError
A domain error carrying an optional catalogued `ErrorCode` and an HTTP status. Implements the `error` interface. Created by `New` (catalogued) or `Generic` (uncatalogued).

### ProblemDetails
RFC 9457 JSON body written by the HTTP handler layer. Contains `type` (URI, omitted when uncatalogued), `title`, `status`, `detail`, `instance`, and `request_id`. Pure data struct — the handler layer owns serialization and response writing.

## Data Model

```
ErrorCode string  // one of 19 catalogued slugs, or empty for Generic

AppError {
    Code   ErrorCode
    Status int
    Detail string
}

ProblemDetails {
    Type      string  // omitempty — absent for uncatalogued errors
    Title     string
    Status    int
    Detail    string
    Instance  string
    RequestID string
}
```

## Operations

| Operation | Description |
|---|---|
| `New(code, detail)` | Catalogued AppError — looks up status+title from catalogue |
| `Generic(status, detail)` | Uncatalogued AppError — no type URI in ProblemDetails |
| `AppError.Error()` | Satisfies error interface |
| `AppError.ToProblemDetails(instance, requestID)` | Pure data transform → ProblemDetails |
