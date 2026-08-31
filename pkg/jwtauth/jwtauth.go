// Package jwtauth provides generic JWKS-backed JWT validation against multiple issuers.
// It performs mechanical validation only — no application policy.
package jwtauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/auth0/go-jwt-middleware/v2/jwks"
	"github.com/auth0/go-jwt-middleware/v2/validator"
)

// IssuerConfig configures one JWKS-backed issuer.
type IssuerConfig struct {
	Name      string // caller-assigned; echoed in ValidatedToken.MatchedIssuer
	IssuerURL *url.URL
	Audience  []string
}

// ValidatedToken is returned on successful validation.
type ValidatedToken struct {
	Subject       string // JWT sub claim
	MatchedIssuer string // echoed IssuerConfig.Name of the issuer that accepted the token
}

// ErrCode is a string error code returned in ValidationError.
type ErrCode string

const (
	// ErrExpired means the token has a valid structure but the exp claim is in the past.
	ErrExpired ErrCode = "expired"
	// ErrNoMatch means no configured issuer accepted the token.
	ErrNoMatch ErrCode = "no_match"
)

// ValidationError is returned when Validate fails.
type ValidationError struct {
	Code ErrCode
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("jwtauth: validation failed: %s", e.Code)
}

// Validator is the interface implemented by MultiIssuerValidator.
// Callers depend on this interface, not the concrete type.
type Validator interface {
	Validate(ctx context.Context, rawToken string) (*ValidatedToken, error)
}

// tokenValidator is an unexported interface over the auth0 validator for testability.
type tokenValidator interface {
	ValidateToken(ctx context.Context, tokenString string) (interface{}, error)
}

// namedValidator pairs a caller-assigned name with its per-issuer validator.
type namedValidator struct {
	name      string
	validator tokenValidator
}

var _ Validator = (*MultiIssuerValidator)(nil)

// newValidatorFn is a package-level var so tests can inject failures.
var newValidatorFn = func(keyFunc func(context.Context) (interface{}, error), sig validator.SignatureAlgorithm, issuer string, audience []string) (tokenValidator, error) {
	return validator.New(keyFunc, sig, issuer, audience)
}

// MultiIssuerValidator tries each configured issuer in registration order
// and returns the first successful match. Order carries no business meaning.
type MultiIssuerValidator struct {
	validators []namedValidator
}

// New constructs a MultiIssuerValidator. Each issuer gets a JWKS caching provider
// backed by OIDC discovery. Returns an error if any issuer validator fails to build.
// RS256 is the only supported signature algorithm — intentional for BBC TAMS scope.
func New(cacheTTL time.Duration, issuers ...IssuerConfig) (*MultiIssuerValidator, error) {
	vs := make([]namedValidator, 0, len(issuers))
	for _, iss := range issuers {
		provider := jwks.NewCachingProvider(iss.IssuerURL, cacheTTL)
		v, err := newValidatorFn(provider.KeyFunc, validator.RS256, iss.IssuerURL.String(), iss.Audience)
		if err != nil {
			return nil, fmt.Errorf("jwtauth: issuer %q: %w", iss.Name, err)
		}
		vs = append(vs, namedValidator{name: iss.Name, validator: v})
	}
	return &MultiIssuerValidator{validators: vs}, nil
}

// Validate tries each issuer in registration order and returns on the first match.
// If all fail, it inspects the token's exp claim: expired → ErrExpired, else → ErrNoMatch.
func (m *MultiIssuerValidator) Validate(ctx context.Context, rawToken string) (*ValidatedToken, error) {
	for _, nv := range m.validators {
		result, err := nv.validator.ValidateToken(ctx, rawToken)
		if err != nil {
			continue
		}
		return &ValidatedToken{
			Subject:       subjectOf(result),
			MatchedIssuer: nv.name,
		}, nil
	}
	if isExpiredToken(rawToken) {
		return nil, &ValidationError{Code: ErrExpired}
	}
	return nil, &ValidationError{Code: ErrNoMatch}
}

// subjectOf extracts the sub claim from the result of ValidateToken.
// Handles both the auth0 validator.ValidatedClaims type (production) and
// test stubs that implement a subject() method.
func subjectOf(result interface{}) string {
	if vc, ok := result.(*validator.ValidatedClaims); ok {
		return vc.RegisteredClaims.Subject
	}
	// test stub path
	type subjecter interface{ subject() string }
	if s, ok := result.(subjecter); ok {
		return s.subject()
	}
	return ""
}

// isExpiredToken parses the JWT payload without signature verification
// and reports whether the exp claim is in the past.
func isExpiredToken(rawToken string) bool {
	parts := strings.SplitN(rawToken, ".", 3)
	if len(parts) != 3 {
		return false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return false
	}
	return claims.Exp > 0 && time.Now().Unix() > claims.Exp
}
