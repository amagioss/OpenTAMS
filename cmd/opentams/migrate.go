package main

import (
	"fmt"
	"net/url"
	"os"
	"strconv"

	gogomigrate "github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/amagioss/opentams/internal/config"
	openmigrations "github.com/amagioss/opentams/migrations"
	"github.com/amagioss/opentams/pkg/dbmigrate"
	"github.com/amagioss/opentams/pkg/logger"
)

func newMigrateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Manage database schema migrations",
		Long: `Manage database schema migrations using embedded SQL files.

Migrations are baked into the binary at build time and applied against the
database configured via the DB_* environment variables (same as 'serve').

Subcommands:
  up [N]     Apply all pending migrations, or exactly N steps
  down N     Roll back N migrations
  version    Print current schema version and dirty flag
  force N    Force schema_migrations to version N (clears dirty)

The 'serve' subcommand validates the schema version at startup and exits
with a structured error if migrations have not been applied.`,
		SilenceUsage: true,
	}
	cmd.AddCommand(
		newMigrateUpCmd(),
		newMigrateDownCmd(),
		newMigrateVersionCmd(),
		newMigrateForceCmd(),
	)
	return cmd
}

func migrateLogger() *zap.Logger {
	log, _ := logger.New(os.Stderr, zapcore.InfoLevel)
	return log
}

func buildMigrateDSN(cfg *config.Config) string {
	u := &url.URL{
		Scheme:   "pgx5",
		User:     url.UserPassword(cfg.DBUser, cfg.DBPassword),
		Host:     fmt.Sprintf("%s:%d", cfg.DBHost, cfg.DBPort),
		Path:     cfg.DBName,
		RawQuery: "sslmode=" + cfg.DBSSLMode,
	}
	return u.String()
}

func newMigrateInstance(cfg *config.Config, log *zap.Logger, verbose bool) (*gogomigrate.Migrate, error) {
	return dbmigrate.New(openmigrations.FS, buildMigrateDSN(cfg), log, verbose)
}

func newMigrateUpCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "up [N]",
		Short: "Apply pending migrations (all, or N steps)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			log := migrateLogger()
			cfg, err := config.Load()
			if err != nil {
				log.Error("config load failed", zap.Error(err))
				return err
			}
			m, err := newMigrateInstance(cfg, log, true)
			if err != nil {
				log.Error("migrate init failed", zap.Error(err))
				return err
			}
			defer dbmigrate.Close(m, log)

			steps := 0
			if len(args) == 1 {
				steps, err = strconv.Atoi(args[0])
				if err != nil || steps <= 0 {
					return fmt.Errorf("n must be a positive integer, got %q", args[0])
				}
			}
			if err := dbmigrate.Up(m, steps); err != nil {
				log.Error("migrate up failed", zap.Error(err))
				return err
			}
			ver, dirty, _ := dbmigrate.Version(m)
			log.Info("migrate up complete", zap.Uint("version", ver), zap.Bool("dirty", dirty))
			return nil
		},
	}
}

func newMigrateDownCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "down N",
		Short: "Roll back N migrations",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			log := migrateLogger()
			steps, err := strconv.Atoi(args[0])
			if err != nil || steps <= 0 {
				return fmt.Errorf("n must be a positive integer, got %q", args[0])
			}
			cfg, err := config.Load()
			if err != nil {
				log.Error("config load failed", zap.Error(err))
				return err
			}
			m, err := newMigrateInstance(cfg, log, true)
			if err != nil {
				log.Error("migrate init failed", zap.Error(err))
				return err
			}
			defer dbmigrate.Close(m, log)

			if err := dbmigrate.Down(m, steps); err != nil {
				log.Error("migrate down failed", zap.Error(err))
				return err
			}
			ver, dirty, _ := dbmigrate.Version(m)
			log.Info("migrate down complete", zap.Uint("version", ver), zap.Bool("dirty", dirty))
			return nil
		},
	}
}

func newMigrateVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print current schema version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			log := migrateLogger()
			cfg, err := config.Load()
			if err != nil {
				log.Error("config load failed", zap.Error(err))
				return err
			}
			m, err := newMigrateInstance(cfg, log, false)
			if err != nil {
				log.Error("migrate init failed", zap.Error(err))
				return err
			}
			defer dbmigrate.Close(m, log)

			ver, dirty, err := dbmigrate.Version(m)
			if err != nil {
				log.Error("version query failed", zap.Error(err))
				return err
			}
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "version: %d  dirty: %v\n", ver, dirty); err != nil {
				return err
			}
			return nil
		},
	}
}

func newMigrateForceCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "force N",
		Short: "Force schema_migrations to version N (clears dirty flag)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			log := migrateLogger()
			v, err := strconv.Atoi(args[0])
			if err != nil {
				return fmt.Errorf("n must be an integer, got %q", args[0])
			}
			cfg, err := config.Load()
			if err != nil {
				log.Error("config load failed", zap.Error(err))
				return err
			}
			m, err := newMigrateInstance(cfg, log, false)
			if err != nil {
				log.Error("migrate init failed", zap.Error(err))
				return err
			}
			defer dbmigrate.Close(m, log)

			if err := dbmigrate.Force(m, v); err != nil {
				log.Error("migrate force failed", zap.Error(err))
				return err
			}
			log.Info("migrate force complete", zap.Int("version", v))
			return nil
		},
	}
}
