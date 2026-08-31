// scaletest-bench is the DB-direct measurement tier (D3) of the OpenTAMS
// scale-test harness described in docs/scale-test-plan.md. It drives the
// metastore methods (InsertSegments / ListSegments /
// DeleteSegmentsByTimerange and related paths) straight through the pgx
// pool against a pre-loaded dataset, with no HTTP layer — isolating
// database / schema cost from API-server cost (which the D4 HTTP tier
// measures).
//
// It addresses rows by replaying the same deterministic generator the
// loader used (matching --preset / --segments / --seed), so it needs no
// lookup queries to find flow_ids or object_ids.
//
// Subcommands:
//
//	list-buckets — print the available workload buckets (no DB).
//	plan         — print the dataset plan the bench will assume (no DB).
//	run          — measure the selected buckets against the DB_* database
//	               and emit a per-bucket report (JSON + markdown summary).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"

	"github.com/amagioss/opentams/internal/metastore"
	"github.com/amagioss/opentams/tools/scaletest/dataset"
	"github.com/amagioss/opentams/tools/scaletest/dbenv"
	"github.com/amagioss/opentams/tools/scaletest/dbstats"
	"github.com/amagioss/opentams/tools/scaletest/report"
)

type cliFlags struct {
	preset         string
	targetSegments int64
	seed           uint64
	buckets        []string
	samples        int
	warmup         int
	reportPath     string
}

func main() {
	if err := newRoot().Execute(); err != nil {
		os.Exit(1)
	}
}

func newRoot() *cobra.Command {
	root := &cobra.Command{
		Use:           "scaletest-bench",
		Short:         "OpenTAMS DB-direct scale benchmark (D3)",
		SilenceUsage:  true,
		SilenceErrors: false,
	}
	root.AddCommand(newListBucketsCmd(), newPlanCmd(), newRunCmd())
	return root
}

// knobsFromFlags mirrors the loader's preset handling so the bench
// addresses exactly the dataset the loader produced. Only the two
// canonical presets are exposed here — a bench run must target a loaded
// dataset, and those are loaded via the loader's "6"/"1" presets.
func knobsFromFlags(f *cliFlags) (dataset.Knobs, error) {
	switch f.preset {
	case "6":
		return dataset.Preset6(f.targetSegments), nil
	case "1":
		return dataset.Preset1(f.targetSegments), nil
	default:
		return dataset.Knobs{}, fmt.Errorf(`unknown preset %q (want "6" or "1")`, f.preset)
	}
}

func bindDatasetFlags(cmd *cobra.Command, f *cliFlags) {
	cmd.Flags().StringVar(&f.preset, "preset", "6",
		`Dataset preset the target DB was loaded with: "6" or "1"`)
	cmd.Flags().Int64Var(&f.targetSegments, "segments", 1_000_000,
		"Segment row count the target DB was loaded with (must match the load)")
	cmd.Flags().Uint64Var(&f.seed, "seed", 42,
		"RNG seed the target DB was loaded with (must match the load)")
}

func newListBucketsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list-buckets",
		Short: "List the available workload buckets (no database access)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			for _, b := range registry() {
				if _, err := fmt.Fprintf(out, "%-22s %-7s %s\n", b.name, b.op, b.desc); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

func newPlanCmd() *cobra.Command {
	var f cliFlags
	cmd := &cobra.Command{
		Use:   "plan",
		Short: "Print the dataset plan the bench will assume (no database access)",
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
	bindDatasetFlags(cmd, &f)
	return cmd
}

func newRunCmd() *cobra.Command {
	var f cliFlags
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Measure the selected buckets and emit a per-bucket report",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runBench(cmd, &f)
		},
	}
	bindDatasetFlags(cmd, &f)
	cmd.Flags().StringSliceVar(&f.buckets, "bucket", nil,
		"Buckets to run (comma-separated or repeated); empty runs all. See list-buckets.")
	cmd.Flags().IntVar(&f.samples, "samples", 1000,
		"Timed calls per bucket")
	cmd.Flags().IntVar(&f.warmup, "warmup", 100,
		"Discarded warm-up calls per bucket (prime caches/plans)")
	cmd.Flags().StringVar(&f.reportPath, "report", "",
		"Write the JSON report to this path (default: stdout)")
	return cmd
}

func runBench(cmd *cobra.Command, f *cliFlags) error {
	selected, unknown := findBuckets(f.buckets)
	if len(unknown) > 0 {
		return fmt.Errorf("unknown bucket(s): %s (see list-buckets)", strings.Join(unknown, ", "))
	}
	if len(selected) == 0 {
		return fmt.Errorf("no buckets selected")
	}

	k, err := knobsFromFlags(f)
	if err != nil {
		return err
	}
	plan, err := dataset.Compose(k)
	if err != nil {
		return err
	}

	dsn, err := dbenv.DSN()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return fmt.Errorf("db connect: %w", err)
	}
	defer pool.Close()

	env := &benchEnv{
		store:   metastore.New(pool),
		pool:    pool,
		gen:     dataset.NewGen(plan, f.seed),
		plan:    plan,
		samples: f.samples,
		warmup:  f.warmup,
	}

	errOut := cmd.ErrOrStderr()
	rep := &report.Report{
		Tool:       "scaletest-bench",
		ToolCommit: buildCommit(),
		StartedAt:  time.Now().UTC(),
		Host:       hostInfo(),
		Dataset:    datasetInfo(f, plan),
	}
	if dbInfo, derr := captureDBInfo(ctx, pool); derr != nil {
		// Non-fatal: a report without DB settings is still useful.
		fmt.Fprintf(errOut, "warning: could not capture db info: %v\n", derr) //nolint:errcheck // diagnostic on stderr; a failed write must not abort the run.
	} else {
		rep.DB = dbInfo
	}

	// DB-side resource capture (D5b): snapshot the pg_stat_* counters
	// before the buckets run, diff against an after-snapshot below. The
	// DB is the bottleneck at scale, so this is where the "why" lives.
	statsTables := []string{"segments", "objects"}
	beforeStats, statsErr := dbstats.Capture(ctx, pool, statsTables)
	if statsErr != nil {
		fmt.Fprintf(errOut, "warning: could not capture db stats: %v\n", statsErr) //nolint:errcheck // best-effort stderr diagnostic.
	}

	// Progress / summary writes go to stderr and are best-effort: a
	// failed diagnostic write must never abort a measurement run.
	for _, b := range selected {
		fmt.Fprintf(errOut, "running bucket %q (%d samples, %d warmup)...\n", b.name, f.samples, f.warmup) //nolint:errcheck // best-effort stderr diagnostic.
		res, rerr := b.run(ctx, env)
		if rerr != nil {
			return fmt.Errorf("bucket %q: %w", b.name, rerr)
		}
		rep.Buckets = append(rep.Buckets, res)
		if ctx.Err() != nil {
			fmt.Fprintln(errOut, "interrupted; reporting buckets completed so far") //nolint:errcheck // best-effort stderr diagnostic.
			break
		}
	}

	if statsErr == nil {
		if afterStats, aerr := dbstats.Capture(ctx, pool, statsTables); aerr != nil {
			fmt.Fprintf(errOut, "warning: could not capture db stats (after): %v\n", aerr) //nolint:errcheck // best-effort stderr diagnostic.
		} else {
			d := dbstats.Diff(beforeStats, afterStats)
			rep.DB.Stats = &d
		}
	}

	// Markdown summary to stderr (human), JSON to file or stdout (machine).
	fmt.Fprintln(errOut, "\n"+rep.Markdown()) //nolint:errcheck // best-effort stderr diagnostic.
	if f.reportPath != "" {
		if err := rep.WriteJSONFile(f.reportPath); err != nil {
			return err
		}
		fmt.Fprintf(errOut, "report written to %s\n", f.reportPath) //nolint:errcheck // best-effort stderr diagnostic.
		return nil
	}
	return rep.WriteJSON(cmd.OutOrStdout())
}
