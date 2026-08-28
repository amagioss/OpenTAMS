package main

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"runtime/debug"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/amagioss/opentams/tools/scaletest/dataset"
	"github.com/amagioss/opentams/tools/scaletest/report"
)

// capturedSettings are the scale-relevant Postgres settings recorded in
// every report so two runs are comparable with their tuning visible
// (docs/scale-test-plan.md §5). Extend as the variant matrix grows.
var capturedSettings = []string{
	"server_version",
	"shared_buffers",
	"effective_cache_size",
	"work_mem",
	"maintenance_work_mem",
	"max_wal_size",
	"max_connections",
	"random_page_cost",
	"effective_io_concurrency",
	"max_parallel_maintenance_workers",
	"default_statistics_target",
	"autovacuum_vacuum_scale_factor",
	"wal_compression",
}

// buildCommit returns the VCS revision the binary was built from, so a
// report ties back to exact harness code. Returns "unknown" for builds
// without embedded VCS info (e.g. `go run`).
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

// hostInfo records where the bench ran. For a DB-direct measurement the
// host↔DB network round-trip is part of every latency number, so the
// host (and its proximity to the DB) is interpretive context.
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

// datasetInfo records the shape the bench addressed, sourced from the
// composed plan plus the run flags (preset/seed are flag-level inputs).
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

// captureDBInfo reads the server version and the scale-relevant settings
// from the live database. A failure is non-fatal to the run (the caller
// logs a warning) — a report missing DB settings is still useful.
func captureDBInfo(ctx context.Context, pool *pgxpool.Pool) (report.DBInfo, error) {
	var version string
	if err := pool.QueryRow(ctx, "SELECT version()").Scan(&version); err != nil {
		return report.DBInfo{}, fmt.Errorf("select version: %w", err)
	}

	settings := make(map[string]string, len(capturedSettings))
	rows, err := pool.Query(ctx,
		"SELECT name, setting, unit FROM pg_settings WHERE name = ANY($1)", capturedSettings)
	if err != nil {
		return report.DBInfo{}, fmt.Errorf("query pg_settings: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name, setting string
		var unit *string
		if err := rows.Scan(&name, &setting, &unit); err != nil {
			return report.DBInfo{}, fmt.Errorf("scan pg_settings: %w", err)
		}
		if unit != nil && *unit != "" {
			setting += *unit
		}
		settings[name] = setting
	}
	if err := rows.Err(); err != nil {
		return report.DBInfo{}, fmt.Errorf("iterate pg_settings: %w", err)
	}
	return report.DBInfo{Version: version, Settings: settings}, nil
}
