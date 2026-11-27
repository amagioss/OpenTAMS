# OpenTAMS Metadata Model

OpenTAMS stores TAMS metadata in PostgreSQL. This document shows the logical metadata model: what each entity represents and how the entities relate. The migration files under [`migrations/`](../../migrations/) remain the source of truth for physical columns, indexes, and constraints.

For request sequences, see [data flows](data-flows.md). For implementation package boundaries, see the [codebase guide](../development/codebase.md).

## Entity Relationship Diagram

```mermaid
erDiagram
    sources ||--o{ source_tags : has
    sources ||--o{ flows : owns
    flows ||--o{ flow_tags : has
    flows ||--o{ flow_collection : groups
    flows ||--o{ segments : contains
    objects ||--o{ segments : referenced_by
    flows ||--o{ allocations : "allocated_for (no FK)"

    sources {
        uuid id PK
        text format
        text label
        text description
        timestamptz created
        timestamptz updated
    }

    source_tags {
        uuid source_id PK,FK
        text name PK
        jsonb value
    }

    flows {
        uuid id PK
        uuid source_id FK
        text format
        text codec
        text container
        jsonb essence_parameters
        boolean read_only
        text timerange
    }

    flow_tags {
        uuid flow_id PK,FK
        text name PK
        jsonb value
    }

    flow_collection {
        uuid flow_id PK,FK
        uuid item_id PK
        text role
        int sort_order
        jsonb container_mapping
    }

    objects {
        text id PK
        text timerange
        text storage_id
        int ref_count
        boolean deleting
        timestamptz claimed_at
    }

    allocations {
        text id PK
        uuid flow_id
        text storage_id
        timestamptz expires_at
        timestamptz created_at
        boolean deleting
        timestamptz claimed_at
    }

    segments {
        bigint id PK
        uuid flow_id FK
        text object_id FK
        text timerange
        bigint lower_ns
        bigint upper_ns
        text ts_offset
        jsonb get_urls
        timestamptz created_at
    }

    idempotency_keys {
        varchar key PK
        text body_hash
        boolean in_flight
        int response_status
        jsonb response_body
        timestamptz acquired_at
        timestamptz expires_at
    }
```

## Core Entities

| Entity | Meaning |
|---|---|
| `sources` | A TAMS source. Sources group one or more flows that represent related media essence. |
| `flows` | A TAMS flow. Flows carry format, codec/container metadata, timing metadata, and read-only state. |
| `segments` | Time-addressed media metadata for a single flow. A segment points to one object and occupies a timerange. |
| `objects` | Object references used by segments. Objects may represent OpenTAMS-controlled bytes or bring-your-own-storage bytes. |
| `allocations` | Object IDs that `POST /flows/{flowId}/storage` issued an upload URL for, and that no segment has registered yet. |
| `idempotency_keys` | Request lifecycle state for idempotent writes. This protects retries; it is not part of the TAMS domain model. |

## Tags And Collections

`source_tags` and `flow_tags` store name/value metadata attached to sources and flows. They are deleted with their parent source or flow.

`flow_collection` records collection membership for a flow. The `item_id` is intentionally modeled as an item reference rather than a foreign key to one table, because collection entries can represent different item types in the TAMS model.

## Segment Time Model

Segments keep both the API timerange string and normalized nanosecond bounds:

- `timerange` preserves the TAMS wire representation.
- `lower_ns` and `upper_ns` support efficient time-range queries.
- `upper_ns` can be `NULL` for open-ended ranges.

At write time PostgreSQL enforces that two segments on the same flow cannot overlap. At read time OpenTAMS can find overlapping segments with indexed bound comparisons.

## Object Ownership Model

The `objects.storage_id` column is the ownership marker:

| `objects.storage_id` | Meaning | Byte ownership |
|---|---|---|
| Non-null | Controlled storage | OpenTAMS issued the storage URL and may delete bytes through GC when unreferenced. |
| `NULL` | BYOS | The client supplied external object access; OpenTAMS stores metadata only and does not delete bytes. |

`objects.ref_count` tracks how many segment rows currently reference the object. When it reaches zero, the object is released. The GC (sweep 1) deletes the bytes and the row of a released controlled object. For a released BYOS object, it deletes only the row.

`objects.deleting` is the GC claim flag, and `objects.claimed_at` is the time of the claim. A row with `deleting = true` cannot get a new segment: registration rejects it with `object-deleting`. This prevents a race where the GC deletes the bytes of an object while another request attaches a new segment to the same object id. Migration 000006 renamed the column from `reaping` to `deleting`.

## Allocations

`POST /flows/{flowId}/storage` writes one `allocations` row for each object ID that it issues. The row records the flow, the storage ID, and `expires_at` (the expiry of the upload URL). It has no foreign key to `flows`, so a flow delete does not delete its allocations.

When a controlled segment registers an allocated ID on the same flow, the same transaction deletes the allocation row and writes the `objects` row. A controlled segment with no allocation and no object row is rejected with `object-not-allocated`.

The GC (sweep 2) deletes the bytes and the row of each allocation that no segment registered, after `expires_at + GC_ALLOCATION_GRACE`. `allocations.deleting` and `allocations.claimed_at` have the same meaning as in `objects`. The decision record is [ADR-0041](../adr/0041-track-storage-allocations-in-a-table.md).

## Idempotency State

`idempotency_keys` stores write-retry state keyed by `X-Idempotency-Key`.

It records:

- the raw request body hash,
- whether the first request is still in flight,
- the cached stable response for replay,
- acquisition and expiry timestamps.

This table does not have foreign keys to domain tables. It protects request processing and is periodically pruned.

## Physical Schema Notes

The logical model above intentionally omits most implementation detail. The PostgreSQL migrations add the physical behavior needed by the service:

- `segments_flow_lower` supports segment listing by flow and lower time bound.
- `no_segment_overlap` prevents overlapping segment timeranges within a flow.
- `flows_essence_gin` supports JSONB lookup over flow essence parameters.
- `idempotency_keys_expires` and `idempotency_keys_inflight_acquired` support idempotency pruning and stale in-flight cleanup.
- `objects_gc_claim_idx` and `objects_gc_retry_idx` support efficient GC claiming and retry scans.

When this document and a migration disagree, the migration is authoritative.
