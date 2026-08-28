package main

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/amagioss/opentams/internal/tamsctl"
)

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage contexts (endpoints + tokens) in ~/.tamsctl/config",
	}
	cmd.AddCommand(
		newConfigSetContextCmd(),
		newConfigUseContextCmd(),
		newConfigSetTokenCmd(),
		newConfigCurrentContextCmd(),
		newConfigViewCmd(),
	)
	return cmd
}

func newConfigSetContextCmd() *cobra.Command {
	var endpoint, token string
	cmd := &cobra.Command{
		Use:   "set-context <name>",
		Short: "Create or update a named context",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(cmd)
			if err != nil {
				return err
			}
			cfg.SetContext(args[0], endpoint, "")
			// Update the token only when --token is given. Passing --token ""
			// explicitly clears it; omitting the flag preserves the existing one.
			if cmd.Flags().Changed("token") {
				_ = cfg.SetToken(args[0], token)
			}
			if cfg.CurrentContext == "" {
				cfg.CurrentContext = args[0]
			}
			if err := cfg.Save(); err != nil {
				return err
			}
			return outf(cmd, "context %q set\n", args[0])
		},
	}
	cmd.Flags().StringVar(&endpoint, "endpoint", "", "API root URL incl. version prefix, e.g. http://host:8080/tams/v1 (required)")
	cmd.Flags().StringVar(&token, "token", "", "bearer token (pass empty to clear)")
	_ = cmd.MarkFlagRequired("endpoint")
	return cmd
}

func newConfigUseContextCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "use-context <name>",
		Short: "Set the current context",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(cmd)
			if err != nil {
				return err
			}
			if err := cfg.UseContext(args[0]); err != nil {
				return err
			}
			if err := cfg.Save(); err != nil {
				return err
			}
			return outf(cmd, "current context is now %q\n", args[0])
		},
	}
}

func newConfigSetTokenCmd() *cobra.Command {
	var contextName, token string
	cmd := &cobra.Command{
		Use:   "set-token",
		Short: "Update the token for a context (flag, " + tamsctl.EnvToken + ", or stdin)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(cmd)
			if err != nil {
				return err
			}
			name := contextName
			if name == "" {
				name = cfg.CurrentContext
			}
			if name == "" {
				return fmt.Errorf("no context selected: pass --context or set a current context")
			}

			tok := token
			if tok == "" {
				tok = os.Getenv(tamsctl.EnvToken)
			}
			if tok == "" {
				tok, err = readTokenStdin(cmd)
				if err != nil {
					return err
				}
			}
			if tok == "" {
				return fmt.Errorf("no token provided")
			}
			if err := cfg.SetToken(name, tok); err != nil {
				return err
			}
			if err := cfg.Save(); err != nil {
				return err
			}
			return outf(cmd, "token updated for context %q\n", name)
		},
	}
	cmd.Flags().StringVar(&contextName, "context", "", "context to update (default current)")
	cmd.Flags().StringVar(&token, "token", "", "bearer token")
	return cmd
}

func readTokenStdin(cmd *cobra.Command) (string, error) {
	_, _ = fmt.Fprint(cmd.ErrOrStderr(), "token: ")
	sc := bufio.NewScanner(cmd.InOrStdin())
	if sc.Scan() {
		return strings.TrimSpace(sc.Text()), nil
	}
	return "", sc.Err()
}

func newConfigCurrentContextCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "current-context",
		Short: "Print the current context name",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(cmd)
			if err != nil {
				return err
			}
			if cfg.CurrentContext == "" {
				return fmt.Errorf("no current context set")
			}
			return outf(cmd, "%s\n", cfg.CurrentContext)
		},
	}
}

func newConfigViewCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "view",
		Short: "List contexts (tokens redacted)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(cmd)
			if err != nil {
				return err
			}
			var b strings.Builder
			fmt.Fprintf(&b, "current-context: %s\n", orNone(cfg.CurrentContext))
			names := make([]string, 0, len(cfg.Contexts))
			for name := range cfg.Contexts {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				ctx := cfg.Contexts[name]
				marker := " "
				if name == cfg.CurrentContext {
					marker = "*"
				}
				fmt.Fprintf(&b, "%s %s\t%s\t%s\n", marker, name, ctx.Endpoint, redact(ctx.Token))
			}
			return outf(cmd, "%s", b.String())
		},
	}
}

func redact(token string) string {
	if token == "" {
		return "(no token)"
	}
	return "(token set)"
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}
