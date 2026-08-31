package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func runVersion(t *testing.T, args ...string) string {
	t.Helper()
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(append([]string{"version"}, args...))
	require.NoError(t, root.Execute())
	return out.String()
}

func TestVersion_PlainTextByDefault(t *testing.T) {
	out := runVersion(t)

	assert.Contains(t, out, "tamsctl ")
	assert.Contains(t, out, "commit:")
	assert.Contains(t, out, "platform:")
	// Default is text, not the JSON envelope.
	assert.NotContains(t, out, "{")
}

func TestVersion_JSONWhenRequested(t *testing.T) {
	out := runVersion(t, "-o", "json")

	var info versionInfo
	require.NoError(t, json.Unmarshal([]byte(out), &info))
	assert.Equal(t, version, info.Version)
	assert.NotEmpty(t, info.GoVersion)
	assert.Contains(t, info.Platform, "/")
}

func TestVersion_TableIsOneRow(t *testing.T) {
	out := runVersion(t, "-o", "table")

	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	require.Len(t, lines, 2, "expected a header + one data row, got:\n%s", out)
	assert.Contains(t, lines[0], "VERSION")
	assert.Contains(t, lines[0], "PLATFORM")
	assert.Contains(t, lines[1], version)
}
