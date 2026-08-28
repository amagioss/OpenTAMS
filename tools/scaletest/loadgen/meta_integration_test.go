//go:build integration

package main

import (
	"context"
	"io"
	"strconv"
	"testing"

	"github.com/amagioss/opentams/internal/dbtest"
	"github.com/amagioss/opentams/tools/scaletest/dbstats"
	"github.com/amagioss/opentams/tools/scaletest/report"
)

// TestOpenDBStatsWiring covers the loadgen --capture-db seam end-to-end
// against a real database: openDBStats reads DB_* env, opens a capped
// side pool, and captures the before-snapshot; then the runLoad tail
// (after-capture → Diff → attach) is simulated and asserted to populate
// report.DB.Stats. This also pins the F1 fix (the side pool is capped to
// 2 connections so it doesn't sit on connection budget during a run).
func TestOpenDBStatsWiring(t *testing.T) {
	ctx := context.Background()
	pool, cleanup, err := dbtest.Setup(ctx, dbtest.DefaultMigrationsPath())
	if err != nil {
		t.Fatalf("dbtest.Setup: %v", err)
	}
	defer cleanup()

	cc := pool.Config().ConnConfig
	t.Setenv("DB_HOST", cc.Host)
	t.Setenv("DB_PORT", strconv.Itoa(int(cc.Port)))
	t.Setenv("DB_USER", cc.User)
	t.Setenv("DB_PASSWORD", cc.Password)
	t.Setenv("DB_NAME", cc.Database)
	t.Setenv("DB_SSLMODE", "disable")

	tables := []string{"segments", "objects"}

	// Disabled: no pool, not ok.
	if p, _, ok := openDBStats(ctx, io.Discard, false, tables); p != nil || ok {
		t.Fatalf("disabled openDBStats returned pool=%v ok=%v, want nil/false", p, ok)
	}

	sp, before, ok := openDBStats(ctx, io.Discard, true, tables)
	if !ok {
		t.Fatal("openDBStats not ok with valid DB env")
	}
	if sp == nil {
		t.Fatal("openDBStats returned nil pool while ok")
	}
	defer sp.Close()
	if sp.Config().MaxConns != 2 {
		t.Errorf("capture pool MaxConns = %d, want 2 (F1)", sp.Config().MaxConns)
	}

	// Simulate the runLoad tail: after-snapshot → Diff → attach.
	after, err := dbstats.Capture(ctx, sp, tables)
	if err != nil {
		t.Fatalf("Capture(after): %v", err)
	}
	d := dbstats.Diff(before, after)
	rep := &report.Report{Tool: "scaletest-loadgen"}
	rep.DB.Stats = &d
	if rep.DB.Stats == nil {
		t.Fatal("db.stats not attached")
	}
	var found bool
	for _, tbl := range d.Tables {
		if tbl.Name == "segments" {
			found = true
		}
	}
	if !found {
		t.Errorf("segments table not in captured stats: %+v", d.Tables)
	}
}
