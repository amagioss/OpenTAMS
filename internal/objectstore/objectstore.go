// Package objectstore provides the Store interface and an S3-compatible
// implementation for OpenTAMS controlled object storage.
//
// The DeleteObjects slice is the GC worker's batch-delete primitive
// (D-25 / D-30): per-id idempotent, transparently chunked at the S3
// max of 1000 ids per call, with sentinel error classes
// (ErrTransient / ErrAuth / ErrInvalidInput) that the GC uses to
// decide which rows to reap and which to leave for the next sweep.
// The segment service is NOT a caller — it has no objectstore
// dependency on its DELETE path.
package objectstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	smithy "github.com/aws/smithy-go"

	"github.com/amagioss/opentams/internal/config"
)

// Sentinel errors for the object-store contract.
// REQ-ARCH-10 classifies failures as transient / permanent / partial /
// authentication; this slice surfaces all four via per-id and slice-level
// errors below.
var (
	ErrTransient    = errors.New("objectstore: transient error (retry eligible)")
	ErrAuth         = errors.New("objectstore: authentication failure")
	ErrInvalidInput = errors.New("objectstore: invalid input")
)

// DeleteResult is the per-id outcome of a batch delete.
type DeleteResult struct {
	ID    string
	Error error
}

// BackendInfo captures the deployment-wide single-backend identity used
// by URL projection (D-2: Phase 1 ships exactly one storage backend).
// The triple <Provider, Region, StoreProduct> drives the response label
// `<provider>.<region>:<store_product>:opentams` (BR-HTTP-05).
//
// StorageID is the opaque identifier persisted on `objects.storage_id`
// so future multi-backend deployments can correlate per-object placement
// with this info — Phase 1 leaves it constant per process.
type BackendInfo struct {
	StorageID    string
	Provider     string
	Region       string
	StoreProduct string
}

// DownloadURLOpts customises GenerateDownloadURL. `Presigned` is the
// `?presigned` query-param projection: `nil` (omitted) and `&true` both
// compute the signed HTTPS URL; `&false` short-circuits — `PresignedURL`
// is left empty and no presign call is made (REQ-HTTP-22, INV-HTTP-04c).
type DownloadURLOpts struct {
	Presigned *bool
}

// DownloadURLSet is the projection-input shape returned by
// GenerateDownloadURL. The handler decides which entries to surface in
// the response based on `?presigned` filtering and BYOS rules; the
// objectstore returns the raw materials.
type DownloadURLSet struct {
	StorageID    string
	StorageURI   string // canonical s3:// URI, always non-empty for controlled.
	PresignedURL string // signed HTTPS URL; empty when Presigned == &false.
	Backend      BackendInfo
}

// Store is the full object-store interface. The DeleteObjects slice is
// scoped here for the GC worker; URL minting is for handlers and storage
// allocation.
type Store interface {
	GenerateUploadURL(ctx context.Context, objectID, contentType string) (string, error)
	GenerateDownloadURL(ctx context.Context, objectID string, opts DownloadURLOpts) (DownloadURLSet, error)
	DeleteObjects(ctx context.Context, ids []string) ([]DeleteResult, error)
	HealthCheck(ctx context.Context) error
}

// s3API is the narrow S3 surface the implementation depends on. The
// AWS-SDK *s3.Client satisfies it; tests substitute a fake.
type s3API interface {
	HeadBucket(ctx context.Context, params *s3.HeadBucketInput, optFns ...func(*s3.Options)) (*s3.HeadBucketOutput, error)
	DeleteObjects(ctx context.Context, params *s3.DeleteObjectsInput, optFns ...func(*s3.Options)) (*s3.DeleteObjectsOutput, error)
}

type presignAPI interface {
	PresignPutObject(ctx context.Context, params *s3.PutObjectInput, optFns ...func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error)
	PresignGetObject(ctx context.Context, params *s3.GetObjectInput, optFns ...func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error)
}

var (
	_ Store      = (*S3Store)(nil)
	_ s3API      = (*s3.Client)(nil)
	_ presignAPI = (*s3.PresignClient)(nil)
)

// loadDefaultConfig is a package-level var so tests can inject a stub.
var loadDefaultConfig = awsconfig.LoadDefaultConfig

// S3Store implements Store against any S3-compatible backend.
type S3Store struct {
	client  s3API
	presign presignAPI
	bucket  string
	expiry  time.Duration
	backend BackendInfo
}

// NewS3Store constructs an S3Store from the application config.
func NewS3Store(cfg *config.Config) (*S3Store, error) {
	opts := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(cfg.ObjectStoreRegion),
	}
	if cfg.ObjectStoreAccessKeyID != "" {
		opts = append(opts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.ObjectStoreAccessKeyID, cfg.ObjectStoreSecretAccessKey, ""),
		))
	}

	awsCfg, err := loadDefaultConfig(context.Background(), opts...)
	if err != nil {
		return nil, fmt.Errorf("objectstore: load config: %w", err)
	}

	var s3Opts []func(*s3.Options)
	if cfg.ObjectStoreEndpoint != "" {
		endpoint := cfg.ObjectStoreEndpoint
		s3Opts = append(s3Opts, func(o *s3.Options) {
			o.BaseEndpoint = aws.String(endpoint)
			o.UsePathStyle = true
		})
	}

	client := s3.NewFromConfig(awsCfg, s3Opts...)
	return &S3Store{
		client:  client,
		presign: s3.NewPresignClient(client),
		bucket:  cfg.ObjectStoreBucket,
		expiry:  cfg.ObjectStorePresignExpiry,
		backend: BackendInfo{
			Provider:     cfg.StorageBackendProvider,
			Region:       cfg.ObjectStoreRegion,
			StoreProduct: cfg.StorageBackendProduct,
			// StorageID is config-driven; left empty here so callers that
			// need it (DB-side `objects.storage_id` writes) supply it
			// explicitly. Phase 1 has exactly one backend, so the value is
			// constant per process.
		},
	}, nil
}

// NewWithBackend constructs an S3Store directly over a backend interface
// — used by tests to inject a fake without going through AWS config.
// presign is allowed to be nil; calls that need it will panic in that
// case (tests for presign use a real *s3.PresignClient via NewS3Store).
func NewWithBackend(backend s3API, bucket string, expiry time.Duration) *S3Store {
	return &S3Store{client: backend, bucket: bucket, expiry: expiry}
}

// NewWithBackendInfo is the test-friendly constructor that injects both
// an s3API and a presignAPI alongside the BackendInfo used by URL
// projection. Production code uses NewS3Store; this exists so tests
// can drive GenerateDownloadURL without spinning up a real PresignClient.
func NewWithBackendInfo(backend s3API, presign presignAPI, bucket string, expiry time.Duration, info BackendInfo) *S3Store {
	return &S3Store{
		client:  backend,
		presign: presign,
		bucket:  bucket,
		expiry:  expiry,
		backend: info,
	}
}

// GenerateUploadURL returns a presigned PUT URL. Pure crypto — no backend
// network call. (INV-OBJ-02 covers the read-side counterpart.)
func (s *S3Store) GenerateUploadURL(ctx context.Context, objectID, contentType string) (string, error) {
	if s.presign == nil {
		return "", fmt.Errorf("%w: presign client not configured", ErrInvalidInput)
	}
	req, err := s.presign.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(s.bucket),
		Key:         aws.String(objectID),
		ContentType: aws.String(contentType),
	}, s3.WithPresignExpires(s.expiry))
	if err != nil {
		return "", fmt.Errorf("objectstore: GenerateUploadURL: %w", err)
	}
	return req.URL, nil
}

// GenerateDownloadURL returns the projection-input URL set for objectID.
// Pure crypto — MUST NOT make a network call to the backend (INV-OBJ-02
// read counterpart).
//
// `opts.Presigned == &false` short-circuits the signing call entirely
// (REQ-HTTP-22, INV-HTTP-04c): `PresignedURL` is left empty and the
// presigner is never invoked. `nil` and `&true` both compute the signed
// HTTPS URL; the handler is responsible for filtering `s3://` out when
// `?presigned=true`.
//
// `StorageURI` is `s3://<bucket>/<objectID>` regardless of the
// short-circuit — handlers may use it for logs even when the response
// omits the s3:// entry.
func (s *S3Store) GenerateDownloadURL(ctx context.Context, objectID string, opts DownloadURLOpts) (DownloadURLSet, error) {
	set := DownloadURLSet{
		StorageID:  s.backend.StorageID,
		StorageURI: "s3://" + s.bucket + "/" + objectID,
		Backend:    s.backend,
	}

	skipSign := opts.Presigned != nil && !*opts.Presigned
	if skipSign {
		return set, nil
	}
	if s.presign == nil {
		return DownloadURLSet{}, fmt.Errorf("%w: presign client not configured", ErrInvalidInput)
	}
	req, err := s.presign.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(objectID),
	}, s3.WithPresignExpires(s.expiry))
	if err != nil {
		return DownloadURLSet{}, fmt.Errorf("objectstore: GenerateDownloadURL: %w", err)
	}
	set.PresignedURL = req.URL
	return set, nil
}

// HealthCheck issues HeadBucket. Used by readiness; nil ⇒ reachable.
func (s *S3Store) HealthCheck(ctx context.Context) error {
	if _, err := s.client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(s.bucket)}); err != nil {
		return fmt.Errorf("objectstore: HealthCheck: %w", err)
	}
	return nil
}

const deleteBatchSize = 1000

// DeleteObjects removes ids from the backend with per-id idempotency
// and transparent chunking. The slice-level error is non-nil only when
// the call could not begin (auth, ctx cancelled before first chunk);
// per-id outcomes always live in the returned slice.
//
// Behaviour contract:
//
//   - Empty input → ([]DeleteResult{}, nil), no backend call (INV-OBJ-08).
//   - Duplicates deduped before dispatch; result slice has one entry per
//     input position (INV-OBJ-07).
//   - Per-id 5xx / network → wraps ErrTransient (INV-OBJ-03).
//   - Slice-level credentials failure → wraps ErrAuth, nil results
//     (INV-OBJ-04).
//   - No internal retry — failed ids stay reaping=true for the next GC
//     sweep.
func (s *S3Store) DeleteObjects(ctx context.Context, ids []string) ([]DeleteResult, error) {
	if len(ids) == 0 {
		return []DeleteResult{}, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("objectstore: DeleteObjects: %w", err)
	}

	// Dedup preserving first-occurrence order.
	uniqueOrder := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		uniqueOrder = append(uniqueOrder, id)
	}

	// Per-id outcome of the deduped set.
	uniqueOutcome := make(map[string]error, len(uniqueOrder))

	for start := 0; start < len(uniqueOrder); start += deleteBatchSize {
		end := start + deleteBatchSize
		if end > len(uniqueOrder) {
			end = len(uniqueOrder)
		}
		chunk := uniqueOrder[start:end]

		objs := make([]types.ObjectIdentifier, len(chunk))
		for i, id := range chunk {
			objs[i] = types.ObjectIdentifier{Key: aws.String(id)}
		}
		out, err := s.client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
			Bucket: aws.String(s.bucket),
			Delete: &types.Delete{Objects: objs, Quiet: aws.Bool(true)},
		})
		if err != nil {
			if classifyAuth(err) {
				return nil, fmt.Errorf("%w: %w", ErrAuth, err)
			}
			// Slice-level non-auth failure on a chunk — record each id in this
			// chunk as transient and continue. ctx.Err on cancel is treated as
			// per-id-cancelled to honour INV-OBJ-05.
			classified := classifyChunkErr(err)
			for _, id := range chunk {
				uniqueOutcome[id] = classified
			}
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				// Stamp every still-unprocessed id with the same cancellation
				// error before bailing. Without this, fan-out below reads
				// the zero-value (nil) for unprocessed ids and reports a
				// silent "success" — directly violates INV-OBJ-05 and would
				// trick the GC into reaping objects whose bytes still exist.
				for j := end; j < len(uniqueOrder); j++ {
					uniqueOutcome[uniqueOrder[j]] = classified
				}
				break
			}
			continue
		}

		// Mark every chunk id as success unless overridden by per-key error.
		for _, id := range chunk {
			uniqueOutcome[id] = nil
		}
		for _, e := range out.Errors {
			id := aws.ToString(e.Key)
			uniqueOutcome[id] = classifyAPIError(aws.ToString(e.Code), aws.ToString(e.Message))
		}
	}

	// Fan deduped outcomes back to original input positions.
	results := make([]DeleteResult, len(ids))
	for i, id := range ids {
		results[i] = DeleteResult{ID: id, Error: uniqueOutcome[id]}
	}
	return results, nil
}

// classifyAuth reports whether err is a slice-level credentials failure.
func classifyAuth(err error) bool {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "AccessDenied", "InvalidAccessKeyId", "SignatureDoesNotMatch", "Unauthorized":
			return true
		}
	}
	return false
}

// classifyChunkErr maps a chunk-level non-auth error to a per-id sentinel.
// Both ctx-cancel and generic chunk errors map to ErrTransient — a single
// branch is kept here for future divergence (e.g., explicit ctx sentinel).
func classifyChunkErr(err error) error {
	return fmt.Errorf("%w: %w", ErrTransient, err)
}

// classifyAPIError maps an S3 per-key Errors[i] entry to a sentinel.
// 5xx-class codes wrap ErrTransient; auth codes wrap ErrAuth; everything
// else (4xx malformed, etc.) is opaque non-sentinel — caller-bug class
// per BR-OBJ-10.
func classifyAPIError(code, msg string) error {
	switch code {
	case "InternalError", "ServiceUnavailable", "SlowDown", "RequestTimeout":
		return fmt.Errorf("%w: %s: %s", ErrTransient, code, msg)
	case "AccessDenied", "InvalidAccessKeyId", "SignatureDoesNotMatch":
		return fmt.Errorf("%w: %s: %s", ErrAuth, code, msg)
	default:
		return fmt.Errorf("objectstore: %s: %s", code, msg)
	}
}
