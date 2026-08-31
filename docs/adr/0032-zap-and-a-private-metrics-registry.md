---
status: "accepted"
date: 2026-08-31
decision-makers: OpenTAMS maintainers
consulted: OpenTAMS maintainers
informed: OpenTAMS contributors
---

# Log with zap, and hold metrics in a private registry

## Context and Problem Statement

Both common Go observability libraries offer a package-level global: `log` writes to a
default logger, and `prometheus.MustRegister` writes to a default registry. Globals are
convenient and have two costs that matter here.

Tests that use a global share state. Two tests that register the same metric name collide,
and the failure depends on test order. A library that registers into the default registry
also imposes its metrics on anything that imports it, whether or not that program wants
them.

How are logging and metrics wired?

## Considered Options

* Package-level globals for both
* Structured logging with a global logger, and an injected metrics registry
* Explicit instances of both, passed as dependencies

## Decision Outcome

Chosen option: "Explicit instances of both, passed as dependencies".

Logging uses `zap`. It is structured, so a log line is queryable fields rather than a
formatted sentence, and it allocates little on the hot path. A `*zap.Logger` is passed to
the components that need it. `pkg/httplog` puts the request logger in the context, and
`LoggerFromContext` is how a strict handler reaches it, since a strict handler has no
`*http.Request`.

Metrics use a `Registry` from `pkg/metrics`. `New` calls `prometheus.NewRegistry()` and
registers the process and Go collectors into it, with options to leave either out. The
default registry is never touched. `/metrics` is served by `promhttp.HandlerFor` over this
registry, and `RawRegisterer()` exists for the middleware that needs to register into it.

Both packages sit in `pkg/`, because neither knows anything about TAMS — see
[ADR-0003](0003-pkg-holds-context-agnostic-libraries.md).

### Consequences

* Good, because tests construct their own registry and logger, so they cannot collide and
  can run in parallel.
* Good, because a test asserts on metrics by reading its own registry, with no global to
  reset between cases.
* Good, because importing `pkg/metrics` adds no metrics to anyone's default registry.
* Good, because structured logs carry the request ID as a field, so a request is traceable
  across every line it produced.
* Bad, because the logger and registry have to be threaded through constructors. Every
  component that needs either takes another parameter.
* Bad, because a third-party library that registers into the default registry is invisible
  to `/metrics`, and pulling those metrics in needs explicit work.
* Bad, because `RawRegisterer()` is an escape hatch. It exists for real reasons and it
  weakens the boundary the type otherwise provides.

## More Information

* Registry construction: [`pkg/metrics/metrics.go`](../../pkg/metrics/metrics.go).
* Context-carried request logger: [`pkg/httplog/`](../../pkg/httplog/).
* Endpoint wiring: [`internal/httpx/metrics/metrics.go`](../../internal/httpx/metrics/metrics.go).
