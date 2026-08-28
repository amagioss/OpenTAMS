---
unit: M6 internal/auth
stage: NFR Requirements
status: Complete
---

# Tech Stack Decisions — internal/auth

## Dependencies: pkg/jwtauth + internal/config + internal/apperror

No external dependencies. `internal/auth` depends only on packages within this module:
- `pkg/jwtauth` — validation mechanics
- `internal/config` — `*config.Config` for constructor
- `internal/apperror` — error translation

## Testing: mock jwtauth.Validator via interface

`jwtauth.Validator` is an exported interface — mock it directly with a struct in `_test.go`. No auth0 library types appear in `internal/auth` tests.

```go
type mockValidator struct {
    validateFn func(ctx context.Context, rawToken string) (*jwtauth.ValidatedToken, error)
}
func (m *mockValidator) Validate(...) (*jwtauth.ValidatedToken, error) { return m.validateFn(...) }
```

Tests cover:
- Successful external token → `Principal{IssuerType: "external"}`
- Successful internal token → `Principal{IssuerType: "internal"}`
- `jwtauth.ErrExpired` → `apperror.ErrTokenExpired`
- `jwtauth.ErrInvalid` / `jwtauth.ErrNoMatch` → `apperror.ErrUnauthorized`
- `DevProvider.Authenticate` always succeeds

## Error translation

`JWTProvider` uses `errors.As(err, &ve)` to extract `*jwtauth.ValidationError`, then switches on `ve.Code`. Unknown error types (not `*ValidationError`) also map to `ErrUnauthorized` — defensive default.
