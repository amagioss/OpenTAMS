// Command opentamsedit forks an OpenTAMS Flow into a new Flow where
// one timerange is replaced by a same-essence "break slate" object.
// It is non-destructive: the --from flow is read-only to this tool.
//
//	opentamsedit fork --from <flowId> --to <flowId> \
//	    --replace "[12s:18s)" --slate-text "AD BREAK"
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
		Use:          "opentamsedit",
		Short:        "OpenTAMS demo flow-fork editor (non-destructive).",
		SilenceUsage: true,
	}
	root.AddCommand(newForkCmd())
	return root
}
