package main

import (
	"github.com/spf13/cobra"

	"github.com/amagioss/opentams/internal/tamsctl"
)

func newSegmentCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "segment",
		Short: "Register, get, and delete flow segments",
	}
	cmd.AddCommand(
		newSegmentRegisterCmd(),
		newSegmentGetCmd(),
		newSegmentDeleteCmd(),
	)
	return cmd
}

func newSegmentRegisterCmd() *cobra.Command {
	var (
		flowID                                   string
		p                                        tamsctl.SegmentRegisterParams
		keyFrameCount, sampleOffset, sampleCount int
		getURLs                                  []string
	)
	cmd := &cobra.Command{
		Use:   "register",
		Short: "Register a fully-specified segment",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateUUID("flow-id", flowID); err != nil {
				return err
			}
			client, err := clientFromCmd(cmd)
			if err != nil {
				return err
			}
			f := cmd.Flags()
			if f.Changed("key-frame-count") {
				p.KeyFrameCount = &keyFrameCount
			}
			if f.Changed("sample-offset") {
				p.SampleOffset = &sampleOffset
			}
			if f.Changed("sample-count") {
				p.SampleCount = &sampleCount
			}
			p.GetURLs = getURLs
			raw, err := client.RegisterSegments(cmd.Context(), flowID, []tamsctl.SegmentRegisterParams{p})
			if err != nil {
				return err
			}
			return emit(cmd, raw)
		},
	}
	f := cmd.Flags()
	f.StringVar(&flowID, "flow-id", "", "flow id (required)")
	f.StringVar(&p.ObjectID, "object-id", "", "object id (required)")
	f.StringVar(&p.Timerange, "timerange", "", "segment timerange (required)")
	f.StringVar(&p.TSOffset, "ts-offset", "", "timestamp offset")
	f.StringVar(&p.ObjectTimerange, "object-timerange", "", "object timerange")
	f.StringVar(&p.LastDuration, "last-duration", "", "last sample duration")
	f.IntVar(&keyFrameCount, "key-frame-count", 0, "key frame count")
	f.IntVar(&sampleOffset, "sample-offset", 0, "sample offset")
	f.IntVar(&sampleCount, "sample-count", 0, "sample count")
	f.StringArrayVar(&getURLs, "get-url", nil, "BYOS get URL (repeatable)")
	for _, name := range []string{"flow-id", "object-id", "timerange"} {
		_ = cmd.MarkFlagRequired(name)
	}
	return cmd
}

func newSegmentGetCmd() *cobra.Command {
	var flowID string
	var p tamsctl.SegmentGetParams
	cmd := &cobra.Command{
		Use:   "get",
		Short: "List segments for a flow",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateUUID("flow-id", flowID); err != nil {
				return err
			}
			client, err := clientFromCmd(cmd)
			if err != nil {
				return err
			}
			return runList(cmd, func(pageToken string) (*tamsctl.Page, error) {
				p.PageToken = pageToken
				return client.ListSegments(cmd.Context(), flowID, p)
			})
		},
	}
	f := cmd.Flags()
	f.StringVar(&flowID, "flow-id", "", "flow id (required)")
	f.StringVar(&p.Timerange, "timerange", "", "filter by timerange")
	f.StringVar(&p.ObjectID, "object-id", "", "filter by object id")
	f.BoolVar(&p.ReverseOrder, "reverse-order", false, "reverse order")
	f.BoolVar(&p.VerboseStorage, "verbose-storage", false, "include verbose storage")
	f.StringVar(&p.AcceptGetURLs, "accept-get-urls", "", "comma-separated get_urls labels to include (empty string returns none)")
	f.StringVar(&p.AcceptStorageIDs, "accept-storage-ids", "", "comma-separated storage ids")
	f.BoolVar(&p.Presigned, "presigned", false, "request presigned URLs")
	f.BoolVar(&p.IncludeObjectTimerange, "include-object-timerange", false, "include object timerange")
	f.IntVar(&p.Limit, "limit", 100, "max results per page")
	addListFlags(cmd)
	_ = cmd.MarkFlagRequired("flow-id")
	return cmd
}

func newSegmentDeleteCmd() *cobra.Command {
	var flowID, timerange, objectID string
	cmd := &cobra.Command{
		Use:   "delete",
		Short: "Delete segments by timerange and/or object id",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateUUID("flow-id", flowID); err != nil {
				return err
			}
			client, err := clientFromCmd(cmd)
			if err != nil {
				return err
			}
			raw, err := client.DeleteSegments(cmd.Context(), flowID, timerange, objectID)
			if err != nil {
				return err
			}
			if len(raw) == 0 {
				return outf(cmd, "delete requested for flow %q\n", flowID)
			}
			return emit(cmd, raw)
		},
	}
	f := cmd.Flags()
	f.StringVar(&flowID, "flow-id", "", "flow id (required)")
	f.StringVar(&timerange, "timerange", "", "filter by timerange")
	f.StringVar(&objectID, "object-id", "", "filter by object id")
	_ = cmd.MarkFlagRequired("flow-id")
	return cmd
}
