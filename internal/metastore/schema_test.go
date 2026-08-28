//go:build integration || perf

package metastore

import (
	"context"
	"errors"
	"testing"
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
