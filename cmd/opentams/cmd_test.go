package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TC-CMD-ROOT-01: root command has expected identity and registered subcommands.
func TestRootCmd_Structure(t *testing.T) {
	root := newRootCmd()

	assert.Equal(t, "opentams", root.Use)
	assert.NotEmpty(t, root.Short)

	names := make(map[string]bool)
	for _, sub := range root.Commands() {
		names[sub.Name()] = true
	}
	assert.True(t, names["serve"], "serve subcommand must be registered")
	assert.True(t, names["gc"], "gc subcommand must be registered")
	assert.True(t, names["migrate"], "migrate subcommand must be registered")
}

// TC-CMD-ROOT-02: --help output mentions each subcommand's role (REQ-USE-03).
func TestRootCmd_HelpMentionsSubcommands(t *testing.T) {
	root := newRootCmd()
	buf := &bytes.Buffer{}
	root.SetOut(buf)
	root.SetArgs([]string{"--help"})
	_ = root.Execute()

	help := buf.String()
	assert.Contains(t, help, "serve")
	assert.Contains(t, help, "gc")
	assert.Contains(t, help, "migrate")
}

// TC-GC-123: gc --help names --once and the GC settings, and drops the
// pre-M16 wording.
func Test_TC_GC_123_HelpText(t *testing.T) {
	root := newRootCmd()
	buf := &bytes.Buffer{}
	root.SetOut(buf)
	root.SetArgs([]string{"gc", "--help"})

	require.NoError(t, root.Execute())
	help := buf.String()

	for _, want := range []string{
		"--once",
		"GC_POLL_INTERVAL",
		"GC_BATCH_SIZE",
		"GC_ALLOCATION_GRACE",
		"OPENTAMS_OBJECT_STORE_ID",
	} {
		assert.Contains(t, help, want)
	}
	for _, unwanted := range []string{"CronJob", "sidecar", "flow delete requests"} {
		assert.NotContains(t, help, unwanted)
	}
}

// TC-GC-124: gc --once with GC_BATCH_SIZE=0 fails before any connection,
// and the error names GC_BATCH_SIZE. main turns the error into exit code 1.
func Test_TC_GC_124_ConfigErrorNamesTheSetting(t *testing.T) {
	// The other required settings are valid, so GC_BATCH_SIZE is the
	// only error that config.Load joins.
	t.Setenv("APP_ENV", "development")
	t.Setenv("DB_HOST", "localhost")
	t.Setenv("DB_NAME", "x")
	t.Setenv("DB_USER", "x")
	t.Setenv("DB_PASSWORD", "x")
	t.Setenv("OBJECT_STORE_BUCKET", "x")
	t.Setenv("OBJECT_STORE_REGION", "us-east-1")
	t.Setenv("STORAGE_BACKEND_PROVIDER", "s3")
	t.Setenv("GC_BATCH_SIZE", "0")

	root := newRootCmd()
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"gc", "--once"})

	err := root.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GC_BATCH_SIZE")
	assert.NotContains(t, err.Error(), "required")
}

// TC-CMD-MIGRATE-01: migrate with no subcommand shows help (M17 implemented).
func TestMigrateCmd_NoSubcommandShowsHelp(t *testing.T) {
	root := newRootCmd()
	buf := &bytes.Buffer{}
	root.SetOut(buf)
	root.SetArgs([]string{"migrate", "--help"})

	err := root.Execute()
	require.NoError(t, err)
	help := buf.String()
	assert.Contains(t, help, "up")
	assert.Contains(t, help, "down")
	assert.Contains(t, help, "version")
	assert.Contains(t, help, "force")
}

// TC-CMD-SERVE-01: serve returns an error when required config env vars are absent.
func TestServeCmd_MissingConfig(t *testing.T) {
	// Ensure required env vars are unset so config.Load fails.
	for _, key := range []string{
		"DB_HOST", "DB_NAME", "DB_USER", "DB_PASSWORD",
		"OBJECT_STORE_BUCKET", "OBJECT_STORE_REGION", "STORAGE_BACKEND_PROVIDER",
		"AUTH_EXTERNAL_ISSUER_URL", "AUTH_EXTERNAL_AUDIENCE",
		"AUTH_INTERNAL_ISSUER_URL", "AUTH_INTERNAL_AUDIENCE",
	} {
		t.Setenv(key, "")
	}
	t.Setenv("APP_ENV", "production")

	root := newRootCmd()
	root.SetArgs([]string{"serve"})
	err := root.Execute()
	require.Error(t, err)
}

// TC-CMD-SERVE-02: serve --help describes its lifecycle role (REQ-USE-03).
func TestServeCmd_HelpDescribesRole(t *testing.T) {
	root := newRootCmd()
	buf := &bytes.Buffer{}
	root.SetOut(buf)
	root.SetArgs([]string{"serve", "--help"})
	_ = root.Execute()

	help := buf.String()
	// Must explain when to run it and what it does — not just list flags.
	assert.Contains(t, help, "SIGTERM")
	assert.Contains(t, help, "migrate")
}
