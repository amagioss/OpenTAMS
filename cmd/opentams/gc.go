package main

import (
	"errors"

	"github.com/spf13/cobra"
)

var errGCNotImplemented = errors.New("gc worker is not yet implemented (M16)")

func newGCCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "gc",
		Short: "Run the garbage collection worker",
		Long: `Processes pending flow delete requests and purges associated segments and
media objects from the object store. Runs independently of the API server —
deploy as a Kubernetes CronJob or a long-running sidecar.

Each sweep emits a structured log summary with: start time, duration, segments
purged, segments failed, and exit reason.`,
		RunE: func(*cobra.Command, []string) error { return errGCNotImplemented },
	}
}
