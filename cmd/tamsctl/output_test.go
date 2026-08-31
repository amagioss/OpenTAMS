package main

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRender_JSONPretty(t *testing.T) {
	var b bytes.Buffer

	require.NoError(t, render(&b, json.RawMessage(`{"id":"f1","n":2}`), "json"))

	assert.Contains(t, b.String(), "\"id\": \"f1\"")
	assert.Contains(t, b.String(), "\"n\": 2")
}

func TestRender_TableFromArray(t *testing.T) {
	var b bytes.Buffer
	body := `[{"id":"a","label":"x"},{"id":"b","label":"y"}]`

	require.NoError(t, render(&b, json.RawMessage(body), "table"))

	out := b.String()
	assert.Contains(t, out, "ID")
	assert.Contains(t, out, "LABEL")
	assert.Contains(t, out, "a")
	assert.Contains(t, out, "y")
}

func TestRender_TableNonArrayFallsBackToJSON(t *testing.T) {
	var b bytes.Buffer

	require.NoError(t, render(&b, json.RawMessage(`{"id":"f1"}`), "table"))

	// Object isn't a row set, so we fall back to pretty JSON.
	assert.Contains(t, b.String(), "\"id\": \"f1\"")
}

func TestRender_EmptyBodyNoOutput(t *testing.T) {
	var b bytes.Buffer

	require.NoError(t, render(&b, nil, "json"))

	assert.Empty(t, b.String())
}

func TestRender_JSONUnescapesHTMLSequences(t *testing.T) {
	// encoding/json (and thus the OpenTAMS server) escapes & < > to \uXXXX by
	// default. Build the input exactly that way, then assert tamsctl's json
	// output decodes it like every other viewer so presigned URLs are
	// copy-paste ready. (Escape literals are deliberately not written in
	// source — editors/formatters tend to "fix" them.)
	url := "https://h/o?a=1&b=2&c=<x>"
	escaped, err := json.Marshal(map[string]string{"url": url})
	require.NoError(t, err)
	require.Contains(t, string(escaped), "u0026", "precondition: server input is HTML-escaped")

	var b bytes.Buffer
	require.NoError(t, render(&b, escaped, "json"))

	// Decoded and copy-paste ready, with no leftover escape sequences.
	assert.Contains(t, b.String(), url)
	assert.NotContains(t, b.String(), "u0026")
	assert.NotContains(t, b.String(), "u003c")
}

func TestRender_JSONRoundTripsValues(t *testing.T) {
	// The unescape step must only touch genuine escapes — rendered output must
	// still parse back to the identical value. The "raw" entry is the literal
	// six-character text & (built from bytes so no escape appears in
	// source); it must survive verbatim, never collapse to "&".
	backslashU := string([]byte{'\\', 'u', '0', '0', '2', '6'})
	in := map[string]string{"url": "a=1&b=2", "lt": "<", "gt": ">", "raw": backslashU}
	escaped, err := json.Marshal(in)
	require.NoError(t, err)

	var b bytes.Buffer
	require.NoError(t, render(&b, escaped, "json"))

	var got map[string]string
	require.NoError(t, json.Unmarshal(b.Bytes(), &got))
	assert.Equal(t, in, got)
}
