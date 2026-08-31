---
unit: M6 internal/auth
stage: Functional Design
status: Complete
---

# Business Logic Model — internal/auth

## Types

```go
type Principal struct {
    Subject    string // JWT sub claim — used as auth_subject in logs
    IssuerType string // "external" | "internal" | "dev"
}

type Provider interface {
    Authenticate(ctx context.Context, rawToken string) (*Principal, error)
}
```

## JWTProvider Construction

```
NewJWTProvider(cfg *config.Config) (*JWTProvider, error)

jwtauth.New(cfg.AuthJWKSTTL,
    jwtauth.IssuerConfig{Name: "external", IssuerURL: parse(cfg.AuthExternalIssuerURL), Audience: []string{cfg.AuthExternalAudience}},
    jwtauth.IssuerConfig{Name: "internal", IssuerURL: parse(cfg.AuthInternalIssuerURL), Audience: []string{cfg.AuthInternalAudience}},
)
→ store validator + issuer name constants
```

## JWTProvider.Authenticate

```
tok, err := p.validator.Validate(ctx, rawToken)
if err != nil:
    var ve *jwtauth.ValidationError
    errors.As(err, &ve):
        ve.Code == jwtauth.ErrExpired → apperror.New(ErrTokenExpired, "token has expired")
        otherwise              → apperror.New(ErrUnauthorized, "invalid token")
    unknown error              → apperror.New(ErrUnauthorized, "invalid token")

issuerType := tok.MatchedIssuer  // "external" or "internal" — names set at construction
return &Principal{Subject: tok.Subject, IssuerType: issuerType}, nil
```

## DevProvider

```go
type DevProvider struct{}

func (DevProvider) Authenticate(_ context.Context, _ string) (*Principal, error) {
    return &Principal{Subject: "dev", IssuerType: "dev"}, nil
}
```

Always succeeds. Constructed only when `cfg.AppEnv == "development"`.

## Compile-time assertions

```go
var _ Provider = (*JWTProvider)(nil)
var _ Provider = (DevProvider)(nil)
```
