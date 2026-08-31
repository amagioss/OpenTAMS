---
status: "accepted"
date: 2026-08-31
decision-makers: OpenTAMS maintainers
consulted: OpenTAMS maintainers
informed: OpenTAMS contributors
---

# Generate the server from the OpenAPI contract in strict-server mode

## Context and Problem Statement

OpenTAMS implements a published specification. The contract is fixed by the BBC and by
our own recorded divergences, so the request and response shapes are not ours to invent.
Hand-writing routing, parameter parsing, and response types against a fixed contract
means writing the same information twice and letting the two copies drift.

How does the contract become Go code?

## Considered Options

* Hand-write handlers, routing, and types, using the spec as documentation
* Generate types only, and hand-write routing
* Generate with `oapi-codegen` in `gin-server` mode, where a handler receives
  `*gin.Context`
* Generate with `oapi-codegen` in `strict-server` mode, where a handler receives typed
  parameters and returns a typed response object

## Decision Outcome

Chosen option: "strict-server mode". `.oapi-codegen.yaml` sets `strict-server: true`
alongside `gin-server: true` and `models: true`.

A strict handler has this shape: typed parameters in, a response object or an `error` out.
It never touches `http.ResponseWriter`. Status codes and content types come from the
generated response type, so a handler cannot return a status the contract does not
declare.

Errors take the other path. A handler returns an `error`, and one central
`ErrorHandler` turns it into a Problem Details body. See
[ADR-0022](0022-rfc9457-as-the-single-error-contract.md).

### Consequences

* Good, because the compiler enforces the contract. Removing a response from the spec
  breaks the build of any handler that still returns it.
* Good, because handlers hold no HTTP plumbing, which makes them testable as plain
  functions.
* Good, because there is exactly one place that writes an error body, so the error format
  cannot drift per endpoint.
* Bad, because a strict handler has no `*http.Request`. Anything that needs the raw
  request — content negotiation is the real case — cannot be a strict handler. That is why
  `/metrics` is excluded from codegen. See
  [ADR-0011](0011-health-and-metrics-outside-codegen.md).
* Bad, because generated response types are type definitions rather than aliases, which
  silently strips custom JSON marshalers. See
  [ADR-0009](0009-union-marshalling-bridge.md).
* Bad, because the generated file is large. `gen/api/opentams.gen.go` is about 14,000
  lines, and it dominates any diff that touches the contract.

## More Information

* Generator configuration, with the reason for each option: [`.oapi-codegen.yaml`](../../.oapi-codegen.yaml).
* The `go:generate` directives: [`tools/generate.go`](../../tools/generate.go).
* Handler-layer design: [`../design/internal-httpx-handlers/`](../design/internal-httpx-handlers/).
