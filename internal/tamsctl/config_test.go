package tamsctl

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoad_MissingFileReturnsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")

	cfg, err := Load(path)

	require.NoError(t, err)
	assert.Empty(t, cfg.CurrentContext)
	assert.Empty(t, cfg.Contexts)
}

func TestSaveLoad_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config")
	cfg := &Config{path: path}
	cfg.SetContext("local", "http://localhost:8080", "tok-local")
	cfg.SetContext("aws", "https://tams.aws.example", "tok-aws")
	require.NoError(t, cfg.UseContext("aws"))

	require.NoError(t, cfg.Save())

	got, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, "aws", got.CurrentContext)
	assert.Equal(t, "http://localhost:8080", got.Contexts["local"].Endpoint)
	assert.Equal(t, "tok-aws", got.Contexts["aws"].Token)
}

func TestSave_FilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permissions")
	}
	dir := filepath.Join(t.TempDir(), "d")
	path := filepath.Join(dir, "config")
	cfg := &Config{path: path}
	cfg.SetContext("local", "http://localhost:8080", "t")

	require.NoError(t, cfg.Save())

	fi, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
	di, err := os.Stat(dir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), di.Mode().Perm())
}

func TestUseContext_UnknownIsError(t *testing.T) {
	cfg := &Config{}
	cfg.SetContext("local", "http://localhost:8080", "t")

	err := cfg.UseContext("nope")

	require.Error(t, err)
}

func TestResolve_ContextDefaults(t *testing.T) {
	cfg := &Config{}
	cfg.SetContext("local", "http://localhost:8080", "tok-local")
	require.NoError(t, cfg.UseContext("local"))

	r, err := cfg.Resolve("", "", "")

	require.NoError(t, err)
	assert.Equal(t, "local", r.Name)
	assert.Equal(t, "http://localhost:8080", r.Endpoint)
	assert.Equal(t, "tok-local", r.Token)
}

func TestResolve_ContextOverride(t *testing.T) {
	cfg := &Config{}
	cfg.SetContext("local", "http://localhost:8080", "tok-local")
	cfg.SetContext("aws", "https://aws.example", "tok-aws")
	require.NoError(t, cfg.UseContext("local"))

	r, err := cfg.Resolve("aws", "", "")

	require.NoError(t, err)
	assert.Equal(t, "aws", r.Name)
	assert.Equal(t, "tok-aws", r.Token)
}

func TestResolve_EnvOverridesContextButFlagWins(t *testing.T) {
	cfg := &Config{}
	cfg.SetContext("local", "http://localhost:8080", "tok-local")
	require.NoError(t, cfg.UseContext("local"))

	t.Setenv("TAMSCTL_TOKEN", "tok-env")
	t.Setenv("TAMSCTL_ENDPOINT", "https://env.example")

	// env beats context
	r, err := cfg.Resolve("", "", "")
	require.NoError(t, err)
	assert.Equal(t, "tok-env", r.Token)
	assert.Equal(t, "https://env.example", r.Endpoint)

	// explicit flags beat env
	r, err = cfg.Resolve("", "https://flag.example", "tok-flag")
	require.NoError(t, err)
	assert.Equal(t, "tok-flag", r.Token)
	assert.Equal(t, "https://flag.example", r.Endpoint)
}

func TestResolve_NoContextNoEndpointIsError(t *testing.T) {
	cfg := &Config{}

	_, err := cfg.Resolve("", "", "")

	require.Error(t, err)
}

func TestResolve_UnknownContextIsError(t *testing.T) {
	cfg := &Config{}
	cfg.SetContext("local", "http://localhost:8080", "t")

	_, err := cfg.Resolve("ghost", "", "")

	require.Error(t, err)
}
