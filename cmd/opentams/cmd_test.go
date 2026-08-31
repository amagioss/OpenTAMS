package main

import (
	"bytes"
	"strings"
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

// TC-CMD-GC-01: gc subcommand returns a "not implemented" error (M16 scope).
func TestGCCmd_NotImplemented(t *testing.T) {
	root := newRootCmd()
	buf := &bytes.Buffer{}
	root.SetErr(buf)
	root.SetArgs([]string{"gc"})

	err := root.Execute()
	require.Error(t, err)
	assert.Contains(t, strings.ToLower(err.Error()), "not")
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
