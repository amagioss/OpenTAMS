package migrations

import "embed"

// FS is the embedded directory of golang-migrate-format up/down
// migration files. Pass it to `dbmigrate.New` to run migrations on
// startup; the SQL filenames follow the
// `NNNNNN_<name>.{up,down}.sql` convention required by
// golang-migrate's iofs source.
//
//go:embed *.sql
var FS embed.FS
