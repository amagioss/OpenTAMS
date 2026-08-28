package main

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/amagioss/opentams/internal/tamsctl"
)

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "tamsctl",
		Short: "Operator CLI for the OpenTAMS (BBC TAMS v8.0) API",
		Long: `tamsctl drives the TAMS v1 API from the command line.

Configure endpoints and tokens as named contexts (kubectl-style) in
~/.tamsctl/config, then switch between local, hosted, or cloud instances
with --context or 'tamsctl config use-context'.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			switch format, _ := cmd.Flags().GetString("output"); format {
			case "json", "yaml", "table":
				return nil
			default:
				return fmt.Errorf("invalid --output %q: want json, yaml, or table", format)
			}
		},
	}
	pf := root.PersistentFlags()
	pf.String("config", "", "config file (default ~/.tamsctl/config)")
	pf.String("context", "", "context to use (overrides current-context)")
	pf.String("endpoint", "", "API root incl. version prefix, e.g. http://host:8080/tams/v1 (overrides context and "+tamsctl.EnvEndpoint+")")
	pf.String("token", "", "bearer token (overrides context and "+tamsctl.EnvToken+")")
	pf.StringP("output", "o", "json", "output format: json|yaml|table")
	pf.Bool("quiet", false, "suppress informational hints on stderr")

	root.AddCommand(
		newConfigCmd(),
		newFlowCmd(),
		newSegmentCmd(),
		newStorageCmd(),
		newSourceCmd(),
		newVersionCmd(),
	)
	return root
}

// configPath resolves the config file path from --config or the default.
func configPath(cmd *cobra.Command) (string, error) {
	if p, _ := cmd.Flags().GetString("config"); p != "" {
		return p, nil
	}
	return tamsctl.DefaultConfigPath()
}

// loadConfig reads the config file referenced by the command's flags.
func loadConfig(cmd *cobra.Command) (*tamsctl.Config, error) {
	path, err := configPath(cmd)
	if err != nil {
		return nil, err
	}
	return tamsctl.Load(path)
}

// clientFromCmd builds an API client from config plus flag/env overrides.
func clientFromCmd(cmd *cobra.Command) (*tamsctl.Client, error) {
	cfg, err := loadConfig(cmd)
	if err != nil {
		return nil, err
	}
	ctxName, _ := cmd.Flags().GetString("context")
	endpoint, _ := cmd.Flags().GetString("endpoint")
	token, _ := cmd.Flags().GetString("token")
	r, err := cfg.Resolve(ctxName, endpoint, token)
	if err != nil {
		return nil, err
	}
	return tamsctl.NewClient(r.Endpoint, r.Token), nil
}

// emit renders an API response body in the requested output format.
func emit(cmd *cobra.Command, raw json.RawMessage) error {
	format, _ := cmd.Flags().GetString("output")
	return render(cmd.OutOrStdout(), raw, format)
}

// outf writes a status line to the command's stdout, propagating write errors.
func outf(cmd *cobra.Command, format string, a ...any) error {
	_, err := fmt.Fprintf(cmd.OutOrStdout(), format, a...)
	return err
}
