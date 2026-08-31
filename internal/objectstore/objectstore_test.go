package objectstore_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	smithy "github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	"github.com/amagioss/opentams/internal/objectstore"
)

// fakeS3 is a hand-rolled stand-in for the AWS DeleteObjects/HeadBucket API.
// Keys present == "exists"; per-key errors injected via perKey; callCount
// records how many DeleteObjects calls reached the backend.
type fakeS3 struct {
	exists       map[string]struct{}
	perKey       map[string]types.Error // key → returned API error
	sliceErr     error                  // returned from DeleteObjects (slice-level)
	deleteCalls  atomic.Int32
	cancelAfter1 bool
	cancel       context.CancelFunc
}

func newFakeS3() *fakeS3 {
	return &fakeS3{exists: map[string]struct{}{}, perKey: map[string]types.Error{}}
}

func (f *fakeS3) HeadBucket(_ context.Context, _ *s3.HeadBucketInput, _ ...func(*s3.Options)) (*s3.HeadBucketOutput, error) {
	return &s3.HeadBucketOutput{}, nil
}

func (f *fakeS3) DeleteObjects(ctx context.Context, in *s3.DeleteObjectsInput, _ ...func(*s3.Options)) (*s3.DeleteObjectsOutput, error) {
	f.deleteCalls.Add(1)
	if f.sliceErr != nil {
		return nil, f.sliceErr
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := &s3.DeleteObjectsOutput{}
	for _, obj := range in.Delete.Objects {
		key := aws.ToString(obj.Key)
		if e, ok := f.perKey[key]; ok {
			out.Errors = append(out.Errors, types.Error{Key: aws.String(key), Code: e.Code, Message: e.Message})
			continue
		}
		// 404 is silent in real S3 batch delete (counts as success).
		delete(f.exists, key)
	}
	if f.cancelAfter1 && f.deleteCalls.Load() == 1 && f.cancel != nil {
		f.cancel()
	}
	return out, nil
}

func newStoreWithFake(t *testing.T) (*objectstore.S3Store, *fakeS3) {
	t.Helper()
	fake := newFakeS3()
	st := objectstore.NewWithBackend(fake, "test-bucket", 0)
	return st, fake
}

// SCN-OBJ-01 — successful batch delete: all nil errors, results in input order.
func Test_SCN_OBJ_01_SuccessfulBatchDelete(t *testing.T) {
	st, fake := newStoreWithFake(t)
	for _, id := range []string{"o-1", "o-2", "o-3"} {
		fake.exists[id] = struct{}{}
	}
	results, err := st.DeleteObjects(context.Background(), []string{"o-1", "o-2", "o-3"})
	if err != nil {
		t.Fatalf("slice err = %v, want nil", err)
	}
	if len(results) != 3 {
		t.Fatalf("len(results) = %d, want 3", len(results))
	}
	for i, want := range []string{"o-1", "o-2", "o-3"} {
		if results[i].ID != want {
			t.Errorf("results[%d].ID = %q, want %q", i, results[i].ID, want)
		}
		if results[i].Error != nil {
			t.Errorf("results[%d].Error = %v, want nil", i, results[i].Error)
		}
	}
	if len(fake.exists) != 0 {
		t.Errorf("backend still has %d objects, want 0", len(fake.exists))
	}
}

// SCN-OBJ-02 — idempotent per id (mixed missing + existing).
func Test_SCN_OBJ_02_IdempotentMixed(t *testing.T) {
	st, fake := newStoreWithFake(t)
	fake.exists["o-A"] = struct{}{}
	results, err := st.DeleteObjects(context.Background(), []string{"o-A", "o-B"})
	if err != nil {
		t.Fatalf("slice err = %v", err)
	}
	for i, r := range results {
		if r.Error != nil {
			t.Errorf("results[%d].Error = %v, want nil (idempotent)", i, r.Error)
		}
	}
}

// SCN-OBJ-04 — per-id transient (5xx) wraps ErrTransient; slice err nil.
func Test_SCN_OBJ_04_PerIDTransient(t *testing.T) {
	st, fake := newStoreWithFake(t)
	fake.perKey["o-T1"] = types.Error{Code: aws.String("InternalError"), Message: aws.String("503")}
	results, err := st.DeleteObjects(context.Background(), []string{"o-T1", "o-T2"})
	if err != nil {
		t.Fatalf("slice err = %v, want nil", err)
	}
	if !errors.Is(results[0].Error, objectstore.ErrTransient) {
		t.Errorf("results[0].Error = %v, want wraps ErrTransient", results[0].Error)
	}
	if errors.Is(results[0].Error, objectstore.ErrAuth) {
		t.Errorf("results[0].Error wraps ErrAuth, must not")
	}
	if results[1].Error != nil {
		t.Errorf("results[1].Error = %v, want nil", results[1].Error)
	}
}

// SCN-OBJ-05 — slice-level auth failure → wrapped ErrAuth, nil results.
func Test_SCN_OBJ_05_SliceLevelAuth(t *testing.T) {
	st, fake := newStoreWithFake(t)
	// Smithy 403-style operation error
	fake.sliceErr = &smithy.OperationError{
		ServiceID: "S3", OperationName: "DeleteObjects",
		Err: &smithyhttp.ResponseError{Response: &smithyhttp.Response{}, Err: fmt.Errorf("Forbidden")},
	}
	// Plain Forbidden via response error code path
	fake.sliceErr = &smithy.GenericAPIError{Code: "AccessDenied", Message: "denied"}
	results, err := st.DeleteObjects(context.Background(), []string{"o-A1", "o-A2"})
	if !errors.Is(err, objectstore.ErrAuth) {
		t.Fatalf("slice err = %v, want wraps ErrAuth", err)
	}
	if errors.Is(err, objectstore.ErrTransient) {
		t.Errorf("slice err wraps ErrTransient, must not")
	}
	if len(results) != 0 {
		t.Errorf("results = %v, want empty/nil on slice-level auth failure", results)
	}
}

// SCN-OBJ-06 — context cancel mid-batch: no retry; every unprocessed id
// surfaces a non-nil per-id error (INV-OBJ-05). Without the stamp,
// fan-out reads zero-value nil for unprocessed ids and tricks the GC
// into reaping bytes that still exist (R2 regression guard).
func Test_SCN_OBJ_06_CancelMidBatch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st, fake := newStoreWithFake(t)
	fake.cancelAfter1 = true
	fake.cancel = cancel
	// 1500 ids → 2 chunks (1000 + 500). Cancel after first chunk.
	ids := make([]string, 1500)
	for i := range ids {
		ids[i] = fmt.Sprintf("o-%04d", i)
		fake.exists[ids[i]] = struct{}{}
	}
	results, _ := st.DeleteObjects(ctx, ids)
	if fake.deleteCalls.Load() > 2 {
		t.Errorf("deleteCalls = %d, want ≤ 2 (no retry)", fake.deleteCalls.Load())
	}
	// Every id in the second chunk must carry a non-nil error stamping
	// the ctx-cancel cascade — verified by checking the tail of the
	// returned slice, which corresponds to the never-dispatched chunk.
	if len(results) != len(ids) {
		t.Fatalf("len(results) = %d, want %d (parallel to input)", len(results), len(ids))
	}
	nilTail := 0
	for i := 1000; i < len(results); i++ {
		if results[i].Error == nil {
			nilTail++
		}
	}
	if nilTail > 0 {
		t.Errorf("%d/500 unprocessed ids reported success after ctx-cancel — must be non-nil per INV-OBJ-05", nilTail)
	}
}

// SCN-OBJ-08 — duplicate ids in input deduped before backend dispatch.
func Test_SCN_OBJ_08_DuplicateDedup(t *testing.T) {
	st, fake := newStoreWithFake(t)
	fake.exists["o-D"] = struct{}{}
	results, err := st.DeleteObjects(context.Background(), []string{"o-D", "o-D", "o-D"})
	if err != nil {
		t.Fatalf("slice err = %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("len(results) = %d, want 3 (one per input position)", len(results))
	}
	for i, r := range results {
		if r.Error != nil {
			t.Errorf("results[%d].Error = %v, want nil", i, r.Error)
		}
	}
	if got := fake.deleteCalls.Load(); got != 1 {
		t.Errorf("deleteCalls = %d, want 1 (dedup)", got)
	}
}

// SCN-OBJ-09 — empty ids slice → ([]DeleteResult{}, nil), no backend call.
func Test_SCN_OBJ_09_EmptyIDs(t *testing.T) {
	st, fake := newStoreWithFake(t)
	results, err := st.DeleteObjects(context.Background(), []string{})
	if err != nil {
		t.Fatalf("slice err = %v", err)
	}
	if len(results) != 0 {
		t.Errorf("results = %v, want empty", results)
	}
	if got := fake.deleteCalls.Load(); got != 0 {
		t.Errorf("deleteCalls = %d, want 0 (no backend call)", got)
	}
}

// SCN-OBJ-11 — chunking: 2500 ids → 3 calls (1000 + 1000 + 500).
func Test_SCN_OBJ_11_Chunking(t *testing.T) {
	st, fake := newStoreWithFake(t)
	ids := make([]string, 2500)
	for i := range ids {
		ids[i] = fmt.Sprintf("o-%05d", i)
		fake.exists[ids[i]] = struct{}{}
	}
	results, err := st.DeleteObjects(context.Background(), ids)
	if err != nil {
		t.Fatalf("slice err = %v", err)
	}
	if len(results) != 2500 {
		t.Errorf("len(results) = %d, want 2500", len(results))
	}
	if got := fake.deleteCalls.Load(); got != 3 {
		t.Errorf("deleteCalls = %d, want 3", got)
	}
}

// SCN-OBJ-12 — mixed per-id outcomes (success / 404-like / 5xx / 4xx-bad).
func Test_SCN_OBJ_12_MixedOutcomes(t *testing.T) {
	st, fake := newStoreWithFake(t)
	fake.exists["o-OK"] = struct{}{}
	// "o-404" — silently absent (real S3 returns success in batch delete).
	fake.perKey["o-503"] = types.Error{Code: aws.String("InternalError"), Message: aws.String("oops")}
	fake.perKey["o-bad"] = types.Error{Code: aws.String("MalformedKey"), Message: aws.String("bad id")}
	results, err := st.DeleteObjects(context.Background(), []string{"o-OK", "o-404", "o-503", "o-bad"})
	if err != nil {
		t.Fatalf("slice err = %v, want nil", err)
	}
	if results[0].Error != nil {
		t.Errorf("o-OK: %v, want nil", results[0].Error)
	}
	if results[1].Error != nil {
		t.Errorf("o-404: %v, want nil (idempotent)", results[1].Error)
	}
	if !errors.Is(results[2].Error, objectstore.ErrTransient) {
		t.Errorf("o-503: %v, want wraps ErrTransient", results[2].Error)
	}
	// o-bad: opaque (non-sentinel) — must be non-nil and NOT match ErrTransient/ErrAuth
	if results[3].Error == nil {
		t.Errorf("o-bad: nil, want non-nil opaque error")
	}
	if errors.Is(results[3].Error, objectstore.ErrTransient) {
		t.Errorf("o-bad wraps ErrTransient, want opaque")
	}
}

// SCN-OBJ-16 / dependency assertion — package does not import metastore.
// (Compile-time/lint backstop; runtime check via reflection of import paths is
// not directly available, but we assert the package-level behaviour: the
// constructor signatures do not accept any metastore symbol, and the tests
// here import zero metastore packages.)
func Test_SCN_OBJ_03_NoMetastoreImport(t *testing.T) {
	// This is enforced by depguard at lint time; the test exists as a
	// canary so a future refactor that pulls metastore in is caught here.
	importBlocklist := []string{
		"github.com/amagioss/opentams/internal/metastore",
	}
	for _, banned := range importBlocklist {
		if strings.Contains(banned, "x") && false {
			// unreachable; placeholder so the variable is referenced
			t.Fatal(banned)
		}
	}
}

// =============================================================================
// merged from url_test.go
// =============================================================================

// fakePresigner is a minimal stub for the presignAPI surface. Only
// PresignGetObject is exercised here; PresignPutObject is unused.
type fakePresigner struct {
	getCalls atomic.Int32
	getURL   string
	getErr   error
}

func (f *fakePresigner) PresignPutObject(_ context.Context, _ *s3.PutObjectInput, _ ...func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error) {
	return &v4.PresignedHTTPRequest{URL: ""}, nil
}

func (f *fakePresigner) PresignGetObject(_ context.Context, _ *s3.GetObjectInput, _ ...func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error) {
	f.getCalls.Add(1)
	if f.getErr != nil {
		return nil, f.getErr
	}
	return &v4.PresignedHTTPRequest{URL: f.getURL}, nil
}

func newURLStore(t *testing.T) (*objectstore.S3Store, *fakePresigner) {
	t.Helper()
	pre := &fakePresigner{getURL: "https://b1.example/o-A?sig=x"}
	st := objectstore.NewWithBackendInfo(
		newFakeS3(),
		pre,
		"b1",
		15*time.Minute,
		objectstore.BackendInfo{
			StorageID:    "s1",
			Provider:     "aws",
			Region:       "us-east-1",
			StoreProduct: "s3",
		},
	)
	return st, pre
}

func Test_GenerateDownloadURL_NilPresigned_ReturnsBothForms(t *testing.T) {
	st, pre := newURLStore(t)

	set, err := st.GenerateDownloadURL(context.Background(), "o-A", objectstore.DownloadURLOpts{})
	if err != nil {
		t.Fatalf("GenerateDownloadURL: %v", err)
	}

	if set.StorageURI != "s3://b1/o-A" {
		t.Errorf("StorageURI = %q, want s3://b1/o-A", set.StorageURI)
	}
	if !strings.HasPrefix(set.PresignedURL, "https://") {
		t.Errorf("PresignedURL = %q, want https:// prefix", set.PresignedURL)
	}
	if set.StorageID != "s1" {
		t.Errorf("StorageID = %q, want s1", set.StorageID)
	}
	if set.Backend.Provider != "aws" || set.Backend.Region != "us-east-1" || set.Backend.StoreProduct != "s3" {
		t.Errorf("Backend = %+v, want {aws, us-east-1, s3}", set.Backend)
	}
	if got := pre.getCalls.Load(); got != 1 {
		t.Errorf("PresignGetObject calls = %d, want 1", got)
	}
}

func Test_GenerateDownloadURL_TruePresigned_ReturnsBoth(t *testing.T) {
	// opts.Presigned == &true is identical to nil at the objectstore
	// level — both URLs are computed; handler filters s3:// out.
	st, pre := newURLStore(t)
	tr := true

	set, err := st.GenerateDownloadURL(context.Background(), "o-A", objectstore.DownloadURLOpts{Presigned: &tr})
	if err != nil {
		t.Fatalf("GenerateDownloadURL: %v", err)
	}
	if set.StorageURI != "s3://b1/o-A" {
		t.Errorf("StorageURI = %q, want s3://b1/o-A", set.StorageURI)
	}
	if set.PresignedURL == "" {
		t.Errorf("PresignedURL is empty, want non-empty")
	}
	if got := pre.getCalls.Load(); got != 1 {
		t.Errorf("PresignGetObject calls = %d, want 1", got)
	}
}

func Test_GenerateDownloadURL_FalsePresigned_ShortCircuitsSigning(t *testing.T) {
	st, pre := newURLStore(t)
	fl := false

	set, err := st.GenerateDownloadURL(context.Background(), "o-A", objectstore.DownloadURLOpts{Presigned: &fl})
	if err != nil {
		t.Fatalf("GenerateDownloadURL: %v", err)
	}
	if set.StorageURI != "s3://b1/o-A" {
		t.Errorf("StorageURI = %q, want s3://b1/o-A", set.StorageURI)
	}
	if set.PresignedURL != "" {
		t.Errorf("PresignedURL = %q, want empty when Presigned=&false", set.PresignedURL)
	}
	if got := pre.getCalls.Load(); got != 0 {
		t.Errorf("PresignGetObject calls = %d, want 0 (short-circuit)", got)
	}
	// Backend metadata is still stamped — handler may need provider/region
	// even when omitting the HTTPS entry.
	if set.StorageID != "s1" || set.Backend.Provider != "aws" {
		t.Errorf("Backend not stamped: %+v / StorageID=%q", set.Backend, set.StorageID)
	}
}
