//go:build integration || perf

// Package objtest starts a MinIO testcontainer for integration and perf
// tests.
package objtest

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/testcontainers/testcontainers-go"
	tcwait "github.com/testcontainers/testcontainers-go/wait"
)

// Credentials of every MinIO container that this package starts.
const (
	AccessKey = "minioadmin"
	SecretKey = "minioadmin"
)

// MinIO is a running container with Bucket created. Endpoint is
// `http://host:port`.
type MinIO struct {
	Endpoint  string
	Bucket    string
	Client    *s3.Client
	terminate func()
}

// StartMinIO starts a container and creates bucket. t.Cleanup stops the
// container. -short skips the test.
func StartMinIO(t *testing.T, bucket string) *MinIO {
	t.Helper()
	if testing.Short() {
		t.Skip("objtest: -short mode skips MinIO containers")
	}
	m, err := StartMinIOE(context.Background(), bucket)
	if err != nil {
		t.Fatalf("objtest.StartMinIO: %v", err)
	}
	t.Cleanup(m.Close)
	return m
}

// StartMinIOE is StartMinIO for TestMain. The caller must call Close.
func StartMinIOE(ctx context.Context, bucket string) (*MinIO, error) {
	req := testcontainers.ContainerRequest{
		Image: "minio/minio:latest",
		Cmd:   []string{"server", "/data"},
		Env: map[string]string{
			"MINIO_ROOT_USER":     AccessKey,
			"MINIO_ROOT_PASSWORD": SecretKey,
		},
		ExposedPorts: []string{"9000/tcp"},
		WaitingFor:   tcwait.ForListeningPort("9000/tcp").WithStartupTimeout(60 * time.Second),
	}
	ctr, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		return nil, fmt.Errorf("start minio: %w", err)
	}
	host, err := ctr.Host(ctx)
	if err != nil {
		_ = ctr.Terminate(ctx)
		return nil, fmt.Errorf("host: %w", err)
	}
	port, err := ctr.MappedPort(ctx, "9000")
	if err != nil {
		_ = ctr.Terminate(ctx)
		return nil, fmt.Errorf("port: %w", err)
	}
	endpoint := "http://" + host + ":" + port.Port()

	cfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion("us-east-1"),
		awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(AccessKey, SecretKey, ""),
		),
	)
	if err != nil {
		_ = ctr.Terminate(ctx)
		return nil, fmt.Errorf("aws config: %w", err)
	}
	cli := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = true
	})
	if _, err := cli.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket: aws.String(bucket),
	}); err != nil {
		_ = ctr.Terminate(ctx)
		return nil, fmt.Errorf("create bucket %q: %w", bucket, err)
	}

	return &MinIO{
		Endpoint: endpoint,
		Bucket:   bucket,
		Client:   cli,
		terminate: func() {
			_ = ctr.Terminate(context.Background())
		},
	}, nil
}

// Close stops the container.
func (m *MinIO) Close() {
	if m == nil || m.terminate == nil {
		return
	}
	m.terminate()
	m.terminate = nil
}
