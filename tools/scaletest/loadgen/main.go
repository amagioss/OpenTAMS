// scaletest-loadgen is the HTTP load tier of the OpenTAMS
// scale-test harness (docs/scale-test-plan.md). It drives the running
// API over HTTP at a configurable open-loop offered rate with a periodic
// jitter burst, issuing a weighted mix of read/write operations and
// reporting latency + load-control telemetry per workload bucket.
//
// It addresses the loaded dataset by replaying the same deterministic
// generator the loader used (matching --preset/--segments/--seed), so it
// needs no lookup queries. Auth: a bearer token is always sent; a dummy
// value works against the dev-auth provider (the realistic JWT path is a
// separate concern). See §4 of the plan.
//
// Subcommands:
//
//	endpoints — print the selected mix + dataset plan (no network).
//	run       — execute the load test and emit a per-bucket report.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/amagioss/opentams/tools/scaletest/dataset"
	"github.com/amagioss/opentams/tools/scaletest/dbstats"
	"github.com/amagioss/opentams/tools/scaletest/report"
)

type cliFlags struct {
	baseURL        string
	token          string
	tokenFile      string
	preset         string
	targetSegments int64
	seed           uint64
	profile        string
	rps            float64
	burstRPS       float64
	burstEvery     time.Duration
	burstFor       time.Duration
	maxInflight    int
	duration       time.Duration
	warmup         time.Duration
	bulkSize       int
	storageLimit   int
	idemKeys       int
	httpTimeout    time.Duration
	reportPath     string
	captureDB      bool

	// ramp-specific
	rampFrom      float64
	rampStep      float64
	rampMax       float64
	stepDur       time.Duration
	shedThreshold float64
	errThreshold  float64
}

func main() {
	if err := newRoot().Execute(); err != nil {
		os.Exit(1)
	}
}

func newRoot() *cobra.Command {
	root := &cobra.Command{
		Use:           "scaletest-loadgen",
		Short:         "OpenTAMS HTTP load generator",
		SilenceUsage:  true,
		SilenceErrors: false,
	}
	root.AddCommand(newEndpointsCmd(), newRunCmd(), newRampCmd())
	return root
}

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

func bindCommonFlags(cmd *cobra.Command, f *cliFlags) {
	cmd.Flags().StringVar(&f.preset, "preset", "6", `Dataset preset the target DB was loaded with: "6" or "1"`)
	cmd.Flags().Int64Var(&f.targetSegments, "segments", 1_000_000, "Segment count the target DB was loaded with (must match the load)")
	cmd.Flags().Uint64Var(&f.seed, "seed", 42, "RNG seed the target DB was loaded with (must match the load)")
	cmd.Flags().StringVar(&f.profile, "profile", "steady", `Workload profile: "steady" (non-destructive 60/40), "destructive" (adds deletes), or "idem-contention" (pair with --idem-keys=1)`)
	cmd.Flags().IntVar(&f.bulkSize, "bulk-size", 100, "Segments per register-bulk request")
	cmd.Flags().IntVar(&f.storageLimit, "storage-limit", 10, "Objects per create-storage-endpoint request")
	cmd.Flags().IntVar(&f.idemKeys, "idem-keys", 1_000_000,
		"Size of the shared idempotency-key pool; 1 = max lock contention (use with --profile=idem-contention)")
}

func newEndpointsCmd() *cobra.Command {
	var f cliFlags
	cmd := &cobra.Command{
		Use:   "endpoints",
		Short: "Print the selected mix and dataset plan (no network access)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			k, err := knobsFromFlags(&f)
			if err != nil {
				return err
			}
			plan, err := dataset.Compose(k)
			if err != nil {
				return err
			}
			m, err := newMix(f.profile)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if _, err := fmt.Fprintf(out, "profile %q (total weight %.0f):\n", f.profile, m.total); err != nil {
				return err
			}
			for _, op := range m.ops {
				if _, err := fmt.Fprintf(out, "  %-24s %5.1f%%\n", op.name, op.weight/m.total*100); err != nil {
					return err
				}
			}
			enc := json.NewEncoder(out)
			enc.SetIndent("", "  ")
			return enc.Encode(plan)
		},
	}
	bindCommonFlags(cmd, &f)
	return cmd
}

func newRunCmd() *cobra.Command {
	var f cliFlags
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run the HTTP load test and emit a per-bucket report",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runLoad(cmd, &f)
		},
	}
	bindCommonFlags(cmd, &f)
	cmd.Flags().StringVar(&f.baseURL, "base-url", "", "Target API base URL, e.g. http://api:8080 (required)")
	cmd.Flags().StringVar(&f.token, "token", "dummy", "Bearer token (dummy is fine against dev-auth)")
	cmd.Flags().StringVar(&f.tokenFile, "token-file", "",
		"Read the bearer token from this file (keeps a real JWT out of process args); overrides --token")
	cmd.Flags().Float64Var(&f.rps, "rps", 200, "Baseline offered requests/sec")
	cmd.Flags().Float64Var(&f.burstRPS, "burst-rps", 0, "Burst offered requests/sec (0 disables bursting)")
	cmd.Flags().DurationVar(&f.burstEvery, "burst-every", 5*time.Minute, "Burst period")
	cmd.Flags().DurationVar(&f.burstFor, "burst-for", 45*time.Second, "Burst duration within each period")
	cmd.Flags().IntVar(&f.maxInflight, "max-inflight", 256, "Max concurrent in-flight requests (open-loop bound); excess is shed")
	cmd.Flags().DurationVar(&f.duration, "duration", time.Minute, "Measurement window duration")
	cmd.Flags().DurationVar(&f.warmup, "warmup", 10*time.Second, "Warm-up window (discarded)")
	cmd.Flags().DurationVar(&f.httpTimeout, "timeout", 30*time.Second, "Per-request HTTP timeout")
	cmd.Flags().StringVar(&f.reportPath, "report", "", "Write the JSON report to this path (default: stdout)")
	cmd.Flags().BoolVar(&f.captureDB, "capture-db", false,
		"Also capture DB pg_stat_* before/after the run (needs DB_* env; loadgen is otherwise HTTP-only)")
	return cmd
}

// warnIfContentionMisconfigured notes when the idem-contention profile is
// selected without a collapsed key pool: every request then gets a unique
// idempotency key, so there is no lock contention to measure.
func warnIfContentionMisconfigured(w io.Writer, f *cliFlags) {
	if f.profile == "idem-contention" && f.idemKeys > 1 {
		fmt.Fprintf(w, "warning: --profile=idem-contention with --idem-keys=%d produces no lock contention; set --idem-keys=1\n", f.idemKeys) //nolint:errcheck // best-effort stderr diagnostic.
	}
}

func runLoad(cmd *cobra.Command, f *cliFlags) error {
	if f.baseURL == "" {
		return fmt.Errorf("--base-url is required")
	}
	if f.rps <= 0 {
		return fmt.Errorf("--rps must be > 0")
	}
	k, err := knobsFromFlags(f)
	if err != nil {
		return err
	}
	plan, err := dataset.Compose(k)
	if err != nil {
		return err
	}
	if plan.DeepFlowCount == 0 || plan.ShallowFlowCount == 0 {
		return fmt.Errorf("dataset must have both deep and shallow flows (got deep=%d shallow=%d)",
			plan.DeepFlowCount, plan.ShallowFlowCount)
	}
	m, err := newMix(f.profile)
	if err != nil {
		return err
	}

	token, err := resolveToken(f.token, f.tokenFile)
	if err != nil {
		return err
	}
	coll := newCollector(bucketSpecsFor(m))
	client := newClient(f.baseURL, token, f.httpTimeout, f.maxInflight)
	gen := dataset.NewGen(plan, f.seed)
	exec := newExecutor(client, gen, plan, m, coll, f.seed, f.bulkSize, f.storageLimit, f.idemKeys)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg := engineConfig{
		warmup:      f.warmup,
		measure:     f.duration,
		maxInflight: f.maxInflight,
		jit: jitter{
			baseline: f.rps,
			burst:    f.burstRPS,
			every:    f.burstEvery,
			dur:      f.burstFor,
		},
	}

	errOut := cmd.ErrOrStderr()
	warnIfContentionMisconfigured(errOut, f)
	fmt.Fprintf(errOut, "loadgen: %s · profile=%s · rps=%.0f burst=%.0f · warmup=%s measure=%s · max-inflight=%d\n",
		f.baseURL, f.profile, f.rps, f.burstRPS, f.warmup, f.duration, f.maxInflight) //nolint:errcheck // best-effort stderr diagnostic.

	// Optional DB-side capture (--capture-db). loadgen is HTTP-only; when
	// the operator has DB creds it can open a side pool just to snapshot
	// pg_stat_* before/after the run. Best-effort: any failure warns and
	// the load test continues without DB stats.
	statsTables := []string{"segments", "objects"}
	statsPool, beforeStats, statsOK := openDBStats(ctx, errOut, f.captureDB, statsTables)
	if statsPool != nil {
		defer statsPool.Close()
	}

	st := runEngine(ctx, cfg, exec.runOne)

	load := report.NewLoadStats(st.attempted, st.sent(), st.completed, st.shed, st.maxInflight, st.measureDur, f.rps, f.burstRPS)
	rep := &report.Report{
		Tool:       "scaletest-loadgen",
		ToolCommit: buildCommit(),
		StartedAt:  time.Now().UTC().Add(-(f.warmup + f.duration)),
		Host:       hostInfo(),
		Dataset:    datasetInfo(f, plan),
		Load:       &load,
		Buckets:    coll.results(st.measureDur),
	}
	if statsOK {
		if after, aerr := dbstats.Capture(ctx, statsPool, statsTables); aerr != nil {
			fmt.Fprintf(errOut, "warning: db stats (after): %v\n", aerr) //nolint:errcheck // best-effort stderr diagnostic.
		} else {
			d := dbstats.Diff(beforeStats, after)
			rep.DB.Stats = &d
		}
	}

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

func newRampCmd() *cobra.Command {
	var f cliFlags
	cmd := &cobra.Command{
		Use:   "ramp",
		Short: "Overload ramp: climb RPS until shed/error crosses a threshold",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runRampCmd(cmd, &f)
		},
	}
	bindCommonFlags(cmd, &f)
	cmd.Flags().StringVar(&f.baseURL, "base-url", "", "Target API base URL (required)")
	cmd.Flags().StringVar(&f.token, "token", "dummy", "Bearer token (dummy is fine against dev-auth)")
	cmd.Flags().StringVar(&f.tokenFile, "token-file", "", "Read the bearer token from this file; overrides --token")
	cmd.Flags().Float64Var(&f.rampFrom, "ramp-from", 200, "Starting offered RPS")
	cmd.Flags().Float64Var(&f.rampStep, "ramp-step", 200, "RPS increment per step")
	cmd.Flags().Float64Var(&f.rampMax, "ramp-max", 5000, "Maximum offered RPS")
	cmd.Flags().DurationVar(&f.stepDur, "step-dur", defaultStepDuration, "Hold each RPS level for this long")
	cmd.Flags().Float64Var(&f.shedThreshold, "shed-threshold", 0.05, "Stop when shed rate crosses this (0 disables)")
	cmd.Flags().Float64Var(&f.errThreshold, "err-threshold", 0.05, "Stop when error rate crosses this (0 disables)")
	cmd.Flags().IntVar(&f.maxInflight, "max-inflight", 256, "Max concurrent in-flight requests (open-loop bound)")
	cmd.Flags().DurationVar(&f.warmup, "warmup", 3*time.Second, "Per-step warm-up (discarded)")
	cmd.Flags().DurationVar(&f.httpTimeout, "timeout", 30*time.Second, "Per-request HTTP timeout")
	cmd.Flags().StringVar(&f.reportPath, "report", "", "Write the JSON report to this path (default: stdout)")
	// --idem-keys is already registered by bindCommonFlags; do not redefine it
	// here (pflag panics on a duplicate flag at registration time).
	return cmd
}

func runRampCmd(cmd *cobra.Command, f *cliFlags) error {
	if f.baseURL == "" {
		return fmt.Errorf("--base-url is required")
	}
	if f.rampFrom <= 0 || f.rampStep <= 0 || f.rampMax < f.rampFrom {
		return fmt.Errorf("invalid ramp bounds: from=%.0f step=%.0f max=%.0f", f.rampFrom, f.rampStep, f.rampMax)
	}
	k, err := knobsFromFlags(f)
	if err != nil {
		return err
	}
	plan, err := dataset.Compose(k)
	if err != nil {
		return err
	}
	if plan.DeepFlowCount == 0 || plan.ShallowFlowCount == 0 {
		return fmt.Errorf("dataset must have both deep and shallow flows")
	}
	m, err := newMix(f.profile)
	if err != nil {
		return err
	}

	token, err := resolveToken(f.token, f.tokenFile)
	if err != nil {
		return err
	}
	client := newClient(f.baseURL, token, f.httpTimeout, f.maxInflight)
	gen := dataset.NewGen(plan, f.seed)
	exec := newExecutor(client, gen, plan, m, newCollector(bucketSpecsFor(m)), f.seed, f.bulkSize, f.storageLimit, f.idemKeys)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errOut := cmd.ErrOrStderr()
	warnIfContentionMisconfigured(errOut, f)
	// Each step gets a fresh collector so its latencies are isolated; the
	// executor's per-op counters keep climbing across steps (so appended
	// writes never collide).
	run := func(ctx context.Context, rps float64) (engineStats, []report.BucketResult) {
		coll := newCollector(bucketSpecsFor(m))
		exec.useCollector(coll)
		fmt.Fprintf(errOut, "ramp step: %.0f rps for %s...\n", rps, f.stepDur) //nolint:errcheck // best-effort stderr diagnostic.
		st := runEngine(ctx, engineConfig{
			warmup:      f.warmup,
			measure:     f.stepDur,
			maxInflight: f.maxInflight,
			jit:         jitter{baseline: rps},
		}, exec.runOne)
		return st, coll.results(st.measureDur)
	}

	startedAt := time.Now().UTC()
	res := runRamp(ctx, rampConfig{
		from:          f.rampFrom,
		step:          f.rampStep,
		maxRPS:        f.rampMax,
		shedThreshold: f.shedThreshold,
		errThreshold:  f.errThreshold,
	}, run)

	rep := &report.Report{
		Tool:       "scaletest-loadgen",
		ToolCommit: buildCommit(),
		StartedAt:  startedAt,
		Host:       hostInfo(),
		Dataset:    datasetInfo(f, plan),
		Ramp:       &res,
	}
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
