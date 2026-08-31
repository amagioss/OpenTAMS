---
status: "accepted"
date: 2026-08-31
decision-makers: OpenTAMS maintainers
consulted: OpenTAMS maintainers
informed: OpenTAMS contributors
---

# Require an idempotency key on Segment registration, backed by PostgreSQL

## Context and Problem Statement

Registering Segments is not naturally idempotent. A retry after a timeout can register the
same batch twice, and the second attempt collides with the non-overlap constraint. The
client then sees a `422` for a batch that in fact succeeded.

Recorders retry. A network blip during a live capture must not become a permanent gap or a
spurious failure.

How does a client retry safely?

## Considered Options

* Do nothing, and let clients detect duplicates themselves
* Make registration naturally idempotent by treating an identical Segment as a success
* Accept an optional idempotency key
* Require an idempotency key, and store its state in PostgreSQL
* Require a key, and store its state in Redis or another cache

## Decision Outcome

Chosen option: "Require an idempotency key, and store its state in PostgreSQL".

`POST /flows/{flowId}/segments` requires `X-Idempotency-Key`. A request without one is
rejected with the `missing-idempotency-key` problem type. Required, not optional, because
an optional safety feature is one most clients will not use until after their first
incident.

`internal/idempotency` stores the key, a hash of the request body, an in-flight flag, and
the cached response in the `idempotency_keys` table. `Acquire` returns one of four
outcomes:

* `StatusAcquired` — this caller owns the key and must `Complete` or `Release`.
* `StatusInFlight` — another caller holds it. The client retries.
* `StatusCached` — a previous success for this key and body hash. The stored response is
  replayed verbatim.
* `StatusConflict` — the key was reused with a different body. The request is rejected.

The body hash is what makes the key safe. A key alone cannot tell a genuine retry from a
different request that reused the key by mistake.

PostgreSQL was chosen over a cache for one reason: the state must be as durable as the
write it protects. A cache eviction between the write and the retry reintroduces the exact
failure the mechanism exists to prevent, and a cache is another service to operate.

Rows carry `expires_at` and an index on it. Expiry is enforced by a reaper, which is
separate from Object garbage collection and deletes no media.

### Consequences

* Good, because a retry after a timeout returns the original response instead of a
  spurious conflict.
* Good, because key state and Segment rows share one database, so they cannot diverge under
  partial failure.
* Good, because reuse of a key with a different body is caught rather than silently
  serving the wrong cached answer.
* Good, because it adds no new infrastructure.
* Bad, because it is a breaking requirement. A client that does not send the header cannot
  register Segments at all.
* Bad, because every registration costs extra database work on the write path.
* Bad, because cached response bodies live in the metadata database and grow it until the
  reaper runs.
* Bad, because the handler must canonicalise the body before hashing. Two encodings of the
  same request otherwise look like a conflict.

## More Information

* State machine and its contract: [`internal/idempotency/idempotency.go`](../../internal/idempotency/idempotency.go).
* Table: [`migrations/000002_idempotency.up.sql`](../../migrations/000002_idempotency.up.sql).
* Handler use: [`internal/httpx/handlers/segments.go`](../../internal/httpx/handlers/segments.go).
