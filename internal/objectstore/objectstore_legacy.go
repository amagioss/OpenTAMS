//go:build legacy_objectstore
// +build legacy_objectstore

// Package objectstore — LEGACY; superseded by objectstore.go (D-30 / D-32).
// Kept under a build tag for git-history reference only.
package objectstore

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/amagioss/opentams/internal/config"
)

// ObjectStore abstracts S3-compatible object storage operations.
type ObjectStore interface {
	GenerateUploadURL(ctx context.Context, objectID, contentType string) (string, error)
	GenerateDownloadURL(ctx context.Context, objectID string) (string, error)
	DeleteObjects(ctx context.Context, objectIDs []string) error
	HealthCheck(ctx context.Context) error
}

type s3API interface {
	HeadBucket(ctx context.Context, params *s3.HeadBucketInput, optFns ...func(*s3.Options)) (*s3.HeadBucketOutput, error)
	DeleteObjects(ctx context.Context, params *s3.DeleteObjectsInput, optFns ...func(*s3.Options)) (*s3.DeleteObjectsOutput, error)
}

type presignAPI interface {
	PresignPutObject(ctx context.Context, params *s3.PutObjectInput, optFns ...func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error)
	PresignGetObject(ctx context.Context, params *s3.GetObjectInput, optFns ...func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error)
}

var (
	_ ObjectStore = (*S3Store)(nil)
	_ s3API       = (*s3.Client)(nil)
	_ presignAPI  = (*s3.PresignClient)(nil)
)

// loadDefaultConfig is a package-level var so tests can inject a stub.
var loadDefaultConfig = awsconfig.LoadDefaultConfig

// S3Store is an ObjectStore backed by any S3-compatible object store.
type S3Store struct {
	client  s3API
	presign presignAPI
	bucket  string
	expiry  time.Duration
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
	}, nil
}

// GenerateUploadURL returns a time-limited presigned PUT URL for
// objectID with the supplied Content-Type. The URL's lifetime is the
// expiry configured at construction time. The caller — typically the
// storage service — is responsible for surfacing the URL to the
// client and recording the eventual object placement once the client
// PUTs to it.
func (s *S3Store) GenerateUploadURL(ctx context.Context, objectID, contentType string) (string, error) {
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

// GenerateDownloadURL returns a time-limited presigned GET URL for
// objectID. The URL's lifetime is the expiry configured at
// construction time and is intended to be embedded in segment
// responses.
func (s *S3Store) GenerateDownloadURL(ctx context.Context, objectID string) (string, error) {
	req, err := s.presign.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(objectID),
	}, s3.WithPresignExpires(s.expiry))
	if err != nil {
		return "", fmt.Errorf("objectstore: GenerateDownloadURL: %w", err)
	}
	return req.URL, nil
}

const deleteBatchSize = 1000

// DeleteObjects removes the supplied object IDs in batches of 1000
// (the S3 DeleteObjects API maximum). Returns the first batch error
// or per-object error encountered; partially-completed batches leave
// successfully-deleted objects deleted (idempotent retries are
// safe).
func (s *S3Store) DeleteObjects(ctx context.Context, objectIDs []string) error {
	for i := 0; i < len(objectIDs); i += deleteBatchSize {
		end := min(i+deleteBatchSize, len(objectIDs))
		objects := make([]types.ObjectIdentifier, end-i)
		for j, id := range objectIDs[i:end] {
			objects[j] = types.ObjectIdentifier{Key: aws.String(id)}
		}
		out, err := s.client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
			Bucket: aws.String(s.bucket),
			Delete: &types.Delete{Objects: objects, Quiet: aws.Bool(true)},
		})
		if err != nil {
			return fmt.Errorf("objectstore: DeleteObjects: %w", err)
		}
		if len(out.Errors) > 0 {
			e := out.Errors[0]
			return fmt.Errorf("objectstore: DeleteObjects: key %s: %s", aws.ToString(e.Key), aws.ToString(e.Message))
		}
	}
	return nil
}

// HealthCheck performs a HeadBucket call to verify the configured
// bucket exists and is reachable. Used by the readiness endpoint;
// returns nil on success and a wrapped error on any S3 failure.
func (s *S3Store) HealthCheck(ctx context.Context) error {
	_, err := s.client.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: aws.String(s.bucket),
	})
	if err != nil {
		return fmt.Errorf("objectstore: HealthCheck: %w", err)
	}
	return nil
}
