//go:build integration || perf

package metastore

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

// TC-META-SCH-01: head schema (current state after TestMain migrations) →
// VerifySchema returns nil and reports the expected version.
func TestVerifySchema_HeadIsAccepted(t *testing.T) {
	store, _ := withTx(t)
	ctx := context.Background()

	state, err := store.VerifySchema(ctx)
	if err != nil {
		t.Fatalf("VerifySchema unexpected error at head: %v", err)
	}
	if state.Version != ExpectedSchemaVersion {
		t.Errorf("Version = %d, want %d (expected head)", state.Version, ExpectedSchemaVersion)
	}
	if state.Dirty {
		t.Error("Dirty = true at head; testcontainer migrations should leave dirty=false")
	}
	if state.Expected != ExpectedSchemaVersion {
		t.Errorf("state.Expected = %d, want %d", state.Expected, ExpectedSchemaVersion)
	}
}

// TC-META-SCH-02: schema_migrations table missing → ErrSchemaNotMigrated.
// Postgres supports transactional DDL, so the DROP within the test tx is
// reverted by Cleanup's rollback and does not leak to other tests.
func TestVerifySchema_TableMissing(t *testing.T) {
	store, tx := withTx(t)
	ctx := context.Background()

	execTx(t, tx, "DROP TABLE schema_migrations")

	_, err := store.VerifySchema(ctx)
	if !errors.Is(err, ErrSchemaNotMigrated) {
		t.Fatalf("VerifySchema error = %v, want ErrSchemaNotMigrated", err)
	}
}

// TC-META-SCH-03: schema_migrations exists but is empty → ErrSchemaNotMigrated.
// Distinct from "table missing" but the operator's recovery action is the
// same — run migrations.
func TestVerifySchema_TableEmpty(t *testing.T) {
	store, tx := withTx(t)
	ctx := context.Background()

	execTx(t, tx, "DELETE FROM schema_migrations")

	_, err := store.VerifySchema(ctx)
	if !errors.Is(err, ErrSchemaNotMigrated) {
		t.Fatalf("VerifySchema error = %v, want ErrSchemaNotMigrated", err)
	}
}

// TC-META-SCH-04: dirty = true → ErrSchemaDirty. The error message must
// include both the dirty version and the `migrate force <version>` hint
// so an on-call operator has a self-contained recovery path.
func TestVerifySchema_Dirty(t *testing.T) {
	store, tx := withTx(t)
	ctx := context.Background()

	execTx(t, tx, "UPDATE schema_migrations SET dirty = true")

	state, err := store.VerifySchema(ctx)
	if !errors.Is(err, ErrSchemaDirty) {
		t.Fatalf("VerifySchema error = %v, want ErrSchemaDirty", err)
	}
	if !state.Dirty {
		t.Error("returned state.Dirty = false; want true so callers can log it")
	}
	if state.Version != ExpectedSchemaVersion {
		t.Errorf("state.Version = %d, want %d", state.Version, ExpectedSchemaVersion)
	}
}

// TC-META-SCH-05: version < ExpectedSchemaVersion → ErrSchemaTooOld.
// Simulates the "binary upgraded but migrations forgotten" deployment
// mistake — the worst-case shape we want to catch loudly at boot.
func TestVerifySchema_TooOld(t *testing.T) {
	store, tx := withTx(t)
	ctx := context.Background()

	old := ExpectedSchemaVersion - 1
	execTx(t, tx, "UPDATE schema_migrations SET version = $1", old)

	state, err := store.VerifySchema(ctx)
	if !errors.Is(err, ErrSchemaTooOld) {
		t.Fatalf("VerifySchema error = %v, want ErrSchemaTooOld", err)
	}
	if state.Version != old {
		t.Errorf("state.Version = %d, want %d", state.Version, old)
	}
	if state.Expected != ExpectedSchemaVersion {
		t.Errorf("state.Expected = %d, want %d", state.Expected, ExpectedSchemaVersion)
	}
}

// TC-META-SCH-06: version > ExpectedSchemaVersion → success (older binary
// on newer schema is supported under expand-contract per the spec). The
// caller decides whether to log it as informational; the probe itself
// must NOT fail.
func TestVerifySchema_NewerSchemaAccepted(t *testing.T) {
	store, tx := withTx(t)
	ctx := context.Background()

	newer := ExpectedSchemaVersion + 1
	execTx(t, tx, "UPDATE schema_migrations SET version = $1", newer)

	state, err := store.VerifySchema(ctx)
	if err != nil {
		t.Fatalf("VerifySchema unexpected error for newer schema: %v", err)
	}
	if state.Version != newer {
		t.Errorf("state.Version = %d, want %d", state.Version, newer)
	}
}

// TC-META-SCH-07: migration 000005 leaves segments with the ADR-0040
// rule 4 invariants: upper_ns NOT NULL and segments_bounds_nonempty in
// place of segments_upper_ns_positive.
func TestSchema_SegmentsBoundsConstraints(t *testing.T) {
	_, tx := withTx(t)
	ctx := context.Background()

	constraintExists := func(name string) bool {
		var n int
		if err := tx.QueryRow(ctx,
			`SELECT COUNT(*) FROM pg_constraint WHERE conrelid = 'segments'::regclass AND conname = $1`, name,
		).Scan(&n); err != nil {
			t.Fatalf("pg_constraint %s: %v", name, err)
		}
		return n == 1
	}
	if !constraintExists("segments_bounds_nonempty") {
		t.Error("segments_bounds_nonempty missing")
	}
	if constraintExists("segments_upper_ns_positive") {
		t.Error("segments_upper_ns_positive still present")
	}
	var nullable string
	if err := tx.QueryRow(ctx,
		`SELECT is_nullable FROM information_schema.columns WHERE table_name = 'segments' AND column_name = 'upper_ns'`,
	).Scan(&nullable); err != nil {
		t.Fatalf("information_schema: %v", err)
	}
	if nullable != "NO" {
		t.Errorf("upper_ns is_nullable = %q, want NO", nullable)
	}
}

// TC-META-SCH-08: the backstops reject what the application must never
// write: an empty or inverted row (23514) and an open end (23502).
// Negative bounds are valid (TAMS permits timestamps before 0:0).
func TestSchema_SegmentsBoundsBackstops(t *testing.T) {
	cases := []struct {
		name     string
		lo       int64
		hi       *int64
		wantCode string
	}{
		{"empty", 5, ptrInt64(5), "23514"},
		{"inverted", 5, ptrInt64(4), "23514"},
		{"zero width at zero", 0, ptrInt64(0), "23514"},
		{"open end", 5, nil, "23502"},
		{"negative valid", -1500000, ptrInt64(-1000000), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, tx := withTx(t)
			ctx := context.Background()
			srcID, flID := uuid.New(), uuid.New()
			execTx(t, tx, `INSERT INTO sources (id, format) VALUES ($1, 'urn:x-nmos:format:video')`, srcID)
			execTx(t, tx, `INSERT INTO flows (id, source_id, format) VALUES ($1, $2, 'urn:x-nmos:format:video')`, flID, srcID)
			execTx(t, tx, `INSERT INTO objects (id, ref_count) VALUES ($1, 1)`, "obj-"+flID.String())

			_, err := tx.Exec(ctx,
				`INSERT INTO segments (flow_id, object_id, timerange, lower_ns, upper_ns) VALUES ($1, $2, 'x', $3, $4)`,
				flID, "obj-"+flID.String(), tc.lo, tc.hi)
			if tc.wantCode == "" {
				if err != nil {
					t.Fatalf("insert: %v", err)
				}
				return
			}
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != tc.wantCode {
				t.Fatalf("err = %v, want SQLSTATE %s", err, tc.wantCode)
			}
			if tc.wantCode == "23514" && pgErr.ConstraintName != "segments_bounds_nonempty" {
				t.Errorf("constraint = %q, want segments_bounds_nonempty", pgErr.ConstraintName)
			}
		})
	}
}

func ptrInt64(v int64) *int64 { return &v }
