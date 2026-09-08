# Contributing to OpenTAMS

Thanks for your interest in OpenTAMS — a cloud-agnostic implementation of the [BBC TAMS v8.0](https://bbc.github.io/tams/) media-asset API. This document is the entry point for everything you need to make a useful first pull request.

OpenTAMS has not cut its first stable release. The architecture, file layout, and even some public packages are still in motion — we try to be honest about that in the [README](README.md) and on every release. If you find something that looks underspecified or contradicts the [requirements spec](docs/requirements.md), that's a contribution waiting to happen — open an issue.

By participating in this project you agree to abide by our [Code of Conduct](CODE_OF_CONDUCT.md).

## Reporting Security Issues

Please **do not** open a public GitHub issue for security findings — see [SECURITY.md](SECURITY.md) for the private reporting channels and our response timeline.

## Prerequisites

You'll need:

- **Go 1.26 or newer** (`go version` should report ≥ 1.26.0). The exact version we develop against is in [`go.mod`](go.mod).
- **Docker** + **Docker Compose v2** for the local development stack (Postgres + MinIO).
- **Git** with reasonable defaults. Commit signing is encouraged but not required.
- **`make`** is optional but convenient — see [the Makefile section](#using-the-makefile) below.

Optional but useful:

- [`golang-migrate`](https://github.com/golang-migrate/migrate) for running schema migrations directly against a database (the repo also exposes `opentams migrate` as a thin wrapper, but the standalone tool is handy when you need to inspect migration state).
- An OpenAPI viewer (Swagger UI, Redocly CLI, or VS Code's REST Client) to explore [`api/opentams-api-bundled.yaml`](api/opentams-api-bundled.yaml).

## One-command Local Setup

```bash
git clone https://github.com/amagioss/opentams.git
cd opentams

# Bootstraps .env, brings up Postgres + MinIO, applies migrations,
# and starts the server on the host. Equivalent to `make stack` +
# `./scripts/start.sh` (migrate + serve).
make run
```

The server listens on `:8080`. `GET /healthz` should return `200`. See the [README's Quickstart](README.md#quickstart) for hitting actual API endpoints.

## Build and Test

```bash
# Build everything
go build ./...

# Static checks
go vet ./...

# Unit tests (fast, no infra)
go test ./...

# Race-checked unit tests — required to pass before a PR is merged
go test -race -count=1 ./pkg/... ./internal/... ./cmd/...

# Integration tests that need real Postgres + S3 (testcontainers; slow)
go test -tags=integration -race -count=1 ./...

# Performance budgets (testcontainers; no -race — budgets assume an unraced binary)
go test -tags=perf -count=1 ./...
```

Tests in `pkg/` and most of `internal/` use mocked dependencies and finish in seconds — that is what the untagged run covers, and it needs no Docker.

Anything that needs a container is behind the `integration` build tag: the metastore, idempotency, and migration suites, the segment concurrency tests, and the scale-test tooling. They spin up Postgres and MinIO via [testcontainers-go](https://golang.testcontainers.org/) and **run on every PR** — the persistence layer is where the interesting bugs live, so it stays on the gate. You do not need to run them locally for every change, but do run them before opening a PR that touches storage.

The `perf` tag carries wall-clock budgets. Those are machine-sensitive, so they run nightly rather than on the gate. Each takes an `OPENTAMS_PERF_*_MS` environment override when a slower machine needs one; the defaults sit next to the assertions.

For a single package while iterating:

```bash
go test -race -count=1 -run TestSomething ./pkg/httpmetrics/...
```

`-count=1` defeats the test cache — useful when you're hunting a flaky test or you've changed something the cache doesn't see.

### Using the Makefile

The repository ships a thin Makefile that wraps the same `go` and `golangci-lint` commands CI runs, so you can drive most workflows with `make <target>`:

```bash
make build          # go build ./...
make vet            # go vet ./...
make test           # race-checked unit tests (matches CI)
make coverage       # unit tests + per-package coverage summary
make lint           # golangci-lint with the project's strict config
make fmt            # apply gofmt and goimports
make integration    # integration tests (testcontainers; also runs on every PR)
make perf           # performance budgets (testcontainers; runs nightly)
make vuln           # fails on any advisory reachable from our code
make docker-build   # multi-arch buildx verification (no push)
make ci             # build + vet + lint + vuln + test (the full CI gate, locally)
make tools-install  # install pinned golangci-lint + activate pre-commit hook
make help           # list all targets
```

`make ci` is the single command we recommend running before opening a PR — it's the same gate CI applies on every push.

**First-clone setup**: run `make tools-install` once after cloning. It installs the pinned `golangci-lint` and `goreleaser` binaries into `$(go env GOPATH)/bin`, and wires up `.githooks/pre-commit` so `git commit` runs the strict lint config against any new findings vs `main` automatically. The hook only flags **new** lint violations introduced by the commit — legacy issues elsewhere in the tree do not gate unrelated work. If you ever need to bypass it (e.g. WIP commit), `git commit --no-verify` skips the hook for that commit.

## Code Style

- **Format**: every Go file must be `gofmt`-clean. Most editors do this on save; otherwise `gofmt -w .` from the repo root.
- **Imports**: `goimports` (group: stdlib, third-party, `github.com/amagioss/opentams/...`).
- **Linting**: the project ships a strict [`.golangci.yml`](.golangci.yml). Run `make lint` (or `golangci-lint run --timeout=5m`) before opening a PR. The `Lint` CI workflow runs the same configuration on every PR. The repo is currently lint-clean; please keep it that way (and surface any new exclusion you add with a justification comment in `.golangci.yml`).
- **Naming**: standard Go conventions (`MixedCaps`, no `Get` prefix on getters unless disambiguating, exported identifiers carry godoc that starts with the identifier name).
- **Errors**: see [`internal/apperror`](internal/apperror) — every error returned across a module boundary should be either a typed `*apperror.AppError` (for problems an HTTP handler will translate to RFC 9457 ProblemDetails) or wrapped with `fmt.Errorf("...: %w", err)`. Plain `errors.New` is fine for internal sentinels.
- **Logging**: use the package-scoped `*zap.Logger` injected through dependencies. Don't reach for `log.Println` or the global `zap.L()` — both bypass the structured fields the rest of the codebase relies on.
- **Tests as documentation**: per [REQ-OSS-09](docs/requirements.md), test names should read like specifications — `TestPostFlowSegments_RejectsRequestsWithoutIdempotencyKey` over `TestSegments_Bad`. A reader who doesn't know TAMS should be able to learn the expected behaviour from the test list.

## Branching and Commits

- Branch from the latest `main`. Topic branches are short-lived; we squash on merge.
- Name branches `<type>/<short-kebab-description>`, mirroring the Conventional Commits type of the work: `feat/segment-pagination`, `fix/idempotency-key-leak`, `docs/contributing-guide`, `refactor/metastore-pool`, `chore/bump-golangci`. Use the same types as commits (`feat`, `fix`, `docs`, `refactor`, `test`, `chore`, …); keep the description lowercase and hyphenated. This is a recommendation, not a CI gate.
- One logical change per PR. If you're tempted to write "and also …" in the description, that's a sign to split the PR.
- We **recommend** [Conventional Commits](https://www.conventionalcommits.org/) for commit messages and PR titles. The existing history uses scopes like `feat(core):`, `refactor(core):`, `fix(metastore):`, `docs(spec):`. We do not enforce the format via CI today — it's a recommendation, not a gate.
- We **recommend** signing your commits with a [Developer Certificate of Origin](https://developercertificate.org/) sign-off (`git commit -s`). It's a one-line attestation that you wrote the code and have the right to contribute it under the project's licence. We don't enforce it via CI today, but it's a no-friction habit that pays off if the project ever needs to demonstrate provenance.

A good commit message looks like:

```
fix(idempotency): release in-flight key on transient errors

POST /flows/{flowId}/segments was leaving idempotency keys in the
in_flight=true state for 24 hours when the handler returned a 5xx,
locking out subsequent retries with the same key. Switch to
Release() on transient failure paths and add a defer safety net.

Refs REQ-IDEM-04.
```

Subject line ≤ 72 chars, imperative mood, no trailing period. Body explains *why*, not *what* (the diff already shows the *what*).

## Pull Request Process

1. **For non-trivial changes, open an issue first.** "Non-trivial" means anything that changes a public package's API, the database schema, an HTTP response shape, or a behavioural rule documented in [`docs/requirements.md`](docs/requirements.md). Quick fixes, doc updates, and isolated test additions don't need a prior issue — go straight to a PR.
2. **Open a PR against `main`** from a fork or a topic branch. Link the issue (`Fixes #123` / `Refs #123`) in the description.
3. **Fill in the PR template.** It's short — we just need to know what changed, what tests cover it, and any spec/requirement IDs the change is anchored to.
4. **The CI gate must be green** before review. The simplest way is `make ci` locally (build + vet + lint + race-checked tests). `make ci` includes `vuln`, which fails on any advisory `govulncheck` can trace from OpenTAMS code. There is no suppression list: if an upgrade is blocked, the gate stays red until it lands. Releases do not run it, so a red gate does not block a release.

The same checks run automatically on every PR via the `Test`, `Lint`, and `Build` workflows — `Test` runs both the untagged suite and the `integration`-tagged one, and both must pass. The nightly `Integration` workflow re-runs the tagged suite against `main` and adds the `perf` budgets.
5. **At least one maintainer approval** is required to merge. Reviews aim to be fast and concrete; if a reviewer is blocking on something they consider important, please address the feedback or push back with reasoning — we'd rather have the conversation in the PR than after merge.
6. **Squash merge** is the default. Your PR title becomes the commit subject on `main`, so write it like a commit message (Conventional Commits scope, imperative mood).

### Tests Required for Behavioural Changes

Every PR that changes runtime behaviour needs tests. We don't enforce a coverage percentage, but we do enforce two things:

- **Bug fixes** ship with a regression test that fails on the parent commit and passes on yours.
- **New features** ship with tests that exercise the documented behaviour, not just the happy path. Edge cases the spec calls out — e.g. REQ-DATA-06's rule that when a client *doesn't* provide `object_timerange`, the server stores nothing rather than computing one — need explicit assertions, not just happy-path coverage.

A common shortcut that we push back on: tests that only assert the implementation's internals (struct fields, function calls). Prefer tests that observe the system from outside (HTTP responses, gathered metric names, database state).

### Documentation Updates

If your change adds, removes, or alters:

- An API endpoint or response shape → update `api/opentams-api-v1.yaml`, then re-bundle and regenerate the Go types:
  ```bash
  redocly bundle api/opentams-api-v1.yaml -o api/opentams-api-bundled.yaml
  go generate ./...
  ```
  The first command requires [Redocly CLI](https://redocly.com/docs/cli/installation) on `PATH`; the second runs the directives in `tools/generate.go` (`oapi-codegen` + the union-bridges post-processor). Both the bundled YAML and `gen/api/opentams.gen.go` are committed to the repo.
- A configuration variable → update the configuration table in [README.md](README.md) and the relevant section of [`docs/requirements.md`](docs/requirements.md).
- A package's public API → update the package's godoc and any functional-design document under [`docs/design/`](docs/design/) that references it.

## Where to Start

Before opening a PR, get oriented:

- **[`docs/architecture/`](docs/architecture/)** — high-level system design, storage ownership, and API flows.
- **[`docs/development/codebase.md`](docs/development/codebase.md)** — package boundaries, extension points, and a "where to start reading" guide.
- **[`docs/conformance.md`](docs/conformance.md)** — what's implemented today vs. deferred. Tells you whether your idea is "extending an existing surface" or "building a deferred surface" — the review path for the two is different.
- **[`docs/configuration.md`](docs/configuration.md)** — full env-var reference if your change touches configuration.
- **[`examples/`](examples/)** — runnable client programs against the live API. Useful as a reference when adding new client-facing surface.

Looking for a first contribution? Some entry points that don't require deep TAMS knowledge:

- Issues tagged [`good first issue`](https://github.com/amagioss/opentams/labels/good%20first%20issue) — small, self-contained, with explicit acceptance criteria. The current seeded set is in [`docs/good-first-issues.md`](docs/good-first-issues.md).
- Improving godoc on any package under `pkg/` — these are reusable libraries and benefit from clearer examples.
- Adding test cases for edge conditions called out in [`docs/requirements.md`](docs/requirements.md) but not yet covered (search the spec for `MUST` and grep for the corresponding test).

If you're not sure whether something is worth a PR, open an issue and ask. We'd rather discuss a one-line idea than receive a 2,000-line PR that turns out to be in the wrong direction.

## Releasing

> This section is for maintainers. Contributors don't need to read it to land a PR.

OpenTAMS releases are tag-driven and fully automated. The maintainer flow is:

```bash
# 1. From a clean checkout of main with all desired commits merged:
git checkout main
git pull --ff-only

# 2. Pick the next semver tag. Below v1.0.0 the minor moves on every
#    feature release and the patch on bug-fix-only releases. Use
#    -rc.N / -beta.N / -alpha.N suffixes for pre-releases — goreleaser
#    will auto-flag those as "Pre-release" on GitHub. Use the dotted
#    form (-rc.1, not -rc1) so pre-releases sort numerically past 9.

# 3. For a STABLE tag only, bump the chart first (see below), and merge
#    that commit before you tag. Skip this step for a pre-release.

# 4. Tag and push.
git tag -a v0.1.0 -m "v0.1.0"
git push origin v0.1.0
```

The push triggers `.github/workflows/release.yml`, which:

1. Runs `goreleaser release --clean` against `.goreleaser.yml`.
2. Builds Linux amd64 + arm64 binaries, packages them into the goreleaser-specific Dockerfile (`build/Dockerfile.goreleaser`), and pushes per-arch images plus a multi-arch manifest to `ghcr.io/amagioss/opentams`. Image tags keep the `v` prefix, so a `v0.1.0` git tag publishes `ghcr.io/amagioss/opentams:v0.1.0`, plus the `v0.1` minor track and `latest` (both suppressed for pre-releases). Cross-builds `tamsctl` for linux/darwin on amd64/arm64 and attaches the archives to the release page.
3. Cosign-signs the manifest keyless using the workflow's GitHub OIDC identity (no key management; signature lives in Rekor and as a discoverable cosign tag in the registry).
4. Generates a syft SPDX SBOM for each pushed image and attaches it to the registry as a cosign SPDX attestation.
5. Generates a SLSA v1.0 build-provenance attestation via `actions/attest-build-provenance` and pushes it to the registry.
6. Creates the GitHub Release with a Conventional-Commits-grouped changelog. The server ships **only** as a signed image; the `tamsctl` CLI archives (plus their `.sha256` files) are the only assets attached to the release page.

Verification commands for downstream consumers are documented in the [`Verifying release artefacts`](README.md#verifying-release-artefacts) section of the README.

### Helm chart versions

The chart carries two version fields, and nothing bumps them automatically:

- `version` is the chart's own version. It is bare semver, with no `v` prefix, because that is what chart-releaser and OCI tag inference expect.
- `appVersion` is the image tag the chart pulls. It carries the `v` prefix, so it reads `v0.1.0`.

**Bump `appVersion` on stable tags only.** A pre-release ships as an image, and the chart continues to track the last stable release. If you bump `appVersion` to a pre-release tag, every chart user is moved onto it.

Bump `version` whenever the chart itself changes, even when the server does not. The two numbers are independent and they are expected to drift apart.

### Testing the release pipeline locally

To exercise the full goreleaser flow without pushing anything anywhere:

```bash
make snapshot           # builds binaries + per-arch images, no push, no sign
make release-check      # validates .goreleaser.yml against the goreleaser schema
make release-dry-run    # snapshot + skip publish/sign/announce (closest to a real release)
```

The `Release` workflow also accepts a `workflow_dispatch` trigger that runs the same `goreleaser release --snapshot --clean --skip=publish,sign` against any branch you pick — useful for confirming a config change before you tag.

### Versioning policy

We follow [semver](https://semver.org/), and until the first major release the entire surface is treated as unstable: we may break HTTP responses, configuration variables, or public Go packages on a minor version bump if we have a good reason. Each release's notes call those breaks out explicitly. Patch versions are reserved for backwards-compatible fixes. `v1.0.0` is when we commit to semver's "no breaks within a major" contract, not before.

## Licensing

By contributing, you agree that your contributions will be licensed under the project's [Apache License 2.0](LICENSE). Per Apache 2.0 §5:

> Unless You explicitly state otherwise, any Contribution intentionally submitted for inclusion in the Work by You to the Licensor shall be under the terms and conditions of this License, without any additional terms or conditions.

There is no separate Contributor Licence Agreement (CLA). The Apache 2.0 inbound = outbound clause above is sufficient for what we're doing here.

If your change includes substantial code copied from another project, please flag it in the PR description and confirm it is licensed compatibly with Apache 2.0.

Third-party dependencies and their licences are listed in [docs/third-party-notices.md](docs/third-party-notices.md). Attribution and trademark notices are in [NOTICE](NOTICE).

## AI-Assisted Contributions

AI-assisted contributions are welcome and are held to the same bar as any other contribution. **You are the author of what you submit, whatever produced the first draft** — you must be able to explain every line in review, you must have run the tests, and you must have the right to contribute the code under Apache 2.0.

You are not required to disclose that you used AI assistance.

The full policy is [AI-assisted contributions](GOVERNANCE.md#ai-assisted-contributions) in the governance document; repository-specific conventions for agents are in [AGENTS.md](AGENTS.md).

## Maintainers and Communication

OpenTAMS is maintained by [Amagi Media Labs Ltd.](https://www.amagi.com) and external contributors. The current roster is [MAINTAINERS.md](MAINTAINERS.md); how decisions get made is [GOVERNANCE.md](GOVERNANCE.md). Day-to-day:

- **Discussions, design questions, "is this in scope?"** — open a [GitHub Discussion](https://github.com/amagioss/opentams/discussions) or a regular issue.
- **Bug reports, feature requests** — GitHub issues with the appropriate template.
- **Security findings** — the channels in [SECURITY.md](SECURITY.md), not public issues.
- **Code-of-Conduct concerns** — `conduct@amagi.com` (see [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md)).

We try to respond to issues and PRs within a few business days. If something has gone quiet for more than a week, please ping the issue or PR — that's a signal we missed it, not that we're ignoring you.

Thanks for helping make OpenTAMS better.