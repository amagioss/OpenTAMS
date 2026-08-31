// Package dbmigrate wraps golang-migrate with zap logging and iofs source support.
// Zero TAMS domain knowledge — reusable across services go-migrate compatible databases.
//
// Importing this package registers the "file" migration source driver as a side effect
// (via _ "github.com/golang-migrate/migrate/v4/source/file"). Callers must register
// their own database driver (e.g. _ "github.com/golang-migrate/migrate/v4/database/pgx/v5").
package dbmigrate

import (
	"embed"
	"errors"
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	// Registers the "file" source driver (file://...) for use by golang-migrate
	// callers that need to read migrations from disk. The package-level doc
	// comment also calls this side effect out for consumers.
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"go.uber.org/zap"
)

// New creates a migrate.Migrate instance using the provided embed.FS as the
// migration source and dsn as the database target. The zap logger is wired
// to golang-migrate's internal logger so migration steps appear in structured logs.
// Set verbose=true to log each migration file as it runs (useful in CLI tools).
func New(fs embed.FS, dsn string, log *zap.Logger, verbose bool) (*migrate.Migrate, error) {
	src, err := iofs.New(fs, ".")
	if err != nil {
		return nil, fmt.Errorf("dbmigrate: open source: %w", err)
	}
	m, err := migrate.NewWithSourceInstance("iofs", src, dsn)
	if err != nil {
		return nil, fmt.Errorf("dbmigrate: connect: %w", err)
	}
	m.Log = &logAdapter{log: log, verbose: verbose}
	return m, nil
}

// NewFromPath creates a migrate.Migrate instance using a filesystem path as the
// migration source. Useful for development or when migrations are not embedded.
// Set verbose=true to log each migration file as it runs.
func NewFromPath(path, dsn string, log *zap.Logger, verbose bool) (*migrate.Migrate, error) {
	m, err := migrate.New("file://"+path, dsn)
	if err != nil {
		return nil, fmt.Errorf("dbmigrate: connect: %w", err)
	}
	m.Log = &logAdapter{log: log, verbose: verbose}
	return m, nil
}

// Up applies all pending migrations (steps=0) or exactly steps migrations (steps>0).
// migrate.ErrNoChange is treated as success.
func Up(m *migrate.Migrate, steps int) error {
	var err error
	if steps > 0 {
		err = m.Steps(steps)
	} else {
		err = m.Up()
	}
	if errors.Is(err, migrate.ErrNoChange) {
		return nil
	}
	return err
}

// Down rolls back steps migrations. steps must be positive.
// migrate.ErrNoChange is treated as success.
func Down(m *migrate.Migrate, steps int) error {
	err := m.Steps(-steps)
	if errors.Is(err, migrate.ErrNoChange) {
		return nil
	}
	return err
}

// Version returns the current schema version, dirty flag, and any error.
func Version(m *migrate.Migrate) (version uint, dirty bool, err error) {
	return m.Version()
}

// Force sets schema_migrations to version v and clears the dirty flag.
func Force(m *migrate.Migrate, v int) error {
	return m.Force(v)
}

// Close releases both the source and database connections held by m.
// Both errors are checked independently and logged at Error level if non-nil.
func Close(m *migrate.Migrate, log *zap.Logger) {
	srcErr, dbErr := m.Close()
	if srcErr != nil {
		log.Error("dbmigrate: close source", zap.Error(srcErr))
	}
	if dbErr != nil {
		log.Error("dbmigrate: close database", zap.Error(dbErr))
	}
}

// logAdapter satisfies migrate.Logger using a zap.Logger.
type logAdapter struct {
	log     *zap.Logger
	verbose bool
}

func (a *logAdapter) Printf(format string, v ...any) {
	a.log.Sugar().Infof(format, v...)
}

func (a *logAdapter) Verbose() bool { return a.verbose }
