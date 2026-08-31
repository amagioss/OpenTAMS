# Working on OpenTAMS with an AI coding agent

Conventions for AI coding agents (Claude Code, Cursor, Copilot, Aider, Codex,
and similar) working in this repository.

Nothing here replaces [`CONTRIBUTING.md`](CONTRIBUTING.md) — a pull request from
an agent is reviewed against exactly the same bar as any other. The policy for
AI-assisted contribution, including who is accountable for the result, is
[AI-assisted contributions](GOVERNANCE.md#ai-assisted-contributions) in the
governance document. The short form: **the human who opens the pull request is
the author, and must be able to explain every line in review.**

## Ground rules

**Do not edit generated code.** `gen/api/` is produced from the OpenAPI
contract. Change `api/opentams-api-v1.yaml` or a file under `api/schemas/`, then
regenerate. An edit made directly to `gen/api/` is silently reverted by the next
`make api-gen`.

**Do not hand-edit `api/opentams-api-bundled.yaml`.** It is the bundler's
output. See the API workflow below — the regeneration chain has an ordering
trap in it.

**The repository is held at zero lint findings.** `make lint` runs a strict
`.golangci.yml`. A change that adds a finding does not merge. Run it before
proposing a change, not after review asks.

**Tests come before implementation.** New behaviour arrives with a test that
fails for the right reason first. See
[Tests required for behavioural changes](CONTRIBUTING.md#tests-required-for-behavioural-changes).

**When documents disagree, the code is the tie-breaker.** The requirements, the
test plan, the design documents under `docs/design/`, and the OpenAPI spec are
each capable of being wrong about what the server does. When you find such a
conflict, do not silently pick one — say which document is stale, and fix the
document in the same change if the code is right.

## Before you propose a change

```bash
make build        # go build ./...
make vet          # go vet ./...
make lint         # golangci-lint, strict — must report zero issues
make test         # unit tests, no infrastructure required
```

Integration tests need Docker (testcontainers brings up real Postgres and S3):

```bash
make integration
```

Full command list: `make help`.

## The API contract workflow

This is where an agent is most likely to break the build, because the chain has
an ordering constraint that is not obvious from the Makefile.

The chain is:

```
api/opentams-api-v1.yaml  +  api/schemas/*.json      (source of truth — edit here)
        │  make api-bundle
        ▼
api/opentams-api-bundled.yaml                        (generated, committed)
        │  make api-gen
        ▼
gen/api/*.gen.go                                     (generated, committed)
        │
        ▼
internal/httpx/…                                     (hand-written; must compile against it)
```

`make api-check` verifies that the committed bundle and generated code match the
source spec.

**It currently fails, and the failure is known.** The source spec is ahead of the
committed bundle in two places. Running `make api-bundle` on its own leaves
`gen/` stale; running `make api-bundle && make api-gen` without also updating
`internal/httpx/conversion/segment.go` **breaks a build that is otherwise
green**. Read [`api/schemas/README.md`](api/schemas/README.md) and
[ADR-0023](docs/adr/0023-rfc9457-problem-details-for-per-segment-failures.md)
before touching any of it. `api-check` is deliberately not wired into `make ci`
for this reason.

If your change is unrelated to the API contract, leave the bundle and `gen/`
alone.

## Repository map

| Path | Contains |
|---|---|
| `cmd/` | Entrypoints: `opentams` (server, migrations, GC), `tamsctl` (CLI) |
| `internal/httpx/` | HTTP layer — handlers, middleware, request/response conversion |
| `internal/domain/` | Domain types and service interfaces |
| `internal/metastore/` | Postgres persistence |
| `internal/store/` | S3-compatible object storage |
| `pkg/` | Packages intended for external consumption |
| `api/` | OpenAPI contract and vendored TAMS JSON Schemas |
| `gen/` | Generated code — never edited by hand |
| `migrations/` | Database migrations — additive, never rewritten once released |
| `docs/adr/` | Architectural decision records |

## Writing an ADR

If your change settles a question that would otherwise be re-litigated — a
divergence from the TAMS specification, a wire-format choice, a consistency
trade-off — record it in `docs/adr/` using MADR 4.0.0.

Two rules, from [`docs/adr/README.md`](docs/adr/README.md):

- **An ADR records a decision the code implements.** An intention is not an ADR.
  If the code does not do it yet, the status is `proposed`, and the ADR says
  plainly what the code does instead.
- **ADR numbers are never reused**, including for abandoned ADRs.

## Things that will get a change rejected

- Lint findings, or a lint rule disabled to avoid one, without justification in
  the pull request.
- Behavioural change with no test, or a test written to assert what the
  implementation happens to do rather than what the requirement says.
- Hand-edits to `gen/` or to `api/opentams-api-bundled.yaml`.
- A rewritten migration that has already shipped in a release.
- Invented documentation: a link to a document that does not exist, a claim
  about a flag or endpoint that was never verified against the code, an ADR for
  something unimplemented and presented as accepted. Verify before you assert —
  this repository's documents are checked against the code, and fabricated
  references are the single most common failure mode of generated changes here.
- Bulk automated issues, reviews, or pull requests filed without a human reading
  them first.

## Scope of this file

This file is the project's contribution guidance for agents, and it is the only
agent-facing file in the public repository. If your checkout contains other
tool-local configuration, that configuration does not override anything here or
in [`CONTRIBUTING.md`](CONTRIBUTING.md).
