# OpenTAMS Data Flows

This document describes the main API flows at the system level. It avoids Go package names and focuses on what each deployed component does.

For the exact wire contract, see the bundled OpenAPI document in `[api/opentams-api-bundled.yaml](../../api/opentams-api-bundled.yaml)`. For implementation details, see the [codebase guide](../development/codebase.md).

## Allocate Controlled Storage

```mermaid
sequenceDiagram
    participant Client
    participant API as OpenTAMS API
    participant DB as PostgreSQL
    participant Store as Object store

    Client->>API: POST /tams/v1/flows/{flowId}/storage
    API->>DB: Read flow and storage backend metadata
    DB-->>API: Flow is writable and backend is available
    API->>Store: Create presigned PUT URL(s)
    Store-->>API: URL(s) and expiry
    API-->>Client: object_id plus PUT URL(s)
    Client->>Store: PUT media bytes directly
```



Storage allocation does not make bytes pass through OpenTAMS. The API returns presigned PUT URLs and the client uploads directly to the object store. Object metadata is not finalized by allocation alone; an object becomes tracked when a segment references it.

## Register Segments

```mermaid
sequenceDiagram
    participant Client
    participant API as OpenTAMS API
    participant DB as PostgreSQL

    Client->>API: POST /tams/v1/flows/{flowId}/segments
    API->>API: Validate auth, path params, body, timeranges
    API->>DB: Acquire idempotency key using raw body hash
    alt First request
        API->>DB: Upsert referenced object rows
        API->>DB: Insert segment rows in one transaction
        DB-->>API: Segment registration committed
        API->>DB: Cache stable response for idempotent replay
        alt All submitted segments accepted
            API-->>Client: 201 Created, no response body
        else Partial bulk success
            API-->>Client: 200 OK with failed_segments body
        end
    else Same key and same body already completed
        DB-->>API: Cached response
        API-->>Client: Replay cached status and body
    else Same key is in flight or same key has different body
        API-->>Client: 409 Conflict
    end
```



Segment registration is the point where OpenTAMS records object references. The metadata transaction preserves the relationship between flows, segments, and referenced objects, and the idempotency record makes safe retries possible for clients.

A full-success registration returns `201 Created` with no response body. `200 OK` is reserved for partial bulk success: array input where at least one segment was registered and at least one non-overlap per-segment validation error is returned in the `failed_segments` body. Whole-request failures use error responses such as `400 Bad Request` or `422 Unprocessable Entity`. Idempotency replay returns the original cached status and body.

## List Segments By Time Range

```mermaid
sequenceDiagram
    participant Client
    participant API as OpenTAMS API
    participant DB as PostgreSQL
    participant Store as Object store

    Client->>API: GET /tams/v1/flows/{flowId}/segments?timerange=...
    API->>API: Validate auth and parse timerange
    API->>DB: Query segments overlapping requested range
    DB-->>API: Ordered page of segment metadata
    opt Client requested access URLs
        API->>Store: Create presigned GET URL(s)
        Store-->>API: URL(s) and expiry
    end
    API-->>Client: Segment page plus pagination headers
```



The metadata query uses stored time bounds and keyset pagination. If object access URLs are requested, OpenTAMS signs GET URLs; clients still download media bytes directly from the object store.

## Register BYOS Segments

```mermaid
sequenceDiagram
    participant Client
    participant API as OpenTAMS API
    participant DB as PostgreSQL
    participant ExternalStore as External object store

    Client->>ExternalStore: Write media bytes outside OpenTAMS
    Client->>API: POST /tams/v1/flows/{flowId}/segments with external object reference
    API->>API: Validate auth, body, and timeranges
    API->>DB: Store segment metadata with storage_id null
    DB-->>API: Segment metadata committed
    alt All submitted BYOS segments accepted
        API-->>Client: 201 Created, no response body
    else Partial bulk success
        API-->>Client: 200 OK with failed_segments body
    end
```



For BYOS objects, OpenTAMS stores the metadata reference but does not own the bytes. BYOS uses the same `POST /segments` status rules as controlled storage: `201 Created` for full success, and `200 OK` only for partial bulk success. Garbage collection does not delete external media.

## Delete Segments And Reclaim Bytes

```mermaid
sequenceDiagram
    participant Client
    participant API as OpenTAMS API
    participant DB as PostgreSQL
    participant GC as OpenTAMS GC
    participant Store as OpenTAMS object store
    participant ExternalStore as External object store

    Client->>API: DELETE segment metadata
    API->>DB: Remove segment rows and update references
    DB-->>API: Metadata deletion committed
    API-->>Client: Delete response
    opt Controlled storage
        GC->>DB: Scan for unreferenced controlled objects
        DB-->>GC: Eligible object references
        GC->>Store: Delete controlled bytes
    end
    opt BYOS
        Client->>ExternalStore: Delete external bytes after OpenTAMS acknowledgment
    end
```



Segment deletion is a metadata operation on the request path. Byte deletion belongs to the separate GC component and only applies to controlled storage. BYOS references are outside OpenTAMS ownership, so clients are responsible for deleting external bytes after OpenTAMS acknowledges the segment metadata delete. The ordering matters: deleting BYOS bytes before deleting TAMS metadata can leave visible segment references that point to missing media.

## Health And Readiness

```mermaid
sequenceDiagram
    participant Probe as Platform probe
    participant API as OpenTAMS API
    participant DB as PostgreSQL
    participant Store as Object store

    Probe->>API: GET /healthz
    API-->>Probe: Process liveness
    Probe->>API: GET /readyz
    API->>DB: Check metadata store readiness
    opt Object store readiness is configured for detailed checks
        API->>Store: Check backend access
    end
    API-->>Probe: Ready or not ready
```



Health endpoints are operational control surfaces. They do not change TAMS metadata and are separated from the media data plane.