package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/amagioss/opentams/internal/httpx/health"
	"github.com/amagioss/opentams/pkg/metrics"
)

type fixedChecker bool

func (c fixedChecker) Ready(context.Context) bool { return bool(c) }

func (fixedChecker) Details(context.Context) health.Details { return health.Details{} }

func adminGet(t *testing.T, h http.Handler, path string) (status int, body string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, nil))
	b, err := io.ReadAll(rec.Result().Body)
	require.NoError(t, err)
	return rec.Code, string(b)
}

// TC-GC-125 (router part), BR-GC-67 step 1: /readyz turns 503 at the
// shutdown signal while /healthz stays 200.
func Test_TC_GC_125_ReadyzIs503AfterStopAndHealthzStays200(t *testing.T) {
	stop := make(chan struct{})
	reg, err := metrics.New(metrics.WithNamespace("opentams"))
	require.NoError(t, err)
	h := newAdminHandler(fixedChecker(true), reg.Gatherer(), stop)

	code, _ := adminGet(t, h, "/readyz")
	assert.Equal(t, http.StatusOK, code, "/readyz before stop")

	close(stop)

	code, _ = adminGet(t, h, "/readyz")
	assert.Equal(t, http.StatusServiceUnavailable, code, "/readyz after stop")
	code, _ = adminGet(t, h, "/healthz")
	assert.Equal(t, http.StatusOK, code, "/healthz after stop")
}

// NFR-GC-R6: /readyz follows the dependency probe.
func Test_TC_GC_126_ReadyzIs503WhenADependencyIsDown(t *testing.T) {
	reg, err := metrics.New(metrics.WithNamespace("opentams"))
	require.NoError(t, err)
	h := newAdminHandler(fixedChecker(false), reg.Gatherer(), make(chan struct{}))

	code, _ := adminGet(t, h, "/readyz")
	assert.Equal(t, http.StatusServiceUnavailable, code)
	code, _ = adminGet(t, h, "/healthz")
	assert.Equal(t, http.StatusOK, code)
}

// Interface spec section 3: the admin listener serves only three paths.
func Test_AdminHandlerServesMetricsAndNoTAMSAPI(t *testing.T) {
	reg, err := metrics.New(metrics.WithNamespace("opentams"))
	require.NoError(t, err)
	h := newAdminHandler(fixedChecker(true), reg.Gatherer(), make(chan struct{}))

	code, body := adminGet(t, h, "/metrics")
	assert.Equal(t, http.StatusOK, code)
	assert.Contains(t, body, "go_goroutines")

	code, _ = adminGet(t, h, "/tams/v1/flows")
	assert.Equal(t, http.StatusNotFound, code)
}

// BR-GC-67 steps 1 and 4: a signal closes stop at once, and cancels the
// batch context only after the grace period.
func Test_BR_GC_67_SignalClosesStopAndCancelsAfterGrace(t *testing.T) {
	const grace = 200 * time.Millisecond
	sig := make(chan os.Signal, 1)
	stop := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	release := forwardShutdown(sig, stop, cancel, grace)
	defer release()

	sig <- syscall.SIGTERM
	sent := time.Now()

	select {
	case <-stop:
	case <-time.After(time.Second):
		t.Fatal("stop not closed after the signal")
	}
	select {
	case <-ctx.Done():
		t.Fatalf("ctx cancelled %s after the signal, before the grace period of %s", time.Since(sent), grace)
	case <-time.After(grace / 2):
	}
	select {
	case <-ctx.Done():
		assert.GreaterOrEqual(t, time.Since(sent), grace)
	case <-time.After(2 * time.Second):
		t.Fatal("ctx not cancelled after the grace period")
	}
}

// Without a signal, release leaves stop open and ctx alive.
func Test_BR_GC_67_ReleaseWithoutSignalChangesNothing(t *testing.T) {
	sig := make(chan os.Signal, 1)
	stop := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	forwardShutdown(sig, stop, cancel, time.Millisecond)()

	select {
	case <-stop:
		t.Fatal("stop closed without a signal")
	default:
	}
	assert.NoError(t, ctx.Err())
}
