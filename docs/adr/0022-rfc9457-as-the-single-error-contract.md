---
status: "accepted"
date: 2026-08-31
decision-makers: OpenTAMS maintainers
consulted: OpenTAMS maintainers
informed: OpenTAMS contributors
---

# Serve every error as an RFC 9457 Problem Details document

## Context and Problem Statement

An API that grows one error shape per subsystem is hard to consume. Validation failures,
authentication failures, conflicts, and dependency outages all need to reach the client,
and each layer has its own natural way to describe what went wrong.

TAMS does not fix a general error format. Without a decision, the framework's default, the
validator's default, and hand-written handler errors each produce a different body.

What does an OpenTAMS error look like?

## Considered Options

* Whatever each layer produces, documented after the fact
* A project-specific error envelope
* RFC 9457 Problem Details, `application/problem+json`

## Decision Outcome

Chosen option: "RFC 9457 Problem Details". It is a published standard, clients have
libraries for it, and it has the fields this API needs.

`internal/apperror` holds the contract. It catalogues 23 error codes as slugs, and each
maps to a fixed HTTP status, a human-readable title, and a stable type URI under
`https://github.com/amagioss/opentams/problems/`. A code cannot be raised without a status
and a URI, because the catalogue supplies all three together.

One place writes error bodies. Strict handlers return an `error` and never touch the
response writer — see [ADR-0007](0007-strict-server-code-generation.md) — and
`middleware.ErrorHandler` converts it. Two adapters bring foreign errors into the same
shape: `validatorErrorHandler` for kin-openapi's validation failures, and
`paramParseErrorHandler` for oapi-codegen's parameter parsing, whose default emits
`{"msg":"..."}`.

Per-segment failures inside a bulk response carry a subset of the same vocabulary rather
than a second format. See [ADR-0023](0023-rfc9457-problem-details-for-per-segment-failures.md).

### Consequences

* Good, because a client writes one error handler. The body shape does not depend on which
  layer failed.
* Good, because the type URI is a stable identifier a client can branch on, where a message
  string is not.
* Good, because status codes cannot drift per call site. The catalogue owns the mapping.
* Bad, because every foreign error needs an adapter. Each new middleware that can fail is
  another conversion to write, and forgetting one leaks a different shape.
* Bad, because the type URIs are GitHub URLs that do not resolve to documents today. They
  work as identifiers and disappoint anyone who follows them.
* Bad, because adding an error code means editing a central catalogue, which is a small
  amount of friction on every new failure mode.

## More Information

* The catalogue: [`internal/apperror/apperror.go`](../../internal/apperror/apperror.go).
* Adapters: [`internal/server/problem.go`](../../internal/server/problem.go) and
  [`internal/server/validator.go`](../../internal/server/validator.go).
* Error catalogue in the requirements: §6.3.1 of [`../requirements.md`](../requirements.md).
