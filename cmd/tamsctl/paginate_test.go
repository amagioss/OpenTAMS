package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// twoPageSegments serves /segments as two pages: the first carries
// X-Paging-NextKey=p2, the second (page=p2) is the last page.
func twoPageSegments(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("page") == "p2" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[{"object_id":"o2","timerange":"[6:0_12:0)"}]`))
			return
		}
		w.Header().Set("X-Paging-NextKey", "p2")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`[{"object_id":"o1","timerange":"[0:0_6:0)"}]`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// runCLI runs tamsctl against serverURL with an isolated config.
func runCLI(t *testing.T, serverURL string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("TAMSCTL_ENDPOINT", serverURL)
	t.Setenv("TAMSCTL_TOKEN", "t")
	root := newRootCmd()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs(args)
	err = root.Execute()
	return out.String(), errBuf.String(), err
}

func TestSegmentGet_JSONEnvelopeIncludesNextPageToken(t *testing.T) {
	srv := twoPageSegments(t)

	out, errOut, err := runCLI(t, srv.URL, "segment", "get", "--flow-id", testUUID)

	require.NoError(t, err)
	assert.Contains(t, out, "\"items\"")
	assert.Contains(t, out, "\"nextPageToken\": \"p2\"")
	assert.Empty(t, errOut, "json mode must not print a stderr hint")
}

func TestSegmentGet_TableHintToStderr(t *testing.T) {
	srv := twoPageSegments(t)

	out, errOut, err := runCLI(t, srv.URL, "segment", "get", "--flow-id", testUUID, "-o", "table")

	require.NoError(t, err)
	assert.Contains(t, out, "OBJECT_ID")
	assert.NotContains(t, out, "nextPageToken", "table stdout must not carry the envelope")
	assert.Contains(t, errOut, "p2")
	assert.Contains(t, errOut, "--page-token p2")
}

func TestSegmentGet_QuietSuppressesHint(t *testing.T) {
	srv := twoPageSegments(t)

	_, errOut, err := runCLI(t, srv.URL, "segment", "get", "--flow-id", testUUID, "-o", "table", "--quiet")

	require.NoError(t, err)
	assert.Empty(t, errOut)
}

func TestSegmentGet_AllEmptyYieldsEmptyArrayNotNull(t *testing.T) {
	// Single empty page (no next cursor). --all must still emit items: []
	// (matching the non-all path), never items: null.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`[]`))
	}))
	t.Cleanup(srv.Close)

	out, _, err := runCLI(t, srv.URL, "segment", "get", "--flow-id", testUUID, "--all")

	require.NoError(t, err)
	assert.Contains(t, out, `"items": []`)
	assert.NotContains(t, out, "null")
}

func TestSegmentGet_AllConcatenatesPages(t *testing.T) {
	srv := twoPageSegments(t)

	out, _, err := runCLI(t, srv.URL, "segment", "get", "--flow-id", testUUID, "--all")

	require.NoError(t, err)
	assert.Contains(t, out, "o1")
	assert.Contains(t, out, "o2")
	// All pages consumed ⇒ no trailing cursor.
	assert.NotContains(t, out, "nextPageToken")
}

// stuckCursorSegments always returns the same non-advancing X-Paging-NextKey.
func stuckCursorSegments(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Paging-NextKey", "stuck")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`[{"object_id":"o"}]`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestSegmentGet_AllRejectsNonAdvancingCursor(t *testing.T) {
	srv := stuckCursorSegments(t)

	_, _, err := runCLI(t, srv.URL, "segment", "get", "--flow-id", testUUID, "--all")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-advancing")
}

func TestSegmentGet_AllRespectsMaxPages(t *testing.T) {
	// Each page advances the cursor, so only --max-pages can stop it.
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Paging-NextKey", fmt.Sprintf("p%d", n))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`[{"object_id":"o"}]`))
	}))
	t.Cleanup(srv.Close)

	_, _, err := runCLI(t, srv.URL, "segment", "get", "--flow-id", testUUID, "--all", "--max-pages", "3")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "--max-pages=3")
}

func TestInvalidOutputFormatRejected(t *testing.T) {
	srv := twoPageSegments(t)

	_, _, err := runCLI(t, srv.URL, "segment", "get", "--flow-id", testUUID, "-o", "xml")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid --output")
}

func TestSegmentGet_YAMLEnvelope(t *testing.T) {
	srv := twoPageSegments(t)

	out, _, err := runCLI(t, srv.URL, "segment", "get", "--flow-id", testUUID, "-o", "yaml")

	require.NoError(t, err)
	assert.Contains(t, out, "items:")
	assert.Contains(t, out, "nextPageToken: p2")
}

func TestSegmentGet_JSONDecodesEscapedURLs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Emit like the real server: json.Marshal HTML-escapes the "&" in URLs.
		body, _ := json.Marshal([]map[string]any{
			{"object_id": "o1", "get_urls": []map[string]string{{"url": "https://h/o?a=1&b=2&x=3"}}},
		})
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)

	for _, extra := range [][]string{{}, {"--all"}} {
		args := append([]string{"segment", "get", "--flow-id", testUUID}, extra...)
		out, _, err := runCLI(t, srv.URL, args...)
		require.NoError(t, err)
		assert.Contains(t, out, "a=1&b=2&x=3", "args=%v", extra)
		assert.NotContains(t, out, "u0026", "args=%v", extra)
	}
}
