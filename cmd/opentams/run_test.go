package main

import (
	"context"
	"errors"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockServer struct {
	startErr       error
	shutdownErr    error
	startCalled    chan struct{}
	shutdownCalled chan struct{}
	blockUntil     chan struct{}
}

func newMockServer() *mockServer {
	return &mockServer{
		startCalled:    make(chan struct{}, 1),
		shutdownCalled: make(chan struct{}, 1),
		blockUntil:     make(chan struct{}),
	}
}

func (m *mockServer) Start(_ context.Context) error {
	m.startCalled <- struct{}{}
	if m.startErr != nil {
		return m.startErr
	}
	<-m.blockUntil
	return nil
}

func (m *mockServer) Shutdown(_ context.Context) error {
	m.shutdownCalled <- struct{}{}
	close(m.blockUntil)
	return m.shutdownErr
}

// TC-CMD-RUN-01: OS signal triggers graceful shutdown and returns nil.
func TestRunServer_SignalShutdown(t *testing.T) {
	mock := newMockServer()
	sigCh := make(chan os.Signal, 1)

	errCh := make(chan error, 1)
	go func() { errCh <- runServer(context.Background(), mock, time.Second, sigCh) }()

	<-mock.startCalled
	sigCh <- syscall.SIGTERM

	select {
	case <-mock.shutdownCalled:
	case <-time.After(2 * time.Second):
		t.Fatal("Shutdown not called within timeout")
	}
	require.NoError(t, <-errCh)
}

// TC-CMD-RUN-02: Start error is returned immediately; Shutdown is not called.
func TestRunServer_StartError(t *testing.T) {
	mock := newMockServer()
	mock.startErr = errors.New("listen: address in use")

	sigCh := make(chan os.Signal, 1)
	err := runServer(context.Background(), mock, time.Second, sigCh)

	require.ErrorIs(t, err, mock.startErr)
	assert.Empty(t, mock.shutdownCalled)
}

// TC-CMD-RUN-03: Context cancellation triggers graceful shutdown and returns nil.
func TestRunServer_ContextCancel(t *testing.T) {
	mock := newMockServer()
	sigCh := make(chan os.Signal, 1)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- runServer(ctx, mock, time.Second, sigCh) }()

	<-mock.startCalled
	cancel()

	select {
	case <-mock.shutdownCalled:
	case <-time.After(2 * time.Second):
		t.Fatal("Shutdown not called within timeout")
	}
	require.NoError(t, <-errCh)
}

// TC-CMD-RUN-04: Shutdown error is returned wrapped.
func TestRunServer_ShutdownError(t *testing.T) {
	mock := newMockServer()
	mock.shutdownErr = errors.New("drain timeout")

	sigCh := make(chan os.Signal, 1)
	errCh := make(chan error, 1)
	go func() { errCh <- runServer(context.Background(), mock, time.Second, sigCh) }()

	<-mock.startCalled
	sigCh <- syscall.SIGTERM

	err := <-errCh
	require.ErrorIs(t, err, mock.shutdownErr)
}
