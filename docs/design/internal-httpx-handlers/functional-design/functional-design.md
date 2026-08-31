# M12 internal/httpx/handlers — Functional Design

## Scope

68 operations across 7 groups. Health/metrics (4 ops) → M13.

| Group | Operations | Services/Stores |
|---|---|---|
| Root + Service info | 6 (HeadRoot, GetRoot, HeadService, GetService, HeadStorageBackends, GetStorageBackends) | `*config.Config` |
| Sources | 18 (List, Get, tags/description/label CRUD + HEAD variants) | `metastore.SourceStore` |
| Flows CRUD | 4 (HeadFlows, GetFlows, HeadFlow, GetFlow, PutFlow, DeleteFlow) | `flow.FlowService` |
| Flow sub-resources | 26 (tags, description, label, read_only, flow_collection, max_bit_rate, avg_bit_rate — each HEAD+GET+PUT+DELETE) | `flow.FlowService` |
| Segments | 6 (HeadFlowSegments, GetFlowSegments, PostFlowSegments, DeleteFlowSegments) | `segment.Service`, `idempotency.Store`, `objectstore.Store` |
| Storage | 1 (PostFlowStorage) | `storage.StorageService` |
| Flow delete requests | 4 (HeadFlowDeleteRequests, GetFlowDeleteRequests, HeadFlowDeleteRequest, GetFlowDeleteRequest) | stub — Phase 1 always 204 sync |

## Handler Struct

```go
type Handler struct {
    log         *zap.Logger
    cfg         *config.Config
    sources     metastore.SourceStore
    flows       flow.FlowService
    segments    segment.Service
    storage     storage.StorageService
    idempotency idempotencyStore
    health      health.Checker
    objects     objectstore.Store
}
```

`idempotencyStore` is a handler-local interface rather than `idempotency.Store`, so
the segments tests can substitute a fake without a database. `objects` is present
for one reason only — synthesising `get_urls` on the read path (see below); no
handler writes to the object store.

No SourceService needed — SourceStore already has all operations. No business logic beyond CRUD for sources.

## Error Strategy

All non-2xx returned as `(nil, *apperror.AppError)`. The oapi-codegen strict wrapper calls `c.Error(err)` on non-nil error return → existing `ErrorHandler` middleware writes ProblemDetails. Generated typed error response types (e.g. `GetFlow404JSONResponse`) are NOT used.

**Exception**: `PostFlowSegments 200 partial success` returns `PostFlowSegments200JSONResponse` with the `flow-segment-bulk-failure` schema. This is the only non-trivial 2xx non-success response. Its per-entry `error` object carries an RFC 9457 subset (`type`, `title`, `detail`) — the parent-level `status`, `instance`, and `request_id` are not repeated per entry — see [ADR-0023](../../../adr/0023-rfc9457-problem-details-for-per-segment-failures.md).

### Param Parse Errors (siw.ErrorHandler)

oapi-codegen's `ServerInterfaceWrapper` calls `siw.ErrorHandler(c, err, statusCode)` for param parse failures (bad UUID, bad query param format, bad header). This is a **separate path** from `ctx.Error()` — our Gin ErrorHandler middleware does NOT catch these.

The generated default writes `{"msg": "..."}` (not ProblemDetails). We must wire `GinServerOptions.ErrorHandler` when calling `RegisterHandlersWithOptions`:

```go
RegisterHandlersWithOptions(router, handler, api.GinServerOptions{
    ErrorHandler: func(c *gin.Context, err error, statusCode int) {
        c.JSON(statusCode, apperror.ProblemDetails{
            Type:   "about:blank",
            Title:  http.StatusText(statusCode),
            Status: statusCode,
            Detail: err.Error(),
        })
    },
})
```

## HEAD Pattern

HEAD operations have their own generated interface methods. Handlers call the same service/store as the corresponding GET, set identical response headers, return the same typed response object. `net/http` strips the body for HEAD automatically.

## Implicit Source Creation (PutFlow)

Already handled inside `PostgresStore.UpsertFlow`: if `source_id` does not exist, a source is inserted inline within the same transaction. No handler-level or service-level action needed.

## Idempotency (PostFlowSegments only)

`PostFlowStorage` has no `X-Idempotency-Key` in the spec.

`PostFlowSegments` handler flow (`segments.go`):
1. Extract `X-Idempotency-Key` from the typed request; a missing key is a 400
   `missing-idempotency-key`, not an optional path.
2. `idempotency.Acquire(ctx, key, bodyHash, cfg.IdempotencyKeyTTL)` → replay the
   cached response on a hit, 409 on conflict.
3. Call `segments.RegisterBatch(ctx, params)` — the `domain.RegisterParams` form.
4. `idempotency.Complete(ctx, key, responseStatus, responseBody)`. A transient
   `RegisterBatch` failure releases the key instead, so the caller can retry.
   Both finalisation paths use a background context bounded by
   `segIdempotencyFinaliseTimeout` (5s) so a cancelled request still settles the key.
5. Return response.

Only the failed-segment body is cached with a 200; a 4xx is completed with a nil
body so it cannot replay as a 201.

## Segments

### Response mapping

The service returns either an error or a `domain.RegisterResult` carrying an
`Outcome`. The handler's whole job is turning that into a status:

| Service result | Status | Body |
|---|---|---|
| `OutcomeAllAccepted` | 201 | none |
| `OutcomePartial` | 200 | `flow-segment-bulk-failure` with `failed_segments` |
| `OutcomeAllRejected` | 200 | same shape, `accepted` empty |
| `ErrInvalidRequest` | 400 | `schema-validation` |
| `metastore.ErrSegmentOverlap` | 422 | `segment-overlap`, **no** `failed_segments` |
| `metastore.ErrSegmentObjectReaping` | 409 | `object-reaping` — retry with a fresh `object_id` |
| `ErrFlowReadOnly` | 403 | `read-only` |
| `ErrFlowNotFound` | 404 | `not-found` |
| anything else | error return → ErrorHandler | 500 ProblemDetails |

All-rejected is a 200 rather than a 4xx: the body already reports per-segment
failures, and the batch itself was well-formed. The overlap path is the opposite
case — the batch as a whole is refused, so it is a single ProblemDetails with no
per-segment array (see [ADR-0024](../../../adr/0024-whole-batch-reject-on-segment-overlap.md)).

An unparseable path UUID is answered by write and read paths differently, and the
split is intentional. `POST` and `DELETE` return 404 `flow not found`: a
syntactically invalid id cannot name a flow, and the write paths refuse to
distinguish "malformed" from "absent" for an unauthenticated-shaped probe. `GET`
and `HEAD` return 400 `invalid-uuid`, where naming the malformed parameter is
ordinary read-side input validation.

The handler also re-checks the 1000-segment batch cap that the OpenAPI validator
already enforces, so a bypassed or misconfigured validator cannot put an over-cap
batch in front of the service.

### BR-HTTP-05 — GET `get_urls` projection

The service stores only what a client supplied; the reader-facing `get_urls` array
is built here, per segment, from the segment's own shape:

- **Controlled** (`len(seg.GetURLs) == 0`) — the handler asks the object store for a
  download URL set and emits a storage-URI entry and an HTTPS entry.
- **BYOS** (`len(seg.GetURLs) > 0`) — the stored entries are emitted as they were
  supplied.

Each controlled entry is labelled `<provider>.<region>:<store_product>:opentams`
from the deployment's single `objectstore.BackendInfo`, so a client can tell which
backend served a URL without a second lookup. Phase 1 ships exactly one backend;
the label format is already multi-backend-shaped.

`?presigned` filters the result on both paths: `true` keeps only the signed HTTPS
entry, `false` keeps only the unsigned one and short-circuits signing so no
needless request is made to the object store.

This inversion — storage-agnostic service, URL-owning handler — is deliberate. It
keeps presigning, backend identity, and query-parameter policy in one layer and out
of the domain types.

### Divergence from TAMS

`GET /flows/{flowId}/segments` on an unknown flow returns 404 rather than the empty
list `REQ-BEH-16` describes. Recorded in
[`docs/conformance.md`](../../../conformance.md).

## BR-HTTP-18 — The conversion boundary

Handlers do not touch `gen/api` union helpers. Every wire ↔ domain hop on the
segments path goes through `internal/httpx/conversion`
(`RegisterParamsFromAPI`, `ListParamsFromAPI`, `DeleteParamsFromAPI`,
`SegmentPageToAPI`, `RegisterAcceptedToAPI`, `RegisterFailureToAPI`), and a
depguard rule keeps that package free of the service, metastore, and object store.

The generated union API is terse and easy to misuse; localising it means one file
to audit when the OpenAPI union shape changes rather than every handler that
touches segments. See
[`design/internal-httpx-conversion/`](../../internal-httpx-conversion/functional-design/functional-design.md).

## Flow Delete Requests (Phase 1 stubs)

- `GetFlowDeleteRequests` → 200 empty array
- `GetFlowDeleteRequest` → 404
- HEAD variants → 200 with empty headers

Updated in M16 when GC is built.

## Union Type Unwrapping

### PutFlow (`Flow` type)

`Flow` is a `oneOf` union. The generated type has `Discriminator()` returning the format URN string. Handlers must unwrap:

```go
switch body.Discriminator() {
case "urn:x-nmos:format:video":
    v, _ := body.AsFlowVideo()
    // ...
case "urn:x-nmos:format:audio":
    a, _ := body.AsFlowAudio()
    // ...
// ... image, data, multi
default:
    return nil, apperror.New(apperror.ErrValidation, "unknown flow format")
}
```

### PostFlowSegments (`FlowSegmentPostBody` type)

`FlowSegmentPostBody` is a `oneOf` of single segment or array (max 1000). Accessors:

- `AsFlowSegmentPost()` → single `FlowSegmentPost`
- `AsFlowSegmentPostBody1()` → `[]FlowSegmentPost` (array variant)

Both return `(value, error)`. `conversion.RegisterParamsFromAPI` tries the **array**
branch first and falls back to the single one, because the bulk shape is the
documented common case; a body matching neither is a 400 `schema-validation`. The
handler never calls `As*` itself — see the conversion boundary below.

## Validation Approach

**BR-HTTP-12 — what the middleware owns, the handler cannot see.** The kin-openapi
validator middleware enforces the request half of the contract before a handler
runs: schema shape, enum values, parameter bounds, and content negotiation. A
strict-server handler receives typed parameters and never sees the `Accept` header,
so `406` is a middleware outcome and is asserted at the server-integration layer,
not in a handler test. Where a handler re-checks something the validator already
covers — the 1000-segment batch cap is the one case — it is defence in depth
against a bypassed or misconfigured validator, and is documented as such.

- Path param UUID parsing: handled by oapi-codegen generated wrapper (typed as `openapi_types.UUID`)
- Request body JSON parsing: handled by oapi-codegen
- Field-level validation (required fields, enum values): hand-rolled inline per handler
- Business rule validation (read_only, overlap, format mismatch): in service/store layer

### PostFlowStorage Mutual Exclusivity

`FlowStoragePost` schema uses `not: allOf` to express "not both `limit` AND `object_ids`". oapi-codegen generates a plain struct — the `not` constraint is not enforced at decode time. Handler must validate explicitly:

```go
if req.Body.Limit != nil && req.Body.ObjectIds != nil {
    return nil, apperror.New(apperror.ErrValidation, "limit and object_ids are mutually exclusive")
}
```

## Design Decisions

| Decision | Choice | Rationale |
|---|---|---|
| oapi-codegen mode | StrictServerInterface=true | Type-safe, framework-agnostic handlers, param extraction generated |
| Error responses | All via error return → ErrorHandler | Centralizes ProblemDetails; no per-handler error writing |
| Param parse errors | GinServerOptions.ErrorHandler writes ProblemDetails | Default writes `{"msg":"..."}` not ProblemDetails; must wire custom handler |
| SourceService | None — use SourceStore directly | Pure CRUD, no business logic, no cross-cutting concerns |
| GAP-M4a (InsertSourceIfNotExists) | Already in UpsertFlow store layer | Discovered during Code Generation prep |
| pkg/httplog change | LoggerFromContext(ctx) added | Enables context-based logger access from strict handlers |
| `ListQuery` (was `ListSegmentsParams`) | Carries Timerange, ObjectID, ReverseOrder, and the storage/URL accept filters | Required by GET /flows/{flowId}/segments spec params |
| IsObjectRegistered | Added to PostgresStore | Required by StorageService.AllocateStorage |
| Store ErrNoRows audit | Already correct | All pgx.ErrNoRows paths return apperror.ErrNotFound |
| FlowSegmentPostBody unwrap | Try AsFlowSegmentPostBody1() (array) first, then AsFlowSegmentPost() | oneOf union; single segment and array are the two variants |
| FlowStoragePost mutual exclusivity | Explicit handler validation: limit AND object_ids both set → 400 | `not: allOf` constraint not enforced at JSON decode time |
| PutFlow discriminator | Switch on Discriminator() → AsFlow*() | oneOf union; format URN determines variant |
