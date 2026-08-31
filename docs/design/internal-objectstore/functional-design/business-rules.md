---
unit: M5 internal/objectstore
stage: Functional Design
status: Complete
---

# Business Rules — internal/objectstore

## BR-OBJ-01: Interface isolation
`ObjectStore` is a Go interface in this package. `S3Store` is the sole Phase 1 implementation. API handlers and business logic depend only on the interface — zero changes required to add a second backend (REQ-ARCH-04, REQ-OSS-08).

## BR-OBJ-02: Credential sourcing
If `config.ObjectStoreAccessKeyID != ""`, use static credentials (`config.ObjectStoreAccessKeyID` + `config.ObjectStoreSecretAccessKey`). Otherwise, use the AWS default credential chain (IRSA on EKS, Workload Identity on GKE, instance profile on EC2). No other credential modes.

## BR-OBJ-03: Presigned URL expiry
Both upload and download URLs use `config.ObjectStorePresignExpiry` (env: `OBJECT_STORE_PRESIGN_EXPIRY`, default `1h`) as the URL TTL (REQ-BEH-25).

## BR-OBJ-04: DeleteObjects reports per-id outcomes, not a single error

```go
DeleteObjects(ctx context.Context, ids []string) ([]DeleteResult, error)
```

`DeleteResult` is `{ID string; Error error}`, one entry per input position — duplicates are deduped before dispatch but still get their own result entry, so a caller can index results against the ids it passed. The returned top-level error is reserved for failures that invalidate the whole call: a credentials failure wraps `ErrAuth` and returns nil results. Everything else is per-id.

The S3 `DeleteObjects` API accepts at most 1000 keys per call, so the deduped set is chunked. A chunk-level failure does not abort the remaining chunks: each id in the failed chunk is recorded as transient and the sweep continues. Cancellation is the exception — every id not yet processed is stamped with the cancellation error before returning, because a zero-valued (nil) result would read as success and let the GC reap an object whose bytes still exist.

There is no internal retry. An id that failed keeps `objects.reaping = true` and is picked up by the next GC sweep, which is where the retry policy belongs.

An empty input is `([]DeleteResult{}, nil)` with no backend call.

The caller is the GC worker. No request path deletes objects (BR-SEG-10).

## BR-OBJ-05: Context propagation
All four interface methods accept `context.Context` as their first argument. Methods pass the context directly to every SDK call. Callers control per-call timeouts via the context (REQ-REL-15).

## BR-OBJ-06: HealthCheck implementation
`HealthCheck(ctx)` calls `HeadBucket` on the configured bucket. Returns `nil` on success, a wrapped error on any failure. Used by `POST /flows/{flowId}/storage` to gate storage allocation (REQ-REL-04).

## BR-OBJ-07: Path-style addressing for non-AWS providers
When `config.ObjectStoreEndpoint != ""`, the S3 client is configured with `s3.WithPathStyle()` forced to `true`. This is required for MinIO, Cloudflare R2, and other non-AWS S3-compatible providers that do not support virtual-hosted bucket addressing.

## BR-OBJ-08: Object key mapping
The object key stored in S3 is the `objectID` string as-is (no prefix, no transformation). The bucket is fixed at construction time from `config.ObjectStoreBucket`.

## BR-OBJ-09: Content-Type locking on upload URLs
`GenerateUploadURL` accepts a `contentType string` parameter (the flow's `codec` value). The presigned PutObject request is signed with that Content-Type. Uploads using a different Content-Type will be rejected by the object store. The HTTP handler includes this `content-type` in the `put_url` response field (REQ-BEH-25, REQ-BEH-25b).

## BR-OBJ-10: Failures are classified into sentinels at the backend boundary

Callers must not parse S3 error strings. `S3Store` classifies every backend failure once, at the point it is observed, and wraps it in a package sentinel:

- 5xx-class and network failures wrap `ErrTransient` — the operation may succeed on a later attempt, so the GC leaves the object claimed and retries on its next sweep.
- Credential and authorisation failures wrap `ErrAuth` — retrying changes nothing until the deployment is fixed, and for a batch delete the whole call fails rather than reporting per-id noise.
- Everything else, 4xx malformed requests included, is returned opaque and unwrapped. Those are caller bugs; giving them a sentinel would invite a retry loop around a request that will never succeed.

The distinction is what lets the GC decide between "try again" and "stop", which is why it lives here and not in the caller.

## BR-OBJ-11: Object lifetime belongs to the GC, never to a request path

Nothing on a user-visible request path deletes bytes. `DELETE /flows/{flowId}/segments` and `DELETE /flows/{flowId}` decrement `objects.ref_count` inside their metastore transaction and return; rows that reach zero stay in `objects` until the GC worker claims them via the `ref_count = 0 AND reaping = false` partial index, deletes the bytes, and reaps the rows.

Two reasons the request path cannot do this. A remote object-store round trip inside a request that has already committed its transaction cannot be rolled back if it fails, so a partial delete would leave metadata and bytes disagreeing with no record of it. And a large delete would put thousands of object-store calls on a latency-bound request.

The `internal/gc` worker that performs the sweep is not yet built; until it lands, zero-ref rows and their bytes accumulate. See [`docs/conformance.md`](../../../conformance.md).
