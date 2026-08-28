package main

import "github.com/spf13/cobra"

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:          "opentams",
		Short:        "BBC TAMS v8.0 API server",
		Long:         `OpenTAMS is a cloud-agnostic implementation of the BBC Time-Addressable Media Store (TAMS) v8.0 API backed by PostgreSQL and S3-compatible object storage.`,
		SilenceUsage: true,
		// Errors are logged structurally inside each subcommand before being returned.
		SilenceErrors: true,
	}
	root.AddCommand(newServeCmd())
	root.AddCommand(newGCCmd())
	root.AddCommand(newMigrateCmd())
	return root
}
