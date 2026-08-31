package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newStorageCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "storage",
		Short: "Allocate storage for a flow",
	}
	cmd.AddCommand(newStorageCreateCmd())
	return cmd
}

func newStorageCreateCmd() *cobra.Command {
	var (
		flowID    string
		limit     int
		objectIDs []string
	)
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Allocate storage (exactly one of --limit or --object-id)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateUUID("flow-id", flowID); err != nil {
				return err
			}
			f := cmd.Flags()
			hasLimit := f.Changed("limit")
			hasObjectIDs := len(objectIDs) > 0
			if hasLimit == hasObjectIDs {
				return fmt.Errorf("provide exactly one of --limit or --object-id")
			}
			client, err := clientFromCmd(cmd)
			if err != nil {
				return err
			}
			raw, err := client.AllocateStorage(cmd.Context(), flowID, limit, objectIDs)
			if err != nil {
				return err
			}
			return emit(cmd, raw)
		},
	}
	f := cmd.Flags()
	f.StringVar(&flowID, "flow-id", "", "flow id (required)")
	f.IntVar(&limit, "limit", 0, "number of object ids to allocate")
	f.StringArrayVar(&objectIDs, "object-id", nil, "explicit object id; repeat per id (--object-id a --object-id b)")
	_ = cmd.MarkFlagRequired("flow-id")
	return cmd
}
