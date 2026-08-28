package tamsctl

import (
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makeJWT builds an unsigned-looking three-segment token with the given
// payload claims. Signature is irrelevant — tamsctl never verifies it.
func makeJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	body, err := json.Marshal(claims)
	require.NoError(t, err)
	payload := base64.RawURLEncoding.EncodeToString(body)
	return header + "." + payload + ".sig"
}

func TestParseExpiry_Valid(t *testing.T) {
	exp := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	tok := makeJWT(t, map[string]any{"exp": exp.Unix()})

	got, ok := ParseExpiry(tok)

	require.True(t, ok)
	assert.Equal(t, exp.Unix(), got.Unix())
}

func TestParseExpiry_NoExpClaim(t *testing.T) {
	tok := makeJWT(t, map[string]any{"sub": "loader"})

	_, ok := ParseExpiry(tok)

	assert.False(t, ok)
}

func TestParseExpiry_Malformed(t *testing.T) {
	for _, tok := range []string{"", "not-a-jwt", "a.b", "a.!!!.c"} {
		_, ok := ParseExpiry(tok)
		assert.False(t, ok, "token %q should not parse", tok)
	}
}

func TestIsExpired(t *testing.T) {
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)

	expired := makeJWT(t, map[string]any{"exp": now.Add(-time.Minute).Unix()})
	assert.True(t, IsExpired(expired, now))

	valid := makeJWT(t, map[string]any{"exp": now.Add(time.Hour).Unix()})
	assert.False(t, IsExpired(valid, now))

	// No exp claim or unparseable ⇒ not treated as locally expired
	// (let the server decide via 401).
	assert.False(t, IsExpired(makeJWT(t, map[string]any{"sub": "x"}), now))
	assert.False(t, IsExpired("garbage", now))
}
