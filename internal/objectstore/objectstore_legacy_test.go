//go:build legacy_objectstore
// +build legacy_objectstore

package objectstore

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/amagioss/opentams/internal/config"
)

// -- mocks --

type mockS3API struct {
	headBucketFn    func(context.Context, *s3.HeadBucketInput, ...func(*s3.Options)) (*s3.HeadBucketOutput, error)
	deleteObjectsFn func(context.Context, *s3.DeleteObjectsInput, ...func(*s3.Options)) (*s3.DeleteObjectsOutput, error)
}

func (m *mockS3API) HeadBucket(ctx context.Context, in *s3.HeadBucketInput, opts ...func(*s3.Options)) (*s3.HeadBucketOutput, error) {
	return m.headBucketFn(ctx, in, opts...)
}

func (m *mockS3API) DeleteObjects(ctx context.Context, in *s3.DeleteObjectsInput, opts ...func(*s3.Options)) (*s3.DeleteObjectsOutput, error) {
	return m.deleteObjectsFn(ctx, in, opts...)
}

type mockPresignAPI struct {
	putFn func(context.Context, *s3.PutObjectInput, ...func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error)
	getFn func(context.Context, *s3.GetObjectInput, ...func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error)
}

func (m *mockPresignAPI) PresignPutObject(ctx context.Context, in *s3.PutObjectInput, opts ...func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error) {
	return m.putFn(ctx, in, opts...)
}

func (m *mockPresignAPI) PresignGetObject(ctx context.Context, in *s3.GetObjectInput, opts ...func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error) {
	return m.getFn(ctx, in, opts...)
}

func newTestStore(client s3API, presign presignAPI) *S3Store {
	return &S3Store{client: client, presign: presign, bucket: "test-bucket", expiry: time.Hour}
}

func presignedReq(url, method string) *v4.PresignedHTTPRequest {
	return &v4.PresignedHTTPRequest{URL: url, Method: method, SignedHeader: http.Header{}}
}

// TC-OBJ-01: GenerateUploadURL success — returns URL and forwards bucket, key, contentType to SDK.
func TestGenerateUploadURL_Success(t *testing.T) {
	const wantURL = "https://bucket.s3.amazonaws.com/obj-001?sig=abc"
	const wantCT = "video/mp2t"
	var gotCT, gotBucket, gotKey string

	store := newTestStore(nil, &mockPresignAPI{
		putFn: func(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error) {
			gotCT = aws.ToString(in.ContentType)
			gotBucket = aws.ToString(in.Bucket)
			gotKey = aws.ToString(in.Key)
			return presignedReq(wantURL, http.MethodPut), nil
		},
	})

	gotURL, err := store.GenerateUploadURL(context.Background(), "obj-001", wantCT)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotURL != wantURL {
		t.Errorf("URL: got %q, want %q", gotURL, wantURL)
	}
	if gotCT != wantCT {
		t.Errorf("ContentType forwarded: got %q, want %q", gotCT, wantCT)
	}
	if gotBucket != "test-bucket" {
		t.Errorf("Bucket: got %q, want %q", gotBucket, "test-bucket")
	}
	if gotKey != "obj-001" {
		t.Errorf("Key: got %q, want %q", gotKey, "obj-001")
	}
}

// TC-OBJ-02: GenerateUploadURL SDK error — returns wrapped error.
func TestGenerateUploadURL_Error(t *testing.T) {
	store := newTestStore(nil, &mockPresignAPI{
		putFn: func(_ context.Context, _ *s3.PutObjectInput, _ ...func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error) {
			return nil, errors.New("signing failed")
		},
	})

	_, err := store.GenerateUploadURL(context.Background(), "obj-001", "video/mp2t")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "objectstore: GenerateUploadURL") {
		t.Errorf("error prefix missing: %v", err)
	}
}

// TC-OBJ-03: GenerateDownloadURL success — returns URL and forwards bucket, key to SDK.
func TestGenerateDownloadURL_Success(t *testing.T) {
	const wantURL = "https://bucket.s3.amazonaws.com/obj-001?sig=abc"
	var gotBucket, gotKey string

	store := newTestStore(nil, &mockPresignAPI{
		getFn: func(_ context.Context, in *s3.GetObjectInput, _ ...func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error) {
			gotBucket = aws.ToString(in.Bucket)
			gotKey = aws.ToString(in.Key)
			return presignedReq(wantURL, http.MethodGet), nil
		},
	})

	gotURL, err := store.GenerateDownloadURL(context.Background(), "obj-001")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotURL != wantURL {
		t.Errorf("URL: got %q, want %q", gotURL, wantURL)
	}
	if gotBucket != "test-bucket" {
		t.Errorf("Bucket: got %q, want %q", gotBucket, "test-bucket")
	}
	if gotKey != "obj-001" {
		t.Errorf("Key: got %q, want %q", gotKey, "obj-001")
	}
}

// TC-OBJ-04: GenerateDownloadURL SDK error — returns wrapped error.
func TestGenerateDownloadURL_Error(t *testing.T) {
	store := newTestStore(nil, &mockPresignAPI{
		getFn: func(_ context.Context, _ *s3.GetObjectInput, _ ...func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error) {
			return nil, errors.New("signing failed")
		},
	})

	_, err := store.GenerateDownloadURL(context.Background(), "obj-001")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "objectstore: GenerateDownloadURL") {
		t.Errorf("error prefix missing: %v", err)
	}
}

// TC-OBJ-05: DeleteObjects empty/nil slice — no SDK call, nil returned.
func TestDeleteObjects_Empty(t *testing.T) {
	for _, ids := range [][]string{nil, {}} {
		called := false
		store := newTestStore(&mockS3API{
			deleteObjectsFn: func(_ context.Context, _ *s3.DeleteObjectsInput, _ ...func(*s3.Options)) (*s3.DeleteObjectsOutput, error) {
				called = true
				return nil, nil
			},
		}, nil)

		if err := store.DeleteObjects(context.Background(), ids); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if called {
			t.Errorf("expected no SDK call for %v slice", ids)
		}
	}
}

// TC-OBJ-06: DeleteObjects ≤1000 items — single batch.
func TestDeleteObjects_SingleBatch(t *testing.T) {
	ids := make([]string, 500)
	for i := range ids {
		ids[i] = fmt.Sprintf("obj-%d", i)
	}

	var gotSize int
	store := newTestStore(&mockS3API{
		deleteObjectsFn: func(_ context.Context, in *s3.DeleteObjectsInput, _ ...func(*s3.Options)) (*s3.DeleteObjectsOutput, error) {
			gotSize = len(in.Delete.Objects)
			return &s3.DeleteObjectsOutput{}, nil
		},
	}, nil)

	if err := store.DeleteObjects(context.Background(), ids); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotSize != 500 {
		t.Errorf("batch size: got %d, want 500", gotSize)
	}
}

// TC-OBJ-07: DeleteObjects >1000 items — multiple batches with correct splits and correct bucket.
func TestDeleteObjects_MultiBatch(t *testing.T) {
	ids := make([]string, 2500)
	for i := range ids {
		ids[i] = fmt.Sprintf("obj-%d", i)
	}

	var callSizes []int
	var gotBuckets []string
	store := newTestStore(&mockS3API{
		deleteObjectsFn: func(_ context.Context, in *s3.DeleteObjectsInput, _ ...func(*s3.Options)) (*s3.DeleteObjectsOutput, error) {
			callSizes = append(callSizes, len(in.Delete.Objects))
			gotBuckets = append(gotBuckets, aws.ToString(in.Bucket))
			return &s3.DeleteObjectsOutput{}, nil
		},
	}, nil)

	if err := store.DeleteObjects(context.Background(), ids); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []int{1000, 1000, 500}
	if len(callSizes) != len(want) {
		t.Fatalf("call count: got %d, want %d", len(callSizes), len(want))
	}
	for i, w := range want {
		if callSizes[i] != w {
			t.Errorf("batch[%d] size: got %d, want %d", i, callSizes[i], w)
		}
		if gotBuckets[i] != "test-bucket" {
			t.Errorf("batch[%d] bucket: got %q, want %q", i, gotBuckets[i], "test-bucket")
		}
	}
}

// TC-OBJ-08: DeleteObjects first batch fails — error returned, subsequent batches not called.
func TestDeleteObjects_BatchError(t *testing.T) {
	ids := make([]string, 1500)
	for i := range ids {
		ids[i] = fmt.Sprintf("obj-%d", i)
	}

	calls := 0
	store := newTestStore(&mockS3API{
		deleteObjectsFn: func(_ context.Context, _ *s3.DeleteObjectsInput, _ ...func(*s3.Options)) (*s3.DeleteObjectsOutput, error) {
			calls++
			return nil, errors.New("S3 error")
		},
	}, nil)

	err := store.DeleteObjects(context.Background(), ids)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "objectstore: DeleteObjects") {
		t.Errorf("error prefix missing: %v", err)
	}
	if calls != 1 {
		t.Errorf("expected 1 SDK call, got %d", calls)
	}
}

// TC-OBJ-15: DeleteObjects partial S3 error (HTTP 200 with Errors in output) — returns error.
func TestDeleteObjects_PartialError(t *testing.T) {
	store := newTestStore(&mockS3API{
		deleteObjectsFn: func(_ context.Context, _ *s3.DeleteObjectsInput, _ ...func(*s3.Options)) (*s3.DeleteObjectsOutput, error) {
			return &s3.DeleteObjectsOutput{
				Errors: []types.Error{{
					Key:     aws.String("obj-001"),
					Message: aws.String("Access Denied"),
				}},
			}, nil
		},
	}, nil)

	err := store.DeleteObjects(context.Background(), []string{"obj-001"})
	if err == nil {
		t.Fatal("expected error for partial S3 delete failure")
	}
	if !strings.Contains(err.Error(), "obj-001") {
		t.Errorf("error should reference failed key: %v", err)
	}
	if !strings.Contains(err.Error(), "Access Denied") {
		t.Errorf("error should include S3 message: %v", err)
	}
}

// TC-OBJ-09: HealthCheck success — nil returned.
func TestHealthCheck_Success(t *testing.T) {
	store := newTestStore(&mockS3API{
		headBucketFn: func(_ context.Context, _ *s3.HeadBucketInput, _ ...func(*s3.Options)) (*s3.HeadBucketOutput, error) {
			return &s3.HeadBucketOutput{}, nil
		},
	}, nil)

	if err := store.HealthCheck(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TC-OBJ-10: HealthCheck error — returns wrapped error.
func TestHealthCheck_Error(t *testing.T) {
	store := newTestStore(&mockS3API{
		headBucketFn: func(_ context.Context, _ *s3.HeadBucketInput, _ ...func(*s3.Options)) (*s3.HeadBucketOutput, error) {
			return nil, errors.New("bucket not found")
		},
	}, nil)

	err := store.HealthCheck(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "objectstore: HealthCheck") {
		t.Errorf("error prefix missing: %v", err)
	}
}

// TC-OBJ-11: NewS3Store with static credentials.
func TestNewS3Store_StaticCreds(t *testing.T) {
	cfg := &config.Config{
		ObjectStoreRegion:          "us-east-1",
		ObjectStoreBucket:          "test-bucket",
		ObjectStorePresignExpiry:   time.Hour,
		ObjectStoreAccessKeyID:     "test-key",
		ObjectStoreSecretAccessKey: "test-secret",
	}
	store, err := NewS3Store(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if store == nil {
		t.Fatal("expected non-nil store")
	}
}

// TC-OBJ-12: NewS3Store with default credential chain (no access key set).
func TestNewS3Store_DefaultChain(t *testing.T) {
	cfg := &config.Config{
		ObjectStoreRegion:        "us-east-1",
		ObjectStoreBucket:        "test-bucket",
		ObjectStorePresignExpiry: time.Hour,
	}
	store, err := NewS3Store(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if store == nil {
		t.Fatal("expected non-nil store")
	}
}

// TC-OBJ-13: NewS3Store with custom endpoint (path-style forced for MinIO etc.).
func TestNewS3Store_CustomEndpoint(t *testing.T) {
	cfg := &config.Config{
		ObjectStoreRegion:          "us-east-1",
		ObjectStoreBucket:          "test-bucket",
		ObjectStorePresignExpiry:   time.Hour,
		ObjectStoreEndpoint:        "http://localhost:9000",
		ObjectStoreAccessKeyID:     "minioadmin",
		ObjectStoreSecretAccessKey: "minioadmin",
	}
	store, err := NewS3Store(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if store == nil {
		t.Fatal("expected non-nil store")
	}
}

// TC-OBJ-14: NewS3Store config load failure — returns wrapped error.
func TestNewS3Store_ConfigError(t *testing.T) {
	orig := loadDefaultConfig
	defer func() { loadDefaultConfig = orig }()
	loadDefaultConfig = func(_ context.Context, _ ...func(*awsconfig.LoadOptions) error) (aws.Config, error) {
		return aws.Config{}, errors.New("config load failed")
	}

	cfg := &config.Config{
		ObjectStoreRegion:        "us-east-1",
		ObjectStoreBucket:        "test-bucket",
		ObjectStorePresignExpiry: time.Hour,
	}
	_, err := NewS3Store(cfg)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "objectstore: load config") {
		t.Errorf("error prefix missing: %v", err)
	}
}
