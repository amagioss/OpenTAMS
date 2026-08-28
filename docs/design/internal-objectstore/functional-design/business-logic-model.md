---
unit: M5 internal/objectstore
stage: Functional Design
status: Complete
---

# Business Logic Model — internal/objectstore

## Interface

```go
type ObjectStore interface {
    GenerateUploadURL(ctx context.Context, objectID, contentType string) (string, error)
    GenerateDownloadURL(ctx context.Context, objectID string) (string, error)
    DeleteObjects(ctx context.Context, ids []string) ([]DeleteResult, error)
    HealthCheck(ctx context.Context) error
}
```

Source: REQ-ARCH-09.

## S3Store Construction

```
NewS3Store(cfg *config.Config) (*S3Store, error)
```

Steps:
1. Build `aws.Config` from region (`cfg.ObjectStoreRegion`).
2. Credential sourcing (BR-OBJ-02):
   - `cfg.ObjectStoreAccessKeyID != ""` → `credentials.NewStaticCredentialsProvider`
   - else → `config.LoadDefaultConfig` with default chain
3. If `cfg.ObjectStoreEndpoint != ""`: apply custom endpoint resolver + force path-style (BR-OBJ-07).
4. Construct `s3.Client` and `s3.PresignClient` (shared expiry from `cfg.ObjectStorePresignExpiry`).
5. Store: `bucket = cfg.ObjectStoreBucket`, `expiry = cfg.ObjectStorePresignExpiry`.

`S3Store` is unexported-fields struct; `*S3Store` satisfies `ObjectStore`.

## GenerateUploadURL

```
input: ctx, objectID string, contentType string
→ s3.PresignPutObject(ctx, bucket, key=objectID, ContentType=&contentType, Expires=expiry)
→ returns presigned URL string or wrapped error
```

## GenerateDownloadURL

```
input: ctx, objectID string
→ s3.PresignGetObject(ctx, bucket, key=objectID, Expires=expiry)
→ returns presigned URL string or wrapped error
```

## DeleteObjects

```
input: ctx, ids []string
→ empty input: return ([]DeleteResult{}, nil), no backend call
→ dedup preserving first-occurrence order
→ chunk into groups of ≤1000 (BR-OBJ-04)
→ for each chunk:
    s3.DeleteObjects(ctx, bucket, objects=[{Key: id} for id in chunk])
    auth failure:   return (nil, ErrAuth)
    other failure:  mark every id in the chunk transient, continue
    cancellation:   mark every unprocessed id too, then stop
→ fan the deduped outcomes back out to one DeleteResult per input position
```

## HealthCheck

```
input: ctx
→ s3.HeadBucket(ctx, bucket)
→ nil on 200, wrapped error otherwise
```

## Credential Sourcing Decision Tree

```
OBJECT_STORE_ACCESS_KEY_ID set?
  YES → StaticCredentialsProvider(keyID, secret, "")
  NO  → aws default chain (IRSA → instance profile → env → shared config)
```

## Endpoint Behavior

```
OBJECT_STORE_ENDPOINT set?
  YES → custom resolver (endpoint) + UsePathStyle=true   (MinIO, R2, GCS S3-compat)
  NO  → standard AWS virtual-hosted resolution
```
