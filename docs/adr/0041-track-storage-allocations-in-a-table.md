---
status: "accepted"
date: 2026-11-25
decision-makers: OpenTAMS maintainers
consulted: OpenTAMS maintainers
informed: OpenTAMS contributors
---

# Track storage allocations in a table

## Context and Problem Statement

`POST /flows/{flowId}/storage` gives a client an object ID and a presigned upload URL. The server writes no row at this time. The `objects` row appears only when a segment refers to the object ([ADR-0019](0019-lazy-object-registration.md)).

Thus the database has no record of bytes that a client uploads and never registers. The garbage collector cannot find them, and they stay in the bucket forever. Also, a client can register a controlled segment with an object ID that the server never issued. The server then has a segment for bytes that it does not know about.

ADR-0019 does not state all of its reasons. It keeps the `objects` row lazy because of three races. An allocation design must keep each of them:

1. **The meaning of `ref_count = 0`.** A row that `/storage` writes starts at `ref_count = 0`. The GC claim selects `ref_count = 0`. Thus the GC can delete the row and its bytes before the upload or the registration. With lazy rows, `ref_count = 0` always means "released".
2. **Parent and child in two transactions.** A row from `/storage` can be deleted before the registration. The registration then gets a foreign-key error (`23503`), or it races the GC. With lazy rows, the object row and the segment row are written in the same transaction.
3. **Two first registrations at the same time.** The upsert `INSERT … ON CONFLICT DO UPDATE` has no read before the write. Thus no increment is lost.

How does OpenTAMS record each issued object ID, and keep these three properties?

## Considered Options

* Write an `objects` row at `/storage`
* A separate `allocations` table. Registration deletes the row ("use-once").
* A separate `allocations` table. Registration keeps the row, and the GC deletes it later.
* No record. A periodic `LIST` of the bucket finds the bytes that have no row.

## Decision Outcome

Chosen option: "A separate `allocations` table. Registration deletes the row ("use-once")", because it keeps the three races of ADR-0019 closed, and it gives a simple meaning to each row.

- `objects` stays lazy. `ref_count = 0` still means "released" (race 1).
- Registration reads and deletes the allocation row, and writes the object row, in the segment transaction (race 2).
- Registration locks the allocation row and the object row. It writes a new object row with `ON CONFLICT DO NOTHING`, and then uses the reuse path. Thus no increment is lost (race 3).
- An allocation row means "issued and not registered". Sweep 2 deletes the bytes of an allocation after `expires_at + GC_ALLOCATION_GRACE`.

The design details are in [`../design/internal-gc/`](../design/internal-gc/README.md). These rules are part of this decision:

- **No foreign key to `flows`.** With `ON DELETE CASCADE`, uploaded bytes lose their only record. With `RESTRICT`, a flow delete is blocked until the allocations expire. Known effect: a flow that a client creates again with the same ID can use the old allocations. A `/storage` request at the same time as a flow delete can leave an allocation for the deleted flow. Sweep 2 deletes it after it expires.
- **`storage_id` on each allocation.** The new object row copies it. Sweep 2 deletes from it. Thus a change of the configured backend does not send a delete to the wrong bucket.
- **Expiry from the presign time.** `expires_at = now() + OBJECT_STORE_PRESIGN_EXPIRY`, written with the database clock. The upstream field `min_object_timeout` has this value. Upstream asks for a grace time after it, which is `GC_ALLOCATION_GRACE` (default 5m).
- **Re-request rule (a).** A second `/storage` request with the same `object_ids` entry on the same flow gives a new expiry. On a different flow it gives 400 `object-wrong-flow`. Upstream says that supplied IDs "must be new and not already in use". It does not say if an allocated ID that is not registered is "in use". Rule (a) is the lenient reading. It lets a client get a new URL after an upload error.
- **Enforcement at once.** A controlled segment with an object ID that has no allocation row and no object row is rejected with `object-not-allocated`. There is no migration flag.
- **Errors.** All new client errors are 400 with the prefix `object-`: `object-exists` (it replaces `object-id-exists`), `object-not-allocated`, `object-wrong-flow`, and `object-deleting` (it replaces the 409 `object-reaping`). Rule: 409 means "the same request can be successful later". 400 means "the request must change". A client must use a new object ID in each of these cases.
- **Sweep 2 never deletes bytes of a registered ID.** If an object row exists for an expired allocation, sweep 2 deletes only the allocation row. Such a leftover allocation occurs when a client registers an allocated ID with `get_urls`, because the BYOS path does not delete the allocation row.
- **Segments with `get_urls` have no allocation check.** The shape rule ([ADR-0026](0026-get-urls-projected-in-the-handler.md)) classifies such a segment as BYOS. Upstream says that `get_urls` "should only be used to add uncontrolled URLs". Thus, for an allocated ID that a client registers with `get_urls`, the bucket bytes are the controlled copy. Sweep 2 keeps these bytes, because an object row exists. A future design for mixed objects must serve them, and must make such a registration delete the allocation.
- **The claim flag is `deleting`.** Migration 000006 renames `objects.reaping` to `objects.deleting` in the same file that creates `allocations`. Both tables use the same name for the flag.

### Consequences

* Good, because the bytes of each abandoned upload get deleted, without a `LIST` of the bucket.
* Good, because a controlled segment can refer only to bytes that the server issued.
* Good, because the three races of ADR-0019 stay closed, and they are now written down.
* Good, because the errors for an object ID have one prefix and one status.
* Bad, because each registration of a new controlled object does one more `DELETE`. The scale case S1 measures it. A result outside the budget goes to the maintainers. It does not change this decision automatically.
* Bad, because the rename in migration 000006 is one step. This deviates from [ADR-0017](0017-expand-contract-schema-evolution.md). During a rolling upgrade, old API pods give 500 for segment registrations until the rollout replaces them. The upgrade procedure (BR-GC-50) asks for a time with low write traffic, or the API replica count 0 during the upgrade. This deviation applies to this change only.
* Bad, because uploads that start before the upgrade have no allocation row. Their registration after the upgrade gets `object-not-allocated`, and their bytes stay in the bucket with no row.
* Bad, because the names `object-id-exists` and `object-reaping` change. This is a breaking change before v1.0.0. The release notes must name it.
* Neutral: examine re-request rule (a) again before v1.0.0. A change to "reject each re-request" is a breaking change after v1.0.0 (REQ-OSS-07, `CONTRIBUTING.md` "Versioning policy").

## More Information

* Design: [`../design/internal-gc/`](../design/internal-gc/README.md), rules BR-GC-01 to BR-GC-17, BR-GC-40 to BR-GC-46, BR-GC-50, BR-GC-70 to BR-GC-74.
* Related: [ADR-0019](0019-lazy-object-registration.md) (lazy object rows, not superseded by this ADR), [ADR-0020](0020-gc-owns-object-lifetime.md), [ADR-0037](0037-object-garbage-collection.md), [ADR-0017](0017-expand-contract-schema-evolution.md).
* Upstream: `api/schemas/flow-storage-post.json` (`object_ids`), `api/schemas/service.json` (`min_object_timeout`), `api/schemas/flow-segment-post.json` (`get_urls`) in `github.com/bbc/tams`.
* Requirements: REQ-BEH-33, REQ-REL-14.
