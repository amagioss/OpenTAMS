package metastore

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ExpectedSchemaVersion is the minimum schema version this binary requires.
//
// Bump it whenever a new migration is added to /migrations whose absence
// would break a request path this binary serves. Under the expand-contract
// rule (`docs/requirements.md` REQ-DEV / REQ-TEST-06), an older binary
// is allowed to run against a newer schema (rolling upgrades, canary
// deploys), so we treat ExpectedSchemaVersion as a *minimum*, not an exact
// match: `version >= ExpectedSchemaVersion` passes; `version <
// ExpectedSchemaVersion` fails with ErrSchemaTooOld.
//
// Current head is migration 000005 (objects_storage_id_reaping).
const ExpectedSchemaVersion int64 = 5

// Sentinel errors returned by VerifySchema. Wrapped so callers can match
// with errors.Is and produce operator-friendly messages without re-parsing
// SQLSTATE codes.
var (
	// ErrSchemaNotMigrated is returned when the schema_migrations table
	// does not exist (no migrations have ever been applied) or is empty
	// (set up but never populated). Recovery: run `opentams migrate` (or
	// the external migrate/migrate container) before starting the server.
	ErrSchemaNotMigrated = errors.New("schema_migrations: not migrated")

	// ErrSchemaDirty is returned when schema_migrations.dirty is true.
	// A dirty row means a previous migration started but did not finish
	// cleanly — the database is in an unknown shape and the server must
	// not serve traffic. Recovery: investigate the partial migration,
	// reconcile the schema by hand, then `migrate force <version>` to
	// clear the dirty flag.
	ErrSchemaDirty = errors.New("schema_migrations: dirty")

	// ErrSchemaTooOld is returned when the migrated version is below
	// ExpectedSchemaVersion. Recovery: run pending migrations.
	ErrSchemaTooOld = errors.New("schema_migrations: version older than required")
)

// SchemaState captures the result of a successful or partially-successful
// schema probe. Callers log it for operator diagnostics.
type SchemaState struct {
	Version  int64
	Dirty    bool
	Expected int64
}

// VerifySchema checks that the metadata-store schema is at or above
// ExpectedSchemaVersion and not in a dirty state. It is intended to be
// called once at server startup, immediately after the pool is opened,
// so an unmigrated or partially-migrated database fails fast with a
// clear error rather than at the first request.
//
// On success, returns the observed (Version, Dirty=false, Expected) state
// for structured logging. Version > Expected is treated as success
// (older binary on newer schema — supported under expand-contract); the
// caller decides whether to log it.
//
// Errors:
//   - ErrSchemaNotMigrated wraps undefined_table (42P01) or no-rows.
//   - ErrSchemaDirty when dirty = true.
//   - ErrSchemaTooOld when version < ExpectedSchemaVersion.
//   - Any other error is a transport/driver error and is returned
//     unwrapped (callers handle it as a connectivity problem).
func (s *PostgresStore) VerifySchema(ctx context.Context) (SchemaState, error) {
	state := SchemaState{Expected: ExpectedSchemaVersion}

	const q = `SELECT version, dirty FROM schema_migrations LIMIT 1`
	row := s.db.QueryRow(ctx, q)
	if err := row.Scan(&state.Version, &state.Dirty); err != nil {
		if isUndefinedTable(err) {
			return state, fmt.Errorf("%w: schema_migrations table missing — run migrations before starting the server", ErrSchemaNotMigrated)
		}
		// pgx returns ErrNoRows for empty result sets via QueryRow.Scan;
		// treat that as "schema_migrations exists but is empty" — same
		// operator action as a missing table.
		if isNoRows(err) {
			return state, fmt.Errorf("%w: schema_migrations table is empty — run migrations before starting the server", ErrSchemaNotMigrated)
		}
		return state, fmt.Errorf("metastore: query schema_migrations: %w", err)
	}

	if state.Dirty {
		return state, fmt.Errorf("%w: version %d — fix the partial migration manually, then run `migrate force %d`",
			ErrSchemaDirty, state.Version, state.Version)
	}

	if state.Version < ExpectedSchemaVersion {
		return state, fmt.Errorf("%w: have version %d, this binary requires at least version %d — run pending migrations",
			ErrSchemaTooOld, state.Version, ExpectedSchemaVersion)
	}

	return state, nil
}

// isUndefinedTable reports whether err is Postgres's "relation does not
// exist" error (SQLSTATE 42P01). Used by VerifySchema to translate a
// fresh, never-migrated database into ErrSchemaNotMigrated.
func isUndefinedTable(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "42P01"
	}
	return false
}

// isNoRows reports whether err is pgx's "no rows in result set" sentinel.
func isNoRows(err error) bool {
	return errors.Is(err, pgx.ErrNoRows)
}
