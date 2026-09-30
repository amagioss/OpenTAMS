package migrations

import (
	"strings"
	"testing"
)

// TC-MIG-05: migration 000005 (objects.storage_id + reaping + both
// partial indexes + segments bounds invariants, ADR-0040 rule 4) is
// embedded and contains the expected DDL fragments.
func TestMigration_000005_Embedded(t *testing.T) {
	up, err := FS.ReadFile("000005_objects_storage_id_reaping.up.sql")
	if err != nil {
		t.Fatalf("read up: %v", err)
	}
	upStr := string(up)
	for _, want := range []string{
		"ADD COLUMN IF NOT EXISTS storage_id TEXT",
		"ADD COLUMN IF NOT EXISTS reaping BOOLEAN NOT NULL DEFAULT false",
		"CREATE INDEX IF NOT EXISTS objects_gc_claim_idx",
		"WHERE ref_count = 0 AND reaping = false",
		"CREATE INDEX IF NOT EXISTS objects_gc_retry_idx",
		"WHERE reaping = true",
		"ALTER COLUMN upper_ns SET NOT NULL",
		"DROP CONSTRAINT IF EXISTS segments_bounds_nonempty",
		"ADD CONSTRAINT segments_bounds_nonempty",
		"CHECK (upper_ns > lower_ns)",
	} {
		if !strings.Contains(upStr, want) {
			t.Errorf("up.sql missing %q", want)
		}
	}
	for _, gone := range []string{
		"segments_upper_ns_positive",
		"upper_ns IS NULL OR upper_ns > 0",
		"R15",
	} {
		if strings.Contains(upStr, gone) {
			t.Errorf("up.sql still contains %q", gone)
		}
	}

	down, err := FS.ReadFile("000005_objects_storage_id_reaping.down.sql")
	if err != nil {
		t.Fatalf("read down: %v", err)
	}
	downStr := string(down)
	for _, want := range []string{
		"DROP CONSTRAINT IF EXISTS segments_bounds_nonempty",
		"ALTER COLUMN upper_ns DROP NOT NULL",
		"DROP INDEX IF EXISTS objects_gc_retry_idx",
		"DROP INDEX IF EXISTS objects_gc_claim_idx",
		"DROP COLUMN IF EXISTS reaping",
		"DROP COLUMN IF EXISTS storage_id",
	} {
		if !strings.Contains(downStr, want) {
			t.Errorf("down.sql missing %q", want)
		}
	}
}
