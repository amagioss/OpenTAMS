//go:build perf

// Performance scenarios for the objectstore slice. Gated behind the
// `perf` build tag — `go test -tags=perf -run Perf
// ./internal/objectstore/...`. Each test function declares its own
// budget in its name and header comment (PERF-01..02).
//
// Backend: MinIO testcontainer (S3-compatible) — the existing fakeS3
// is in-process and would not exercise the AWS SDK marshalling that
// dominates batch-delete latency.

package objectstore_test

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/testcontainers/testcontainers-go"
	tcwait "github.com/testcontainers/testcontainers-go/wait"

	"github.com/amagioss/opentams/internal/objectstore"
	"github.com/amagioss/opentams/internal/perftest"
)

const (
	objPerfBucket = "opentams-perf"
)

var (
	perfS3Client *s3.Client
)

func TestMain(m *testing.M) { os.Exit(runObjPerfMain(m)) }

func runObjPerfMain(m *testing.M) int {
	flag.Parse()
	if testing.Short() {
		return m.Run()
	}
	cli, cleanup, err := setupMinIO(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"objectstore perf: MinIO setup failed: %v (perf tests will skip)\n", err)
		return m.Run()
	}
	defer cleanup()
	perfS3Client = cli
	return m.Run()
}

// setupMinIO spins up a `minio/minio:latest` container, waits for it,
// builds an S3 client pointed at it, and creates the test bucket.
func setupMinIO(ctx context.Context) (*s3.Client, func(), error) {
	const accessKey = "minioadmin"
	const secretKey = "minioadmin"
	req := testcontainers.ContainerRequest{
		Image: "minio/minio:latest",
		Cmd:   []string{"server", "/data"},
		Env: map[string]string{
			"MINIO_ROOT_USER":     accessKey,
			"MINIO_ROOT_PASSWORD": secretKey,
		},
		ExposedPorts: []string{"9000/tcp"},
		WaitingFor:   tcwait.ForListeningPort("9000/tcp").WithStartupTimeout(60 * time.Second),
	}
	ctr, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("start minio: %w", err)
	}
	host, err := ctr.Host(ctx)
	if err != nil {
		_ = ctr.Terminate(ctx)
		return nil, nil, fmt.Errorf("host: %w", err)
	}
	port, err := ctr.MappedPort(ctx, "9000")
	if err != nil {
		_ = ctr.Terminate(ctx)
		return nil, nil, fmt.Errorf("port: %w", err)
	}
	endpoint := "http://" + host + ":" + port.Port()

	cfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion("us-east-1"),
		awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(accessKey, secretKey, ""),
		),
	)
	if err != nil {
		_ = ctr.Terminate(ctx)
		return nil, nil, fmt.Errorf("aws config: %w", err)
	}
	cli := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = true
	})
	if _, err := cli.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket: aws.String(objPerfBucket),
	}); err != nil {
		_ = ctr.Terminate(ctx)
		return nil, nil, fmt.Errorf("create bucket: %w", err)
	}

	cleanup := func() { _ = ctr.Terminate(context.Background()) }
	return cli, cleanup, nil
}

func requireS3(t *testing.T) *s3.Client {
	t.Helper()
	if perfS3Client == nil {
		t.Skip("perf: MinIO container not available")
	}
	return perfS3Client
}

// putN uploads `n` empty objects with key `prefix-i`. Returns the keys.
func putN(t *testing.T, prefix string, n int) []string {
	t.Helper()
	cli := perfS3Client
	keys := make([]string, n)
	ctx := context.Background()
	for i := 0; i < n; i++ {
		keys[i] = prefix + "-" + strconv.Itoa(i)
		if _, err := cli.PutObject(ctx, &s3.PutObjectInput{
			Bucket: aws.String(objPerfBucket),
			Key:    aws.String(keys[i]),
		}); err != nil {
			t.Fatalf("PutObject[%d]: %v", i, err)
		}
	}
	return keys
}

// =============================================================================
// SCN-OBJ-PERF-01 — DeleteObjects 1000-id batch p99 ≤ 1s
// =============================================================================
func Test_SCN_OBJ_PERF_01_BatchP99(t *testing.T) {
	perftest.SkipIfShort(t)
	requireS3(t)

	const batchSize = 1000
	const batches = 100
	st := objectstore.NewWithBackend(perfS3Client, objPerfBucket, 0)

	// 100 batches × 1000 = 100k pre-loaded keys.
	allKeys := putN(t, "pf01", batches*batchSize)

	samples := make([]time.Duration, batches)
	for b := 0; b < batches; b++ {
		batch := allKeys[b*batchSize : (b+1)*batchSize]
		t0 := time.Now()
		if _, err := st.DeleteObjects(context.Background(), batch); err != nil {
			t.Fatalf("DeleteObjects[%d]: %v", b, err)
		}
		samples[b] = time.Since(t0)
	}
	got := perftest.P99(samples)
	budget := perftest.EnvOverrideMS("OPENTAMS_PERF_OBJ_BATCH_P99_MS", 1000)
	if got > budget {
		t.Errorf("p99 = %v, want ≤ %v", got, budget)
	}
}

// =============================================================================
// SCN-OBJ-PERF-02 — Sustained throughput ≥ 1000 ids/sec
// =============================================================================
func Test_SCN_OBJ_PERF_02_SustainedThroughput(t *testing.T) {
	perftest.SkipIfShort(t)
	requireS3(t)

	const batchSize = 1000
	const batches = 100
	const total = batches * batchSize
	st := objectstore.NewWithBackend(perfS3Client, objPerfBucket, 0)

	allKeys := putN(t, "pf02", total)

	t0 := time.Now()
	for b := 0; b < batches; b++ {
		batch := allKeys[b*batchSize : (b+1)*batchSize]
		if _, err := st.DeleteObjects(context.Background(), batch); err != nil {
			t.Fatalf("DeleteObjects[%d]: %v", b, err)
		}
	}
	elapsed := time.Since(t0)
	rate := float64(total) / elapsed.Seconds()
	want := perftest.EnvOverrideQPS("OPENTAMS_PERF_OBJ_BULK_QPS", 1000)
	if rate < want {
		t.Errorf("rate = %.1f ids/sec, want ≥ %.1f (elapsed=%v)", rate, want, elapsed)
	}
}
