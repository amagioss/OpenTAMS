package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/amagioss/opentams/internal/tamsctl"
)

func newSourceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "source",
		Short: "Query sources",
	}
	cmd.AddCommand(newSourceGetCmd())
	return cmd
}

func newSourceGetCmd() *cobra.Command {
	var (
		label, format string
		tags          []string
		tagsExist     []string
		limit         int
	)
	cmd := &cobra.Command{
		Use:   "get",
		Short: "List sources with optional filters",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			tagMap := map[string]string{}
			for _, t := range tags {
				name, value, ok := strings.Cut(t, "=")
				if !ok {
					return fmt.Errorf("invalid --tag %q: want name=value", t)
				}
				tagMap[name] = value
			}
			client, err := clientFromCmd(cmd)
			if err != nil {
				return err
			}
			return runList(cmd, func(pageToken string) (*tamsctl.Page, error) {
				return client.ListSources(cmd.Context(), tamsctl.SourceGetParams{
					Label:     label,
					Format:    format,
					Tags:      tagMap,
					TagsExist: tagsExist,
					Limit:     limit,
					PageToken: pageToken,
				})
			})
		},
	}
	f := cmd.Flags()
	f.StringVar(&label, "label", "", "filter by label")
	f.StringVar(&format, "format", "", "filter by format URN")
	f.StringArrayVar(&tags, "tag", nil, "tag filter name=value (repeatable)")
	f.StringSliceVar(&tagsExist, "tags-exist", nil, "comma-separated tag names that must exist")
	f.IntVar(&limit, "limit", 100, "max results per page")
	addListFlags(cmd)
	return cmd
}
