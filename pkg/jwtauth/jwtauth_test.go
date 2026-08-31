package jwtauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/auth0/go-jwt-middleware/v2/validator"
)

// -- mock tokenValidator --

type mockTokenValidator struct {
	calls      int
	validateFn func(ctx context.Context, tokenString string) (interface{}, error)
}

func (m *mockTokenValidator) ValidateToken(ctx context.Context, tokenString string) (interface{}, error) {
	m.calls++
	return m.validateFn(ctx, tokenString)
}

// -- helpers --

// makeToken builds a minimal JWT-shaped string with the given payload claims.
// The signature is fake — only the payload is meaningful for isExpiredToken tests.
func makeToken(claims map[string]interface{}) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256"}`))
	payload, _ := json.Marshal(claims)
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	return fmt.Sprintf("%s.%s.fakesig", header, encoded)
}

func validatedClaims(subject string) *mockValidatedClaims {
	return &mockValidatedClaims{sub: subject}
}

type mockValidatedClaims struct{ sub string }

func (m *mockValidatedClaims) subject() string { return m.sub }

// -- TC-JWT-01: single issuer validates → ValidatedToken with correct Subject and MatchedIssuer --
func TestValidate_SingleIssuerMatch(t *testing.T) {
	mv := &mockTokenValidator{
		validateFn: func(_ context.Context, _ string) (interface{}, error) {
			return validatedClaims("user-123"), nil
		},
	}
	v := newTestValidator(namedValidator{name: "external", validator: mv})

	tok, err := v.Validate(context.Background(), makeToken(map[string]interface{}{"sub": "user-123"}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok.Subject != "user-123" {
		t.Errorf("Subject: got %q, want %q", tok.Subject, "user-123")
	}
	if tok.MatchedIssuer != "external" {
		t.Errorf("MatchedIssuer: got %q, want %q", tok.MatchedIssuer, "external")
	}
}

// TC-JWT-02: two issuers, first fails, second matches → second issuer name returned.
func TestValidate_FallbackToSecondIssuer(t *testing.T) {
	mv1 := &mockTokenValidator{
		validateFn: func(_ context.Context, _ string) (interface{}, error) {
			return nil, errors.New("invalid signature")
		},
	}
	mv2 := &mockTokenValidator{
		validateFn: func(_ context.Context, _ string) (interface{}, error) {
			return validatedClaims("svc-account"), nil
		},
	}
	v := newTestValidator(
		namedValidator{name: "external", validator: mv1},
		namedValidator{name: "internal", validator: mv2},
	)

	tok, err := v.Validate(context.Background(), makeToken(map[string]interface{}{"sub": "svc-account"}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok.MatchedIssuer != "internal" {
		t.Errorf("MatchedIssuer: got %q, want %q", tok.MatchedIssuer, "internal")
	}
}

// TC-JWT-03: first issuer matches → second not called (iteration stops on first success).
func TestValidate_StopsOnFirstMatch(t *testing.T) {
	mv1 := &mockTokenValidator{
		validateFn: func(_ context.Context, _ string) (interface{}, error) {
			return validatedClaims("user-123"), nil
		},
	}
	mv2 := &mockTokenValidator{
		validateFn: func(_ context.Context, _ string) (interface{}, error) {
			return validatedClaims("should-not-be-called"), nil
		},
	}
	v := newTestValidator(
		namedValidator{name: "external", validator: mv1},
		namedValidator{name: "internal", validator: mv2},
	)

	if _, err := v.Validate(context.Background(), makeToken(nil)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mv2.calls != 0 {
		t.Errorf("second validator called %d times, want 0", mv2.calls)
	}
}

// TC-JWT-04: all issuers fail, token has past exp → ErrExpired.
func TestValidate_ExpiredToken(t *testing.T) {
	mv := &mockTokenValidator{
		validateFn: func(_ context.Context, _ string) (interface{}, error) {
			return nil, errors.New("token expired")
		},
	}
	v := newTestValidator(namedValidator{name: "external", validator: mv})

	expiredToken := makeToken(map[string]interface{}{
		"sub": "user",
		"exp": time.Now().Add(-time.Hour).Unix(),
	})
	_, err := v.Validate(context.Background(), expiredToken)
	if err == nil {
		t.Fatal("expected error")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected *ValidationError, got %T", err)
	}
	if ve.Code != ErrExpired {
		t.Errorf("Code: got %q, want %q", ve.Code, ErrExpired)
	}
}

// TC-JWT-05: all issuers fail, token has future exp → ErrNoMatch.
func TestValidate_InvalidTokenFutureExp(t *testing.T) {
	mv := &mockTokenValidator{
		validateFn: func(_ context.Context, _ string) (interface{}, error) {
			return nil, errors.New("bad signature")
		},
	}
	v := newTestValidator(namedValidator{name: "external", validator: mv})

	futureToken := makeToken(map[string]interface{}{
		"sub": "user",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	_, err := v.Validate(context.Background(), futureToken)
	if err == nil {
		t.Fatal("expected error")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected *ValidationError, got %T", err)
	}
	if ve.Code != ErrNoMatch {
		t.Errorf("Code: got %q, want %q", ve.Code, ErrNoMatch)
	}
}

// TC-JWT-06: zero issuers → ErrNoMatch immediately.
func TestValidate_ZeroIssuers(t *testing.T) {
	v := newTestValidator()

	_, err := v.Validate(context.Background(), "any.token.here")
	if err == nil {
		t.Fatal("expected error")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected *ValidationError, got %T", err)
	}
	if ve.Code != ErrNoMatch {
		t.Errorf("Code: got %q, want %q", ve.Code, ErrNoMatch)
	}
}

// TC-JWT-07: ValidationError.Error() returns non-empty string.
func TestValidationError_Error(t *testing.T) {
	for _, code := range []ErrCode{ErrExpired, ErrNoMatch} {
		ve := &ValidationError{Code: code}
		if ve.Error() == "" {
			t.Errorf("ValidationError{Code:%q}.Error() returned empty string", code)
		}
	}
}

// TC-JWT-08: isExpiredToken helper covers all branches.
func TestIsExpiredToken(t *testing.T) {
	cases := []struct {
		name  string
		token string
		want  bool
	}{
		{
			name:  "expired",
			token: makeToken(map[string]interface{}{"exp": time.Now().Add(-time.Hour).Unix()}),
			want:  true,
		},
		{
			name:  "future exp",
			token: makeToken(map[string]interface{}{"exp": time.Now().Add(time.Hour).Unix()}),
			want:  false,
		},
		{
			name:  "no exp claim",
			token: makeToken(map[string]interface{}{"sub": "user"}),
			want:  false,
		},
		{
			name:  "malformed token",
			token: "notavalidjwt",
			want:  false,
		},
		{
			name:  "bad base64 payload",
			token: "header.!!!.sig",
			want:  false,
		},
		{
			name:  "non-json payload",
			token: "header." + base64.RawURLEncoding.EncodeToString([]byte("not-json")) + ".sig",
			want:  false,
		},
		{
			name:  "exp zero",
			token: makeToken(map[string]interface{}{"exp": 0}),
			want:  false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isExpiredToken(tc.token)
			if got != tc.want {
				t.Errorf("isExpiredToken(%q) = %v, want %v", summarise(tc.token), got, tc.want)
			}
		})
	}
}

// TC-JWT-09: New with valid issuers returns a validator with one namedValidator per issuer.
func TestNew_Success(t *testing.T) {
	u, _ := url.Parse("https://issuer.example.com/")
	v, err := New(time.Minute, IssuerConfig{Name: "ext", IssuerURL: u, Audience: []string{"api"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(v.validators) != 1 {
		t.Errorf("validators: got %d, want 1", len(v.validators))
	}
	if v.validators[0].name != "ext" {
		t.Errorf("name: got %q, want %q", v.validators[0].name, "ext")
	}
}

// TC-JWT-10: New propagates validator construction errors, wrapping issuer name.
// NOTE: not marked t.Parallel() — mutates the package-level newValidatorFn var.
func TestNew_ValidatorError(t *testing.T) {
	orig := newValidatorFn
	t.Cleanup(func() { newValidatorFn = orig })
	newValidatorFn = func(_ func(context.Context) (interface{}, error), _ validator.SignatureAlgorithm, _ string, _ []string) (tokenValidator, error) {
		return nil, errors.New("build failed")
	}

	u, _ := url.Parse("https://issuer.example.com/")
	_, err := New(time.Minute, IssuerConfig{Name: "ext", IssuerURL: u, Audience: []string{"api"}})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "ext") {
		t.Errorf("error %q should contain issuer name", err.Error())
	}
}

// TC-JWT-11: Validate with a real *validator.ValidatedClaims extracts Subject from RegisteredClaims.
func TestValidate_ValidatedClaimsSubject(t *testing.T) {
	mv := &mockTokenValidator{
		validateFn: func(_ context.Context, _ string) (interface{}, error) {
			return &validator.ValidatedClaims{
				RegisteredClaims: validator.RegisteredClaims{Subject: "vc-user"},
			}, nil
		},
	}
	v := newTestValidator(namedValidator{name: "ext", validator: mv})

	tok, err := v.Validate(context.Background(), makeToken(nil))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok.Subject != "vc-user" {
		t.Errorf("Subject: got %q, want %q", tok.Subject, "vc-user")
	}
}

// TC-JWT-12: Validate with an unknown result type returns empty Subject.
func TestValidate_UnknownResultType(t *testing.T) {
	mv := &mockTokenValidator{
		validateFn: func(_ context.Context, _ string) (interface{}, error) {
			return struct{}{}, nil // neither *ValidatedClaims nor subjecter
		},
	}
	v := newTestValidator(namedValidator{name: "ext", validator: mv})

	tok, err := v.Validate(context.Background(), makeToken(nil))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok.Subject != "" {
		t.Errorf("Subject: got %q, want empty", tok.Subject)
	}
}

func summarise(s string) string {
	if len(s) > 40 {
		return s[:40] + "..."
	}
	return s
}

// -- helpers used by tests only --

func newTestValidator(validators ...namedValidator) *MultiIssuerValidator {
	return &MultiIssuerValidator{validators: validators}
}
