---
status: "accepted"
date: 2026-08-31
decision-makers: OpenTAMS maintainers
consulted: OpenTAMS maintainers
informed: OpenTAMS contributors
---

# Validate requests from the embedded spec with kin-openapi

## Context and Problem Statement

The contract states required fields, string patterns, enum members, and numeric bounds for
every operation. `oapi-codegen` generates types, so a request that decodes into the right
Go struct is not yet a valid request. Something has to enforce the rest.

Each handler can check its own inputs, or one middleware can check every request against
the specification.

## Considered Options

* Hand-rolled validation in each handler
* A struct-tag validator such as `go-playground/validator`
* Middleware that validates against the OpenAPI document with `kin-openapi`

## Decision Outcome

Chosen option: "Middleware that validates against the OpenAPI document".

`.oapi-codegen.yaml` sets `embedded-spec: true`, so the generated package exposes
`GetSwagger()`. `internal/server` calls it once at startup and installs
`ginmiddleware.OapiRequestValidatorWithOptions`. Every request-shape rule in the contract
is then enforced in one place, from the same document that generated the types.

Two options are set deliberately:

* `swagger.Servers` is set to `nil`. OpenTAMS runs behind reverse proxies, and
  kin-openapi's host matching would otherwise reject valid requests on the `Host` header.
  `SilenceServersWarning` suppresses the boot warning this causes.
* `AuthenticationFunc` is `NoopAuthenticationFunc`. Authentication runs earlier in the
  chain, and no `AuthenticationFunc` is registered against the bearer scheme, so
  kin-openapi's own pass would reject everything.

The middleware runs after authentication and before the strict handlers. An
unauthenticated caller therefore gets `401`, not `400`, and a strict handler can assume
its input is spec-valid.

A handler re-checks a rule only as declared defence in depth. The 1000-segment batch cap
is the one such case, and it is documented as deliberate.

### Consequences

* Good, because validation cannot drift from the contract. Both come from the same
  embedded document.
* Good, because handlers hold no boilerplate checks, so they read as business logic.
* Good, because a contract change tightens validation with no code change.
* Bad, because the embedded document adds roughly 30-50 KB to the binary and promotes
  `kin-openapi` from an indirect dependency to a runtime one.
* Bad, because kin-openapi's messages are not ours. `validatorErrorHandler` exists to
  convert them into the Problem Details contract.
* Bad, because validation happens before the handler, so a `400` cannot be asserted in a
  handler unit test. Those assertions live at the server-integration layer.

## More Information

* Wiring and the reason for each option: [`internal/server/server.go`](../../internal/server/server.go).
* Error conversion: [`internal/server/validator.go`](../../internal/server/validator.go).
* The rules `BR-SRV-05` and `BR-SRV-14` in [`../design/internal-server/`](../design/).
