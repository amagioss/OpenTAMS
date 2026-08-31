// Command opentamspub is the OpenTAMS demo publisher. It chunks an
// input (file or live stream) into MPEG-TS segments via ffmpeg and
// registers them against an OpenTAMS Flow, using bulk
// POST /flows/{id}/storage to amortise the allocation round-trip.
//
// Two subcommands:
//
//	opentamspub file <input>     — chunk a fixed input and upload all at once
//	opentamspub live --input <…> — chunk continuously and upload as ffmpeg appends
//
// See ../../scripts/demo.sh for the demo-friendly wrapper.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return newRootCmd().ExecuteContext(ctx)
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "opentamspub",
		Short: "OpenTAMS demo publisher (file + live ingest).",
		Long: `Chunks media into MPEG-TS segments via ffmpeg and registers
each chunk against an OpenTAMS Flow. Uses bulk POST /flows/{id}/storage
to keep the per-chunk hot path off the allocation round-trip.

Environment:
  OPENTAMS_BASE_URL   default http://localhost:8080
  OPENTAMS_TOKEN      default "dev"`,
		SilenceUsage: true,
	}
	root.AddCommand(newFileCmd(), newLiveCmd())
	return root
}
