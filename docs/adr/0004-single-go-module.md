---
status: "accepted"
date: 2026-08-31
decision-makers: OpenTAMS maintainers
consulted: OpenTAMS maintainers
informed: OpenTAMS contributors
---

# Ship the server, the CLI, and the tools as one Go module

## Context and Problem Statement

The repository holds more than the API server. It also holds `tamsctl` (a client CLI), a
scale-test harness under `tools/scaletest/`, several media utilities, and code generators.
These have different dependency needs. The scale-test harness pulls in load-generation and
reporting libraries that the server does not need.

One module, or several?

## Considered Options

* One module for everything
* A module per deliverable: server, CLI, tools
* One module, with heavy tooling isolated behind build tags

## Decision Outcome

Chosen option: "One module, with heavy tooling isolated behind build tags". The repository
has exactly one `go.mod`, declaring `github.com/amagioss/opentams`. Code that is not part
of a shipped binary sits behind a build tag, so it stays out of the default build graph.

Four tags do this work:

* `tools` — generator entry points under `tools/`.
* `integration` — tests that need a real PostgreSQL or object store.
* `perf` — tests that assert latency budgets.
* `legacy_objectstore` — the superseded object-store implementation, kept for comparison.

We rejected a module per deliverable. Multi-module repositories need `replace` directives
or tagged releases to let one module use another, and every shared type change then needs
two commits. The server and `tamsctl` share `internal/domain` and `internal/timerange`
heavily, so that cost would be constant.

### Consequences

* Good, because a change to a shared type compiles or fails everywhere at once. There is
  no window where the CLI is built against a stale copy of the server's types.
* Good, because `go build ./...`, `go test ./...`, and one `golangci-lint` run cover the
  whole repository, which keeps CI and the `Makefile` simple.
* Good, because there is one dependency graph to audit, so `govulncheck` and the SBOM
  describe everything the repository contains.
* Bad, because `go.mod` lists dependencies that the server binary never uses, such as
  `testcontainers-go`. Anyone reading `go.mod` as a picture of the server's runtime
  dependencies is misled, and must read the build tags too.
* Bad, because build tags are easy to forget. Untagged test code that needs a database
  breaks `go test -short`, and only CI catches it.

## More Information

* Module path history: the module was renamed when the project moved to the `amagioss`
  organisation.
* Tag usage and how to run each suite: [`../development/codebase.md`](../development/codebase.md)
  and the `Makefile`.
