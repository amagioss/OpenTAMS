// Package dbstats captures the portable DB-side resource signal for the
// OpenTAMS scale-test harness: a before/after snapshot of standard
// `pg_stat_*` views, diffed into a report.DBStatsDelta. The views are
// identical on RDS and Cloud SQL, so this works on any managed Postgres
// with only a connection — no host agent (which managed Postgres does
// not permit anyway). See docs/scale-test-plan.md §6.3.
package dbstats

import (
	"sort"
	"time"

	"github.com/amagioss/opentams/tools/scaletest/report"
)

// topQueriesN caps how many pg_stat_statements entries the diff keeps.
const topQueriesN = 10

// nonNeg clamps a cumulative-counter delta to >= 0 (a negative value
// means the counter was reset/wrapped between snapshots).
func nonNeg(v int64) int64 {
	return max(v, 0)
}

// TableSnap is one table's cumulative/current counters at capture time.
type TableSnap struct {
	LiveTup          int64
	DeadTup          int64
	AutovacuumCount  int64
	AutoanalyzeCount int64
	SizeBytes        int64
}

// QuerySnap is one pg_stat_statements row at capture time (cumulative).
type QuerySnap struct {
	Text    string
	Calls   int64
	TotalMs float64
}

// Snapshot is the set of database counters captured at one instant.
// Cumulative counters (commits, blocks, WAL, …) are diffed across two
// snapshots; per-table tuple counts are reported as the later snapshot's
// current value. Zero fields denote views/columns that weren't available
// (best-effort capture across Postgres versions / extensions).
type Snapshot struct {
	At          time.Time
	Commits     int64
	Rollbacks   int64
	BlksHit     int64
	BlksRead    int64
	Deadlocks   int64
	TempFiles   int64
	TempBytes   int64
	WALBytes    int64
	Checkpoints int64
	Tables      map[string]TableSnap
	Queries     map[int64]QuerySnap // keyed by pg_stat_statements queryid
}

// Diff computes the change from before to after, producing the
// serializable report delta. Cumulative counters subtract; per-table
// tuple counts take the after value; cache-hit ratio is computed over
// the window (block activity during the run, not lifetime).
//
// Cumulative deltas are clamped to >= 0: a negative value means the
// counter was reset (pg_stat_reset) or wrapped between snapshots, which
// makes the delta meaningless — reporting 0 is less misleading than a
// negative WAL/commit count.
func Diff(before, after Snapshot) report.DBStatsDelta {
	d := report.DBStatsDelta{
		Commits:     nonNeg(after.Commits - before.Commits),
		Rollbacks:   nonNeg(after.Rollbacks - before.Rollbacks),
		BlksHit:     nonNeg(after.BlksHit - before.BlksHit),
		BlksRead:    nonNeg(after.BlksRead - before.BlksRead),
		Deadlocks:   nonNeg(after.Deadlocks - before.Deadlocks),
		TempFiles:   nonNeg(after.TempFiles - before.TempFiles),
		TempBytes:   nonNeg(after.TempBytes - before.TempBytes),
		WALBytes:    nonNeg(after.WALBytes - before.WALBytes),
		Checkpoints: nonNeg(after.Checkpoints - before.Checkpoints),
	}
	if total := d.BlksHit + d.BlksRead; total > 0 {
		d.CacheHitRatio = float64(d.BlksHit) / float64(total)
	}
	d.Tables = diffTables(before.Tables, after.Tables)
	d.TopQueries = diffQueries(before.Queries, after.Queries)
	return d
}

func diffTables(before, after map[string]TableSnap) []report.TableStatsDelta {
	if len(after) == 0 {
		return nil
	}
	names := make([]string, 0, len(after))
	for name := range after {
		names = append(names, name)
	}
	sort.Strings(names)

	out := make([]report.TableStatsDelta, 0, len(names))
	for _, name := range names {
		a := after[name]
		b := before[name] // zero if the table wasn't in the before snapshot
		out = append(out, report.TableStatsDelta{
			Name:            name,
			LiveTuples:      a.LiveTup,
			DeadTuples:      a.DeadTup,
			Autovacuums:     nonNeg(a.AutovacuumCount - b.AutovacuumCount),
			Autoanalyzes:    nonNeg(a.AutoanalyzeCount - b.AutoanalyzeCount),
			SizeBytesBefore: b.SizeBytes,
			SizeBytesAfter:  a.SizeBytes,
		})
	}
	return out
}

func diffQueries(before, after map[int64]QuerySnap) []report.QueryStat {
	if len(after) == 0 {
		return nil
	}
	var stats []report.QueryStat
	for id, a := range after {
		b := before[id] // zero if the query first appeared during the run
		callsDelta := a.Calls - b.Calls
		timeDelta := a.TotalMs - b.TotalMs
		if callsDelta <= 0 && timeDelta <= 0 {
			continue // unchanged during the run
		}
		mean := 0.0
		if callsDelta > 0 {
			mean = timeDelta / float64(callsDelta)
		}
		stats = append(stats, report.QueryStat{
			Query:       a.Text,
			Calls:       callsDelta,
			TotalTimeMs: timeDelta,
			MeanTimeMs:  mean,
		})
	}
	sort.Slice(stats, func(i, j int) bool { return stats[i].TotalTimeMs > stats[j].TotalTimeMs })
	if len(stats) > topQueriesN {
		stats = stats[:topQueriesN]
	}
	return stats
}
