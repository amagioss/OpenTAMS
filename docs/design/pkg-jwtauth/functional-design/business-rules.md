---
unit: M6 pkg/jwtauth
stage: Functional Design
status: Complete
---

# Business Rules — pkg/jwtauth

## BR-JWT-01: Generic mechanics only — no business policy
This package does mechanical JWT validation against configured issuers. It has no knowledge of external vs internal identity, issuer preference, or application-level authorization. All policy belongs in the caller.

## BR-JWT-02: IssuerConfig carries a caller-assigned name
`IssuerConfig.Name` is set by the caller and echoed back in `ValidatedToken.MatchedIssuer`. The package treats it as an opaque identifier — it assigns no meaning to the name.

## BR-JWT-03: Multi-issuer iteration is mechanical
`MultiIssuerValidator.Validate` iterates configured issuers in registration order and returns the first successful match. Registration order carries no business meaning — it is an implementation detail. Callers must not rely on ordering for policy decisions; they must use `MatchedIssuer` name comparison instead.

## BR-JWT-04: Typed string error codes
Validation failures return `*ValidationError{Code ErrCode}` where `ErrCode` is a string constant. String codes are self-documenting in logs and safe to switch on by downstream consumers.

| Code | Meaning |
|---|---|
| `"expired"` | Token signature valid but `exp` claim is in the past |
| `"invalid"` | Token is malformed, signature invalid, or audience/issuer mismatch |
| `"no_match"` | No configured issuer accepted the token |

## BR-JWT-05: ValidatedToken carries Subject and MatchedIssuer
On success, `ValidatedToken.Subject` is the `sub` claim from the JWT. `ValidatedToken.MatchedIssuer` is the `Name` from the matching `IssuerConfig`. No other claims are returned — callers that need additional claims must extend via custom claims (future).

## BR-JWT-06: JWKS caching TTL is global
One `cacheTTL time.Duration` applies to all configured issuers. Per-issuer TTL is not supported in Phase 1.

## BR-JWT-07: RS256 only in Phase 1
`MultiIssuerValidator` validates RS256-signed tokens only. ES256 support is deferred.

## BR-JWT-08: Validator is an interface
`Validator` is an exported interface so callers can substitute mocks or alternative implementations in tests without depending on `MultiIssuerValidator` concretely.
