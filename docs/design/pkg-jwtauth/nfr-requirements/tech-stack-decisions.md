---
unit: M6 pkg/jwtauth
stage: NFR Requirements
status: Complete
---

# Tech Stack Decisions — pkg/jwtauth

## Library: auth0/go-jwt-middleware/v2 (decided)

**Packages used:**

| Package | Purpose |
|---|---|
| `github.com/auth0/go-jwt-middleware/v2/jwks` | JWKS caching provider — fetches via OIDC discovery, caches with TTL |
| `github.com/auth0/go-jwt-middleware/v2/validator` | JWT signature + claims validation (RS256, issuer, audience, expiry) |

`jwtmiddleware` (the HTTP middleware package) is NOT imported — `validator.ValidateToken` is called directly.

**Underlying crypto**: go-jose v2 (used internally by auth0 library).
**Rejected**: go-jose/v3 direct — would require writing JWKS cache ourselves. golang-jwt/jwt v5 — no JWKS support.

## Testing: narrow unexported interface + struct mocks

`*validator.Validator` is a concrete type. We define an unexported interface covering only the method we call:

```go
type tokenValidator interface {
    ValidateToken(ctx context.Context, tokenString string) (interface{}, error)
}
```

`*validator.Validator` satisfies this at compile time (verified by blank-identifier assertion in test file). Tests inject struct mocks that return pre-set `*validator.ValidatedClaims` or errors.

**Rationale**: Unit tests verify error classification logic (`ErrExpired` priority over `ErrNoMatch`, mechanical iteration) and `ValidatedToken` construction — not auth0's JWT crypto, which is trust-and-don't-retest. Real JWKS HTTP fetching is auth0's responsibility; we don't unit-test it here.
**Rejected**: `httptest.Server` serving a real JWKS — tests auth0's code not ours. Real RSA key pair + signed JWTs — correct for integration tests, overkill for unit tests of routing logic.

## Error wrapping

`ValidationError` is a typed struct with `Code ErrCode` (string). Callers use `errors.As` to extract it. No `fmt.Errorf` wrapping — the error IS the type.
