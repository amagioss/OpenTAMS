package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseFrameRate(t *testing.T) {
	fr, err := parseFrameRate("25")
	require.NoError(t, err)
	assert.Equal(t, 25, fr.Numerator)
	assert.Equal(t, 1, fr.Denominator)

	fr, err = parseFrameRate("30000/1001")
	require.NoError(t, err)
	assert.Equal(t, 30000, fr.Numerator)
	assert.Equal(t, 1001, fr.Denominator)

	for _, bad := range []string{"", "0", "-5", "abc", "25/0", "25/x", "/30"} {
		_, err := parseFrameRate(bad)
		assert.Error(t, err, "expected error for %q", bad)
	}
}

const testUUID = "9d3a4a1e-b191-4bdc-80ba-91e3605dc18a"

func TestFlowCreate_RequiresFrameRateOrVFR(t *testing.T) {
	_, _, err := runCLI(t, "http://127.0.0.1:0",
		"flow", "create", "--id", testUUID, "--source-id", testUUID,
		"--codec", "video/mp4", "--frame-width", "1920", "--frame-height", "1080")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "frame rate")
}

func TestFlowCreate_VFRWithFrameRateConflicts(t *testing.T) {
	_, _, err := runCLI(t, "http://127.0.0.1:0",
		"flow", "create", "--id", testUUID, "--source-id", testUUID,
		"--codec", "video/mp4", "--frame-width", "1920", "--frame-height", "1080",
		"--vfr", "--frame-rate", "25")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "must not be set with --vfr")
}

func TestFlowCreate_RequiresSourceOrNewSource(t *testing.T) {
	_, _, err := runCLI(t, "http://127.0.0.1:0",
		"flow", "create", "--codec", "video/mp4",
		"--frame-width", "1920", "--frame-height", "1080", "--frame-rate", "25")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "--new-source")
}

func TestFlowCreate_NewSourceWithSourceIDConflicts(t *testing.T) {
	_, _, err := runCLI(t, "http://127.0.0.1:0",
		"flow", "create", "--source-id", testUUID, "--new-source",
		"--codec", "video/mp4", "--frame-width", "1920", "--frame-height", "1080", "--frame-rate", "25")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "mutually exclusive")
}

func TestFlowCreate_AutoGeneratesIDsAndPrintsThem(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)

	out, errOut, err := runCLI(t, srv.URL,
		"flow", "create", "--new-source", "--codec", "video/mp4",
		"--frame-width", "1920", "--frame-height", "1080", "--frame-rate", "25")

	require.NoError(t, err)
	assert.Contains(t, errOut, "generated flow id:")
	assert.Contains(t, errOut, "generated source id:")
	_ = out
}

func TestFlowGet_RejectsMalformedUUID(t *testing.T) {
	// Each of these passes either no check or google/uuid's lenient Parse, but
	// the server's strict regex (which we mirror) rejects them.
	cases := map[string]string{
		"truncated":   "9d3a4a1e-b191-4bdc-80ba-91e3605dc18", // 11 hex in last group
		"uppercase":   "9D3A4A1E-B191-4BDC-80BA-91E3605DC18A",
		"no-hyphens":  "9d3a4a1eb1914bdc80ba91e3605dc18a",
		"urn-prefix":  "urn:uuid:9d3a4a1e-b191-4bdc-80ba-91e3605dc18a",
		"bad-version": "9d3a4a1e-b191-0bdc-80ba-91e3605dc18a", // version 0
	}
	for name, id := range cases {
		t.Run(name, func(t *testing.T) {
			_, _, err := runCLI(t, "http://127.0.0.1:0", "flow", "get", "--id", id)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "must be a canonical lowercase UUID")
		})
	}
}

func TestFlowGet_AcceptsCanonicalUUID(t *testing.T) {
	// A canonical v4 id (as minted by the CLI) must pass validation and reach
	// the server.
	var hit bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)

	_, _, err := runCLI(t, srv.URL, "flow", "get", "--id", testUUID)

	require.NoError(t, err)
	assert.True(t, hit, "request should have reached the server")
}
