package main

import (
	"math/rand"
	"testing"
)

func TestProfileSteadyHasNoDeletes(t *testing.T) {
	m, err := newMix("steady")
	if err != nil {
		t.Fatalf("newMix(steady): %v", err)
	}
	for _, op := range m.ops {
		if op.kind == opDeleteByTimerange || op.kind == opDeleteByObject {
			t.Errorf("steady profile must not contain delete op %q", op.name)
		}
	}
	// Reads must outweigh writes 60/40-ish, and list-by-timerange must be
	// the heavier read (per the agreed mix).
	var tr, fl float64
	for _, op := range m.ops {
		switch op.kind {
		case opListByTimerange:
			tr = op.weight
		case opListByFlow:
			fl = op.weight
		}
	}
	if tr <= fl {
		t.Errorf("list-by-timerange weight %.0f should exceed list-by-flow %.0f", tr, fl)
	}
}

func TestProfileDestructiveHasDeletes(t *testing.T) {
	m, err := newMix("destructive")
	if err != nil {
		t.Fatalf("newMix(destructive): %v", err)
	}
	var hasDel bool
	for _, op := range m.ops {
		if op.kind == opDeleteByTimerange || op.kind == opDeleteByObject {
			hasDel = true
		}
	}
	if !hasDel {
		t.Error("destructive profile must contain delete ops")
	}
}

func TestNewMixUnknownProfile(t *testing.T) {
	if _, err := newMix("bogus"); err == nil {
		t.Error("expected error for unknown profile")
	}
}

func TestMixPickDistribution(t *testing.T) {
	m, err := newMix("steady")
	if err != nil {
		t.Fatalf("newMix: %v", err)
	}
	r := rand.New(rand.NewSource(1)) //nolint:gosec // test-only, deterministic distribution check.
	const n = 200_000
	counts := make(map[opKind]int)
	for range n {
		counts[m.pick(r)]++
	}
	for _, op := range m.ops {
		want := op.weight / m.total
		got := float64(counts[op.kind]) / n
		if diff := got - want; diff > 0.02 || diff < -0.02 {
			t.Errorf("op %q empirical %.3f vs weight %.3f (diff %.3f)", op.name, got, want, diff)
		}
	}
}

func TestMixReadWriteSplitSteady(t *testing.T) {
	m, err := newMix("steady")
	if err != nil {
		t.Fatalf("newMix: %v", err)
	}
	reads := m.weightFor(opListByFlow) + m.weightFor(opListByTimerange)
	if frac := reads / m.total; frac < 0.58 || frac > 0.62 {
		t.Errorf("steady read fraction = %.3f, want ~0.60", frac)
	}
}

func TestProfileIdemContention(t *testing.T) {
	m, err := newMix("idem-contention")
	if err != nil {
		t.Fatalf("newMix(idem-contention): %v", err)
	}
	// The profile must be dominated by idempotent-retry so the run
	// actually concentrates load on the dedupe path.
	if frac := m.weightFor(opIdempotentRetry) / m.total; frac < 0.6 {
		t.Errorf("idem-contention idempotent fraction = %.2f, want >= 0.6", frac)
	}
	// And it must not contain deletes (non-destructive).
	for _, op := range m.ops {
		if op.kind == opDeleteByTimerange || op.kind == opDeleteByObject {
			t.Errorf("idem-contention must not contain delete op %q", op.name)
		}
	}
}
