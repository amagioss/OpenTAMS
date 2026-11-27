//go:build integration

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	tcwait "github.com/testcontainers/testcontainers-go/wait"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"github.com/amagioss/opentams/internal/config"
	"github.com/amagioss/opentams/internal/objtest"
)

var (
	gcMinIOOnce sync.Once
	gcMinIO     *objtest.MinIO
	gcMinIOErr  error
	gcBucketSeq atomic.Int64
)

// sharedMinIO starts one MinIO container for the package. TestMain stops it.
func sharedMinIO(t *testing.T) *objtest.MinIO {
	t.Helper()
	gcMinIOOnce.Do(func() {
		gcMinIO, gcMinIOErr = objtest.StartMinIOE(context.Background(), "gc-shared")
	})
	require.NoError(t, gcMinIOErr, "start MinIO")
	return gcMinIO
}

func stopSharedMinIO() {
	if gcMinIO != nil {
		gcMinIO.Close()
	}
}

// gcEnv is a migrated database of its own, a bucket of its own on the
// shared MinIO, and the configuration that config.Load reads for them.
type gcEnv struct {
	cfg    *config.Config
	pool   *pgxpool.Pool
	s3     *s3.Client
	bucket string
}

// newGCEnv sets the environment of test plan section 7 and loads it.
func newGCEnv(t *testing.T) *gcEnv {
	t.Helper()
	ctx := context.Background()
	freshMigrateDB(t)
	migrateUp(t)

	mn := sharedMinIO(t)
	bucket := fmt.Sprintf("gc-%d", gcBucketSeq.Add(1))
	_, err := mn.Client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err, "create bucket")

	t.Setenv("OBJECT_STORE_BUCKET", bucket)
	t.Setenv("OBJECT_STORE_REGION", "us-east-1")
	t.Setenv("OBJECT_STORE_ENDPOINT", mn.Endpoint)
	t.Setenv("OBJECT_STORE_ACCESS_KEY_ID", objtest.AccessKey)
	t.Setenv("OBJECT_STORE_SECRET_ACCESS_KEY", objtest.SecretKey)
	t.Setenv("STORAGE_BACKEND_PROVIDER", "s3")
	t.Setenv("SERVER_PORT", strconv.Itoa(freePort(t)))

	cfg, err := config.Load()
	require.NoError(t, err, "config.Load")

	pool, err := pgxpool.New(ctx, fmt.Sprintf("host=%s port=%d dbname=%s user=%s password=%s sslmode=disable",
		cfg.DBHost, cfg.DBPort, cfg.DBName, cfg.DBUser, cfg.DBPassword))
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	return &gcEnv{cfg: cfg, pool: pool, s3: mn.Client, bucket: bucket}
}

func migrateUp(t *testing.T) {
	t.Helper()
	root := newRootCmd()
	root.SetArgs([]string{"migrate", "up"})
	require.NoError(t, root.Execute(), "migrate up")
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close())
	return port
}

func observedLogger() (*zap.Logger, *observer.ObservedLogs) {
	core, logs := observer.New(zap.DebugLevel)
	return zap.New(core), logs
}

func (e *gcEnv) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	_, err := e.pool.Exec(context.Background(), sql, args...)
	require.NoError(t, err, sql)
}

func (e *gcEnv) rowExists(t *testing.T, table, id string) bool {
	t.Helper()
	var ok bool
	require.NoError(t, e.pool.QueryRow(context.Background(),
		`SELECT EXISTS (SELECT 1 FROM `+table+` WHERE id = $1)`, id).Scan(&ok))
	return ok
}

func (e *gcEnv) putKey(t *testing.T, key string) {
	t.Helper()
	_, err := e.s3.PutObject(context.Background(), &s3.PutObjectInput{
		Bucket: aws.String(e.bucket), Key: aws.String(key), Body: strings.NewReader("0123456789abcdef"),
	})
	require.NoError(t, err, "put %s", key)
}

func (e *gcEnv) keyExists(t *testing.T, key string) bool {
	t.Helper()
	_, err := e.s3.HeadObject(context.Background(), &s3.HeadObjectInput{
		Bucket: aws.String(e.bucket), Key: aws.String(key),
	})
	if err == nil {
		return true
	}
	var nf *types.NotFound
	require.True(t, errors.As(err, &nf), "HeadObject %s: %v", key, err)
	return false
}

// startGC runs runGC without --once. The returned function closes stop
// and returns the exit code.
func startGC(t *testing.T, cfg *config.Config, log *zap.Logger) (stopAndWait func() int) {
	t.Helper()
	stop := make(chan struct{})
	code := make(chan int, 1)
	go func() { code <- runGC(context.Background(), cfg, log, false, stop) }()

	var once sync.Once
	exit := -1
	stopAndWait = func() int {
		once.Do(func() {
			close(stop)
			select {
			case exit = <-code:
			case <-time.After(30 * time.Second):
				t.Error("runGC did not return 30 s after stop")
			}
		})
		return exit
	}
	t.Cleanup(func() { stopAndWait() })
	return stopAndWait
}

func adminURL(cfg *config.Config, path string) string {
	return fmt.Sprintf("http://127.0.0.1:%d%s", cfg.ServerPort, path)
}

func httpGet(url string) (status int, body string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return 0, "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), err
}

// eventually polls cond until it holds or timeout passes.
func eventually(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("%s: not true after %s", what, timeout)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func statusOf(url string) int {
	code, _, err := httpGet(url)
	if err != nil {
		return 0
	}
	return code
}

// TC-GC-120: gc --once returns 0 after a normal cycle (BR-GC-69, AC-GC-14).
// The test goes through the cobra command, so it also covers RunE.
func Test_TC_GC_120_OnceExitsZeroAndDeletesAReleasedObject(t *testing.T) {
	e := newGCEnv(t)
	e.exec(t, `INSERT INTO objects (id, ref_count, storage_id) VALUES ('r1', 0, 'default')`)
	e.putKey(t, "r1")

	root := newRootCmd()
	root.SetArgs([]string{"gc", "--once"})
	require.NoError(t, root.Execute(), "gc --once: want exit code 0")

	assert.False(t, e.rowExists(t, "objects", "r1"), "object row r1")
	assert.False(t, e.keyExists(t, "r1"), "key r1")
}

// TC-GC-121: gc --once returns 1 after a batch error (BR-GC-69, AC-GC-14).
func Test_TC_GC_121_OnceExitsOneAfterABatchError(t *testing.T) {
	e := newGCEnv(t)
	e.cfg.ObjectStoreEndpoint = fmt.Sprintf("http://127.0.0.1:%d", freePort(t))
	e.exec(t, `INSERT INTO objects (id, ref_count, storage_id) VALUES ('r1', 0, 'default')`)
	log, logs := observedLogger()

	code := runGC(context.Background(), e.cfg, log, true, make(chan struct{}))

	assert.Equal(t, 1, code)
	var deleting bool
	require.NoError(t, e.pool.QueryRow(context.Background(),
		`SELECT deleting FROM objects WHERE id = 'r1'`).Scan(&deleting))
	assert.True(t, deleting, "r1 keeps its claim for the retry")
	assert.Equal(t, 1, logs.FilterMessage("gc batch error").FilterField(zap.String("stage", "s3_delete")).Len())
	once := logs.FilterMessage("gc once completed").All()
	require.Len(t, once, 1)
	assert.Equal(t, true, once[0].ContextMap()["released_batch_error"])
}

// TC-GC-122: gc --once opens no listener (BR-GC-69). The object store
// accepts the connection and never answers, so the run is in progress
// while the test dials SERVER_PORT.
func Test_TC_GC_122_OnceOpensNoListener(t *testing.T) {
	hang, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = hang.Close() })
	accepted := make(chan net.Conn, 4)
	go func() {
		for {
			c, err := hang.Accept()
			if err != nil {
				return
			}
			accepted <- c
		}
	}()
	t.Cleanup(func() {
		close(accepted)
		for c := range accepted {
			_ = c.Close()
		}
	})

	e := newGCEnv(t)
	e.cfg.ObjectStoreEndpoint = "http://" + hang.Addr().String()
	e.exec(t, `INSERT INTO objects (id, ref_count, storage_id) VALUES ('r1', 0, 'default')`)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan int, 1)
	go func() { done <- runGC(ctx, e.cfg, zap.NewNop(), true, make(chan struct{})) }()

	select {
	case c := <-accepted:
		accepted <- c
	case <-time.After(30 * time.Second):
		t.Fatal("runGC did not reach the object store")
	}
	conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp", fmt.Sprintf("127.0.0.1:%d", e.cfg.ServerPort))
	if err == nil {
		_ = conn.Close()
	}
	require.Error(t, err, "SERVER_PORT accepted a connection during gc --once")

	cancel()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("runGC did not return after cancel")
	}
}

// TC-GC-125: admin endpoints and shutdown (BR-GC-67, NFR-GC-R6, NFR-GC-S1).
func Test_TC_GC_125_AdminEndpointsAndShutdown(t *testing.T) {
	e := newGCEnv(t)
	stopAndWait := startGC(t, e.cfg, zap.NewNop())

	eventually(t, 30*time.Second, "/healthz 200", func() bool { return statusOf(adminURL(e.cfg, "/healthz")) == http.StatusOK })
	assert.Equal(t, http.StatusOK, statusOf(adminURL(e.cfg, "/readyz")))
	eventually(t, 30*time.Second, "/metrics has last_success", func() bool {
		code, body, err := httpGet(adminURL(e.cfg, "/metrics"))
		return err == nil && code == http.StatusOK &&
			strings.Contains(body, `opentams_gc_last_success_timestamp_seconds{sweep="released"}`) &&
			strings.Contains(body, `opentams_gc_last_success_timestamp_seconds{sweep="allocation"}`)
	})

	// The 503 between the signal and the listener stop is in the unit test
	// Test_TC_GC_125_ReadyzIs503AfterStopAndHealthzStays200: here the
	// listener stops before a request can see it.
	assert.Equal(t, 0, stopAndWait(), "exit code after shutdown")
	_, _, err := httpGet(adminURL(e.cfg, "/healthz"))
	assert.Error(t, err, "listener still open after runGC returned")
}

// TC-GC-126: /readyz with the database down (NFR-GC-R6).
func Test_TC_GC_126_ReadyzIs503AfterTheDatabaseStops(t *testing.T) {
	ctx := context.Background()
	ctr, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("gc126"), postgres.WithUsername("test"), postgres.WithPassword("test"),
		testcontainers.WithWaitStrategy(
			tcwait.ForLog("database system is ready to accept connections").WithOccurrence(2),
			tcwait.ForListeningPort("5432/tcp"),
		))
	require.NoError(t, err)
	t.Cleanup(func() { _ = ctr.Terminate(context.Background()) })
	host, err := ctr.Host(ctx)
	require.NoError(t, err)
	port, err := ctr.MappedPort(ctx, "5432")
	require.NoError(t, err)
	t.Setenv("DB_HOST", host)
	t.Setenv("DB_PORT", port.Port())
	t.Setenv("DB_NAME", "gc126")
	migrateUp(t)

	mn := sharedMinIO(t)
	t.Setenv("OBJECT_STORE_ENDPOINT", mn.Endpoint)
	t.Setenv("OBJECT_STORE_BUCKET", mn.Bucket)
	t.Setenv("OBJECT_STORE_ACCESS_KEY_ID", objtest.AccessKey)
	t.Setenv("OBJECT_STORE_SECRET_ACCESS_KEY", objtest.SecretKey)
	t.Setenv("SERVER_PORT", strconv.Itoa(freePort(t)))
	cfg, err := config.Load()
	require.NoError(t, err)

	stopAndWait := startGC(t, cfg, zap.NewNop())
	eventually(t, 30*time.Second, "/readyz 200", func() bool { return statusOf(adminURL(cfg, "/readyz")) == http.StatusOK })

	stopTimeout := 10 * time.Second
	require.NoError(t, ctr.Stop(ctx, &stopTimeout))

	eventually(t, 10*time.Second, "/readyz 503", func() bool {
		return statusOf(adminURL(cfg, "/readyz")) == http.StatusServiceUnavailable
	})
	assert.Equal(t, http.StatusOK, statusOf(adminURL(cfg, "/healthz")))
	assert.Equal(t, 0, stopAndWait())
}

// TC-GC-127: the admin listener ignores the TLS settings (logic model
// section 10).
func Test_TC_GC_127_AdminListenerIgnoresTLSSettings(t *testing.T) {
	t.Setenv("SERVER_TLS_CERT_FILE", "/nonexistent/cert.pem")
	t.Setenv("SERVER_TLS_KEY_FILE", "/nonexistent/key.pem")
	e := newGCEnv(t)
	stopAndWait := startGC(t, e.cfg, zap.NewNop())

	eventually(t, 30*time.Second, "plain HTTP /healthz 200", func() bool {
		return statusOf(adminURL(e.cfg, "/healthz")) == http.StatusOK
	})
	assert.Equal(t, 0, stopAndWait())
}
