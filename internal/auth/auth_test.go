package auth_test

import (
	"context"
	"errors"
	"testing"

	"github.com/amagioss/opentams/internal/apperror"
	"github.com/amagioss/opentams/internal/auth"
	"github.com/amagioss/opentams/pkg/jwtauth"
)

// -- mock jwtauth.Validator --

type mockValidator struct {
	validateFn func(ctx context.Context, rawToken string) (*jwtauth.ValidatedToken, error)
}

func (m *mockValidator) Validate(ctx context.Context, rawToken string) (*jwtauth.ValidatedToken, error) {
	return m.validateFn(ctx, rawToken)
}

// -- compile-time interface assertions --

var _ auth.Provider = (*auth.JWTProvider)(nil)
var _ auth.Provider = (*auth.DevProvider)(nil)

// TC-AUTH-01: successful external token → Principal with correct Subject and IssuerType.
func TestJWTProvider_ExternalToken(t *testing.T) {
	mv := &mockValidator{
		validateFn: func(_ context.Context, _ string) (*jwtauth.ValidatedToken, error) {
			return &jwtauth.ValidatedToken{Subject: "user-123", MatchedIssuer: "external"}, nil
		},
	}
	// External tokens are never checked against the internal allowlist.
	p := auth.NewJWTProvider(mv, nil)

	principal, err := p.Authenticate(context.Background(), "any.token.here")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if principal.Subject != "user-123" {
		t.Errorf("Subject: got %q, want %q", principal.Subject, "user-123")
	}
	if principal.IssuerType != "external" {
		t.Errorf("IssuerType: got %q, want %q", principal.IssuerType, "external")
	}
}

// TC-AUTH-02: internal token whose subject is on the allowlist → Principal.
func TestJWTProvider_InternalToken_Allowed(t *testing.T) {
	const sub = "system:serviceaccount:opentams:loader"
	mv := &mockValidator{
		validateFn: func(_ context.Context, _ string) (*jwtauth.ValidatedToken, error) {
			return &jwtauth.ValidatedToken{Subject: sub, MatchedIssuer: "internal"}, nil
		},
	}
	p := auth.NewJWTProvider(mv, []string{sub})

	principal, err := p.Authenticate(context.Background(), "any.token.here")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if principal.IssuerType != "internal" {
		t.Errorf("IssuerType: got %q, want %q", principal.IssuerType, "internal")
	}
	if principal.Subject != sub {
		t.Errorf("Subject: got %q, want %q", principal.Subject, sub)
	}
}

// TC-AUTH-02b: internal token whose subject is NOT on the allowlist → ErrForbidden.
func TestJWTProvider_InternalToken_NotAllowed(t *testing.T) {
	mv := &mockValidator{
		validateFn: func(_ context.Context, _ string) (*jwtauth.ValidatedToken, error) {
			return &jwtauth.ValidatedToken{Subject: "system:serviceaccount:other:intruder", MatchedIssuer: "internal"}, nil
		},
	}
	p := auth.NewJWTProvider(mv, []string{"system:serviceaccount:opentams:loader"})

	_, err := p.Authenticate(context.Background(), "any.token.here")
	assertAppErrorCode(t, err, apperror.ErrForbidden)
}

// TC-AUTH-02c: internal token with an empty subject → ErrForbidden (never matches the allowlist).
func TestJWTProvider_InternalToken_EmptySubject(t *testing.T) {
	mv := &mockValidator{
		validateFn: func(_ context.Context, _ string) (*jwtauth.ValidatedToken, error) {
			return &jwtauth.ValidatedToken{Subject: "", MatchedIssuer: "internal"}, nil
		},
	}
	p := auth.NewJWTProvider(mv, []string{"system:serviceaccount:opentams:loader"})

	_, err := p.Authenticate(context.Background(), "any.token.here")
	assertAppErrorCode(t, err, apperror.ErrForbidden)
}

// TC-AUTH-03: ErrExpired → apperror.ErrTokenExpired.
func TestJWTProvider_ExpiredToken(t *testing.T) {
	mv := &mockValidator{
		validateFn: func(_ context.Context, _ string) (*jwtauth.ValidatedToken, error) {
			return nil, &jwtauth.ValidationError{Code: jwtauth.ErrExpired}
		},
	}
	p := auth.NewJWTProvider(mv, nil)

	_, err := p.Authenticate(context.Background(), "expired.token.here")
	if err == nil {
		t.Fatal("expected error")
	}
	var ae *apperror.AppError
	if !errors.As(err, &ae) {
		t.Fatalf("expected *apperror.AppError, got %T", err)
	}
	if ae.Code != apperror.ErrTokenExpired {
		t.Errorf("Code: got %q, want %q", ae.Code, apperror.ErrTokenExpired)
	}
}

// TC-AUTH-04: ErrNoMatch → apperror.ErrUnauthorized.
func TestJWTProvider_NoMatch(t *testing.T) {
	mv := &mockValidator{
		validateFn: func(_ context.Context, _ string) (*jwtauth.ValidatedToken, error) {
			return nil, &jwtauth.ValidationError{Code: jwtauth.ErrNoMatch}
		},
	}
	p := auth.NewJWTProvider(mv, nil)

	_, err := p.Authenticate(context.Background(), "invalid.token.here")
	if err == nil {
		t.Fatal("expected error")
	}
	var ae *apperror.AppError
	if !errors.As(err, &ae) {
		t.Fatalf("expected *apperror.AppError, got %T", err)
	}
	if ae.Code != apperror.ErrUnauthorized {
		t.Errorf("Code: got %q, want %q", ae.Code, apperror.ErrUnauthorized)
	}
}

// TC-AUTH-05: unknown error (not *ValidationError) → apperror.ErrUnauthorized.
func TestJWTProvider_UnknownError(t *testing.T) {
	mv := &mockValidator{
		validateFn: func(_ context.Context, _ string) (*jwtauth.ValidatedToken, error) {
			return nil, errors.New("unexpected internal failure")
		},
	}
	p := auth.NewJWTProvider(mv, nil)

	_, err := p.Authenticate(context.Background(), "any.token.here")
	if err == nil {
		t.Fatal("expected error")
	}
	var ae *apperror.AppError
	if !errors.As(err, &ae) {
		t.Fatalf("expected *apperror.AppError, got %T", err)
	}
	if ae.Code != apperror.ErrUnauthorized {
		t.Errorf("Code: got %q, want %q", ae.Code, apperror.ErrUnauthorized)
	}
}

// TC-AUTH-06: DevProvider always returns a principal with IssuerType "dev".
func TestDevProvider_AlwaysSucceeds(t *testing.T) {
	p := &auth.DevProvider{}

	principal, err := p.Authenticate(context.Background(), "any.token.value")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if principal.IssuerType != "dev" {
		t.Errorf("IssuerType: got %q, want %q", principal.IssuerType, "dev")
	}
	if principal.Subject == "" {
		t.Error("Subject should be non-empty")
	}
}

// assertAppErrorCode fails the test unless err is an *apperror.AppError with the wanted code.
func assertAppErrorCode(t *testing.T, err error, want apperror.ErrorCode) {
	t.Helper()
	if err == nil {
		t.Fatal("expected error")
	}
	var ae *apperror.AppError
	if !errors.As(err, &ae) {
		t.Fatalf("expected *apperror.AppError, got %T", err)
	}
	if ae.Code != want {
		t.Errorf("Code: got %q, want %q", ae.Code, want)
	}
}
