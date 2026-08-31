// Package auth provides application-level authentication policy for OpenTAMS.
// It translates pkg/jwtauth mechanics into domain errors and the Principal type.
package auth

import (
	"context"
	"errors"

	"github.com/amagioss/opentams/internal/apperror"
	"github.com/amagioss/opentams/pkg/jwtauth"
)

// Principal represents an authenticated caller.
type Principal struct {
	Subject    string // JWT sub claim
	IssuerType string // echoes jwtauth.ValidatedToken.MatchedIssuer
}

// Provider authenticates a raw bearer token and returns the caller's Principal.
type Provider interface {
	Authenticate(ctx context.Context, rawToken string) (*Principal, error)
}

// internalIssuerName is the IssuerConfig.Name registered for the in-cluster
// service-to-service issuer; tokens it accepts are gated by the subject allowlist.
const internalIssuerName = "internal"

// JWTProvider implements Provider via a jwtauth.Validator. Tokens accepted by the
// internal issuer are additionally authorized against an allowlist of subjects
// (e.g. Kubernetes ServiceAccount "sub" claims). External tokens are not gated.
type JWTProvider struct {
	v               jwtauth.Validator
	internalAllowed map[string]struct{}
}

var _ Provider = (*JWTProvider)(nil)

// NewJWTProvider constructs a JWTProvider backed by the given validator.
// internalAllowedSubjects lists the exact "sub" claims permitted on tokens
// accepted by the internal issuer; an empty list rejects all internal callers.
func NewJWTProvider(v jwtauth.Validator, internalAllowedSubjects []string) *JWTProvider {
	allowed := make(map[string]struct{}, len(internalAllowedSubjects))
	for _, s := range internalAllowedSubjects {
		allowed[s] = struct{}{}
	}
	return &JWTProvider{v: v, internalAllowed: allowed}
}

// Authenticate validates the token and translates jwtauth errors to apperror types.
// Internal-issuer tokens whose subject is not on the allowlist are rejected as forbidden.
func (p *JWTProvider) Authenticate(ctx context.Context, rawToken string) (*Principal, error) {
	tok, err := p.v.Validate(ctx, rawToken)
	if err == nil {
		if tok.MatchedIssuer == internalIssuerName {
			if _, ok := p.internalAllowed[tok.Subject]; !ok {
				return nil, apperror.New(apperror.ErrForbidden, "service account not permitted")
			}
		}
		return &Principal{Subject: tok.Subject, IssuerType: tok.MatchedIssuer}, nil
	}

	var ve *jwtauth.ValidationError
	if errors.As(err, &ve) && ve.Code == jwtauth.ErrExpired {
		return nil, apperror.New(apperror.ErrTokenExpired, "token has expired")
	}
	return nil, apperror.New(apperror.ErrUnauthorized, "token is not valid")
}

// DevProvider implements Provider for local development; it always succeeds.
type DevProvider struct{}

var _ Provider = (*DevProvider)(nil)

// Authenticate always returns a fixed dev principal without inspecting the token.
func (p *DevProvider) Authenticate(_ context.Context, _ string) (*Principal, error) {
	return &Principal{Subject: "dev", IssuerType: "dev"}, nil
}
