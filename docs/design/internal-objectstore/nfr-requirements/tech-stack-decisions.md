---
unit: M5 internal/objectstore
stage: NFR Requirements
status: Complete
---

# Tech Stack Decisions — internal/objectstore

## SDK: aws-sdk-go-v2 (already decided)

**Decision**: `github.com/aws/aws-sdk-go-v2` module set.
**Rationale**: The S3-compatible API is the universal interface across AWS S3, MinIO, Oracle Object Storage, Linode, GCS (S3-interop), Cloudflare R2. Endpoint override covers all vendors.
**Rejected**: `gocloud.dev` — no Oracle/Linode drivers and no batch delete support.

**Packages used**:
| Package | Purpose |
|---|---|
| `github.com/aws/aws-sdk-go-v2/config` | Default credential chain loading (IRSA, instance profile, env, shared config) |
| `github.com/aws/aws-sdk-go-v2/credentials` | Static credentials provider when access key is configured |
| `github.com/aws/aws-sdk-go-v2/service/s3` | S3 client (`HeadBucket`, `DeleteObjects`) and `PresignClient` (`PresignPutObject`, `PresignGetObject`) |

No `s3manager` — presigned URLs delegate upload/download to the client; server never touches media bytes.

## Testing: narrow unexported interfaces + struct mocks

**Decision**: Define two unexported interfaces covering only the SDK calls made by this package; inject them in `S3Store`; provide struct mocks in `_test.go` files.

```go
type s3API interface {
    HeadBucket(ctx context.Context, in *s3.HeadBucketInput, opts ...func(*s3.Options)) (*s3.HeadBucketOutput, error)
    DeleteObjects(ctx context.Context, in *s3.DeleteObjectsInput, opts ...func(*s3.Options)) (*s3.DeleteObjectsOutput, error)
}

type presignAPI interface {
    PresignPutObject(ctx context.Context, in *s3.PutObjectInput, opts ...func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error)
    PresignGetObject(ctx context.Context, in *s3.GetObjectInput, opts ...func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error)
}
```

`*s3.Client` satisfies `s3API` and `*s3.PresignClient` satisfies `presignAPI` at compile time — verified by blank-identifier assertions in production code.

**Rationale**: Allows full unit test coverage without a real S3 or MinIO instance. Mocks are hand-rolled structs in `_test.go` — no mock generation library needed for four methods.
**Rejected**: Integration-only testing — too heavy for a unit boundary; real-MinIO tests belong in the integration suite. Mock generation libraries (mockery, gomock) — unnecessary for four methods.

## Error wrapping: fmt.Errorf with %w

**Decision**: All SDK errors wrapped as `fmt.Errorf("objectstore: <MethodName>: %w", err)`.
**Rationale**: Caller (HTTP handler) translates to `apperror.New(apperror.ErrDependencyUnavailable, ...)`. No error type inspection inside this package — keeps the package independent of apperror.
**Rejected**: Returning raw SDK errors — leaks SDK types to callers. Typed error structs — unnecessary given the caller's flat translation to apperror.
