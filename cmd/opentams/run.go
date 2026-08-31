package main

import (
	"context"
	"fmt"
	"os"
	"time"
)

type serverRunner interface {
	Start(ctx context.Context) error
	Shutdown(ctx context.Context) error
}

// runServer starts srv in a background goroutine and blocks until a signal is
// received on sigCh or ctx is cancelled. It then performs a graceful shutdown
// bounded by shutdownPeriod. A Shutdown error takes precedence over a nil
// Start result.
func runServer(ctx context.Context, srv serverRunner, shutdownPeriod time.Duration, sigCh <-chan os.Signal) error {
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Start(ctx) }()

	select {
	case err := <-serveErr:
		return err
	case <-sigCh:
	case <-ctx.Done():
	}

	shutCtx, cancel := context.WithTimeout(context.Background(), shutdownPeriod)
	defer cancel()

	if err := srv.Shutdown(shutCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	return <-serveErr
}
