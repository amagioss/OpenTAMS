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

// TC-GC-173: migration 000006 renames the GC claim flag to deleting,
// creates the allocations table with its two partial indexes, and its
// down file reverses both. TC-MIG-05 above stays unchanged.
func TestMigration_000006_Contents(t *testing.T) {
	up := readMigration(t, "000006_allocations_and_deleting.up.sql")
	for _, want := range []string{
		"ALTER TABLE objects RENAME COLUMN reaping TO deleting",
		"CREATE TABLE IF NOT EXISTS allocations",
		"allocations_gc_claim_idx",
		"allocations_gc_retry_idx",
	} {
		if !strings.Contains(up, want) {
			t.Errorf("up.sql missing %q", want)
		}
	}
	down := readMigration(t, "000006_allocations_and_deleting.down.sql")
	for _, want := range []string{
		"DROP TABLE IF EXISTS allocations",
		"RENAME COLUMN deleting TO reaping",
	} {
		if !strings.Contains(down, want) {
			t.Errorf("down.sql missing %q", want)
		}
	}
}

// TC-GC-172: migration 000006 is one implicit transaction. A failed
// statement must roll back the rename too, so neither file may manage
// its own transaction or build an index CONCURRENTLY (BR-GC-51).
func TestMigration_000006_IsOneTransaction(t *testing.T) {
	up := readMigration(t, "000006_allocations_and_deleting.up.sql")
	down := readMigration(t, "000006_allocations_and_deleting.down.sql")
	if !strings.Contains(up, "CREATE INDEX IF NOT EXISTS segments_object_id_idx ON segments (object_id);") {
		t.Error("up.sql must build segments_object_id_idx with a plain CREATE INDEX")
	}
	if !strings.Contains(down, "DROP INDEX IF EXISTS segments_object_id_idx") {
		t.Error("down.sql must drop segments_object_id_idx")
	}
	for name, body := range map[string]string{"up.sql": up, "down.sql": down} {
		for _, word := range []string{"CONCURRENTLY", "BEGIN", "COMMIT"} {
			if containsWord(body, word) {
				t.Errorf("%s contains %q", name, word)
			}
		}
	}
}

func readMigration(t *testing.T, name string) string {
	t.Helper()
	b, err := FS.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

// containsWord matches word without case, as a whole SQL token, so that
// an identifier such as "begin_ns" does not count as BEGIN.
func containsWord(body, word string) bool {
	upper := strings.ToUpper(body)
	for i := 0; ; {
		j := strings.Index(upper[i:], word)
		if j < 0 {
			return false
		}
		start, end := i+j, i+j+len(word)
		if !isIdentByte(upper, start-1) && !isIdentByte(upper, end) {
			return true
		}
		i = end
	}
}

func isIdentByte(s string, i int) bool {
	if i < 0 || i >= len(s) {
		return false
	}
	c := s[i]
	return c == '_' || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}
