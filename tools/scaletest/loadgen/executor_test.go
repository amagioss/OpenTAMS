package main

import "testing"

func TestIdemSlotSharedKeys(t *testing.T) {
	// keys=1 → every call collapses to slot 0 (max contention on one
	// idempotency_keys row).
	for i := int64(0); i < 100; i++ {
		if got := idemSlot(i, 1); got != 0 {
			t.Fatalf("idemSlot(%d, 1) = %d, want 0", i, got)
		}
	}
	// keys=4 → calls spread over exactly 4 slots, deterministically.
	if idemSlot(5, 4) != 1 || idemSlot(8, 4) != 0 || idemSlot(7, 4) != 3 {
		t.Errorf("idemSlot mod-4 wrong: %d %d %d", idemSlot(5, 4), idemSlot(8, 4), idemSlot(7, 4))
	}
	// keys <= 0 is treated as 1 (no divide-by-zero).
	if idemSlot(9, 0) != 0 {
		t.Errorf("idemSlot(9, 0) = %d, want 0", idemSlot(9, 0))
	}
	// Large key space → effectively unique (slot == i), restoring the
	// default non-contended behaviour.
	if idemSlot(42, 1_000_000) != 42 {
		t.Errorf("idemSlot(42, 1e6) = %d, want 42", idemSlot(42, 1_000_000))
	}
}
