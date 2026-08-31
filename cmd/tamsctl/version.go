package main

import (
	"encoding/json"
	"runtime"

	"github.com/spf13/cobra"
)

type versionInfo struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	Date      string `json:"date"`
	GoVersion string `json:"goVersion"`
	Platform  string `json:"platform"`
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version, build, and platform info",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			info := versionInfo{
				Version:   version,
				Commit:    commit,
				Date:      date,
				GoVersion: runtime.Version(),
				Platform:  runtime.GOOS + "/" + runtime.GOARCH,
			}

			// Plain text by default (the persistent -o default is json, but a
			// version banner reads better as text). Structured json/yaml and a
			// one-row table are produced only when -o is explicitly set.
			format, _ := cmd.Flags().GetString("output")
			switch {
			case format == "table":
				raw, err := json.Marshal([]versionInfo{info})
				if err != nil {
					return err
				}
				return render(cmd.OutOrStdout(), raw, "table")
			case cmd.Flags().Changed("output") && (format == "json" || format == "yaml"):
				raw, err := json.Marshal(info)
				if err != nil {
					return err
				}
				return render(cmd.OutOrStdout(), raw, format)
			default:
				return outf(cmd, "tamsctl %s\n  commit:   %s\n  built:    %s\n  go:       %s\n  platform: %s\n",
					info.Version, info.Commit, info.Date, info.GoVersion, info.Platform)
			}
		},
	}
}
