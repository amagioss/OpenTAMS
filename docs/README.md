# OpenTAMS Documentation

## Start here

| Document | What it is |
|---|---|
| [`requirements.md`](requirements.md) | The binding behavioural contract. Every `MUST` should have a test that fails if it is broken. |
| [`conformance.md`](conformance.md) | What OpenTAMS implements from BBC TAMS v8.0, and what it does not. |
| [`configuration.md`](configuration.md) | Every environment variable, its default, and its effect. |
| [`demo.md`](demo.md) | End-to-end walkthrough against a local instance. |
| [`architecture/`](architecture/) | System overview, data flows, metadata model. |

## Design and decisions

| Document | What it is |
|---|---|
| [`adr/`](adr/) | Architecture decision records — why a choice was made, and what was rejected. |
| [`design/`](design/) | Per-package functional design: business rules, logic models, tech-stack decisions. |
| [`implementation-spec.md`](implementation-spec.md) | The original module split and build order. |
| [`tamsctl-design-spec.md`](tamsctl-design-spec.md) | Design for the `tamsctl` CLI. |
| [`helm-chart-generation-spec.md`](helm-chart-generation-spec.md) | Design for the generated Helm chart. |

## Testing and operations

| Document | What it is |
|---|---|
| [`scale-test-plan.md`](scale-test-plan.md) | Load and scale test design. |
| [`scale-test-runbook.md`](scale-test-runbook.md) | How to run a scale test. |
| [`deployment/`](deployment/) | Released images and deployment notes. |
| [`development/`](development/) | Codebase tour for new contributors. |
| [`good-first-issues.md`](good-first-issues.md) | Curated starting points for a first contribution. |
| [`vulnerability-status.md`](vulnerability-status.md) | Current `govulncheck` status, and the upgrade sequence that clears it. |
| [`third-party-notices.md`](third-party-notices.md) | Direct dependencies and their licences. |

## Project and governance

These live at the repository root, not under `docs/`.

| Document | What it is |
|---|---|
| [`CONTRIBUTING.md`](../CONTRIBUTING.md) | How to build, test, and submit a change. |
| [`GOVERNANCE.md`](../GOVERNANCE.md) | Who decides what, how disagreements resolve, trademark and AI-contribution policy. |
| [`MAINTAINERS.md`](../MAINTAINERS.md) | The current maintainer roster. |
| [`SUPPORT.md`](../SUPPORT.md) | Where to ask a question, and what response to expect. |
| [`SECURITY.md`](../SECURITY.md) | How to report a vulnerability. |
| [`CODE_OF_CONDUCT.md`](../CODE_OF_CONDUCT.md) | Expected conduct, and how to report a problem. |
| [`AGENTS.md`](../AGENTS.md) | Conventions for AI coding agents working in this repository. |
| [`NOTICE`](../NOTICE) | Attribution, BBC specification provenance, trademark notice. |

## When documents disagree

Documentation drift is a bug, not a standing condition. Everything published here
is meant to describe OpenTAMS as it actually behaves. If it does not, that is a
defect to report, not a caveat to read around.

**When resolving one, the code is the tie-breaker.** A requirement, a design
document, and a test can each be wrong about what the server does; the server
cannot. So correct the document rather than the reader's expectations, and if the
divergence was deliberate, record it as an [ADR](adr/).

## Module numbers (`M1`–`M20`)

Design and requirements documents refer to packages by a build-order number: `M4` is
`internal/metastore`, `M9` is `internal/service/segment`, `M16` is the unbuilt garbage
collector. The full mapping is the section headings of
[`implementation-spec.md`](implementation-spec.md). They are historical labels from the
original build plan, not a versioning scheme.

## Identifier conventions

| Prefix | Defined in | Meaning |
|---|---|---|
| `REQ-*` | [`requirements.md`](requirements.md) | Requirement |
| `BR-*` | [`design/*/functional-design/`](design/) | Business rule, scoped to one package (`BR-SEG-*`, `BR-META-*`, `BR-HTTP-*`, `BR-CONV-*`, `BR-OBJ-*`) |
| `NFR-*` | [`design/*/nfr-requirements/`](design/) | Non-functional requirement |
| `TC-*`, `SCN-*`, `INV-*` | the test function that carries it | Test label, scenario, or invariant — scoped to one package, no central catalogue |

The last row matters: test files carry labels like `TC-CFG-01`, `TC-AUTH-03`, or
`SCN-SEG-04` in comments. Those are **local to the package** and are defined by the test
they sit above, not by any document. Do not go looking for a catalogue that lists them —
there isn't one, and the same label can mean different things in different packages.

Behaviour the project commits to lives in [`requirements.md`](requirements.md) as `REQ-*`,
and what is and is not implemented in [`conformance.md`](conformance.md). Those are the
documents to reason from; the test labels are navigation aids inside a package.
