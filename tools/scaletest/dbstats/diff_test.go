package dbstats

import (
	"testing"
	"time"
)

func TestDiffCounterDeltasAndCacheRatio(t *testing.T) {
	before := Snapshot{
		Commits: 100, Rollbacks: 5, BlksHit: 1000, BlksRead: 100,
		Deadlocks: 0, TempFiles: 1, TempBytes: 500, WALBytes: 1_000_000, Checkpoints: 2,
	}
	after := Snapshot{
		Commits: 1100, Rollbacks: 6, BlksHit: 10_000, BlksRead: 1000,
		Deadlocks: 1, TempFiles: 3, TempBytes: 2500, WALBytes: 5_000_000, Checkpoints: 4,
	}
	d := Diff(before, after)
	if d.Commits != 1000 {
		t.Errorf("commits delta = %d, want 1000", d.Commits)
	}
	if d.Rollbacks != 1 || d.Deadlocks != 1 || d.Checkpoints != 2 {
		t.Errorf("deltas wrong: %+v", d)
	}
	if d.WALBytes != 4_000_000 {
		t.Errorf("WAL delta = %d, want 4_000_000", d.WALBytes)
	}
	if d.TempBytes != 2000 || d.TempFiles != 2 {
		t.Errorf("temp deltas wrong: files=%d bytes=%d", d.TempFiles, d.TempBytes)
	}
	// Cache hit over the window: hitΔ=9000, readΔ=900 → 9000/9900 ≈ 0.909.
	if got := d.CacheHitRatio; got < 0.905 || got > 0.913 {
		t.Errorf("cache hit ratio = %v, want ~0.909", got)
	}
}

func TestDiffCacheRatioZeroBlocksNoPanic(t *testing.T) {
	d := Diff(Snapshot{BlksHit: 10, BlksRead: 2}, Snapshot{BlksHit: 10, BlksRead: 2})
	if d.CacheHitRatio != 0 {
		t.Errorf("cache ratio with no block activity = %v, want 0", d.CacheHitRatio)
	}
}

func TestDiffClampsNegativeDeltas(t *testing.T) {
	// after < before simulates a pg_stat_reset between snapshots; the
	// delta must clamp to 0, not report a negative WAL/commit count.
	before := Snapshot{Commits: 1000, WALBytes: 5_000_000, Checkpoints: 9,
		Tables: map[string]TableSnap{"segments": {AutovacuumCount: 3, SizeBytes: 10}}}
	after := Snapshot{Commits: 10, WALBytes: 1000, Checkpoints: 1,
		Tables: map[string]TableSnap{"segments": {AutovacuumCount: 0, SizeBytes: 20}}}
	d := Diff(before, after)
	if d.Commits != 0 || d.WALBytes != 0 || d.Checkpoints != 0 {
		t.Errorf("negative deltas not clamped: commits=%d wal=%d ckpt=%d", d.Commits, d.WALBytes, d.Checkpoints)
	}
	if len(d.Tables) != 1 || d.Tables[0].Autovacuums != 0 {
		t.Errorf("table autovacuum delta not clamped: %+v", d.Tables)
	}
}

func TestDiffTables(t *testing.T) {
	before := Snapshot{Tables: map[string]TableSnap{
		"segments": {LiveTup: 1000, DeadTup: 0, AutovacuumCount: 0, SizeBytes: 1 << 20},
	}}
	after := Snapshot{Tables: map[string]TableSnap{
		"segments": {LiveTup: 5000, DeadTup: 200, AutovacuumCount: 2, SizeBytes: 4 << 20},
	}}
	d := Diff(before, after)
	if len(d.Tables) != 1 {
		t.Fatalf("tables = %d, want 1", len(d.Tables))
	}
	tbl := d.Tables[0]
	if tbl.Name != "segments" {
		t.Errorf("name = %q", tbl.Name)
	}
	// Tuple counts are the current (after) snapshot.
	if tbl.LiveTuples != 5000 || tbl.DeadTuples != 200 {
		t.Errorf("tuples = %d/%d, want 5000/200", tbl.LiveTuples, tbl.DeadTuples)
	}
	// Autovacuum count is the delta over the run.
	if tbl.Autovacuums != 2 {
		t.Errorf("autovacuums = %d, want 2", tbl.Autovacuums)
	}
	if tbl.SizeBytesBefore != 1<<20 || tbl.SizeBytesAfter != 4<<20 {
		t.Errorf("sizes = %d/%d", tbl.SizeBytesBefore, tbl.SizeBytesAfter)
	}
}

func TestDiffTopQueriesByTotalTimeDelta(t *testing.T) {
	before := Snapshot{Queries: map[int64]QuerySnap{
		1: {Text: "SELECT a", Calls: 10, TotalMs: 100},
		2: {Text: "INSERT b", Calls: 5, TotalMs: 50},
	}}
	after := Snapshot{Queries: map[int64]QuerySnap{
		1: {Text: "SELECT a", Calls: 20, TotalMs: 150},  // Δ totalMs 50, callsΔ 10
		2: {Text: "INSERT b", Calls: 105, TotalMs: 950}, // Δ totalMs 900, callsΔ 100
		3: {Text: "new q", Calls: 1, TotalMs: 5},        // appeared during run
	}}
	d := Diff(before, after)
	if len(d.TopQueries) < 2 {
		t.Fatalf("top queries = %d, want >= 2", len(d.TopQueries))
	}
	// Ranked by total-time delta: INSERT b (900) first.
	if d.TopQueries[0].Query != "INSERT b" {
		t.Errorf("top query = %q, want INSERT b", d.TopQueries[0].Query)
	}
	if d.TopQueries[0].Calls != 100 || d.TopQueries[0].TotalTimeMs != 900 {
		t.Errorf("top query stats wrong: %+v", d.TopQueries[0])
	}
}

func TestSnapshotAtPreserved(t *testing.T) {
	// Diff doesn't need At, but a Snapshot should carry its capture time.
	s := Snapshot{At: time.Unix(100, 0)}
	if s.At.Unix() != 100 {
		t.Errorf("At not preserved")
	}
}
