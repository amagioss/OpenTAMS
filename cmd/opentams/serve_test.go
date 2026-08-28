package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/amagioss/opentams/internal/auth"
	"github.com/amagioss/opentams/internal/config"
)

// TC-CMD-AUTH-01: external-only auth (internal issuer unset) must build
// successfully — internal auth is optional per the config contract
// (both-or-neither; neither is valid).
func TestBuildAuth_ExternalOnly(t *testing.T) {
	cfg := &config.Config{
		AppEnv:                "production",
		AuthExternalIssuerURL: "https://issuer.example.com/",
		AuthExternalAudience:  "opentams",
		AuthJWKSTTL:           15 * time.Minute,
	}

	p, err := buildAuth(cfg)

	require.NoError(t, err)
	// Production env builds the JWT-backed provider, not the dev provider.
	assert.IsType(t, &auth.JWTProvider{}, p)
}

// TC-CMD-AUTH-02: external + internal issuers both configured build successfully.
func TestBuildAuth_ExternalAndInternal(t *testing.T) {
	cfg := &config.Config{
		AppEnv:                      "production",
		AuthExternalIssuerURL:       "https://issuer.example.com/",
		AuthExternalAudience:        "opentams",
		AuthInternalIssuerURL:       "https://kubernetes.default.svc/",
		AuthInternalAudience:        "opentams-internal",
		AuthInternalAllowedSubjects: []string{"system:serviceaccount:opentams:loader"},
		AuthJWKSTTL:                 15 * time.Minute,
	}

	p, err := buildAuth(cfg)

	require.NoError(t, err)
	assert.IsType(t, &auth.JWTProvider{}, p)
}

// TC-CMD-AUTH-03: development uses the permissive dev provider.
func TestBuildAuth_Development(t *testing.T) {
	cfg := &config.Config{AppEnv: "development"}

	p, err := buildAuth(cfg)

	require.NoError(t, err)
	assert.IsType(t, &auth.DevProvider{}, p)
}
