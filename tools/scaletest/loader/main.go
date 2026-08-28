// scaletest-loader generates and bulk-loads the bimodal OpenTAMS dataset
// described in docs/scale-test-plan.md.
//
// Subcommands:
//
//	plan   — compute the row plan from the knobs and print it as JSON
//	         (no database access; safe for sanity-checking flags).
//	load   — apply the plan against the Postgres targeted by DB_*
//	         environment variables (same vars as `opentams serve`).
//
// The loader expects migrations to be already applied. It drops the
// GiST EXCLUDE constraint on `segments` for the bulk COPY and re-adds
// it at the end; ADD CONSTRAINT validates non-overlap on the loaded
// data — a generation bug surfaces there, before any measurement run.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"

	"github.com/amagioss/opentams/tools/scaletest/dataset"
	"github.com/amagioss/opentams/tools/scaletest/dbenv"
)

type cliFlags struct {
	preset         string
	targetSegments int64
	chunkDuration  int
	deepDepth      int64
	shallowDepth   int64
	deepFlowCount  int64
	renditions     int
	seed           uint64
	copyChunkRows  int64
	workers        int64
	reset          bool
}

func main() {
	if err := newRoot().Execute(); err != nil {
		// cobra has already printed the error.
		os.Exit(1)
	}
}

func newRoot() *cobra.Command {
	root := &cobra.Command{
		Use:           "scaletest-loader",
		Short:         "OpenTAMS scale-test dataset loader (D2)",
		SilenceUsage:  true,
		SilenceErrors: false,
	}
	root.AddCommand(newPlanCmd(), newLoadCmd(), newResetCmd())
	return root
}

func bindKnobFlags(cmd *cobra.Command, f *cliFlags) {
	cmd.Flags().StringVar(&f.preset, "preset", "6",
		`Preset: "6" (6s chunks, breadth), "1" (1s chunks, depth), or "custom"`)
	cmd.Flags().Int64Var(&f.targetSegments, "segments", 1_000_000,
		"Target segment row count")
	cmd.Flags().IntVar(&f.chunkDuration, "chunk-sec", 6,
		"Chunk duration in seconds (custom preset only; 1 or 6)")
	cmd.Flags().Int64Var(&f.deepDepth, "deep-depth", 0,
		"Segments per deep flow (custom preset only)")
	cmd.Flags().Int64Var(&f.shallowDepth, "shallow-depth", 0,
		"Segments per shallow flow (custom preset only)")
	cmd.Flags().Int64Var(&f.deepFlowCount, "deep-flows", 0,
		"Deep flow count (custom preset only)")
	cmd.Flags().IntVar(&f.renditions, "renditions", 6,
		"Renditions per source (custom preset only)")
	cmd.Flags().Uint64Var(&f.seed, "seed", 42,
		"Deterministic RNG seed")
	cmd.Flags().Int64Var(&f.copyChunkRows, "chunk-rows", 1_000_000,
		"Rows per CopyFrom call (chunked load); smaller bounds WAL per transaction")
	cmd.Flags().Int64Var(&f.workers, "workers", 1,
		"Parallel COPY workers for the segments table (1 = sequential). Each holds one pool connection.")
}

func knobsFromFlags(f *cliFlags) (dataset.Knobs, error) {
	switch f.preset {
	case "6":
		return dataset.Preset6(f.targetSegments), nil
	case "1":
		return dataset.Preset1(f.targetSegments), nil
	case "custom":
		return dataset.Knobs{
			TargetSegments:      f.targetSegments,
			ChunkDurationSec:    f.chunkDuration,
			DeepDepth:           f.deepDepth,
			ShallowDepth:        f.shallowDepth,
			DeepFlowCount:       f.deepFlowCount,
			RenditionsPerSource: f.renditions,
		}, nil
	default:
		return dataset.Knobs{}, fmt.Errorf(`unknown preset %q (want "6", "1", or "custom")`, f.preset)
	}
}

func newPlanCmd() *cobra.Command {
	var f cliFlags
	cmd := &cobra.Command{
		Use:   "plan",
		Short: "Compute and print the row plan as JSON (no database access)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			k, err := knobsFromFlags(&f)
			if err != nil {
				return err
			}
			p, err := dataset.Compose(k)
			if err != nil {
				return err
			}
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(p)
		},
	}
	bindKnobFlags(cmd, &f)
	return cmd
}

func newLoadCmd() *cobra.Command {
	var f cliFlags
	cmd := &cobra.Command{
		Use:   "load",
		Short: "Apply the plan against the Postgres targeted by DB_* env vars",
		RunE: func(cmd *cobra.Command, _ []string) error {
			k, err := knobsFromFlags(&f)
			if err != nil {
				return err
			}
			p, err := dataset.Compose(k)
			if err != nil {
				return err
			}
			dsn, err := dbenv.DSN()
			if err != nil {
				return err
			}
			// ^C / SIGTERM cancels the context so the in-flight chunk
			// can wind down cleanly. Already-committed chunks survive
			// (each chunk is its own transaction); a second signal
			// from the user will hard-kill via the runtime default.
			ctx, stop := signal.NotifyContext(context.Background(),
				os.Interrupt, syscall.SIGTERM)
			defer stop()
			poolCfg, err := pgxpool.ParseConfig(dsn)
			if err != nil {
				return fmt.Errorf("db: parse dsn: %w", err)
			}
			// Each parallel segments-COPY worker holds one connection;
			// +1 leaves headroom for the surrounding DDL (drop/rebuild,
			// ANALYZE) and the small per-table COPYs that bracket the
			// segments phase. Min 4 keeps the sequential path
			// (--workers=1) comfortable.
			workers := max(f.workers, 1)
			poolMax := max(workers+1, 4)
			poolCfg.MaxConns = int32(poolMax) //nolint:gosec // bounded by user-supplied --workers (validated below)

			pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
			if err != nil {
				return fmt.Errorf("db connect: %w", err)
			}
			defer pool.Close()

			out := cmd.OutOrStdout()
			errOut := cmd.ErrOrStderr()

			if f.reset {
				if _, err := fmt.Fprintln(errOut,
					"reset: TRUNCATE sources, flows, flow_collection, objects, segments, source_tags, flow_tags",
				); err != nil {
					return err
				}
				if err := Reset(ctx, pool); err != nil {
					return fmt.Errorf("reset: %w", err)
				}
			}

			if _, err := fmt.Fprintf(out,
				"loading: sources=%d flows=%d flow_collection=%d objects=%d segments=%d chunk_rows=%d workers=%d\n",
				p.SourcesCount, p.TotalFlows, p.FlowCollectionRows, p.ObjectsCount, p.TotalSegments,
				f.copyChunkRows, workers,
			); err != nil {
				return err
			}
			start := time.Now()
			opts := LoadOptions{
				ChunkSize: f.copyChunkRows,
				Workers:   workers,
				ProgressFn: func(table string, done, total int64) {
					pct := float64(done) / float64(total) * 100
					elapsed := time.Since(start).Round(time.Second)
					// Progress on stderr so it doesn't pollute the
					// load-complete stdout stream consumed by scripts.
					// A failed write here must not abort the load.
					_, _ = fmt.Fprintf(errOut, "[%s] %s: %d/%d (%.1f%%)\n",
						elapsed, table, done, total, pct)
				},
			}
			if err := Load(ctx, pool, p, f.seed, opts); err != nil {
				return fmt.Errorf("load: %w", err)
			}
			if _, err := fmt.Fprintf(out, "load complete in %s\n",
				time.Since(start).Round(time.Second)); err != nil {
				return err
			}
			return nil
		},
	}
	bindKnobFlags(cmd, &f)
	// --reset is load-specific (destructive); not bound by bindKnobFlags
	// so it cannot accidentally apply to `plan` or future read-only
	// subcommands.
	cmd.Flags().BoolVar(&f.reset, "reset", false,
		"TRUNCATE all loader tables (RESTART IDENTITY CASCADE) before loading. Destructive.")
	return cmd
}

// newResetCmd exposes Reset as a standalone subcommand for the case
// where the caller wants to flush the loader's tables without loading
// new data (e.g. before tearing down an environment, or between two
// `load` runs that compare different knob settings).
//
// Same `DB_*` env requirements as `load`. No row-count or preset
// flags — Reset is schema-aware, not plan-aware.
func newResetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "reset",
		Short: "TRUNCATE all loader tables (RESTART IDENTITY CASCADE). Destructive.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			dsn, err := dbenv.DSN()
			if err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(context.Background(),
				os.Interrupt, syscall.SIGTERM)
			defer stop()
			pool, err := pgxpool.New(ctx, dsn)
			if err != nil {
				return fmt.Errorf("db connect: %w", err)
			}
			defer pool.Close()

			if _, err := fmt.Fprintln(cmd.ErrOrStderr(),
				"reset: TRUNCATE sources, flows, flow_collection, objects, segments, source_tags, flow_tags",
			); err != nil {
				return err
			}
			start := time.Now()
			if err := Reset(ctx, pool); err != nil {
				return fmt.Errorf("reset: %w", err)
			}
			if _, err := fmt.Fprintf(cmd.OutOrStdout(),
				"reset complete in %s\n", time.Since(start).Round(time.Millisecond),
			); err != nil {
				return err
			}
			return nil
		},
	}
}
