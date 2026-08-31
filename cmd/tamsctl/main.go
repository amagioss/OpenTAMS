// Command tamsctl is a kubectl-style operator CLI for the OpenTAMS
// (BBC TAMS v8.0) API: it manages flows, segments, storage allocation, and
// source queries against any configured endpoint.
package main

import (
	"fmt"
	"os"
)

// Build-time variables populated via -ldflags (see `make build-cli`). They
// default so a plain `go build`/`go run`/`go test` yields sensible values.
var (
	version = "dev"     //nolint:gochecknoglobals // build-time injection point
	commit  = "none"    //nolint:gochecknoglobals // build-time injection point
	date    = "unknown" //nolint:gochecknoglobals // build-time injection point
)

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
