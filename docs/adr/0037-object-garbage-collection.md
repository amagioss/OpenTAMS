---
status: "proposed"
date: 2026-11-25
decision-makers: OpenTAMS maintainers
consulted: OpenTAMS maintainers
informed: OpenTAMS contributors
---

# Reclaim Object storage from a separate worker

## Context and Problem Statement

[ADR-0020](0020-gc-owns-object-lifetime.md) removes the delete of media bytes from each request path. A segment delete only decreases `objects.ref_count`. A row at `ref_count = 0` stays. Some process must delete the bytes and the row, and that process must operate somewhere.

The work is different from request handling. It is a periodic sweep over a table. It is slow, it can stop at any point, and it must not take database connections from the API. It also has a race: an object at `ref_count = 0` can get a new reference (a "comeback") while the sweep decides to delete it. Upstream TAMS permits a comeback: a client modifies a segment when it deletes the segment and registers it again with the same object.

[ADR-0041](0041-track-storage-allocations-in-a-table.md) adds a second kind of work: the bytes of allocations that no segment registered.

Where does the reclamation operate, and how does it stay safe?

## Considered Options

* A background goroutine in the API server
* A separate worker, as a CronJob
* A separate worker, as a long-running Deployment
* No reclamation. Object-store lifecycle rules delete old bytes.

## Decision Outcome

Chosen option: "A separate worker, as a long-running Deployment", in the same binary as the subcommand `opentams gc`.

The design is in [`../design/internal-gc/`](../design/internal-gc/README.md). The main points:

* **Two sweeps.** Sweep 1 deletes released objects (`ref_count = 0`). Sweep 2 deletes expired allocations. Each sweep has its own loop. A cycle tries the retry rows first, then claims batches until the backlog is empty, then stays idle for `GC_POLL_INTERVAL` (default 15m). A batch has up to `GC_BATCH_SIZE` rows (default 1000, the maximum of one S3 `DeleteObjects` call).
* **The claim flag is `deleting`.** The sweep sets it in a short transaction with `FOR UPDATE SKIP LOCKED`, and commits before the object-store call. Registration does not use a row with `deleting = true`. The client gets `object-deleting` and uses a new object ID. This closes the comeback race. Migration 000006 renames the column from `reaping` to `deleting`.
* **Delete at the next cycle.** There is no deadline before deletion. A comeback is possible until the claim. Upstream gives no minimum time. A deadline column (`reap_after`) can be added later with an additive migration.
* **A guard before each irreversible delete.** Sweep 1 counts the segments of each candidate before the claim. If the count is not 0, it corrects `ref_count` and does not delete. Sweep 2 does not delete the bytes of an ID that has an object row.
* **BYOS rows.** Sweep 1 deletes a released BYOS row (`storage_id` NULL) with no object-store call. OpenTAMS does not own those bytes.
* **Failures.** An error for one object keeps its claim. The next cycle tries it again. A batch-level error (database, credentials, or an error for each object of one call) stops the cycle. An interruption at any point loses no data, because the object-store delete can be done again.
* **Replicas.** Two or more replicas can operate at the same time. `SKIP LOCKED` divides the claims. A duplicate retry is harmless.
* **Operation.** The process has an admin listener (`/healthz`, `/readyz`, `/metrics`). `opentams gc --once` does one cycle of each sweep and stops (exit code 0 or 1). The Helm chart deploys the worker by default (`gc.enabled: true`).

The project rejected the CronJob mode. A CronJob process is short, so Prometheus cannot collect its metrics reliably, and the backlog waits until the next schedule.

Object-store lifecycle rules are not a substitute. A lifecycle rule sees the age of one object. It cannot see that a segment on a different flow still refers to the bytes. Thus it deletes live data. `../configuration.md` gives this warning.

### Consequences

* Good, because the sweep does not interfere with request handling. It has its own process, its own connection pool, and its own schedule.
* Good, because the claim flag makes the comeback race explicit and recoverable. A segment never refers to deleted media.
* Good, because the reclamation can stop (replica count 0), start again, or change its settings without a change to the API servers.
* Good, because the worker is a subcommand of the same binary. There is one image and one version.
* Bad, because the reclamation is only as fast as the cycle. Storage is free after the next cycle, not when the client deletes a flow.
* Bad, because a successful `DELETE` response says nothing about the bytes. Only the metrics and the logs of the GC show the progress.
* Bad, because a client that modifies a segment (delete and register again) loses the object if a cycle claims it between the two calls. The client must then upload again with a new object ID. A client that must prevent this keeps a reference to the object in a different flow during the change.
* Bad, because bytes with no row and no allocation (for example, from uploads that started before the GC release) are not deleted. A reconciliation with a bucket listing is later work.

## More Information

* Design: [`../design/internal-gc/`](../design/internal-gc/README.md).
* Runbooks: [`../runbooks/gc.md`](../runbooks/gc.md).
* Command: [`cmd/opentams/gc.go`](../../cmd/opentams/gc.go).
* Schema: [`migrations/000005_objects_storage_id_reaping.up.sql`](../../migrations/000005_objects_storage_id_reaping.up.sql) (flag and partial indexes), migration 000006 (rename to `deleting`, `allocations`), migration 000007 (index on `segments.object_id`).
* Why no request path deletes bytes: [ADR-0020](0020-gc-owns-object-lifetime.md).
* Allocation record: [ADR-0041](0041-track-storage-allocations-in-a-table.md).
