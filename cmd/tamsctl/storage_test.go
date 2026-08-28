package main

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/amagioss/opentams/internal/tamsctl"
)

func tamsctlLoad(t *testing.T, home string) (*tamsctl.Config, error) {
	t.Helper()
	return tamsctl.Load(filepath.Join(home, ".tamsctl", "config"))
}

// runStorage executes `tamsctl storage create` with the given args against an
// isolated config dir, returning combined output and error.
func runStorage(t *testing.T, args ...string) (string, error) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(append([]string{"storage", "create"}, args...))
	err := root.Execute()
	return out.String(), err
}

func TestStorageCreate_NeitherLimitNorObjectIDs(t *testing.T) {
	_, err := runStorage(t, "--flow-id", testUUID)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "exactly one")
}

func TestStorageCreate_BothLimitAndObjectIDs(t *testing.T) {
	_, err := runStorage(t, "--flow-id", testUUID, "--limit", "5", "--object-id", "o1")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "exactly one")
}

func TestStorageCreate_MissingFlowID(t *testing.T) {
	_, err := runStorage(t, "--limit", "5")

	require.Error(t, err)
}

func TestStorageCreate_RejectsMalformedFlowID(t *testing.T) {
	_, err := runStorage(t, "--flow-id", "not-a-uuid", "--limit", "5")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be a canonical lowercase UUID")
}

// runCfg runs `tamsctl config ...` against an isolated HOME and returns the
// resulting config path so tests can inspect what was persisted.
func runCfg(t *testing.T, home string, args ...string) error {
	t.Helper()
	t.Setenv("HOME", home)
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(append([]string{"config"}, args...))
	return root.Execute()
}

func TestSetContext_OmittedTokenPreserves_EmptyTokenClears(t *testing.T) {
	home := t.TempDir()

	require.NoError(t, runCfg(t, home, "set-context", "c1", "--endpoint", "http://h", "--token", "secret"))
	// Update endpoint only — token must survive.
	require.NoError(t, runCfg(t, home, "set-context", "c1", "--endpoint", "http://h2"))

	cfg, err := tamsctlLoad(t, home)
	require.NoError(t, err)
	assert.Equal(t, "secret", cfg.Contexts["c1"].Token)
	assert.Equal(t, "http://h2", cfg.Contexts["c1"].Endpoint)

	// Explicit empty token clears it.
	require.NoError(t, runCfg(t, home, "set-context", "c1", "--endpoint", "http://h2", "--token", ""))
	cfg, err = tamsctlLoad(t, home)
	require.NoError(t, err)
	assert.Empty(t, cfg.Contexts["c1"].Token)
}
