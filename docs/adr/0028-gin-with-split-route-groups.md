---
status: "accepted"
date: 2026-08-31
decision-makers: OpenTAMS maintainers
consulted: OpenTAMS maintainers
informed: OpenTAMS contributors
---

# Use Gin, and split routing into a public group and an authenticated group

## Context and Problem Statement

`oapi-codegen` emits a router binding for a chosen framework. The choice matters less than
usual here, because strict handlers never touch the framework's context — see
[ADR-0007](0007-strict-server-code-generation.md). What the framework really provides is
middleware ordering and route grouping.

Ordering is the hard part. Three probe endpoints must answer without a token, every other
operation must not, and request validation must run after authentication rather than
before, so an unauthenticated caller sees `401` and not `400`.

Which framework, and how are the routes arranged?

## Considered Options

* `net/http` with `chi`, and hand-written middleware ordering
* Echo
* Gin

## Decision Outcome

Chosen option: "Gin". It has first-class `oapi-codegen` support, and `oapi-codegen/gin-middleware`
supplies the kin-openapi validator binding used by
[ADR-0010](0010-spec-driven-request-validation.md).

Routing is two groups on one engine, and the order inside each is the decision:

Engine-wide middleware runs for everything — request ID, panic recovery, request logging,
HTTP metrics, and the Problem Details error handler. Metrics skip `/healthz`, `/readyz`,
and `/metrics`, so probe and scrape traffic does not distort request statistics.

The **public** group mounts the three unauthenticated endpoints and nothing else.

The **authenticated** group applies `middleware.Auth` first, then the validator, then the
generated handlers. Authentication before validation is what makes a bad token a `401`.
Validation before the handlers is what lets a strict handler assume a spec-valid request.

The two groups cannot collide, because the three public operations are excluded from
codegen — see [ADR-0011](0011-health-and-metrics-outside-codegen.md).

`applyGinMode` sets Gin's mode from `APP_ENV`, so debug output is a development-only
behaviour.

### Consequences

* Good, because the security boundary is one line of routing. A new operation is
  authenticated by default, because it is generated into the authenticated group.
* Good, because middleware order is explicit in one function, not spread across handlers.
* Good, because the framework binding stays shallow. Strict handlers do not import Gin, so
  replacing it would touch the server package and little else.
* Bad, because the public group is a manual list. Adding a fourth unauthenticated endpoint
  means editing both the router and the generator config, and nothing links the two.
* Bad, because Gin brings its own routing and binding machinery that we use almost none of.
* Bad, because Gin's default error and panic behaviour has to be replaced rather than
  configured, which is why `PanicWriter` and the custom error handler exist.

## More Information

* Engine construction and middleware order: [`internal/server/server.go`](../../internal/server/server.go).
* Business rules `BR-SRV-03`, `BR-SRV-06`, `BR-SRV-14`: [`../design/`](../design/).
