package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"runtime"
	"runtime/debug"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/amagioss/opentams/tools/scaletest/dataset"
	"github.com/amagioss/opentams/tools/scaletest/dbenv"
	"github.com/amagioss/opentams/tools/scaletest/dbstats"
	"github.com/amagioss/opentams/tools/scaletest/report"
)

// openDBStats optionally opens a side pool and takes the "before"
// pg_stat_* snapshot for --capture-db. loadgen is HTTP-only, so this is
// best-effort: if disabled, or the DB env/connection/snapshot fails, it
// warns and returns statsOK=false so the run proceeds without DB stats.
// The returned pool (if non-nil) is the caller's to Close.
func openDBStats(ctx context.Context, warn io.Writer, enabled bool, tables []string) (pool *pgxpool.Pool, before dbstats.Snapshot, statsOK bool) {
	if !enabled {
		return nil, dbstats.Snapshot{}, false
	}
	dsn, derr := dbenv.DSN()
	if derr != nil {
		fmt.Fprintf(warn, "warning: --capture-db set but DB env incomplete: %v\n", derr) //nolint:errcheck // best-effort stderr diagnostic.
		return nil, dbstats.Snapshot{}, false
	}
	cfg, cerr := pgxpool.ParseConfig(dsn)
	if cerr != nil {
		fmt.Fprintf(warn, "warning: --capture-db parse dsn: %v\n", cerr) //nolint:errcheck // best-effort stderr diagnostic.
		return nil, dbstats.Snapshot{}, false
	}
	// Two sequential snapshots — cap the side pool so it doesn't sit on
	// connection budget for the whole run while the API is also using the
	// same max_connections.
	cfg.MaxConns = 2
	p, perr := pgxpool.NewWithConfig(ctx, cfg)
	if perr != nil {
		fmt.Fprintf(warn, "warning: --capture-db connect failed: %v\n", perr) //nolint:errcheck // best-effort stderr diagnostic.
		return nil, dbstats.Snapshot{}, false
	}
	bs, berr := dbstats.Capture(ctx, p, tables)
	if berr != nil {
		fmt.Fprintf(warn, "warning: db stats (before): %v\n", berr) //nolint:errcheck // best-effort stderr diagnostic.
		return p, dbstats.Snapshot{}, false
	}
	return p, bs, true
}

// buildCommit returns the VCS revision the binary was built from so a
// report ties back to exact harness code; "unknown" for `go run`.
func buildCommit() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	for _, s := range info.Settings {
		if s.Key == "vcs.revision" {
			rev := s.Value
			if len(rev) > 12 {
				rev = rev[:12]
			}
			return rev
		}
	}
	return "unknown"
}

// resolveToken returns the bearer token to send: the contents of
// tokenFile (whitespace-trimmed) when set — so a real JWT stays out of
// the process arguments and shell history — otherwise the --token value.
func resolveToken(tokenFlag, tokenFile string) (string, error) {
	if tokenFile == "" {
		return tokenFlag, nil
	}
	b, err := os.ReadFile(tokenFile) //nolint:gosec // operator-supplied token path.
	if err != nil {
		return "", fmt.Errorf("read token file %q: %w", tokenFile, err)
	}
	return strings.TrimSpace(string(b)), nil
}

func hostInfo() report.HostInfo {
	name, err := os.Hostname()
	if err != nil {
		name = "unknown"
	}
	return report.HostInfo{
		Hostname: name,
		OS:       runtime.GOOS,
		Arch:     runtime.GOARCH,
		NumCPU:   runtime.NumCPU(),
	}
}

func datasetInfo(f *cliFlags, plan dataset.Plan) report.DatasetInfo {
	return report.DatasetInfo{
		Preset:           f.preset,
		Seed:             f.seed,
		ChunkDurationSec: plan.ChunkDurationSec,
		TotalSegments:    plan.TotalSegments,
		TotalFlows:       plan.TotalFlows,
		SourcesCount:     plan.SourcesCount,
		ObjectsCount:     plan.ObjectsCount,
		DeepFlowCount:    plan.DeepFlowCount,
		DeepDepth:        plan.DeepDepth,
		ShallowDepth:     plan.ShallowDepth,
	}
}
