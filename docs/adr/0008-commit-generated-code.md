---
status: "accepted"
date: 2026-08-31
decision-makers: OpenTAMS maintainers
consulted: OpenTAMS maintainers
informed: OpenTAMS contributors
---

# Commit the generated code and pin the generator through `go.mod`

## Context and Problem Statement

`gen/api/` is produced by `oapi-codegen` from the bundled contract. Generated code can be
committed, or it can be produced during the build. Committing it puts machine output in
review. Generating it at build time makes every build depend on a tool version, and makes
the code invisible until someone runs the generator.

Do we commit `gen/api/`, and how is the generator version fixed?

## Considered Options

* Generate during the build, commit nothing
* Commit the output, and install the generator separately
* Commit the output, and pin the generator as an ordinary module dependency

## Decision Outcome

Chosen option: "Commit the output, and pin the generator as an ordinary module
dependency".

`tools/generate.go` invokes the generator with `go run`, so the version comes from
`go.mod` like any other dependency. `tools/tools.go` carries the `tools` build tag and
imports the generator packages, which keeps them in `go.mod` without pulling them into the
server's build graph. Dependabot then sees the generator and proposes upgrades to it in
the same way it does for a runtime library.

`gen/api/opentams.gen.go` and `gen/api/opentams_json_bridges.gen.go` are committed. Both
carry a `Code generated ... DO NOT EDIT.` header, and post-processing is additive only:
`genunionbridges` writes a new sibling file and never edits the generator's output. See
[ADR-0009](0009-union-marshalling-bridge.md).

`make api-check` regenerates both into a scratch directory and fails if the committed copy
differs.

### Consequences

* Good, because `go build ./...` works on a clean checkout with no extra tools and no
  network access beyond the module cache.
* Good, because a contract change shows its full effect in the pull request. A reviewer
  sees which types and handlers actually moved.
* Good, because upgrading the generator is a reviewable diff rather than a silent change
  in build output.
* Good, because pinning through `go.mod` means one version for CI, for a contributor's
  machine, and for the release build.
* Bad, because a stale commit is possible. Someone can change the spec and forget to
  regenerate, and only `make api-check` catches it.
* Bad, because ~14,000 lines of generated Go sit in the repository, which inflates clone
  size and buries real changes in a large diff.
* Bad, because generated code must be excluded from lint and coverage by hand.

## More Information

* Tool pinning: [`tools/tools.go`](../../tools/tools.go) and [`tools/generate.go`](../../tools/generate.go).
* Drift detection: the `api-check` target in the [`Makefile`](../../Makefile), run by the
  Contract workflow.
