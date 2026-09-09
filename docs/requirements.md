# OpenTAMS — Requirements Specification v1.2

> **Cloud-Agnostic Implementation of BBC TAMS v8.0 API**

| Field | Value |
|-------|-------|
| TAMS Spec Version | v8.0 (pinned to commit hash at build time) |
| License | Apache 2.0 |
| Status | Draft |
| Previous Version | v1.1 |
| Changes From v1.1 | Internal auth pair (`AUTH_INTERNAL_*`) is now optional so plain Docker / VM deployments can run without a Kubernetes ServiceAccount issuer; new `DB_SSLMODE` knob with secure-by-default `require` in production and `prefer` in development; schema startup probe formalised in REQ-DEV-05a |
| Changes From v1.0 | GC worker (conditional Phase 1), segment deletion in Phase 1, Auth0/ServiceAccount auth, TLS at ingress clarification, circuit breaker, retry guidance, idempotency keys, migration rollback, alerting rules, connection pool exhaustion, backup/restore reconciliation |

---

## 1. Purpose

OpenTAMS is an open-source, cloud-agnostic server implementing the BBC Time-Addressable Media Store (TAMS) v8.0 API specification. Any TAMS v8.0 compliant client must work against OpenTAMS without modification.

OpenTAMS is an **implementation** of an existing open standard. Spec compliance is the primary product requirement. All functional requirements trace to the TAMS v8.0 specification unless explicitly marked as OpenTAMS extensions.

> **User Personas:** All requirements in this document are stated from the perspective of one or more user personas defined in Section 15. Refer to Section 15 when evaluating any requirement from a specific persona's viewpoint.

### 1.1 Motivation

The only existing TAMS implementation (AWS Labs) is coupled to AWS services and not production-ready. There is no cloud-agnostic option, no local development path, and no community-maintained implementation. OpenTAMS fills this gap.

### 1.2 Context: Intended Use

TAMS is designed for broadcast-scale segmented media workflows: live ingest, file-based ingest, playout, editing, and transcoding. OpenTAMS targets deployment scenarios involving hundreds of concurrent media workflows with segment durations in the range of 1–10 seconds.

This context informs the performance baselines (Section 8) but does not constrain the API. OpenTAMS implements the TAMS API generically — it has no knowledge of specific application domains.

---

## 2. TAMS v8.0 Compliance

### 2.1 Compliance Principle

[REQ-COMP-01] OpenTAMS implements the TAMS v8.0 API as defined in the OpenAPI specification at `github.com/bbc/tams`, pinned to a specific commit hash recorded in the codebase.

[REQ-COMP-02] The implemented API version is reported in the `GET /service` response via the `api_version` field.

[REQ-COMP-03] All API requests are validated against the TAMS v8.0 schema. Invalid requests return 400.

[REQ-COMP-04] Unknown additional fields in request bodies are accepted and ignored (not rejected). This provides forward compatibility with newer TAMS spec minor versions that add optional fields.

[REQ-COMP-05] Requests to undefined paths return 404 with a structured error response (see Section 6.3).

### 2.2 Feature Implementation Status

All features are categorized against the TAMS v8.0 spec.

#### Implemented in Phase 1

**Service:**
- `GET /` — list root paths (only implemented endpoints advertised)
- `GET /service` — service info including `api_version`, `min_object_timeout`, `min_presigned_url_timeout`, `event_stream_mechanisms` (empty array in Phase 1), storage backend references
- `GET /service/storage-backends` — list configured storage backends
- `HEAD` supported on all above

**Sources (implicit lifecycle):**
- `GET /sources` — list with pagination, filtering by label, format, tag.{name}, tag_exists.{name}
- `GET /sources/{sourceId}` — single source
- `GET/PUT/DELETE /sources/{sourceId}/tags/{name}` — individual tag CRUD
- `GET/PUT/DELETE /sources/{sourceId}/description`
- `GET/PUT/DELETE /sources/{sourceId}/label`
- `GET /sources/{sourceId}/tags` — list all tags
- `HEAD` supported on all above

**Flows:**
- `GET /flows` — list with pagination, filtering by source_id, timerange, format, codec, label, tag.{name}, tag_exists.{name}, frame_width, frame_height
- `GET /flows/{flowId}` — single flow (supports `include_timerange`, `timerange` params)
- `PUT /flows/{flowId}` — create (201) or replace (204)
- `DELETE /flows/{flowId}` — synchronous metadata deletion
- `GET/PUT/DELETE /flows/{flowId}/tags/{name}`
- `GET/PUT/DELETE /flows/{flowId}/description`
- `GET/PUT/DELETE /flows/{flowId}/label`
- `GET/PUT /flows/{flowId}/read_only`
- `GET/PUT/DELETE /flows/{flowId}/flow_collection`
- `GET/PUT/DELETE /flows/{flowId}/avg_bit_rate`
- `GET/PUT/DELETE /flows/{flowId}/max_bit_rate`
- `GET /flows/{flowId}/tags` — list all tags
- `HEAD` supported on all above

**Segments:**
- `GET /flows/{flowId}/segments` — with all TAMS v8.0 query parameters: timerange, object_id, reverse_order, verbose_storage, accept_get_urls, accept_storage_ids, presigned, include_object_timerange
- `POST /flows/{flowId}/segments` — single or array, with partial failure semantics
- `DELETE /flows/{flowId}/segments` — delete by timerange query parameter (required for GC worker)
- `HEAD` supported on GET

**Storage:**
- `POST /flows/{flowId}/storage` — both `limit` and `object_ids` modes, optional `storage_id`

**Health (OpenTAMS extension):**
- `GET /healthz` — liveness probe (process alive, no dependency check)
- `GET /readyz` — readiness probe (see Section 7.3)

#### Conditional Phase 1 (subject to bandwidth)

| Feature | Rationale |
|---------|-----------|
| GC Worker (all 5 mechanisms) | See Section 4.10. Requires `DELETE /flows/{flowId}/segments` above. Implemented as separate binary or mode of main binary. |

#### Deferred to Phase 2+

| Feature | Spec Reference | Rationale |
|---------|---------------|-----------|
| Webhooks (all `/service/webhooks` endpoints) | TAMS v8.0 Webhooks | Not needed for core read/write workflows |
| `/objects/{objectId}` GET | TAMS v8.0 Objects | Not needed until multi-region or CDN distribution |
| `/objects/{objectId}/instances` POST/DELETE | TAMS v8.0 Objects | Same as above |
| Async flow DELETE + `/flow-delete-requests` | TAMS v8.0 Flow Delete Requests | Phase 1 flows are small enough for synchronous metadata delete |
| `POST /service` (update service info) | TAMS v8.0 Service | Read-only service info sufficient |
| Object garbage collection (active) | TAMS v8.0 behavioral | Covered as conditional Phase 1 via GC worker (Section 4.10); mandatory Phase 2 if deferred |
| ETag concurrency on Flow/Source updates | TAMS v8.0 optional | No concurrent writers to same entity in Phase 1 |
| OpenAPI spec endpoint (`/openapi.json`) | OpenTAMS extension | Developers reference TAMS spec directly |
| Deprecation signaling headers (RFC 8594) | OpenTAMS extension | Phase 2 when deprecated fields are removed |
| Caching headers on responses | OpenTAMS extension | Phase 2 exploration |
| Distributed tracing (OpenTelemetry) | OpenTAMS extension | Phase 1 request-ID propagation sufficient; Phase 2 via OpenTelemetry |
| Backpressure mechanism | OpenTAMS extension | Requires client-side changes; Phase 2 |
| Storage quotas / admission control | OpenTAMS extension | Per-tenant storage limits; Phase 2 |
| Active circuit breaker (sony/gobreaker) | OpenTAMS extension | Passive monitoring sufficient for Phase 1; Phase 2 |
| RBAC / ABAC authorization | OpenTAMS extension | Phase 2 via OpenFGA |
| Client library / SDK | OpenTAMS extension | Phase 1 focus is TAMS API compliance; SDK is a non-trivial additional surface. Sample programs (REQ-OSS-12) cover the integration learning path for Phase 1. |

---

## 3. Data Model

### 3.1 Source

Created implicitly when a Flow references a new `source_id`. No direct create/delete endpoint.

**Required fields:** `id` (UUID), `format` (content-format enum).

**Optional fields:** `label`, `description`, `tags`, `created_by`, `updated_by`.

**Server-managed (read-only):** `created`, `updated`, `source_collection` (inferred from Flow relationships), `collected_by` (inferred from Flow relationships).

[REQ-DATA-01] On implicit creation, the Source receives `id` (from the Flow's `source_id`), `format` (from the Flow's format), and `created` timestamp. All other fields are unset.

[REQ-DATA-02] `source_collection` and `collected_by` are correctly derived from `flow_collection` relationships across Flows. They are read-only and consistent with the current state of all Flows.

### 3.2 Flow

Created via `PUT /flows/{flowId}` with client-generated UUID. Five format variants: video, audio, image, data, multi.

**Core required:** `id` (UUID), `source_id` (UUID), `format` (content-format enum).

**Format-specific required:**
- Video: `codec` (MIME type), `essence_parameters.frame_width`, `essence_parameters.frame_height`. `essence_parameters.frame_rate` required when `essence_parameters.vfr` is false or unset.
- Audio: `codec` (MIME type), `essence_parameters.sample_rate`, `essence_parameters.channels`.
- Image: `codec` (MIME type), `essence_parameters.frame_width`, `essence_parameters.frame_height`.
- Data: `codec` (MIME type). `essence_parameters.data_type` optional.
- Multi: `format` only. No codec or essence_parameters required.

**Optional fields:** `label`, `description`, `tags`, `container` (MIME type), `container_mapping`, `avg_bit_rate`, `max_bit_rate`, `segment_duration`, `generation`, `read_only`, `flow_collection`, `metadata_version`, `created_by`, `updated_by`.

**Server-managed (read-only):** `created`, `metadata_updated`, `segments_updated`, `timerange` (computed from segments), `collected_by` (inferred from other Flows' `flow_collection`).

**Store-and-return fields (no server-side logic):** `generation`, `metadata_version`, `segment_duration`, `container_mapping`.

[REQ-DATA-03] `container_mapping` is stored per the TAMS v8.0 schema. The server validates structural conformance but does not interpret mapping semantics. Supported mapping types: MPEG-TS (pid), ISOBMFF (track_id), generic (track_index). MXF mapping is deferred to Phase 2.

[REQ-DATA-04] `vfr` is a boolean field on video Flows. When `vfr` is false or unset, `frame_rate` is required. When `vfr` is true, `frame_rate` is optional.

### 3.3 Flow Segment

Maps a Media Object (or portion) to a position on a Flow's timeline.

**Required:** `object_id` (string), `timerange`.

**Optional:** `ts_offset` (defaults `0:0`, negative values supported), `object_timerange`, `last_duration`, `key_frame_count`, `get_urls` (for uncontrolled URLs on POST).

**Deprecated (accepted, stored, returned, not used in logic):** `sample_offset`, `sample_count`.

**On GET response:** inherits `get_urls` array from object instances, each with `url`, `storage_id`, `presigned`, `label`, `controlled`.

[REQ-DATA-05] Timeline math: `segment_ts = media_object_ts + ts_offset`. Negative `ts_offset` values are supported.

### 3.4 Media Object (metadata only)

Phase 1 does not expose `/objects` endpoints but tracks object metadata internally.

[REQ-DATA-06] The server tracks object timeranges. When a client provides `object_timerange` on segment registration, it is stored. When not provided, no `object_timerange` is stored or returned — the server does not compute a value. Subsequent segment registrations for the same object are validated against the stored object timerange only when the timerange was explicitly client-provided. Rationale: a server-computed value would represent usage (the segment's portion of the object), not extent (the full object timeline); validating against it would falsely reject valid segments that use more of the object than the first segment did.

[REQ-DATA-07] The server tracks which flows reference each object (via segment registrations) and maintains a reference count per object. This reference count is used for GC: when a segment is deleted, the object's reference count is decremented. When the count reaches zero, the object is deleted from the object store.

### 3.5 Primitives

**Timestamp:** `{sign?}{seconds}:{nanoseconds}`. Regex: `^-?(0|[1-9][0-9]*):(0|[1-9][0-9]{0,8})$`. Nanosecond precision. Negative values supported.

**TimeRange:** Bracket notation. `[` inclusive, `(` exclusive. `_` separator. Examples: `[0:0_10:0)` = 10 seconds, `_` = eternity, `()` = never, `[1:0]` = instantaneous.

**Content Format:** `urn:x-nmos:format:video`, `urn:x-nmos:format:audio`, `urn:x-tam:format:image`, `urn:x-nmos:format:data`, `urn:x-nmos:format:multi`.

**UUID:** RFC 9562, lowercase hex.

**Tags:** Key = freeform string. Value = string OR array of strings.

**MIME Type:** Standard type/subtype without parameters.

---

## 4. Behavioral Rules

### 4.1 Source Lifecycle

[REQ-BEH-01] Sources are created implicitly when a Flow references a `source_id` that does not exist. There are no direct Source create or delete endpoints.

[REQ-BEH-02] When a Flow is created referencing an existing Source, the Flow's `format` must match the Source's `format`. Incompatible formats return 400.

[REQ-BEH-03] Implicit Source creation is idempotent — creating a Source that already exists is a no-op.

### 4.2 Flow Lifecycle

[REQ-BEH-04] `PUT /flows/{flowId}` creates a new Flow (201) or replaces an existing Flow (204). The client generates the UUID.

[REQ-BEH-05] Flow PUT is idempotent. Concurrent PUTs to the same flow ID are safe. Last write wins.

[REQ-BEH-06] `source_id` and `format` are always immutable on an existing Flow. Attempts to change them return 400.

[REQ-BEH-07] When a Flow has segments: `codec` and `essence_parameters` fields that would affect existing segment interpretation are immutable. Attempts to change them return 400.

[REQ-BEH-08] When a Flow has no segments: `codec`, `essence_parameters`, and all optional fields may be replaced via PUT, subject to Source format compatibility (REQ-BEH-02).

[REQ-BEH-09] `DELETE /flows/{flowId}` removes all segment metadata and flow metadata synchronously. For each deleted segment, the referenced media object's reference count is decremented. Media objects with a reference count of zero are deleted from the object store. Returns 404 for non-existent flows.

[REQ-BEH-10] Objects whose reference count does not reach zero after flow deletion (i.e., shared objects still referenced by other flows) are not deleted. Their reference counts remain accurate.

### 4.3 read_only Enforcement

[REQ-BEH-11] When `read_only` is true on a Flow: segment writes (`POST segments`), segment deletion (`DELETE segments`), storage allocation (`POST storage`), flow deletion (`DELETE`), flow replacement (`PUT`), and metadata sub-resource updates (`PUT/DELETE` on tags, label, description, bitrates, flow_collection) are all rejected with 403.

[REQ-BEH-12] The only allowed write operation on a `read_only` flow is `PUT /flows/{flowId}/read_only` (to change the read_only state itself).

### 4.4 Segment Registration

[REQ-BEH-13] `POST /flows/{flowId}/segments` accepts a single segment or an array.

[REQ-BEH-14] Full success returns 201. Partial success returns 200 with a `failed_segments` array containing the original payloads and failure reasons for each failed segment.

[REQ-BEH-15] Segments on the same Flow MUST NOT have overlapping timeranges. Overlapping segments are rejected with 422. This applies to overlaps with existing segments AND overlaps within the same batch. **Any overlap rejects the entire batch — no segments are persisted, and no `failed_segments` array is returned.** This diverges from the TAMS specification's first-wins ordering, deliberately; see [ADR-0024](adr/0024-whole-batch-reject-on-segment-overlap.md).

[REQ-BEH-16] `GET /flows/{flowId}/segments` returns 404 for a non-existent flow. This diverges from the TAMS specification, which asks for an empty list, deliberately: returning `[]` for a mistyped flow id turns a client bug into a plausible-looking empty result. Recorded in [`conformance.md`](conformance.md).

[REQ-BEH-17] The server stores segment metadata as provided by the client. It does not validate the existence or content of referenced media objects. Data quality of media references is the client's responsibility.

[REQ-BEH-18] Clients may provide `get_urls` on segment POST to register uncontrolled (external) download URLs alongside or instead of controlled (server-managed) URLs.

### 4.5 Overlap Detection

[REQ-BEH-19] The non-overlapping segment invariant is enforced by the metadata store implementation. The metadata store interface contract specifies: "InsertSegments MUST reject any batch that would create overlapping segments on the same flow, whether overlapping with existing segments or within the batch itself. The implementation MUST be safe under concurrent writes to the same flow."

[REQ-BEH-20] A shared domain module provides timerange parsing, overlap predicate logic, and timerange arithmetic. This module is independently testable without a running metadata store. Metadata store implementations use this module for their overlap enforcement logic.

### 4.6 Copy-on-Write / Edit-by-Reference

[REQ-BEH-21] Multiple flows may reference the same media object via segment registration. No media is copied. This is a core TAMS capability.

[REQ-BEH-22] Segments support `ts_offset` for remapping a media object's timeline onto a different flow timeline position. Segments support `object_timerange` for referencing a portion of a media object.

### 4.7 Immutability

[REQ-BEH-23] Once a segment is written to a position on a Flow's timeline, it cannot be modified. Flows can be extended (adding segments to empty timeline regions). Segment erasure by timerange is supported via `DELETE /flows/{flowId}/segments` (Phase 1).

### 4.8 Storage Allocation

[REQ-BEH-24] `POST /flows/{flowId}/storage` allocates media object identifiers and returns time-limited upload URLs. Supports `limit` (server allocates N objects) and `object_ids` (client specifies IDs) modes, mutually exclusive. Optional `storage_id` parameter accepted (single backend in Phase 1).

[REQ-BEH-25] The server generates time-limited URLs for media upload and download. The URL generation mechanism is provided by the object store interface. Each backend implementation uses its native signing mechanism. Upload URLs are signed with the flow's `codec` value as the Content-Type, binding the URL to that media type.

[REQ-BEH-25a] If a presigned upload URL expires before the client completes the upload, the client MUST call `POST /flows/{flowId}/storage` again to obtain a fresh URL. In `limit` mode, a new `object_id` is allocated. In `object_ids` mode, the client MAY re-request the same `object_id` provided it has not already been registered as a segment; re-requesting an `object_id` that is already registered as a segment returns 400.

[REQ-BEH-25b] Each `put_url` object in the storage allocation response includes a `content-type` field set to the flow's `codec` value. The client MUST use this exact Content-Type header when uploading via the presigned URL; uploads with a mismatched Content-Type will be rejected by the object store.

### 4.9 Request Handling

[REQ-BEH-26] All GET endpoints support HEAD, returning identical headers without a response body.

[REQ-BEH-27] Requests with unsupported `Accept` headers return 406 Not Acceptable.

[REQ-BEH-28] Malformed JSON, invalid UUIDs, and invalid timerange syntax return 400.

[REQ-BEH-29] Optional fields not set are omitted from JSON responses. Empty strings and empty arrays are distinct from absent fields.

[REQ-BEH-30] The server enforces configurable request timeouts. Requests exceeding the timeout are terminated with an appropriate error response.

[REQ-BEH-31] CORS is configurable and disabled by default. When enabled, the server responds to preflight OPTIONS requests with configured allowed origins, methods, and headers.

### 4.10 Segment Deletion

[REQ-BEH-32] `DELETE /flows/{flowId}/segments` deletes all segments within the specified `timerange` query parameter. Returns 204 on success. Returns 404 for non-existent flows.

[REQ-BEH-33] For each deleted segment, the referenced media object's reference count is decremented. When an object's reference count reaches zero, it is deleted from the object store. This is the mechanism by which TAMS controls object lifecycle — no S3 lifecycle policies are relied upon for this purpose.

[REQ-BEH-34] Segment deletion on a `read_only` flow returns 403.

[REQ-BEH-35] Segment deletion is atomic per request: either all matching segments in the specified timerange are deleted, or none are (on failure).

### 4.11 GC Worker (not implemented)

> **Status: not implemented.** No released build of OpenTAMS runs a garbage collector.
> `opentams gc` exits with an error, `GC_POLL_INTERVAL` and `GC_BATCH_SIZE` are parsed
> and ignored, and objects orphaned by partial failures accumulate until deleted by hand.
> REQ-GC-01 through REQ-GC-08 describe the intended design and are the acceptance
> criteria for that work — they do not describe current behaviour.

> This section is conditional Phase 1 scope, subject to development bandwidth. All requirements in this section depend on REQ-BEH-32–35 being implemented first.

The GC worker is responsible for automated cleanup of segments and flows based on configurable retention policies. It operates against the OpenTAMS API (same API all clients use) and requires no internal access.

[REQ-GC-01] The GC worker is implemented as a separate binary or as an explicit mode of the main server binary (e.g., `opentams --mode=gc-worker`). It runs independently of the API server and is deployed as a separate process or job.

[REQ-GC-02] **Segment Retention (Loop Recording):** Flows tagged with `segment_retention_offset` are eligible for automatic segment pruning. The GC worker polls `GET /flows?tag_exists.segment_retention_offset=true` at a configurable interval, then deletes old segments via `DELETE /flows/<flow_id>/segments?timerange=<timerange up to now minus offset>`. TAMS decrements ref counts and deletes objects from the object store when ref count reaches zero.

[REQ-GC-03] **Flow Retention (Inactivity-based):** Flows tagged with `flow_retention_offset` are eligible for automatic deletion on inactivity. The GC worker polls `GET /flows?tag_exists.flow_retention_offset=true`, evaluates the inactivity condition against the flow's `segments_updated` timestamp, and deletes inactive flows via `DELETE /flows/<flow_id>`. TAMS handles segment deletion, ref count updates, and object store cleanup.

[REQ-GC-04] **Empty Flow Cleanup:** The GC worker identifies flows with no segments that are also inactive (no updates within a configurable period), and deletes them via `DELETE /flows/<flow_id>`. No object store cleanup is required as there are no segments.

[REQ-GC-05] **Object Reference Count GC:** Object deletion is driven by segment deletion (REQ-BEH-33). The GC worker does not need to directly manage object deletion; it is a side effect of segment and flow deletion. For forced cleanup: references to a specific object can be found via `GET /objects/<object_id>` (Phase 2 endpoint) or by querying flows; segments are then deleted via `DELETE /flows/<flow_id>/segments?object_id=<object_id>`.

[REQ-GC-06] **Manual / Business-rule Deletion:** External systems (e.g., FCS) may trigger deletion directly via `DELETE /flows/<flow_id>/segments` or `DELETE /flows/<flow_id>`. TAMS handles ref counts and object store cleanup as per REQ-BEH-09 and REQ-BEH-33.

[REQ-GC-07] GC worker polling intervals, retention tag names, and inactivity thresholds are configurable. Defaults are documented in the configuration reference.

[REQ-GC-08] GC worker activity is observable: each deletion action is logged with the flow_id, timerange, and reason. GC worker errors are surfaced in metrics.

---

## 5. Architecture

### 5.1 Separation of Metadata and Media Planes

[REQ-ARCH-01] The server manages metadata (Sources, Flows, Segments, Object metadata). It never reads, writes, or inspects media content. Media upload and download occur directly between clients and the object store via time-limited URLs.

[REQ-ARCH-02] The server manages media storage lifecycle (allocate object IDs, generate URLs, track references). This is metadata-plane activity, not media-plane activity.

### 5.2 Stateless Server

[REQ-ARCH-03] The server process holds no persistent state. All persistent data resides in the metadata store and object store. Multiple server instances may run behind a load balancer, all sharing the same data stores.

### 5.3 Pluggable Backends via Interfaces

[REQ-ARCH-04] The metadata store and object store are accessed through defined interfaces. Phase 1 implements one backend for each. The interface boundary must be clean enough that adding a second implementation requires zero changes to API handlers or business logic.

**Metadata store interface contract:**

[REQ-ARCH-05] The metadata store must support: structured data with relationships, transactional writes, range-overlap queries on segment timeranges, cursor-based pagination, multi-predicate filtering (tags, labels, format, codec, frame dimensions), concurrent access from multiple server instances, and referential integrity.

[REQ-ARCH-06] The metadata store must enforce the non-overlapping segment invariant per flow (REQ-BEH-19). The enforcement mechanism is implementation-specific.

[REQ-ARCH-07] Bulk segment registration must be atomic: either all segments in a batch succeed, or individually failed segments are identified while the rest succeed (partial failure mode).

[REQ-ARCH-08] The server assumes the metadata store provides read-after-write consistency. Deployments using eventual-consistency configurations must accept the corresponding trade-offs.

**Object store interface contract:**

[REQ-ARCH-09] The object store interface provides: `GenerateUploadURL(objectID, contentType)` (returns time-limited upload URL signed for the given Content-Type), `GenerateDownloadURL(objectID)` (returns time-limited or direct download URL), `DeleteObjects(objectIDs)` (batch delete by object IDs), `HealthCheck` (tests connectivity).

[REQ-ARCH-10] The interface defines a structured error model. Errors are classified as: transient (retry-eligible), permanent (requires intervention), partial (some operations succeeded), authentication (credentials invalid).

[REQ-ARCH-11] Time-limited URL generation should be a local operation where possible (cryptographic signing without network calls). Backends that require network calls for URL generation must document this behavior.

**Auth provider interface:**

[REQ-ARCH-12] The auth provider interface supports: credential validation, identity extraction, and configurability. Phase 1 implements JWT verification only (external OIDC issuer for external-facing; Kubernetes ServiceAccount tokens for service-to-service) and a dev mode no-op provider.

### 5.4 Schema Migrations

[REQ-ARCH-13] Database schema changes are applied via an explicit migration step, separate from server startup. An optional auto-migration mode is provided for development convenience, disabled by default.

[REQ-ARCH-14] Schema migrations follow the expand-contract pattern: new structures are added alongside old ones, readers are migrated, then old structures are removed across multiple releases. Each individual migration is backward-compatible with the previous release's server binary.

[REQ-ARCH-15] On startup, the server validates the metadata store schema version. If the schema is absent or incompatible, the server logs a clear error identifying the expected and actual schema versions, and exits.

[REQ-ARCH-16] Every schema migration must include a corresponding down migration that reverses the operation and is idempotent on its own. The migration binary supports a `--down` mode for rollback. Every release that includes an up migration must have a test that validates the down migration passes before production rollout. Standard migration tooling (e.g., golang-migrate) is used to manage migration versioning.

### 5.5 Phase 1 Implementation Choices

These are engineering decisions, not requirements. They are recorded here for context.

| Concern | Phase 1 Choice | Rationale |
|---------|---------------|-----------|
| Language | Go | Native concurrency, single-binary deployment, AI-assisted development compatibility |
| Metadata store | PostgreSQL | Mature, transactional, range query support, widely available |
| Object store | S3-compatible (MinIO for dev) | De facto standard, multiple cloud providers and self-hosted options |
| Metrics format | Prometheus exposition | Widely adopted, k8s ecosystem standard |
| Dashboard | Grafana | Pairs with Prometheus, extensible |
| Log aggregation | Loki-compatible (structured JSON) | Pairs with Grafana, label-based querying |
| Container orchestration | Kubernetes | Existing infrastructure at target deployment |

---

## 6. Security

### 6.1 Authentication

[REQ-SEC-01] Two authentication modes:
- **JWT:** token validated against one or two configured JWKS endpoints.
  - **External issuer** (`AUTH_EXTERNAL_ISSUER_URL` / `AUTH_EXTERNAL_AUDIENCE`) authenticates human and API clients. Required in production.
  - **Internal issuer** (`AUTH_INTERNAL_ISSUER_URL` / `AUTH_INTERNAL_AUDIENCE`) authenticates service-to-service callers from a separate IdP — typically the Kubernetes API server's ServiceAccount issuer, but also any workload-identity issuer (Auth0 M2M, AWS Cognito client_credentials, GCP Workload Identity, etc.). Optional. When configured, both fields must be set together (both-or-neither). Plain Docker / VM deployments that have no separate service-to-service IdP simply omit this pair; service callers authenticate via the external issuer with a distinct audience claim if semantic separation is desired.
  - Token expiry provides replay protection — no additional request signing is required.
- **Dev mode:** no authentication. Enabled by `APP_ENV=development`. Never the default.

> *Decision: JWT-only authentication for Phase 1. Rationale: API keys require a key management surface (create, rotate, revoke) which is Phase 2 scope; shipping a static key model as a permanent interface creates a hard-to-remove legacy path. JWTs delegate identity management to existing infrastructure (external OIDC provider, optional second IdP for service-to-service). Rejected: API keys (deferred — no runtime management in Phase 1); mTLS (too operationally complex for open-source deployments). Tradeoff: operators must configure an OIDC provider for production; dev mode covers local development without that dependency.*

> *Decision (v1.2): the internal issuer pair is optional and decoupled from Kubernetes. Rationale: v1.1 hard-required `AUTH_INTERNAL_*` in production, naming the Kubernetes API server as the canonical issuer. That assumption broke any non-Kubernetes deployment topology — plain Docker, ECS, Nomad, or a single VM — even though no other component of the runtime has a Kubernetes coupling. Making the pair optional restores parity with the rest of the binary, which is portable across container runtimes. Validation enforces both-or-neither so a half-configured pair fails fast at startup. Rejected: silently accepting a half-configured pair (would surface as "no_match" 401s at request time, hard to diagnose); requiring the operator to fake values when not in use (operational footgun — fake values break JWKS fetch and therefore the external issuer too if the validator short-circuits). Tradeoff: deployments with genuine service-to-service IdPs must remember to configure both halves; the both-or-neither check makes the misconfiguration loud rather than silent.*

[REQ-SEC-02] Authentication applies to TAMS API requests only. Media upload and download use time-limited URLs generated by the server, which embed their own authorization.

[REQ-SEC-03] Runtime API key management (create, revoke, rotate) is deferred to Phase 2.

### 6.2 Authorization

[REQ-SEC-04] Phase 1: all authenticated requests are authorized for all operations (all-or-nothing model). The architecture must support adding authorization rules (RBAC, ABAC) in Phase 2 without changing the API contract or the auth provider interface.

### 6.3 Error Responses

[REQ-SEC-05] All non-2xx responses use RFC 9457 (Problem Details) body structure with `Content-Type: application/problem+json`. Fields: `type` (URI identifying the error class — omitted when no specific type is defined, per RFC 9457 §3.1 which treats absent `type` as equivalent to `about:blank`), `title` (human-readable category), `status` (HTTP status code), `detail` (specific error description), `instance` (request path). Extension field: `request_id`.

> *Decision: RFC 9457 Problem Details for all error responses. Rationale: TAMS v8.0 uses a non-standard `error.json` schema; RFC 9457 is the industry standard for HTTP APIs and is better understood by client developers. The `type` URI provides a stable, linkable identifier for each error class. Rejected: TAMS error.json (non-standard, no ecosystem tooling); custom schema (unnecessary divergence from standard). Tradeoff: deviates from strict TAMS spec compliance on error format — documented as an OpenTAMS extension. RFC 9457 obsoletes RFC 7807 and recommends `application/problem+json` as the content type.*

Example:
```json
{
  "type": "https://github.com/amagioss/opentams/problems/segment-overlap",
  "title": "Segment Overlap",
  "status": 422,
  "detail": "Segment [100:0_106:0) overlaps existing segment [103:0_109:0)",
  "instance": "/flows/abc-123/segments",
  "request_id": "550e8400-e29b-41d4-a716-446655440000"
}
```

### 6.3.1 Error Type URI Catalogue

All `type` URIs use the base `https://github.com/amagioss/opentams/problems/` prefix. The following URIs are stable identifiers for programmatic error handling. Client code SHOULD match on `type` URI, not on `status` or `detail` text (which may change). The URIs are dereferenceable to human-readable documentation for each problem type.

| `type` URI | HTTP Status | Triggered by |
|-----------|------------|--------------|
| `https://github.com/amagioss/opentams/problems/segment-overlap` | 422 | `POST /segments` — submitted segment overlaps an existing segment or another segment in the same batch |
| `https://github.com/amagioss/opentams/problems/invalid-timerange` | 400 | Any endpoint — timerange string fails to parse or is semantically invalid (e.g. end before start) |
| `https://github.com/amagioss/opentams/problems/invalid-uuid` | 400 | Any endpoint — path or body UUID does not match the UUID pattern |
| `https://github.com/amagioss/opentams/problems/invalid-json` | 400 | Any endpoint — request body is not valid JSON |
| `https://github.com/amagioss/opentams/problems/schema-validation` | 400 | Any endpoint — request body fails JSON schema validation (missing required field, wrong type, etc.) |
| `https://github.com/amagioss/opentams/problems/immutable-field` | 400 | `PUT /flows/{flowId}` — attempt to change `source_id`, `format`, or (when segments exist) `codec` / `essence_parameters` |
| `https://github.com/amagioss/opentams/problems/format-mismatch` | 400 | `PUT /flows/{flowId}` — Flow `format` is incompatible with the existing Source `format` |
| `https://github.com/amagioss/opentams/problems/missing-idempotency-key` | 400 | `POST /flows/{flowId}/segments` — `X-Idempotency-Key` header absent |
| `https://github.com/amagioss/opentams/problems/storage-mode-conflict` | 400 | `POST /flows/{flowId}/storage` — both `limit` and `object_ids` provided, or neither provided. **Reserved — not currently emitted.** |
| `https://github.com/amagioss/opentams/problems/object-id-exists` | 400 | `POST /flows/{flowId}/storage` — a supplied `object_id` in `object_ids` mode already exists as a registered segment |
| `https://github.com/amagioss/opentams/problems/vfr-frame-rate-conflict` | 400 | `PUT /flows/{flowId}` — `vfr: false` set without `frame_rate`, or `vfr: true` set with `frame_rate` |
| `https://github.com/amagioss/opentams/problems/batch-too-large` | 400 | `POST /flows/{flowId}/segments` — array exceeds 1000 segments |
| `https://github.com/amagioss/opentams/problems/unauthorized` | 401 | Any TAMS API endpoint — missing or invalid JWT Bearer token |
| `https://github.com/amagioss/opentams/problems/token-expired` | 401 | Any TAMS API endpoint — JWT has expired |
| `https://github.com/amagioss/opentams/problems/read-only` | 403 | Any write operation on a flow with `read_only: true` (except `PUT /read_only`) |
| `https://github.com/amagioss/opentams/problems/not-found` | 404 | Any endpoint — referenced resource (flow, source, tag) does not exist |
| `https://github.com/amagioss/opentams/problems/rate-limited` | 429 | Any endpoint — rate limit exceeded. **Reserved — not currently emitted.** |
| `https://github.com/amagioss/opentams/problems/not-acceptable` | 406 | Any endpoint — client `Accept` header requests an unsupported content type. **Reserved — not currently emitted.** |
| `https://github.com/amagioss/opentams/problems/dependency-unavailable` | 503 | `POST /flows/{flowId}/storage` — object store unreachable; or any endpoint when connection pool exhausted. **Reserved — not currently emitted.** |

### 6.4 Hardening

[REQ-SEC-06] Container image: non-root user, minimal base image, no shell.

[REQ-SEC-07] TLS for external-facing communication is terminated at the ingress layer by default. For deployments without an ingress (e.g., standalone Docker), the server supports TLS directly via configuration flags (`SERVER_TLS_CERT_FILE`, `SERVER_TLS_KEY_FILE`). When neither is set, the server runs HTTP only — acceptable within a trusted cluster network but not for direct external exposure.

> *Decision: Ingress-terminated TLS as default; optional server-level TLS via config flags. Rationale: containerised deployments behind K8s ingress or a reverse proxy should not duplicate TLS management in the application. Standalone Docker deployments have no ingress layer and need server-level TLS to avoid exposing bare HTTP externally. Rejected: mandatory app-level TLS (adds cert management burden for K8s users who already have ingress); no TLS option at all (breaks standalone Docker deployments exposed directly). Chosen: ingress-first with optional server TLS as an escape hatch.*

[REQ-SEC-08] Metadata store connections use TLS, provided by the managed database service (e.g., RDS TLS). No additional TLS configuration is required at the application layer.

[REQ-SEC-09] Secrets injectable via environment variables or mounted files.

[REQ-SEC-10] Configurable request body size limits with defaults sufficient for bulk segment registration. Maximum segments per `POST /flows/{flowId}/segments` batch: 1000. Maximum list page size: 1000 items. Maximum media objects per `POST /flows/{flowId}/storage` request: 100, in either mode — each object costs a presigned-URL round trip, and the caller-supplied mode a registration lookup as well, so the bound is lower than the others. These maximums are documented in the OpenAPI specification and enforced at runtime — requests exceeding them return 400.

[REQ-SEC-11] Dependency vulnerability scanning in CI.

### 6.5 Audit

[REQ-SEC-12] All API requests are logged with authenticated identity (`auth_subject`), enabling access auditing via structured log queries. Dedicated audit log is deferred to Phase 2.

---

## 7. Reliability

### 7.1 Availability Posture

[REQ-REL-01] The server architecture supports high availability through stateless horizontal scaling and metadata store redundancy. The server does not implement its own replication, failover, or clustering. HA is achieved through infrastructure: multiple stateless server instances, a highly available metadata store, and a durable object store.

[REQ-REL-02] Specific availability SLAs are not defined for Phase 1. The architecture must not preclude achieving 99.9% or higher availability in production deployments.

> **SLO Framework:** A formal SLO framework (error budgets, burn rate alerts) is under discussion pending product and customer requirements. Phase 1 provides the observability foundation (latency percentiles, error rates, metrics) needed to define SLOs once requirements are clarified.

### 7.2 Partial Dependency Failure

[REQ-REL-03] When the object store is unreachable, metadata-only operations continue normally. This includes: flow CRUD, source queries, segment registration, segment queries (returning structurally valid URLs that may not currently resolve).

[REQ-REL-04] Storage allocation (`POST /storage`) checks cached object store health status and returns 503 immediately when the object store is known to be unavailable.

[REQ-REL-05] A background health checker periodically tests all dependencies and caches results. Individual dependency health is observable via metrics.

### 7.3 Health Probes

[REQ-REL-06] `/healthz` (liveness): returns 200 when the server process is running. No dependency checks.

[REQ-REL-07] `/readyz` (readiness): checks metadata store connectivity. Returns 200 when the metadata store is reachable. Returns 503 when unreachable. Response body is intentionally empty — Kubernetes readiness probes evaluate HTTP status only and ignore the body. Per-dependency health breakdown is available via `/health/details` for DevOps tooling and monitoring dashboards.

[REQ-REL-08] Health blip tracking: consecutive readiness failures are counted. When 3 consecutive failures occur, the condition is flagged in metrics and logged. When health recovers (2 consecutive successes), the blip is resolved and logged. These thresholds are fixed constants.

### 7.4 Graceful Shutdown

[REQ-REL-09] On termination signal: the server stops accepting new connections, completes in-flight requests within a configurable drain timeout, and releases all resources (metadata store connections, open handles). The readiness probe immediately returns 503 to prevent new traffic routing.

### 7.5 Startup

[REQ-REL-10] The server is ready to handle requests at target latency within seconds of starting. Connection pools are established during startup before the readiness probe reports healthy.

[REQ-REL-11] Startup validates all configuration and metadata store schema version. Invalid configuration or incompatible schema causes immediate exit with a clear error message identifying the specific problem.

### 7.6 Data Integrity

[REQ-REL-12] The metadata store contains the authoritative timeline index. PostgreSQL backup is managed by the managed database service (e.g., RDS automated backups). Loss of the metadata store results in loss of all timeline information. Media objects remain in the object store but are not discoverable without the metadata index.

[REQ-REL-13] **Backup consistency reconciliation procedure:** After restoring the metadata DB to a point-in-time snapshot at timestamp `T`, an operator can run a reconciliation job to verify consistency with the object store:
1. Obtain the S3 Inventory snapshot closest to timestamp `T`.
2. Query all `object_ids` from the restored metadata DB.
3. Compare against the S3 Inventory manifest and classify discrepancies:
   - **In metadata, not in S3** → dangling reference (segment points to deleted/missing object). Resolution: mark affected segments as invalid or delete the segments (triggers ref-count cleanup).
   - **In S3, not in metadata** → orphaned object (object exists but is not indexed). Resolution: move to a quarantine prefix; hold for N days, then delete.
4. Reconciliation tooling requirements and runbook are provided as an operational document.

[REQ-REL-14] Orphaned objects (allocated via storage but never registered as segments, or resulting from partial failures) are addressed by the GC worker (conditional Phase 1, Section 4.11). If the GC worker is not implemented in Phase 1, orphaned objects accumulate and are addressed in Phase 2. Object store lifecycle policies are **not** used as an automated safety net, as TAMS is the authoritative controller of object lifecycle.

> *Decision: Reference-count-based object GC managed by OpenTAMS; S3 lifecycle policies not used. Rationale: TAMS allows multiple segments across different flows to reference the same media object (copy-on-write). S3 lifecycle policies operate on object age or prefix — they cannot know whether an object is still referenced by another flow. Deleting a referenced object would cause silent data corruption. OpenTAMS must be the sole authority on object lifecycle. Rejected: S3 lifecycle policies (cannot account for cross-flow references); client-managed deletion (no guarantee of cleanup on failure). Tradeoff: OpenTAMS must reliably decrement ref counts and run GC — orphan accumulation is the failure mode if GC is deferred.*

### 7.7 Circuit Breaker

[REQ-REL-15] All calls to external dependencies (metadata store, object store) propagate a Go `context` with a configurable per-call timeout. This prevents unbounded waits on dependency failures and ensures the server remains responsive under degraded conditions. Phase 1 implementation.

[REQ-REL-16] Latency metrics on all external dependency calls provide passive monitoring for dependency degradation. Operators can observe elevated latency and error rates before cascading failures occur. Phase 1 implementation.

[REQ-REL-17] Active circuit breaking (e.g., wrapping DB and object store calls with a circuit breaker library such as sony/gobreaker) is deferred to Phase 2. Phase 1 relies on context timeouts (REQ-REL-15) and metrics-driven alerting (Section 9.6).

---

## 8. Performance

### 8.1 Baseline Targets

All targets are guidelines validated via load testing against a reference configuration. They are not SLAs. Actual performance varies with deployment resources and configuration.

| Metric | Target | Notes |
|--------|--------|-------|
| Segment read (GET) p99 latency | < 50ms per page | Measured at the server, excluding network |
| Segment write (single POST) p99 latency | < 200ms | Guideline |
| Segment write (bulk POST) throughput | ≥ 120 segments/second | Guideline |
| Time-limited URL generation p99 latency | < 20ms | Upload and download paths |
| Flow creation (PUT) p99 latency | < 100ms | Guideline |
| Sustained API throughput | ~ 700 calls/sec | Validated against reference test configuration |

[REQ-PERF-01] The system sustains approximately 700 API calls/sec with the above latency targets. Horizontal scaling via multiple stateless instances is supported for higher throughput.

### 8.2 Architectural Decisions for Performance

[REQ-PERF-02] The metadata store must support efficient range-overlap queries on segment timeranges, indexed by flow ID. This is the read hot path.

[REQ-PERF-03] Time-limited URL generation is a local operation where possible (no network round-trips per URL).

[REQ-PERF-04] Bulk segment registration uses efficient batch operations in the metadata store, not individual inserts in a loop.

[REQ-PERF-05] Metadata store and HTTP connections are pooled and reused. Pool sizes are configurable with explicit minimum and maximum connection bounds. When the connection pool is exhausted, new requests fail immediately rather than queuing indefinitely. Pool exhaustion is tracked via metrics (utilization percentage, acquire timeout count). Requests that fail due to pool exhaustion return 503 with a jittered `Retry-After` value.

> *Decision: Connection pool exhaustion returns 503 immediately rather than queuing requests. Rationale: queuing under exhaustion hides the problem, increases tail latency unpredictably, and can cause cascading timeouts as queued requests expire. Failing fast with 503 + Retry-After lets clients back off and gives operators a clear signal via metrics. Rejected: unbounded queue (hides exhaustion, unpredictable latency); bounded queue (still masks the signal, adds complexity). Tradeoff: clients see 503 under load spikes — but with jittered Retry-After they recover gracefully.*

### 8.3 Pagination

[REQ-PERF-06] All list endpoints support cursor-based pagination per the TAMS v8.0 spec: `page` (opaque cursor) and `limit` query parameters. Response headers: `Link` (rel=next), `X-Paging-Limit`, `X-Paging-NextKey`. Segment GET additionally: `X-Paging-Timerange`, `X-Paging-Count`, `X-Paging-Reverse-Order`.

> *Decision: Cursor-based (opaque) pagination for all list endpoints. Rationale: offset-based pagination is unreliable under concurrent writes — a segment inserted between pages causes rows to shift, producing duplicates or gaps in results. Cursor pagination is stable regardless of concurrent mutations. The cursor is opaque so the backend can encode any sort strategy without changing the API contract. Rejected: offset/limit (unstable under writes); keyset with exposed columns (leaks internal sort implementation). Tradeoff: clients cannot jump to an arbitrary page — they must walk pages sequentially.*

[REQ-PERF-07] Default page size: 100 items. Maximum page size: 1000 items. These are spec constants and apply to all list endpoints (sources, flows, segments). Values above 1000 are clamped — the server returns up to 1000 items and reflects the effective limit in `X-Paging-Limit`.

### 8.4 Load Testing

[REQ-PERF-08] The repository includes load testing scripts and a reference test configuration. Performance targets are documented alongside the reference configuration. Operators can run the same tests against their deployment to establish baselines.

---

## 9. Observability & Operations

### 9.1 Structured Logging

[REQ-OBS-01] Every API request produces a structured JSON log entry at INFO level. Log entries use a dimensional schema:

**Stable dimensions (present on every entry):**
- `timestamp` — RFC 3339 with microsecond precision
- `level` — INFO, WARN, ERROR, DEBUG
- `domain` — resource area: service, flows, sources, segments, storage, auth, health, system
- `action` — operation type: create, read, update, delete, register, allocate, validate, check
- `outcome` — result: success, failure, partial_failure, rejected
- `request_id` — UUID, correlates all log entries for a single request

**Contextual fields (present when applicable):**
- `method`, `path`, `status`, `duration_ms` — HTTP request metadata
- `flow_id`, `source_id`, `object_id` — resource identifiers
- `auth_subject`, `client_ip` — caller identity
- `segments_count`, `segments_failed` — bulk operation metadata
- `error`, `error_detail`, `cause` — error information and causal chain
- `batch_timerange` — span of a bulk segment registration
- `idempotency_key` — the client-provided `X-Idempotency-Key` header value on `POST /segments` requests; present when the header is supplied

**Log levels:**
- **INFO:** access log. One entry per request. Always emitted.
- **WARN:** notable client-side or data quality issues. Partial failures in bulk POST. Rate limiting activation. Deprecated field usage.
- **ERROR:** server-side failures. Metadata store unreachable. Object store errors. Unhandled exceptions.
- **DEBUG:** internal state for development. Off by default in production.

[REQ-OBS-02] Log level is configurable at runtime without server restart.

[REQ-OBS-03] For bulk POST partial failures: per-failed-segment WARN entries are emitted, correlated to the parent request via `request_id`, containing the segment's `object_id`, `timerange`, and specific failure reason.

### 9.2 Request ID

[REQ-OBS-04] The server uses a client-provided `X-Request-ID` header if present, otherwise generates a UUID. The request ID is included in the response via `X-Request-ID` header, in all log entries, and in error response bodies. The request ID is propagated to all downstream calls (metadata store query logs, object store operation logs) for end-to-end correlation within a single server instance. Distributed tracing across services is deferred to Phase 2 via OpenTelemetry.

### 9.3 Metrics

[REQ-OBS-05] The server exposes operational metrics in a standard format covering:
- Request throughput and latency by endpoint and status
- Metadata store query latency and connection pool utilization
- Object store operation latency (URL generation, health checks)
- Health check results per dependency
- Rate limiter rejections by category
- Active in-flight requests
- Response body size distribution
- Connection pool exhaustion events and acquire timeout counts

[REQ-OBS-06] Metric labels use bounded cardinality: route patterns (not actual request paths), status code classes where appropriate.

### 9.4 Dashboard

[REQ-OBS-07] A system health dashboard definition is provided covering:
- API health: request rate, error rate (4xx, 5xx), latency percentiles (p50, p95, p99)
- Metadata store health: query latency, connection pool usage, errors
- Object store health: URL generation success/failure, operation errors
- Health blip tracking: count and duration of readiness failures
- Rate limiter activity: rejection rate by category
- Connection pool exhaustion: utilization and acquire timeout rate

### 9.5 Runbooks

[REQ-OBS-08] Four model runbooks ship with the repository, each following the format: Symptoms → Dashboard View → Investigation Steps → Recovery Verification.

1. **Metadata store unreachable:** readiness probe failing, all operations returning errors.
2. **Object store unreachable:** storage allocation failing, media downloads failing.
3. **Server instances crashing:** restart loops, resource exhaustion, migration failures.
4. **User-reported failure triage:** a client reports a failed API call. Trace the `request_id` from the error response through structured logs to root cause. Covers: 4xx validation errors, partial-failure 200 responses on `POST /segments`, and 5xx application errors.

These are marked as drafts to be validated after first production deployment.

### 9.6 Alerting Rules

[REQ-OBS-09] The repository includes a starter set of alerting rule definitions. Rules are organized by operator persona:

- **DevOps / Platform:** 5xx error rate exceeds threshold; readiness probe failure duration exceeds threshold; database connection pool exhaustion; server memory/CPU utilization.
- **Operator:** storage allocation failure rate; GC worker error rate (if Phase 1 GC implemented); object store error rate.
- **Developer / L2 Support:** rate limiter rejection rate spike; unexpected 4xx error pattern.

Alert definitions are provided as a starting point and are expected to be extended based on operational experience and observed usage patterns.

### 9.7 Log Volume

[REQ-OBS-10] At target throughput (~700 calls/sec), the server generates approximately 30GB/day of log data at INFO level. This is documented so operators can plan log storage. Log level can be raised to WARN to reduce volume.

---

## 10. Rate Limiting and Idempotency

> **Status: rate limiting is not implemented.** REQ-RATE-01 through REQ-RATE-08 below
> describe an intended design that no released version of OpenTAMS provides. The server
> has no rate-limiting middleware, emits no 429, and sets no `Retry-After` header.
> `SERVER_RATE_LIMIT_RPS` and `SERVER_RATE_LIMIT_BURST` are parsed at startup and then
> ignored. Deploy a rate limiter in front of OpenTAMS
> (ingress controller, API gateway, or reverse proxy) if you need one. The requirements
> are kept here because they are the starting point for the eventual design — they will
> be revisited as an ADR before any implementation lands.
>
> REQ-RATE-09 and the key-lifecycle rules that follow it cover **idempotency**, which
> *is* implemented. They are filed in this section for historical reasons.

[REQ-RATE-01] *(not implemented)* The server supports configurable rate limiting to protect against overload and abuse.

[REQ-RATE-02] *(not implemented)* Three rate limiting layers:
- **Per-identity:** based on authenticated caller identity (API key ID or JWT subject). Primary mechanism.
- **Per-source:** based on request origin (e.g., IP address). Fallback for unauthenticated requests. Supports configurable trusted proxy identification to extract original client identity behind proxies.
- **Global:** maximum aggregate request rate the server accepts, regardless of caller. Protects backend dependencies from overload.

[REQ-RATE-03] *(not implemented)* Rate-limited requests return 429 with a `Retry-After` header.

[REQ-RATE-04] *(not implemented)* Rate limiting is observable via metrics (rejection count by category) and logged at WARN level.

[REQ-RATE-05] *(not implemented)* All thresholds are configurable. Defaults support the documented scale target (~700 calls/sec) without tuning. Per-identity overrides are supported for callers that need higher limits.

[REQ-RATE-06] *(not implemented)* Rate limiting can be disabled for development environments via configuration.

[REQ-RATE-07] *(not implemented)* All 429 and 503 responses include a `Retry-After` header with a jittered value. Jitter prevents thundering herd behaviour where many clients retry simultaneously after a transient overload or dependency failure.

[REQ-RATE-08] *(not implemented)* Client retry guidance is documented in the API reference and README:
- Clients MUST retry only on 429 (rate limited) and 503 (service unavailable) responses.
- Clients MUST honour the `Retry-After` header value before retrying.
- Clients SHOULD apply exponential backoff with jitter for successive retries.
- Clients MUST NOT retry on 4xx errors other than 429.

[REQ-RATE-09] `POST /flows/{flowId}/segments` requires an `X-Idempotency-Key` header. Requests missing this header return 400. The server stores a hash of the request body alongside the key.

**Key lifecycle states** (within the `IDEMPOTENCY_KEY_TTL` window):
- **in-flight**: original request has acquired the key but not yet finished. A concurrent retry with the same key returns 409.
- **cached**: original request has finished with a *deterministic* outcome (see below). A retry with the same key and same body returns the original result without reprocessing. A retry with the same key and a *different* body returns 409.
- **released**: original request has finished with a *transient* outcome. The key is no longer registered; a retry with the same key (any body) is processed as a fresh request.

**Cache policy — deterministic vs transient outcomes:**
- **Deterministic outcome (cached for replay):** any HTTP status `< 500` produced by a catalogued business-rule failure or success. Examples: 201 success, 200 partial success (`flow-segment-bulk-failure`), 422 segment-overlap, 403 read-only flow, 404 flow-not-found, 400 schema-validation, 400 invalid-timerange. The same input would produce the same outcome on retry, so caching is correct.
- **Transient outcome (released, not cached):** any HTTP status `≥ 500`, any uncatalogued internal error, or any failure from a downstream dependency (database, object store, auth provider). The underlying cause may resolve before the retry; caching would defeat the retry's purpose by locking the client out of the correct behaviour for the full TTL window. The server therefore deletes the in-flight key on these paths so the client's retry succeeds once the issue clears.

**Crash-safety backstop — stale in-flight reaper:** if a process crash, panic, or kernel-level kill prevents the server from registering either outcome (cached or released), a background reaper deletes any in-flight key whose acquisition is older than `IDEMPOTENCY_STALE_THRESHOLD`. The threshold is enforced at startup to be `>= http.Server.WriteTimeout` (default 60s) — reaping any faster could race the handler's own finalisation and silently drop legitimate work. The reaper runs every `IDEMPOTENCY_REAPER_INTERVAL` (default 1m) with ±25% jitter to avoid thundering-herd DELETE storms across HA replicas. Worst-case client lockout after a hard crash is therefore `~ STALE_THRESHOLD + REAPER_INTERVAL` (≈2 minutes with defaults), down from `IDEMPOTENCY_KEY_TTL` (1h). After the reap, retries with the same key are processed as a fresh request.

Operators should treat sustained 409 rates on an otherwise-healthy `POST /segments` as an indicator of crash-loop or resource-exhaustion in the server fleet — the reaper handles the steady-state recovery, but a process that crashes faster than the reaper interval will keep producing fresh in-flight rows.

This makes client retries safe under the retry guidance above and eliminates duplicate segment registrations from retry storms, while keeping the transient-infrastructure-failure recovery window bounded to single-digit minutes even under crash conditions.

> *Decision: `X-Idempotency-Key` required (not optional) on all `POST /flows/{flowId}/segments` requests. Rationale: segments is the only high-volume mutating endpoint where retry storms cause data integrity issues (overlapping timeranges). Making the key required ensures all clients implement safe retry from day one — an optional key results in most clients omitting it and discovering the problem under production load. Rejected: optional key (most clients won't implement it); no idempotency (retry storms cause duplicates). Tradeoff: all clients must generate and track idempotency keys per logical write operation.*

> *Decision: deterministic outcomes are cached, transient outcomes are released. Rationale: a naive "cache every response" policy turns a single transient 5xx into a 24-hour client lockout — every retry replays the cached 5xx instead of seeing a (now-recovered) 200. A naive "cache only 2xx" policy reprocesses 422 overlap errors on every retry, which both wastes work and risks producing a different outcome if concurrent requests mutate adjacent state in between. Splitting on the catalogued AppError status (`< 500` deterministic, `≥ 500` transient) lets the cache do its job for stable client errors while keeping retries effective for infrastructure flakes. Rejected: cache everything (5xx lockout); cache nothing (defeats idempotency); cache by HTTP method or path (orthogonal to the actual deterministic-vs-transient distinction).*

> *Decision: a background reaper deletes orphaned in-flight rows after `IDEMPOTENCY_STALE_THRESHOLD`, with the threshold clamped at startup to `>= http.Server.WriteTimeout`. Rationale: the explicit Complete/Release plus the handler's deferred Release safety net cover any path where the Go runtime gets to run a deferred function. They do **not** cover hard process death — OOM-kill, kernel panic, `SIGKILL` — where the row is left in_flight with no goroutine to ever finalise it. Without recovery, the next retry with the same key collides on the in-flight row and gets a 409 until `IDEMPOTENCY_KEY_TTL` expiry. The reaper closes that gap. The startup clamp is the critical safety property: a threshold smaller than `WriteTimeout` could race the reaper against a still-running handler, silently dropping the row before `Complete` runs and turning a successful registration into a "ghost" in_flight that the client perceives as a 409. Clamping (rather than failing startup) keeps the server bootable on misconfiguration while loudly logging the override. Rejected: (a) "no reaper, rely on TTL" — turns every hard crash into a TTL-long client lockout; (b) "reap inside Acquire on every call" — adds a second DELETE to the request hot path for recovery work that should be amortised across many requests; (c) "fail startup if `STALE_THRESHOLD < WriteTimeout`" — punishes operators who set the threshold conservatively low without realising the WriteTimeout is its floor; the warning + auto-clamp surfaces the misconfiguration without a deploy-time outage; (d) "leader election" — unnecessary because `DELETE WHERE in_flight = true AND acquired_at <= now() - threshold` is atomic and idempotent across replicas; jitter handles thundering-herd. Tradeoff: a client retry within `[crash, crash + STALE_THRESHOLD + REAPER_INTERVAL]` still sees a 409, but the worst case (~2 min with defaults) is bounded by operationally tunable knobs rather than the cache TTL.*

> *Decision: `IDEMPOTENCY_KEY_TTL` default lowered from 24h to 1h. Rationale: the original 24h default was sized as a "client retries within a day" allowance, but the reaper now bounds the only correctness-relevant window (in-flight orphan recovery) to ~2 minutes. The remaining role of TTL is purely how long a *cached* completed response is kept warm for replay. Real-world client retry storms (network partitions, dependency outages) resolve in seconds-to-minutes, not hours; a 1h cache window covers all realistic retry budgets while reducing the live row count from ~8.6M (24h × 100 req/s) to ~360K. Rejected: 24h (excessive storage for the actual retry profile); 5min (too short for clients with exponential backoff that may legitimately retry minutes later). Tradeoff: a client that delays a retry past 1h will reprocess instead of replaying. Acceptable — request bodies for `POST /segments` are small and reprocessing is correct (the second attempt is ordering-equivalent to a fresh request).*

---

## 11. Configuration

[REQ-CFG-01] All operational parameters are configurable via environment variables. Phase 1 does not support configuration files. 12-factor app principle.

### 11.0 Quick Start Configuration

**Local development (minimum required):**

```bash
APP_ENV=development          # Disables auth — never use in production
DB_HOST=localhost
DB_NAME=opentams
DB_USER=opentams
DB_PASSWORD=local-dev-only-change-me
OBJECT_STORE_BUCKET=opentams
OBJECT_STORE_REGION=us-east-1
OBJECT_STORE_ENDPOINT=http://localhost:9000   # Required for MinIO
OBJECT_STORE_ACCESS_KEY_ID=local-dev-only-access-key
OBJECT_STORE_SECRET_ACCESS_KEY=local-dev-only-secret-key
```

**Production (minimum required):**

```bash
DB_HOST=<your-db-host>
DB_NAME=opentams
DB_USER=opentams
DB_PASSWORD=<secret>
# DB_SSLMODE defaults to "require" in production. Set verify-full when the
# metadata store presents a CA-issued certificate you wish to validate.
OBJECT_STORE_BUCKET=<your-bucket>
OBJECT_STORE_REGION=<your-region>
# OBJECT_STORE_ENDPOINT — omit for AWS S3; required for MinIO/GCS/R2/etc.
AUTH_EXTERNAL_ISSUER_URL=https://<your-oidc-provider>
AUTH_EXTERNAL_AUDIENCE=<your-audience>
# AUTH_INTERNAL_* is optional. Set both together when service-to-service
# callers authenticate via a separate IdP (e.g. Kubernetes ServiceAccount
# tokens, an M2M issuer). Omit on plain Docker / VM deployments where
# service callers use the external issuer with a distinct audience.
# AUTH_INTERNAL_ISSUER_URL=https://<kubernetes-api-server-or-m2m-idp>
# AUTH_INTERNAL_AUDIENCE=<internal-audience>
```

All other parameters have defaults suitable for production. See the full reference in Section 11.1.

[REQ-CFG-02] The complete configuration reference is below. Every parameter is documented with: name, type, default, required/optional, description, and whether a restart is required to take effect.

[REQ-CFG-03] The server validates all configuration at startup and fails fast with a clear error message identifying the specific invalid parameter and the validation rule that failed.

[REQ-CFG-04] All parameters require a server restart to take effect except `LOG_LEVEL`, which is configurable at runtime without restart.

### 11.1 Configuration Reference

All parameters apply to `opentams serve` unless marked **gc-only** (applies to `opentams gc` only) or **both** (applies to both).

| Parameter | Type | Default | Required | Restart? | Applies to | Description |
|-----------|------|---------|----------|----------|------------|-------------|
| `SERVER_PORT` | integer | `8080` | no | yes | serve | Port the HTTP server listens on. |
| `SERVER_TLS_CERT_FILE` | string (path) | — | no | yes | serve | Path to TLS certificate file. When set together with `SERVER_TLS_KEY_FILE`, the server serves HTTPS instead of HTTP. For standalone Docker deployments without an ingress layer. |
| `SERVER_TLS_KEY_FILE` | string (path) | — | no | yes | serve | Path to TLS private key file. Required if `SERVER_TLS_CERT_FILE` is set. |
| `SERVER_GRACEFUL_SHUTDOWN_PERIOD` | duration | `30s` | no | yes | serve | Time allowed for in-flight requests to complete on shutdown. |
| `SERVER_RATE_LIMIT_RPS` | integer | `1000` | no | yes | serve | Intended global rate limit in requests per second. Parsed and ignored — see §10. |
| `SERVER_RATE_LIMIT_BURST` | integer | `100` | no | yes | serve | Intended burst allowance above the RPS limit. Parsed and ignored — see §10. |
| `DB_HOST` | string | — | **yes** | yes | both | Metadata store hostname. |
| `DB_PORT` | integer | `5432` | no | yes | both | Metadata store port. |
| `DB_NAME` | string | — | **yes** | yes | both | Metadata store database name. |
| `DB_USER` | string | — | **yes** | yes | both | Metadata store username. |
| `DB_PASSWORD` | string | — | **yes** | yes | both | Metadata store password. |
| `DB_SSLMODE` | enum | `require` (production), `prefer` (development) | no | yes | both | libpq sslmode for the metadata-store connection. Valid values: `disable`, `allow`, `prefer`, `require`, `verify-ca`, `verify-full`. Production default is `require` (fail closed on plaintext PostgreSQL) — set to `verify-full` to additionally validate the server certificate against a CA. Development default is `prefer` so a local Postgres without TLS works out of the box. |
| `DB_POOL_MIN` | integer | `2` | no | yes | both | Minimum metadata store connections in pool. |
| `DB_POOL_MAX` | integer | `10` | no | yes | both | Maximum metadata store connections in pool. |
| `OBJECT_STORE_BUCKET` | string | — | **yes** | yes | serve | Object store bucket name. |
| `OBJECT_STORE_REGION` | string | — | **yes** | yes | serve | Object store region (e.g. `us-east-1`). |
| `OBJECT_STORE_ENDPOINT` | string | — | no* | yes | serve | Custom object store endpoint URL. **Required for non-AWS S3-compatible providers** (e.g. MinIO, GCS, Cloudflare R2). Omit when using AWS S3 directly. |
| `OBJECT_STORE_ACCESS_KEY_ID` | string | — | no | yes | serve | Object store access key. If absent, Pod Identity (IRSA/Workload Identity) is used. |
| `OBJECT_STORE_SECRET_ACCESS_KEY` | string | — | no | yes | serve | Object store secret key. Required if `OBJECT_STORE_ACCESS_KEY_ID` is set. |
| `OBJECT_STORE_PRESIGN_EXPIRY` | duration | `1h` | no | yes | serve | Expiry duration for presigned upload and download URLs. |
| `AUTH_EXTERNAL_ISSUER_URL` | string (URL) | — | **yes** | yes | serve | OIDC issuer URL for external client JWTs. Used to fetch JWKS for token validation. Required in production. Optional when `APP_ENV=development` (auth disabled). |
| `AUTH_EXTERNAL_AUDIENCE` | string | — | **yes** | yes | serve | Expected audience claim for external client JWTs. Required in production. Optional when `APP_ENV=development`. |
| `AUTH_INTERNAL_ISSUER_URL` | string (URL) | — | no | yes | serve | OIDC issuer URL for service-to-service JWTs from a separate IdP (e.g. Kubernetes API server, an M2M issuer). Optional — omit on plain Docker / VM topologies where service callers authenticate via the external issuer. Both-or-neither: if set, `AUTH_INTERNAL_AUDIENCE` must also be set, and vice versa. |
| `AUTH_INTERNAL_AUDIENCE` | string | — | no | yes | serve | Expected audience claim for service-to-service JWTs. Optional — must be set together with `AUTH_INTERNAL_ISSUER_URL`. |
| `AUTH_JWKS_TTL` | duration | `15m` | no | yes | serve | Cache TTL for JWKS key sets fetched from issuer endpoints. |
| `IDEMPOTENCY_KEY_TTL` | duration | `1h` | no | yes | serve | How long completed (cached) idempotency keys are retained. `POST /segments` retries within this window with the same key+body return the original result without reprocessing. Must be greater than `IDEMPOTENCY_STALE_THRESHOLD`. |
| `IDEMPOTENCY_STALE_THRESHOLD` | duration | `60s` | no | yes | serve | How long an in-flight key may exist before the reaper considers its owning request orphaned (server crash before Complete/Release). Clamped at startup to `>= http.Server.WriteTimeout` (60s in MVP) — a value smaller than WriteTimeout is silently raised and a warning logged, because reaping faster could race a genuinely-running request. |
| `IDEMPOTENCY_REAPER_INTERVAL` | duration | `1m` | no | yes | serve | Period of the background goroutine that runs both the TTL Prune (cached rows past `IDEMPOTENCY_KEY_TTL`) and the orphan ReapStale (in-flight rows past `IDEMPOTENCY_STALE_THRESHOLD`). Each tick adds ±25% jitter to avoid thundering-herd DELETE storms across HA replicas. |
| `APP_ENV` | enum | `production` | no | yes | both | Runtime environment. Valid values: `production`, `development`. Development mode disables auth. |
| `LOG_LEVEL` | enum | `info` | no | **no** | both | Log verbosity. Valid values: `debug`, `info`, `warn`, `error`. Updated at runtime by sending `SIGHUP` to the server process — the process re-reads `LOG_LEVEL` from the environment without restart. |
| `GC_POLL_INTERVAL` | duration | `5m` | no | yes | gc-only | How long GC worker sleeps between sweep runs. Conditional Phase 1. |
| `GC_BATCH_SIZE` | integer | `100` | no | yes | gc-only | Maximum segments processed per GC sweep. Conditional Phase 1. |

---

## 12. Testing

### 12.1 Spec Conformance

[REQ-TEST-01] The test suite includes conformance tests that execute TAMS v8.0 example API usage patterns against OpenTAMS and verify correct responses. These tests run in CI.

### 12.2 Coverage

[REQ-TEST-02] 100% statement coverage on all application modules, measured by the implementation language's standard coverage tool. Enforced in CI.

### 12.3 Test Types

| Type | What It Tests | Runs Against |
|------|--------------|--------------|
| Unit | Individual functions, dependencies mocked via interfaces | In-memory mocks |
| Integration | Each interface implementation against real backends | Containerized metadata store + object store |
| End-to-end | Complete write-read cycle: create flow → allocate storage → upload media → register segments → query segments → download → verify bytes match | Full stack |
| Load | Performance targets against reference configuration | Full stack |

[REQ-TEST-03] The timerange domain module (parsing, overlap detection, arithmetic) is exhaustively tested with table-driven tests covering all bound combinations, edge cases (instantaneous timeranges, eternity, never), and negative timestamps.

### 12.4 CI Pipeline

[REQ-TEST-04] Every push triggers: linting, all unit tests with race detection, all integration tests, end-to-end tests, container image build, coverage enforcement. No merge if any step fails.

### 12.5 Multi-Backend Validation

[REQ-TEST-05] Basic functionality is verified against at least one object store beyond the development default (e.g., a second S3-compatible provider). This may be a documented manual test for Phase 1, automated in Phase 2.

### 12.6 Migration Rollback Testing

[REQ-TEST-06] Every release that includes a schema migration must include a test that exercises the down migration (rollback) path and verifies idempotency. This test runs in CI before any production deployment involving a migration.

---

## 13. Deployment & Developer Experience

### 13.1 Container Image

[REQ-DEV-01] Multi-stage build producing a minimal container image. Non-root user. No shell. Multi-architecture support.

### 13.2 Local Development

[REQ-DEV-02] A single command starts the complete development environment: metadata store, object store, and server. Produces a working system. No manual steps, no separate migration command, no credential configuration.

[REQ-DEV-03] A developer with a container runtime installed can go from cloning the repository to a working OpenTAMS instance with a single command. The quickstart documentation guides them through a complete write-read cycle (create flow, upload media, query segments, download media, verify bytes match).

### 13.3 Production Deployment

[REQ-DEV-04] Container orchestration manifests (e.g., Kubernetes) are provided for deployment, service, configuration, and secrets.

[REQ-DEV-05] Migration runs as a separate step before server deployment. In container orchestration, this is a pre-deployment job. If migration fails, the new version does not roll out. If migration needs to be reversed, the `--down` migration mode is used (REQ-ARCH-16).

[REQ-DEV-05a] **Schema startup probe.** Immediately after opening the metadata-store connection pool and before serving any traffic, the server queries `schema_migrations` and validates the result against the schema version this binary requires:

| Observed state | Server behaviour | Operator recovery |
|---|---|---|
| `schema_migrations` table missing (SQLSTATE 42P01) | Refuse to start; structured-error log naming the missing table | Run pending migrations |
| `schema_migrations` exists but empty | Refuse to start; structured-error log naming the empty table | Run pending migrations |
| `dirty = true` | Refuse to start; structured-error log including the dirty version and `migrate force <version>` hint | Reconcile the partial migration manually, then `migrate force <version>` |
| `version < expected` | Refuse to start; structured-error log naming both versions | Run pending migrations |
| `version == expected` | Start; info log `schema validated` | — |
| `version > expected` | Start; info log naming both versions (older binary on newer schema is supported under the expand-contract rule) | — |

The probe is startup-only — it does not run on every request and is not part of `/readyz` or `/health/details`, since the schema cannot change without a server restart. The "expected" version is a property of the binary, bumped each time a migration is added whose absence would break a request path that binary serves; this preserves expand-contract for forward compatibility (newer DB, older binary) while catching the more common deployment mistake (binary upgraded, migration forgotten) loudly at boot.

> **v1.2 decision.** *Why fail fast at startup rather than lazy-checking on first request?* A server that boots green, passes `/readyz`, then fails the first POST `/segments` with an opaque foreign-key error is the worst possible operator experience: it survives the deploy gate (`/readyz` answers 200), telemetry shows the binary is up, but the system is broken in a way that's only surfaced by a real user request. Failing fast at boot keeps the failure adjacent to the deploy that introduced it. *Why a minimum-version check rather than exact match?* Exact match would block all rolling upgrades and canaries; expand-contract requires that an older binary keeps working against a newer schema. *Why not embed `migrations/` and auto-derive the expected version?* Considered and rejected — embedding ships SQL inside the binary unnecessarily and removes the single deliberate moment ("does my new column break the previous binary?") that catches expand-contract violations during code review.

---

## 14. Open-Source & Community

### 14.1 Repository Quality

[REQ-OSS-01] The repository includes at minimum:

| File | Content |
|------|---------|
| README.md | One-liner, badges, architecture overview, quickstart, features, configuration summary, links |
| LICENSE | Apache 2.0 full text |
| CONTRIBUTING.md | One-command dev setup, commit conventions, PR process, review expectations |
| CODE_OF_CONDUCT.md | Contributor Covenant v2.1 |
| SECURITY.md | Vulnerability disclosure process, contact, expected response time |
| CHANGELOG.md | v0.1.0 entry |
| docs/architecture/ | High-level system design, storage model, API flows |
| docs/development/codebase.md | Component model, interface boundaries, code organization |
| Issue templates | Bug report, feature request |
| PR template | Checklist: tests pass, coverage maintained, docs updated |

### 14.2 Documentation

[REQ-OSS-02] Documentation serves four audiences:

| Audience | Time | Needs |
|----------|------|-------|
| Scanner | 10 seconds | One-liner + architecture overview. Decides relevance. |
| Evaluator | 5 minutes | Quickstart. Runs locally. Hits API. Must work flawlessly. |
| Operator | 30 minutes | Deploys to real environment. Configures auth, storage. Runs health checks. |
| Contributor | 30 minutes | Clones, runs dev setup, makes a change, runs tests, submits PR. |

[REQ-OSS-03] TAMS concept documentation links to BBC's official docs. OpenTAMS documentation focuses on: conformance status (which TAMS features are implemented), deployment, configuration, and extension.

[REQ-OSS-04] A TAMS v8.0 conformance page maps every spec feature to its implementation status in OpenTAMS (implemented, conditional Phase 1, deferred, not applicable) with rationale for deferrals.

### 14.3 API Version Management

[REQ-OSS-05] OpenTAMS versioning is independent of TAMS spec versioning. The implemented TAMS spec version is tracked explicitly in the codebase and reported via the API.

[REQ-OSS-06] TAMS spec version upgrades (e.g., v8.0 → v9.0) constitute a major OpenTAMS version change with a documented upgrade path.

[REQ-OSS-07] OpenTAMS follows semantic versioning. Within a major OpenTAMS version: the TAMS spec version does not change, the API contract is stable, schema migrations are backward-compatible, and configuration changes are backward-compatible (new parameters with defaults, no removals).

### 14.4 Extensibility

[REQ-OSS-08] The codebase is structured for extensibility: adding a new storage backend means implementing the object store interface. Adding a new metadata store means implementing the metadata store interface. Adding a new auth provider means implementing the auth provider interface. No core logic changes required.

[REQ-OSS-09] The test suite serves as behavioral documentation. Tests should be readable as specification: a contributor unfamiliar with TAMS should understand the expected behavior by reading the tests.

### 14.5 Community Readiness

[REQ-OSS-10] At least 3 good-first-issues are open at launch, each self-contained with clear acceptance criteria and pointers to relevant code.

[REQ-OSS-11] **Deferred to Phase 2.** A client library in the server's implementation language will be provided in Phase 2 with: typed models for all TAMS entities, methods for all implemented endpoints, authentication handling, and pagination support. Phase 1 clients integrate directly via HTTP using the OpenAPI spec and sample programs (REQ-OSS-12).

[REQ-OSS-12] Sample programs demonstrating a complete write workflow (create flow → upload segments → register) and a complete read workflow (query segments → download → verify) are included.

---

## 15. Usability

### 15.1 HTTP API

[REQ-USE-01] The core write workflow (PUT flow → POST storage → upload to presigned URL → POST segments) and core read workflow (GET segments → download from presigned URL) must be explicitly documented in the API reference and quickstart, with each step's purpose explained and the consequence of skipping or reordering steps.

[REQ-USE-02] The API reference must document each error code with: what triggered it, why the rule exists, and how to resolve it. This corrective guidance lives in documentation — not in the wire format of error responses.

[REQ-USE-07] The `detail` field in 4xx error responses must be specific to the occurrence, naming the offending input where applicable (e.g. `"'frame_rate' is required when 'vfr' is false"`, not `"validation error"`). Programmatic error handling uses `type` per RFC 9457; `detail` exists for human diagnosis from the response alone without requiring access to server logs or the error catalogue.

> *Decision: wire-level `detail` specificity (not a structured `errors` extension). Rationale: RFC 9457 §3.1 specifies `detail` as "a human-readable explanation specific to this occurrence." A structured `errors` extension array (RFC 9457 §3.2) was considered for multi-field validation but rejected for Phase 1 — well-written `detail` strings are sufficient and the existing error catalogue covers most cases with single-cause `type` URIs. Rejected: generic `detail` strings ("validation error") that force developers to read logs; structured `errors` extension (over-engineering for Phase 1). Tradeoff: multi-field validation errors must be expressed as readable prose within `detail`.*

### 15.2 CLI

[REQ-USE-03] `--help` on each subcommand (`serve`, `gc`, `migrate`) must describe the subcommand's role in the server lifecycle — not just its flags. A new operator must be able to answer "when do I run this and what does it affect?" from help text alone.

[REQ-USE-04] On startup, `serve` and `gc` emit a structured log entry listing effective configuration. Secret values (`DB_PASSWORD`, `OBJECT_STORE_SECRET_ACCESS_KEY`, `OBJECT_STORE_ACCESS_KEY_ID`) are shown as `[redacted]`. Flags take precedence over environment variables; this precedence is documented in `--help`.

[REQ-USE-05] Every non-zero exit emits a structured log line containing: what failed, whether it is safe to retry, and what the operator should check next.

[REQ-USE-06] Each `gc` sweep emits a structured summary log line on completion containing: sweep start time, duration, segments purged, segments failed, and exit reason.

---

## 16. User Personas

These personas guide design decisions and documentation priorities. They describe generic TAMS API consumers, not application-specific roles.

### 16.1 Media Writer
Any system that creates flows and registers segments. Needs: fast storage allocation, reliable bulk segment registration with partial failure handling, clear error responses, safe retry behaviour via idempotency keys.

### 16.2 Media Reader
Any system that queries segments and retrieves media. Needs: fast segment queries, reliable time-limited download URLs, correct timerange query behavior, pagination support.

### 16.3 Media Manager
Any system that manages flow lifecycle, metadata, and retention. Needs: flow creation, flow deletion, segment deletion by timerange, metadata updates, tag management, edit-by-reference via copy-on-write.

### 16.4 DevOps / Platform Team
Deploys and operates OpenTAMS. Needs: container image, orchestration manifests, metrics endpoint, health probes, structured logs, dashboard, alerting rules, configuration documentation, migration tooling with rollback support, runbooks.

### 16.5 L2 Support
Diagnoses production incidents. Needs: system health dashboard to diagnose within 30 seconds, structured logs searchable by flow_id/request_id, health blip tracking with configurable threshold.

### 16.6 Security / Compliance
Reviews access patterns and audits system usage. Needs: structured logs with `auth_subject` for access auditing. Phase 2: RBAC and dedicated audit log.

### 16.7 External Developer
Discovers OpenTAMS, evaluates it, builds integrations. Needs: conformance page, quickstart, sample programs, client library, API reference (TAMS v8.0 spec).

### 16.8 Open-Source Contributor
Forks, understands codebase, submits PRs. Needs: docs/development/codebase.md, CONTRIBUTING.md, clean interface boundaries, comprehensive tests, good-first-issues.

---

## 17. Acceptance Criteria

Phase 1 is complete when ALL of the following are true.

### 17.1 Primary Workflow

1. Local dev environment starts all services with a single command.
2. `PUT /flows/{id}` with a new `source_id` creates the Flow (201) and implicitly creates the Source with matching format.
3. `POST /flows/{id}/storage` with `limit: 1` returns an upload URL and object_id.
4. Uploading a file to the upload URL succeeds.
5. `POST /flows/{id}/segments` with the object_id and a timerange returns 201.
6. `GET /flows/{id}/segments?timerange=...` returns the segment with a download URL.
7. Downloading from the URL returns the exact bytes uploaded.
8. Bulk `POST /flows/{id}/segments` with an array registers all segments. Partial failure returns 200 with `failed_segments`.

### 17.2 API Compliance

9. All list endpoints support cursor-based pagination with `Link` header.
10. Source and Flow list endpoints support tag filtering (`tag.{name}`, `tag_exists.{name}`).
11. All sub-resource endpoints (tags, label, description, read_only, flow_collection, bitrates) work correctly.
12. `DELETE /flows/{id}` removes the flow and all segment metadata. Unreferenced media objects are deleted from the object store via ref-count GC.
13. `DELETE /flows/{id}/segments?timerange=...` deletes segments in the specified range and decrements ref counts. Objects with zero refs are deleted.
14. Segment POST rejects overlapping timeranges with 422 — both against existing segments and within the same batch.
15. `GET /service` returns API version, storage backend information, and timeout values.
16. `read_only` enforcement: when true, segment writes, segment deletion, storage allocation, flow PUT, metadata updates, and deletion are rejected. `PUT read_only` still works.
17. HEAD is supported on every GET endpoint.
18. Segment GET returns empty list for non-existent flow, not 404.
19. Creating a Flow with format incompatible with its Source returns 400.
20. Flow PUT that changes `source_id` or `format` on an existing Flow returns 400.
21. Flow PUT that changes `codec` on a Flow with segments returns 400.
22. Creating a video Flow with `vfr: false` and no `frame_rate` returns 400.
23. Posting a segment with `get_urls` registers uncontrolled download URLs.
24. Object timerange is tracked — client-provided `object_timerange` is stored and validated on subsequent registrations.
25. All non-2xx responses conform to RFC 9457 structure with `Content-Type: application/problem+json` and `request_id`.
26. Malformed JSON, invalid UUIDs, invalid timerange syntax return 400.
27. Unknown fields in request bodies are accepted and ignored.
28. Copy-on-write: two flows referencing the same object coexist. Deleting one flow does not affect the other's segments or media access.
29. `X-Idempotency-Key` on POST /segments: duplicate request with same key and same body for a *deterministic* outcome (status < 500) returns the original result without reprocessing. Same key with different body returns 409. Same key while original request is still in-flight returns 409. Same key whose original request produced a *transient* outcome (status ≥ 500 or downstream failure) is processed as a fresh request — transient outcomes are released, not cached, so a retry can succeed once the underlying issue resolves.

### 17.3 Auth & Resilience

30. Request with valid external JWT (e.g. Auth0) succeeds.
31. Request without credentials returns 401.
32. When `AUTH_INTERNAL_*` is configured, request with valid internal JWT (e.g. Kubernetes ServiceAccount token, M2M IdP token) succeeds. When unconfigured, the deployment has a single authentication surface — the external issuer — and the internal-issuer claim path is not registered.
32a. Server fails fast at startup if exactly one of `AUTH_INTERNAL_ISSUER_URL` / `AUTH_INTERNAL_AUDIENCE` is set (both-or-neither validation), with an error naming the missing field.
32b. With `DB_SSLMODE=require` (production default), the server refuses to connect to a metadata store that does not offer TLS. With `DB_SSLMODE=verify-full`, the server additionally validates the metadata-store certificate against the system CA bundle.
33. Dev mode (no auth): all requests allowed.
34. `/healthz` returns 200 when process is running.
35. `/readyz` returns 200 when metadata store is reachable, 503 otherwise. Response body is intentionally empty. Per-dependency health breakdown is available via `/health/details`.
36. When object store is unreachable: metadata-only operations succeed, storage allocation returns 503.
37. *(deferred — rate limiting is not implemented; see §10)*
38. 503 responses include jittered `Retry-After`.
39. Server shuts down gracefully on termination signal: in-flight requests complete, then process exits.
40. All external dependency calls respect context timeout propagation.

### 17.4 Quality

41. 100% statement coverage on all application modules, verified in CI.
42. CI pipeline passes: lint, unit tests (with race detection), integration tests, e2e tests, container image build.
43. Migration rollback test passes for every release containing a migration.
44. Spec conformance tests pass.
45. All logs are structured JSON with dimensional schema.
46. Performance targets validated via load test against reference configuration.
47. Configuration validated at startup — invalid config causes clear error and exit.

### 17.5 Operations

48. Metrics endpoint is functional and includes all required metrics including pool exhaustion.
49. Dashboard definition renders all panels.
50. Starter alerting rules included in repository.
51. 4 runbooks included in repository.
52. Health blip tracking works: consecutive failures counted, flagged at threshold, resolved on recovery.
53. Migration step runs successfully. Auto-migrate works for dev mode. `--down` rollback mode works.
53a. Schema startup probe rejects all four bad states with a structured-error log: missing `schema_migrations` table, empty `schema_migrations`, `dirty = true`, and `version < expected`. The error message in each case includes the operator's recovery action.
53b. Schema startup probe accepts `version == expected` (info log `schema validated`) and `version > expected` (info log naming both versions; server starts and serves traffic — older binary on newer schema is supported under expand-contract).
54. Orchestration manifests can deploy the server.

### 17.6 Open-Source & Community

55. Repository is public with Apache 2.0 license.
56. CI green with badge visible in README.
57. Tagged release.
58. All repository files from Section 14.1 are present.
59. Quickstart tested on a clean environment — works end-to-end.
60. At least 3 good-first-issues open.
61. Sample writer and reader programs work against local dev environment.
62. Client library is importable by an external project.
63. TAMS v8.0 conformance page documents all feature statuses including conditional Phase 1 items.

---

## 18. Known Limitations (Phase 1)

These are documented in README and release notes.

1. **Segment deletion and object GC are conditional Phase 1.** `DELETE /flows/{flowId}/segments` and ref-count-based object cleanup are targeted for Phase 1 subject to bandwidth. If not implemented in Phase 1: flow DELETE removes metadata only; unreferenced media objects accumulate; Phase 2 GC becomes mandatory.
2. **No webhooks / event streaming.** Clients must poll for changes.
3. **No MXF container mapping.** MPEG-TS, ISOBMFF, and generic mappings supported.
4. **Single storage backend.** Multiple backends supported architecturally but Phase 1 configures one.
5. **All-or-nothing authorization.** No RBAC or ABAC. All authenticated users have full access.
6. **No API key runtime management.** Keys provisioned via configuration, rotation requires redeployment.
7. **`object_ids` storage mode not performance-tested.** The `limit` mode is the primary and tested path.
8. **No backpressure mechanism.** Server has no mechanism to signal clients to slow ingest. Requires client-side changes; deferred to Phase 2.
9. **No distributed tracing.** Phase 1 uses request ID propagation within a single server process. Cross-service trace correlation via OpenTelemetry is deferred to Phase 2.

---

## 19. Risks

| Risk | Likelihood | Mitigation |
|------|-----------|------------|
| TAMS timerange query and overlap logic more complex than expected | Medium | Domain module with exhaustive table-driven tests. Start with core cases, tighten edge cases post-launch. |
| Time-limited URL behavior differs across object store providers | Medium | Integration tests against target provider. Document tested providers. |
| Scope creep | High | This document is the contract. Deferred items (Section 2.2) stay deferred. |
| AI-generated code has subtle bugs | Medium | 100% test coverage + test-first methodology. All code reviewed by builder. |
| TAMS spec evolves during development | Low | Pinned to v8.0 commit hash. Spec updates evaluated after Phase 1 release. |
| Object store provider behavior differences beyond API | Medium | Multi-backend smoke test (REQ-TEST-05). Document tested providers. |
| Interface design couples to first implementation | Medium | Review interface against at least one alternative backend before finalizing. |
| Orphaned objects accumulate faster than expected | Low | GC worker (conditional Phase 1) is the primary mitigation. If GC deferred, monitor storage growth and prioritize Phase 2 GC. |
| Quickstart doesn't work for new users | Medium | Test on clean environment before launch. |
| 100% test coverage slows velocity | Medium | Modules are small by design. Tests generated alongside implementation. Coverage is quality assurance, not overhead. |
| GC worker not completed in Phase 1 | Medium | Clearly documented as conditional scope. Object accumulation is tolerable short-term; Phase 2 GC is mandatory if deferred. |

---

## 20. Glossary

| Term | Definition |
|------|-----------|
| Source | Abstract content identity. Groups Flows. Created implicitly when a Flow references a source_id. |
| Flow | Immutable timeline of media in a specific format/codec. Belongs to one Source. Created via PUT with client-generated UUID. |
| Flow Segment | Maps a Media Object (or portion) to a position on a Flow's timeline. Timeranges must not overlap on the same Flow. |
| Media Object | Binary media data in object store. Referenced by Segments. Can be shared across Flows (copy-on-write). Deleted when reference count reaches zero. |
| Reference Count | Per-object count of how many segments reference it. Decremented on segment delete. Object deleted from object store when count reaches zero. |
| Time-limited URL | URL with embedded, time-bounded authorization for direct object store upload/download without permanent credentials. |
| Storage Backend | A configured object store instance. A TAMS server can have multiple backends. |
| Controlled Instance | A media object instance on a server-managed storage backend. |
| Uncontrolled Instance | A media object instance at an external URL registered by a client. |
| Copy-on-Write | Creating new segment metadata referencing existing media objects. No media is copied. |
| Metadata Plane | The TAMS server and its metadata store. Manages Sources, Flows, Segments, Object metadata. |
| Media Plane | The object store(s) holding actual media data. Clients interact directly via time-limited URLs. |
| GC Worker | Standalone process (or mode of main binary) responsible for automated cleanup of segments and flows based on retention policies. |
| Idempotency Key | Client-provided `X-Idempotency-Key` header on POST /segments. Ensures duplicate requests return the original result, making retries safe. |
