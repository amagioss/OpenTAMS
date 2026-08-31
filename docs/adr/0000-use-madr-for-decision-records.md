---
status: "accepted"
date: 2026-08-25
decision-makers: OpenTAMS maintainers
consulted: OpenTAMS maintainers
informed: OpenTAMS contributors
---

# Use MADR for architecture decision records

## Context and Problem Statement

OpenTAMS has accumulated a substantial number of design decisions — the metastore
technology, the error contract, the segment-overlap semantics, the code-generation
toolchain, the TAMS specification version to target — but the reasoning behind them
lives in working notes rather than in the repository. A contributor reading the code
can see *what* was decided and cannot see *why*, or which alternatives were rejected
and on what grounds. Decisions therefore get re-litigated, or silently reversed by a
change whose author did not know a constraint existed.

How should OpenTAMS record decisions so that the reasoning travels with the code and
survives maintainer turnover?

## Considered Options

* [MADR](https://adr.github.io/madr/) 4.0.0
* [Nygard-style ADRs](https://cognitect.com/blog/2011/11/15/documenting-architecture-decisions) (title, status, context, decision, consequences)
* [Y-Statements](https://medium.com/olzzio/y-statements-10eb07b5a177)
* A single running `DECISIONS.md` changelog
* No formal record; rely on commit messages, PR discussion, and code comments

## Decision Outcome

Chosen option: "MADR 4.0.0", because it is the most widely adopted structured ADR
template and — unlike the Nygard format — it requires **Considered Options** and
per-option trade-offs as first-class sections. Recording the rejected alternatives is
the part that pays off years later, and it is exactly the part that is lost today.

Conventions for this repository:

* ADRs live in `docs/adr/` as `NNNN-kebab-case-title.md`, zero-padded to four digits.
* Numbers are assigned in order and are **never** reused or renumbered, including for
  rejected ADRs. A gap in the sequence is not a defect. The founding set `0000`-`0037`
  is the one exception: it was written as a single change before the first public
  release and numbered by design layer, so the sequence reads from the substrate
  outward. Nothing outside the repository linked to those numbers at the time. From
  `0038` onward, numbers are chronological and this rule holds without exception.
* `status` is one of `proposed`, `rejected`, `accepted`, `deprecated`, or
  `superseded by ADR-NNNN`.
* An accepted ADR is immutable in substance. To change a decision, write a new ADR and
  set the old one's status to `superseded by ADR-NNNN`. Typo and link fixes are fine.
* Start from [`adr-template.md`](adr-template.md) (the MADR 4.0.0 *minimal* template
  plus the metadata block).
* Anything that changes a public package API, the database schema, an HTTP response
  shape, the API contract in `api/`, or a documented behavioural rule needs an ADR
  before it is merged.

### Consequences

* Good, because the rationale ships with the code and is reviewable in the same pull
  request as the change it justifies.
* Good, because rejected options are written down, so a future contributor proposing a
  rejected approach finds the reasoning instead of rediscovering it.
* Good, because MADR is a recognised format — contributors who have used ADRs elsewhere
  need no onboarding.
* Bad, because MADR is more ceremony than the Nygard five-section format, and a thin
  decision can end up padded to fill the template. Sections are optional; delete the
  ones that add nothing rather than writing filler.
* Bad, because ADRs go stale if writing them is treated as after-the-fact paperwork.
  The pull-request template links the ADR requirement to keep authoring close to the
  decision.

## More Information

* [MADR project](https://github.com/adr/madr) and [template directory](https://github.com/adr/madr/tree/develop/template)
* [adr.github.io](https://adr.github.io/) — umbrella site and template comparison
* Michael Nygard, [*Documenting Architecture Decisions*](https://cognitect.com/blog/2011/11/15/documenting-architecture-decisions) (2011) — the origin of the practice
