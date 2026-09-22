# Seeded Good First Issues

This document holds the three `good first issue` candidates for OpenTAMS, in the form a maintainer can paste into a GitHub issue with minimal editing. The issues themselves are filed on GitHub with the `good first issue` label; this file is the source-of-truth draft so the wording can be reviewed in PR before any issues go live.

> **Maintainer checklist before filing**: confirm the `good first issue` label exists on the repository (Settings → Labels), then file each issue below as-is. The body sections (`Background`, `What we want`, `Acceptance criteria`, `File pointers`, `Hints`, `Out of scope`, `Who to ping`) map directly to what `.github/ISSUE_TEMPLATE/feature_request.yml` collects, so contributors land on a familiar shape.

The three issues are deliberately picked to span concerns: a self-contained CLI feature (issue 1), documentation-as-code spread across multiple files (issue 2), and a behavioural-spec / TDD task (issue 3). A new contributor can pick any one and work in isolation; none of them depend on the others.

---

## Issue 1: Add `opentams version` subcommand

**Title**: `feat(cli): add 'opentams version' subcommand`

**Labels**: `good first issue`, `enhancement`

### Background

OpenTAMS releases inject `version`, `commit`, and `date` strings into the binary at build time via `goreleaser` ldflags (see [`cmd/opentams/main.go`](../cmd/opentams/main.go) — the `version`, `commit`, `date` package globals). Today nothing surfaces these to the operator: there is no `opentams version` command, and the values are kept reachable only by an `init()` that reads them so the strict linter doesn't flag them as unused.

A real CLI ships a `version` subcommand. Operators verify what they're running with `opentams version` before filing bug reports; deployment automation parses the output to confirm the right tag landed; security tooling cross-references the printed digest against attested build provenance.

### What we want

A new `opentams version` subcommand that prints the build-time variables in a format suitable for both human and machine consumption.

Default output, designed for humans:

```
opentams version v0.1.0 (commit abc123, built 2026-04-23T10:15:00Z)
```

With `--json`, designed for parsing:

```json
{"version":"v0.1.0","commit":"abc123","date":"2026-04-23T10:15:00Z","go_version":"go1.26.6"}
```

`go_version` should come from `runtime.Version()`; the rest from the existing package globals.

### Acceptance criteria

1. New file `cmd/opentams/version.go` defining `newVersionCmd()` returning a `*cobra.Command`.
2. Wired into `newRootCmd()` (in `cmd/opentams/root.go`) alongside `serve`, `migrate`, and `gc`.
3. Default output exactly matches the human format above (with the actual values substituted).
4. `--json` flag produces canonical JSON (key order: `version`, `commit`, `date`, `go_version`) followed by a single `\n`.
5. Test file `cmd/opentams/version_test.go` covers: default output shape, `--json` output shape, behaviour when version is the default `dev` (e.g. `go run ./cmd/opentams version` should print something legible, not crash).
6. The `init()` workaround in `cmd/opentams/main.go` (`func init() { _ = version; _ = commit; _ = date }`) and its `//nolint:gochecknoinits` annotation can be removed — the new subcommand makes the package vars legitimately referenced.
7. README's [Releases verification section](../README.md#verifying-release-artefacts) gains one line: "Run `docker run --rm ghcr.io/amagioss/opentams:vX.Y.Z version` to confirm the image is the version you intended to deploy."

### File pointers

- [`cmd/opentams/main.go`](../cmd/opentams/main.go) — has the `version`/`commit`/`date` globals + the `init()` workaround to remove.
- [`cmd/opentams/root.go`](../cmd/opentams/root.go) — wire the new command here.
- [`cmd/opentams/serve.go`](../cmd/opentams/serve.go) — closest existing subcommand to use as a template.

### Hints

- `runtime.Version()` returns the Go runtime version OpenTAMS was compiled against (`go1.26.6`).
- For the `--json` output, build a small struct and `encoding/json.Marshal` it; do not hand-roll JSON.
- The existing tests use plain `testing` (not testify) — match that style.

### Out of scope

- Reading version info from runtime debug build info as a fallback. Worth doing eventually but adds a layer of indirection that obscures the goreleaser injection path. File a follow-up if you want to add it after this issue lands.
- Multiple output formats (yaml, table). `--json` is the one machine-readable form; everything else is over-engineered for a `version` command.

### Who to ping

- `@amagioss/opentams-maintainers` (general)
- For ldflags / release-pipeline questions: anyone who's recently touched [`.goreleaser.yml`](../.goreleaser.yml) or [`.github/workflows/release.yml`](../.github/workflows/release.yml).

---

## Issue 2: Improve godoc on `pkg/` packages

**Title**: `docs: add godoc to exported symbols in pkg/<package>`

**Labels**: `good first issue`, `documentation`

> **Note for the maintainer**: this is intentionally one issue per `pkg/` package, so a contributor picks the package they want and the work is bounded. Open it once per package — `pkg/jwtauth`, `pkg/httpmetrics`, `pkg/dbmigrate`, `pkg/httplog`, `pkg/httprecovery`, `pkg/requestid`, `pkg/metrics`, `pkg/logger` — substituting the package name in the title and body.

### Background

The packages under `pkg/` are reusable libraries: anything there can be `go get`'d by an external consumer without depending on OpenTAMS-specific business rules. Because they're reusable, godoc matters more than for `internal/` code — `pkg.go.dev` rendering is the project's first impression.

The project's strict-lint configuration surfaces `revive`'s "exported X should have comment" finding for ~150 symbols across `pkg/`. We're cleaning these up package-by-package, and good-first-issue contributors are very welcome to take one package each.

### What we want

For one chosen `pkg/<name>` package, add godoc comments to every exported identifier (types, functions, methods, constants, variables) that doesn't already have one. Each comment should:

1. Start with the identifier name (Go's standard convention).
2. Describe **what** it is and **why** a caller would use it — not just restate the type signature.
3. Be one to three sentences for most APIs; longer if it documents behaviour the type signature can't convey (concurrency, allocation, error semantics).

Example, before:

```go
type Validator interface {
    Validate(ctx context.Context, token string) (*ValidatedToken, error)
}
```

After:

```go
// Validator authenticates a raw bearer token against one or more configured
// OIDC issuers. Implementations are safe for concurrent use; JWKS caches
// refresh on demand when an unknown `kid` is encountered, so adding a new
// signing key to the issuer does not require a Validator restart.
type Validator interface {
    Validate(ctx context.Context, token string) (*ValidatedToken, error)
}
```

### Acceptance criteria

1. `golangci-lint run --new-from-rev=main ./pkg/<name>/...` reports zero `revive.exported` findings introduced by the PR.
2. `go test -race -count=1 ./pkg/<name>/...` continues to pass — godoc-only edits should never affect tests.
3. Each comment starts with the identifier name (Go convention; `revive` enforces this).
4. No comment is longer than 5 lines without a structural reason. We're documenting, not novelising.
5. Existing godoc comments are not paraphrased or re-styled unless they're factually wrong.

### File pointers

- The package directory itself: `pkg/<name>/`.
- `pkg/<name>/<name>.go` — usually the main file; start here.
- `pkg/<name>/<name>_test.go` — sometimes test files have helpful examples that can inform the godoc.

### Hints

- Run `make lint` locally and grep the output for `pkg/<name>` — every reported `revive.exported` line points at a missing godoc.
- `gofmt` and `goimports` are run by `make fmt`; the godoc additions will need `gofmt` to settle.
- If you're not sure what an identifier does, read its tests — that's documentation we already have, just in a different form.

### Out of scope

- Documenting unexported (lowercase) identifiers. They're not part of the package's public surface.
- Major restructuring of existing comments — keep the diff narrow.
- Adding new tests, examples, or runnable godoc examples (`Example*` functions). Worth doing, but a separate issue.

### Who to ping

- `@amagioss/opentams-maintainers`
- For the specific package's domain: see the package's existing maintainer history (`git log pkg/<name>/`) — the most recent contributor is usually the most context-rich reviewer.

---

## Issue 3: Add a regression test for an uncovered `MUST` in the requirements spec

**Title**: `test: cover REQ-<area>-<num> with an explicit regression test`

**Labels**: `good first issue`, `testing`

### Background

OpenTAMS treats [`docs/requirements.md`](requirements.md) as the binding behavioural contract: every `MUST`-tier rule should have at least one test that fails on the parent commit if the rule is broken. We gate this culture in [CONTRIBUTING.md](../CONTRIBUTING.md) ("tests as documentation" — the test name should read like the rule it covers).

In practice, a handful of `MUST` rules don't yet have an explicit regression test — they may be implicitly covered by a happy-path test, or covered by integration but not unit, or covered through an indirect path that wouldn't fail loudly if the rule were broken. The goal of this issue is to add one such test.

### What we want

1. Pick a `MUST` rule from `docs/requirements.md` that lacks an explicit unit-level regression test. Suggested candidates (the maintainer can extend this list):
   - **REQ-DATA-06**: when a client does not provide `object_timerange` on segment registration, the server stores nothing for that field rather than computing a value (first-write-wins denormalization is the corollary).
   - **REQ-IDEM-04**: a `POST /segments` that returns 5xx must not leave the idempotency key in `in_flight=true` state (the bug we shipped Tier-2 to fix; a regression test specifically against re-introducing it).
   - **REQ-SEG-04**: an empty time-range (`()`) on segment registration must be rejected with 400, not silently accepted as a no-op.
2. Write a test under the most-appropriate package — usually `internal/service/segment` or `internal/httpx/handlers` — named for the rule (`TestRegisterSegments_REQ_DATA_06_DoesNotComputeMissingObjectTimerange`).
3. Confirm the test fails on a deliberately-broken implementation (RED), passes on the current implementation (GREEN). Mention the RED-confirmation step in the PR description so reviewers know it's a real regression test, not a tautology.

### Acceptance criteria

1. New test added at the appropriate layer; existing tests untouched.
2. Test name embeds the requirement ID (REQ-XXX-YY).
3. Test passes on `main` (you should not need to change implementation code).
4. PR description includes a one-paragraph "RED confirmation" — the line you'd revert to make the test fail, and the symptom you observed.
5. If the test name reveals a typo or stale reference in [`docs/requirements.md`](requirements.md), open a separate issue / PR for the doc correction. Don't bundle.

### File pointers

- [`docs/requirements.md`](requirements.md) — the spec; grep for `MUST` to find candidates.
- [`internal/service/segment/`](../internal/service/segment/) — most likely target package.
- [`internal/httpx/handlers/`](../internal/httpx/handlers/) — alternative target if the rule is HTTP-shaped.

### Hints

- The existing test files have heavy use of table-driven tests; a single rule may merit just one extra row in an existing table rather than a brand-new function.
- `pgx`'s testcontainer fixtures are heavyweight — prefer the in-memory mocks in `internal/metastore/store_test.go` if you can.
- "Tests as documentation" doesn't mean "long tests". The best ones are short and scrutable; `t.Run("rejects empty timerange", ...)` is more readable than `TestRegisterSegments_RejectsEmptyTimerangeFromTAMSv8Section512`.

### Out of scope

- Coverage-percentage targets. We don't gate on coverage; we gate on "behavioural rules have explicit regression tests".
- Refactoring the existing test setup. Pick a rule, add a test, that's the issue.
- Implementation changes. If you find a rule the implementation actually doesn't satisfy, that's a separate (and very welcome) bug-report issue, not this one.

### Who to ping

- `@amagioss/opentams-maintainers`
- For spec-interpretation questions: anyone who's recently touched [`docs/requirements.md`](requirements.md) (`git log docs/requirements.md`).

---

## Pre-launch checklist for the maintainer

Before filing these issues:

- [ ] Confirm the `docs/requirements.md` REQ IDs cited above match the current spec — they were correct as of the document's last review (2026-05-04), but the spec evolves.
- [ ] After filing each issue, link it back here (or remove this file once they're filed; either is fine).
