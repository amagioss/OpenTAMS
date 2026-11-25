---
unit: M16 internal/gc
stage: Functional Design
status: Draft (not implemented)
---

# Interface Specification — object garbage collection

This document is the external contract of the GC change. It has one section for each interface surface. A client developer can use section 1. An operator can use sections 2 to 7.

| Surface | Status |
|---|---|
| TAMS HTTP API | Changed (section 1) |
| CLI `opentams gc` | New (section 2) |
| Admin HTTP endpoints of the GC process | New (section 3) |
| Prometheus metrics | New (section 4) |
| Structured logs | New (section 5) |
| Configuration (environment variables) | Changed (section 6) |
| Helm values | Changed (section 7) |
| Webhooks, event streams, SDK | No change. This change adds no such surface. |

## 1. TAMS HTTP API

Authentication does not change. All endpoints keep their existing authentication and authorization (bearer JWT). The API contract file `api/opentams-api-v1.yaml` does not change.

### 1.1 `POST /tams/v1/flows/{flowId}/storage`

Behaviour change: the server records each issued object ID (BR-GC-01). An object ID that a client supplies is examined against the object rows and the allocation rows (BR-GC-03).

Success (no change in shape):

```http
POST /tams/v1/flows/7f0d…/storage
Content-Type: application/json

{"limit": 2}
```

```http
HTTP/1.1 201 Created
Content-Type: application/json

{"media_objects": [
  {"object_id": "1b4e…", "put_url": {"url": "https://bucket…/1b4e…?X-Amz-…", "content-type": "video/h264"}},
  {"object_id": "9c2a…", "put_url": {"url": "https://bucket…/9c2a…?X-Amz-…", "content-type": "video/h264"}}
]}
```

Errors:

| Status | Type (suffix of `https://github.com/amagioss/opentams/problems/`) | Cause | Client action |
|---|---|---|---|
| 400 | `object-exists` | An `object_ids` entry is registered | Use a different ID |
| 400 | `object-wrong-flow` | An `object_ids` entry is allocated to a different flow | Use a different ID, or use the flow that requested it |
| 400 | `object-deleting` | The GC is about to delete this ID | Use a different ID |
| 400 | `schema-validation` | `limit` below 1, more than 100 IDs, or both or neither mode (no change) | Correct the request |
| 403 | `read-only` | The flow is read-only (no change) | — |
| 404 | `not-found` | The flow does not exist (no change) | — |

Example:

```http
HTTP/1.1 400 Bad Request
Content-Type: application/problem+json

{"type": "https://github.com/amagioss/opentams/problems/object-wrong-flow",
 "title": "Object Allocated to Another Flow",
 "status": 400,
 "detail": "object_id \"obj-1\" is allocated to a different flow",
 "instance": "/tams/v1/flows/7f0d…/storage",
 "request_id": "…"}
```

The request is all or nothing. If one ID gets an error, the server allocates no ID of the request.

A second request for the same `object_ids` entry on the same flow returns 201 with a new URL, and the allocation gets a new expiry.

### 1.2 `POST /tams/v1/flows/{flowId}/segments`

Behaviour change:

- A controlled segment (no `get_urls`) must use an object ID that `POST …/storage` allocated for the same flow, or an object ID that is registered and not claimed by the GC.
- A rejected segment does not stop the other segments of the request.

The rejected segments come back in the existing bulk-failure body with status 200:

```http
HTTP/1.1 200 OK
Content-Type: application/json

{"failed_segments": [
  {"object_id": "obj-9",
   "timerange": "[10:0_12:0)",
   "error": {"type": "https://github.com/amagioss/opentams/problems/object-not-allocated",
             "title": "Object Not Allocated",
             "status": 400,
             "detail": "object_id \"obj-9\" was not allocated by POST /flows/{flowId}/storage, or its allocation expired and was deleted"}}
]}
```

| Type in `failed_segments[].error.type` | Cause | Client action |
|---|---|---|
| `object-not-allocated` | No allocation and no registration for the ID | Call `POST …/storage` and upload again |
| `object-wrong-flow` | The allocation belongs to a different flow | Register on the correct flow, or allocate again |
| `object-deleting` | The GC claimed the ID | Allocate a new ID and upload again |

If all segments of the request are rejected, the response is also 200 with `failed_segments` (no change in this rule).

Removed: the response 409 with type `object-reaping`. The 409 response stays only for an idempotency conflict.

No change: 201 (all accepted), 400 for a request that is not valid, 403, 404, and 422 `segment-overlap` (whole request).

### 1.3 `GET /tams/v1/service`

New field `min_presigned_url_timeout`. It has the same value as `min_object_timeout`:

```json
{"type": "urn:x-tams:service:tams",
 "api_version": "8.0",
 "min_object_timeout": "3600:0",
 "min_presigned_url_timeout": "3600:0"}
```

### 1.4 `DELETE /tams/v1/flows/{flowId}` and `DELETE /tams/v1/flows/{flowId}/segments`

No change in the request, the response, or the status codes. Both stay synchronous. The effect on `ref_count` becomes correct (BR-GC-20, BR-GC-21). The media bytes are deleted later by the GC, not by the request.

## 2. CLI `opentams gc`

```text
opentams gc [--once]
```

| Mode | Behaviour | Exit code |
|---|---|---|
| `opentams gc` | Starts sweep 1 and sweep 2 as two loops, and opens the admin listener. Operates until SIGTERM or SIGINT. | 0 after a shutdown. 1 if the start is not successful (configuration, database, schema version). |
| `opentams gc --once` | Does one cycle of each sweep and stops. Opens no listener. | 0 if no cycle stopped because of a batch-level error. 1 in the other case, or if the start is not successful. |

The command reads only environment variables (section 6). It has no other flags.

## 3. Admin HTTP endpoints of the GC process

The GC process listens on `SERVER_PORT` (default 8080). It serves only these three paths. It does not serve the TAMS API. The paths have no authentication, as on the API server.

| Path | 200 when | Non-200 |
|---|---|---|
| `GET /healthz` | The process operates | No other status. If the process stops, the connection is refused. |
| `GET /readyz` | The database and the object store are available, and the process did not get SIGTERM | 503 |
| `GET /metrics` | Always | — |

## 4. Prometheus metrics

All names start with `opentams_gc_`. The label `sweep` has the values `released` (sweep 1) and `allocation` (sweep 2). No metric has an `object_id` label.

| Name | Type | Labels | Meaning |
|---|---|---|---|
| `opentams_gc_items_total` | counter | `sweep`, `outcome` | Rows that the GC removed. `outcome` is `deleted` (bytes and row), `byos_row_only` (sweep 1, row only), or `registered_leftover` (sweep 2, row only). |
| `opentams_gc_refcount_drift_total` | counter | — | Sweep 1 found segments for an object at `ref_count = 0` and corrected the count (BR-GC-33). |
| `opentams_gc_errors_total` | counter | `sweep`, `stage` | Errors. `stage` is `retry_select`, `claim`, `s3_delete`, `row_delete`, or `gauge`. |
| `opentams_gc_backlog` | gauge | `sweep`, `state` | Rows that wait. `state` is `pending` (candidates), `retrying` (`deleting = true`), or `unknown_storage` (BR-GC-37, BR-GC-45). Updated at the end of each cycle. |
| `opentams_gc_oldest_overdue_allocation_seconds` | gauge | — | Time since the oldest candidate of sweep 2 passed `expires_at + GC_ALLOCATION_GRACE`. 0 if there is no candidate. |
| `opentams_gc_allocations_rows` | gauge | — | All rows of the `allocations` table. |
| `opentams_gc_last_success_timestamp_seconds` | gauge | `sweep` | Unix time of the last successful batch. An empty batch is also a success. |
| `opentams_gc_batch_duration_seconds` | histogram | `sweep` | Time of one claim batch, from the claim to the row delete. Default buckets. |

The process also exports the standard Go and process metrics of the registry (`pkg/metrics`).

With two or more replicas, each replica exports the gauges with the same value. Use `max by (sweep, state)` in queries and alerts.

### 4.1 Alerts (documented only, because the chart has no PrometheusRule)

| Alert | Expression | Runbook |
|---|---|---|
| GCDown | `time() - max by (sweep) (opentams_gc_last_success_timestamp_seconds) > 2 * <GC_POLL_INTERVAL in seconds> + 600` | R1 |
| GCRetryStuck | `max by (sweep) (opentams_gc_backlog{state="retrying"}) > 0` for 2 h | R2 |
| GCRefcountDrift | `increase(opentams_gc_refcount_drift_total[1h]) > 0` | R3 |
| GCBacklogGrowing | `deriv(max by (sweep) (opentams_gc_backlog{state="pending"})[2h:]) > 0` for 6 h | R4 |
| GCAllocationLag | `max(opentams_gc_oldest_overdue_allocation_seconds) > 3600` | R4 |
| GCUnknownStorage | `max by (sweep) (opentams_gc_backlog{state="unknown_storage"}) > 0` | R5 |

The runbooks are in [`../../../runbooks/gc.md`](../../../runbooks/gc.md).

## 5. Structured logs

The GC uses the JSON logger of the project (`pkg/logger`). Each line has the standard fields of that logger.

| Level | Message | Fields | When |
|---|---|---|---|
| INFO | `gc object removed` | `object_id`, `storage_id`, `sweep`, `outcome` | One line for each row that the GC removes (audit, NFR-GC-U6) |
| INFO | `gc cycle completed` | `sweep`, `deleted`, `row_only`, `failed`, `drift`, `duration_ms`, `ended_by` (`empty`, `batch_error`, `shutdown`) | One line at the end of each cycle |
| INFO | `gc once completed` | `released_*` and `allocation_*` with the fields of the cycle line | `--once` only |
| WARN | `gc refcount drift` | `object_id`, `stored_ref_count`, `actual_ref_count` | BR-GC-33 |
| WARN | `gc object delete failed` | `sweep`, `object_id`, `error` | BR-GC-63 |
| WARN | `gc gauge update failed` | `sweep`, `error` | Backlog query error |
| ERROR | `gc batch error` | `sweep`, `stage`, `error` | BR-GC-62 |
| DEBUG | `gc batch claimed` | `sweep`, `selected`, `claimed`, `row_only` | Each claim |

The `storage_id` field is empty for a BYOS row.

## 6. Configuration

| Variable | Type | Default | Valid values | Used by | Restart needed |
|---|---|---|---|---|---|
| `GC_POLL_INTERVAL` | Go duration | `15m` | more than 0 | `gc` | yes |
| `GC_BATCH_SIZE` | integer | `1000` | 1 to 1000 | `gc` | yes |
| `GC_ALLOCATION_GRACE` | Go duration | `5m` | 0 or more | `gc` | yes |
| `OBJECT_STORE_PRESIGN_EXPIRY` | Go duration | `1h` | 300 s or more (new rule) | `serve`, `gc` | yes |
| `OPENTAMS_OBJECT_STORE_ID` | string | `default` | not empty | `serve`, `gc`. Must be equal in both. | yes |
| `SERVER_PORT` | integer | `8080` | 1 to 65535 | `serve`, `gc` (admin listener) | yes |
| `SERVER_GRACEFUL_SHUTDOWN_PERIOD` | Go duration | `30s` | (no change) | `serve`, `gc` | yes |

The database and object-store settings are the same as for `serve` (`docs/configuration.md`). A value that is not valid stops the process at start with exit code 1 and an error that names the variable.

## 7. Helm values

| Key | Default | Effect |
|---|---|---|
| `gc.enabled` | `true` | Deploys the GC Deployment, Service, and (if enabled) ServiceMonitor and NetworkPolicy |
| `gc.replicaCount` | `1` | Replicas of the GC Deployment. 0 stops the GC (runbook R6). |
| `gc.pollInterval` | `15m` | `GC_POLL_INTERVAL` |
| `gc.batchSize` | `1000` | `GC_BATCH_SIZE` |
| `gc.allocationGrace` | `5m` | `GC_ALLOCATION_GRACE` |
| `gc.resources` | `{}` | Container resources of the GC pods |

The GC pods use the labels `app.kubernetes.io/name: <name>-gc` and `app.kubernetes.io/instance: <release>`.

- `<name>` is the output of the chart helper `opentams.name`. It is `nameOverride` if that value is set. If not, it is the chart name `opentams`. Thus the default label is `app.kubernetes.io/name: opentams-gc`.
- `<release>` is the Helm release name.

The API pods use `app.kubernetes.io/name: <name>`. Thus the API Service does not select the GC pods.

Resource names (Deployment, Service, ServiceMonitor) use `<fullname>-gc`. `<fullname>` is the output of `opentams.fullname`, not `opentams.name`.
