---
status: "accepted"
date: 2026-08-31
decision-makers: OpenTAMS maintainers
consulted: OpenTAMS maintainers
informed: OpenTAMS contributors
---

# Reserve `pkg/` for context-agnostic libraries

## Context and Problem Statement

Go gives `internal/` a compiler-enforced meaning: nothing outside the module can import
it. `pkg/` has no enforced meaning at all. Many Go repositories use `pkg/` as a second
name for "source code", which tells a reader nothing.

What separates `pkg/` from `internal/` in this repository?

## Considered Options

* Put everything in `internal/`, and add `pkg/` only when something is published
* Split by layer: `pkg/` for libraries, `internal/` for the application
* Split by intent: `pkg/` for code that knows nothing about TAMS, `internal/` for code
  that does

## Decision Outcome

Chosen option: "Split by intent". A package belongs in `pkg/` when it would still make
sense in a service that had never heard of TAMS. Everything that knows what a Flow, a
Source, or a Segment is belongs in `internal/`.

The rule holds today. `pkg/` contains `logger`, `metrics`, `requestid`, `httplog`,
`httpmetrics`, `httprecovery`, `jwtauth`, and `dbmigrate` — eight packages, none of which
mentions a TAMS concept. `internal/` contains `domain`, `metastore`, `objectstore`,
`service`, `httpx`, `timerange`, and the rest.

The test is mechanical, which is the point: grep a candidate package for TAMS vocabulary.
A hit means it belongs in `internal/`.

### Consequences

* Good, because the boundary is checkable rather than a matter of taste, so it survives
  contributors who did not write the original rule.
* Good, because it keeps the coupling one-way. `internal/` imports `pkg/`, never the
  reverse, and that constraint falls out of the rule instead of needing a linter.
* Good, because a `pkg/` package can be extracted into its own module later without
  untangling domain types first.
* Bad, because a package can drift. Adding one TAMS-aware function to a `pkg/` package
  breaks the rule quietly, and only review catches it.
* Bad, because `pkg/` is importable by anything that depends on the module, so those
  eight packages carry a compatibility obligation we have not yet made explicit.

## More Information

* The layout, annotated: [`../development/codebase.md`](../development/codebase.md).
* Module boundaries: [ADR-0004](0004-single-go-module.md).
