---
unit: M6 internal/auth
stage: Functional Design
status: Complete
---

# Business Rules — internal/auth

## BR-AUTH-01: Provider is the application's auth abstraction
`Provider` is the only type API handlers and middleware depend on. No handler ever imports `pkg/jwtauth` or `auth0/go-jwt-middleware` types directly.

## BR-AUTH-02: Principal is the validated identity shape for the application
`Principal{Subject, IssuerType}` is defined now and is the stable result type for the entire app layer. `Subject` feeds `auth_subject` in structured logs (REQ-SEC-12). `IssuerType` encodes the business meaning of which issuer matched.

## BR-AUTH-03: JWTProvider applies issuer selection policy
`JWTProvider` calls `jwtauth.Validator.Validate` and maps `ValidatedToken.MatchedIssuer` to `IssuerType`:
- `MatchedIssuer == "external"` → `IssuerType: "external"`
- `MatchedIssuer == "internal"` → `IssuerType: "internal"`

This is the only place in the codebase where issuer names carry business meaning.

The `internal` issuer is **optional**: when `AUTH_INTERNAL_ISSUER_URL` / `AUTH_INTERNAL_AUDIENCE` are unset, `cmd/opentams/serve.go::buildAuth` constructs the validator with only the external issuer. In that topology, no token can ever produce `IssuerType: "internal"`, and the deployment has a single authentication surface (the external issuer). Half-configuring the internal pair (only one of the two env vars set) is rejected at startup by `internal/config` per BR-CFG-03/04.

## BR-AUTH-04: Error translation is JWTProvider's responsibility
`JWTProvider` translates `*jwtauth.ValidationError` to `apperror` types:
- `jwtauth.ErrExpired` → `apperror.New(apperror.ErrTokenExpired, ...)`
- `jwtauth.ErrInvalid`, `jwtauth.ErrNoMatch` → `apperror.New(apperror.ErrUnauthorized, ...)`

No caller above `JWTProvider` sees `jwtauth` error types.

## BR-AUTH-05: DevProvider is the no-op for development
When `APP_ENV=development`, the application constructs a `DevProvider` instead of `JWTProvider`. `DevProvider.Authenticate` always returns `&Principal{Subject: "dev", IssuerType: "dev"}, nil` regardless of the token value (including empty string). DevProvider is never constructed in production.

## BR-AUTH-06: HTTP middleware adapter deferred
`Principal` shape is defined now to give future middleware a stable contract. The actual HTTP middleware that extracts the Bearer token and injects `Principal` into context is implemented in M14 (`internal/httpx/middleware`), not here.

## BR-AUTH-07: FGA extension point
`Provider.Authenticate` returns `(*Principal, error)`. Phase 2 fine-grained authorization (FGA) extends this by adding an `Authorize(ctx, principal, action, resource)` method to a separate interface — `Provider` does not change.
