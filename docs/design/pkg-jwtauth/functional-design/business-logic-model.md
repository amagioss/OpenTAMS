---
unit: M6 pkg/jwtauth
stage: Functional Design
status: Complete
---

# Business Logic Model — pkg/jwtauth

## Types

```go
type IssuerConfig struct {
    Name      string   // caller-assigned, echoed in ValidatedToken.MatchedIssuer
    IssuerURL *url.URL
    Audience  []string
}

type ValidatedToken struct {
    Subject       string // JWT sub claim
    MatchedIssuer string // echoed IssuerConfig.Name
}

type ErrCode string

const (
    ErrExpired ErrCode = "expired"
    ErrInvalid ErrCode = "invalid"
    ErrNoMatch ErrCode = "no_match"
)

type ValidationError struct {
    Code ErrCode
}

func (e *ValidationError) Error() string

type Validator interface {
    Validate(ctx context.Context, rawToken string) (*ValidatedToken, error)
}
```

## MultiIssuerValidator Construction

```
New(cacheTTL time.Duration, issuers ...IssuerConfig) (*MultiIssuerValidator, error)
```

For each `IssuerConfig`:
1. `jwks.NewCachingProvider(issuer.IssuerURL, cacheTTL)` → JWKS key function
2. `validator.New(keyFunc, validator.RS256, issuer.IssuerURL.String(), issuer.Audience)` → per-issuer validator
3. Store alongside `issuer.Name`

Returns error if any issuer validator fails to construct.

## Validate — Mechanical Multi-Issuer Matching

```
Validate(ctx, rawToken string) (*ValidatedToken, error)

for each (name, validator) in registered issuers (registration order):
    result, err := validator.ValidateToken(ctx, rawToken)
    if err == nil:
        sub = result.(*validator.ValidatedClaims).RegisteredClaims.Subject
        return &ValidatedToken{Subject: sub, MatchedIssuer: name}, nil

// all failed — classify the error
if any issuer returned an expiry error:
    return nil, &ValidationError{Code: ErrExpired}
return nil, &ValidationError{Code: ErrNoMatch}
```

Note: if a token is expired, it will fail expiry validation on every issuer that otherwise matches the signature. The package surfaces `ErrExpired` over `ErrNoMatch` when any issuer detects expiry, because expiry is more informative to the caller.

## Compile-time assertion

```go
var _ Validator = (*MultiIssuerValidator)(nil)
```
