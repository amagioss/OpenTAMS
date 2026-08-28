# Governance

This document describes how decisions get made in OpenTAMS: who decides what,
how disagreements resolve, and how those answers themselves change. The current
roster lives in [`MAINTAINERS.md`](MAINTAINERS.md).

OpenTAMS is a young project. This is deliberately a lightweight governance
model, written to be honest about where the project actually is rather than to
describe a foundation-scale structure it does not have.

## Roles

**Contributor** — anyone who opens an issue, comments on one, reviews a pull
request, or sends a change. No prior commitment and no application. Everything
in [`CONTRIBUTING.md`](CONTRIBUTING.md) applies from the first contribution.

**Maintainer** — a contributor with write access, listed in
[`MAINTAINERS.md`](MAINTAINERS.md). Maintainers merge changes, cut releases,
triage security reports, and decide what is in scope for the project.

There is no separate committer tier and no benevolent-dictator role. Where this
document says "the maintainers", it means the people in that file.

## How decisions get made

**Lazy consensus is the default.** A proposal that has been visible for a
reasonable period with no sustained objection is accepted. In practice that
means: open a pull request or an issue, give people time to look, and merge
when someone approves and nobody has objected.

"A reasonable period" scales with the blast radius:

| Change | Review expectation |
|---|---|
| Typo, comment, docs clarification | One maintainer approval. |
| Bug fix, test, internal refactor | One maintainer approval. |
| New feature, new configuration surface | One maintainer approval; give other maintainers 2 business days to weigh in before merging. |
| API contract, database migration, release or supply-chain tooling | Two maintainer approvals. These are the paths pinned in [`CODEOWNERS`](.github/CODEOWNERS). |
| Governance, licensing, security policy, maintainer roster | Two maintainer approvals and 5 business days of visibility. |

**An objection must carry a reason.** A maintainer can block a change, but a
block is an argument, not a veto — it has to say what is wrong and, where
possible, what would resolve it. A block that its author will not explain
does not hold.

**When consensus fails**, the maintainers decide by simple majority of the
current roster. This has not happened yet; if it does, the outcome and the
reasoning get written down in the issue that prompted it.

## Decisions that need an ADR

Architectural decisions are recorded in [`docs/adr/`](docs/adr/) using
[MADR](https://adr.github.io/madr/) 4.0.0. Two rules govern that directory, and
they matter enough to repeat here:

- **An ADR records a decision the code implements.** Intentions and roadmap
  items do not get an ADR.
- **When documents disagree, that is a bug.** A specification, a requirement, and
  a test can all be wrong about what the server does; the server cannot. The code
  is the tie-breaker for deciding *which* document is wrong — it is not a licence
  to leave it wrong. Fix the document in the same change.

Write an ADR when a change settles a question that future contributors would
otherwise re-litigate: a divergence from the TAMS specification, a wire-format
choice, a storage or consistency trade-off. See
[`docs/adr/README.md`](docs/adr/README.md).

## Scope

OpenTAMS is an implementation of the BBC TAMS API specification. That bounds
what belongs here.

**In scope**: conformance with the TAMS specification; the storage, metastore,
and service layers behind it; the operational surface needed to run it
(configuration, observability, deployment manifests, migrations); the
`tamsctl` client.

**Out of scope**: media processing, transcoding, and playout; features that
require diverging from TAMS without a strong reason; vendor-specific
integrations that cannot be expressed through the existing storage and
metastore interfaces.

Divergences from TAMS are sometimes necessary — see
[ADR-0001](docs/adr/0001-whole-batch-reject-on-segment-overlap.md) for one. Each
one needs an ADR that states what breaks for a conformant TAMS client, and each
one is recorded in [`docs/conformance.md`](docs/conformance.md).

## Changing the maintainer roster

### Adding a maintainer

1. An existing maintainer nominates a contributor in a pull request against
   [`MAINTAINERS.md`](MAINTAINERS.md), stating what the nominee has contributed.
2. Lazy consensus over 5 business days, requiring at least two maintainer
   approvals and no unresolved objection.
3. On merge, the new maintainer receives write access and is added to
   [`CODEOWNERS`](.github/CODEOWNERS).

What counts is sustained, high-quality contribution and good judgement in
review — not a commit count. Documentation, triage, and review are
contributions.

### Stepping down

Open a pull request moving yourself to the Emeritus section. No approval
needed; a maintainer may always step down. Access is removed on merge.

### Removing a maintainer

A maintainer may be removed for sustained inactivity (roughly six months with
no contribution or review, and no response to a check-in), or for a
Code-of-Conduct violation serious enough to warrant it. Removal requires a
majority of the remaining maintainers and a pull request that records the
reason. Where the reason is a Code-of-Conduct matter, the record says that a
violation occurred without republishing the details of the report.

## Changing this document

Governance changes follow the governance row of the table above: a pull
request, two maintainer approvals, and 5 business days of visibility. There is
no mechanism for changing governance outside of a public pull request.

## Concentration of maintainership

Every current maintainer is employed by Amagi Media Labs Ltd. We are stating
that plainly rather than implying a broader base than exists. It has practical
consequences a user should weigh:

- Amagi's priorities shape the roadmap more than an outside contributor's.
- A single employer could, today, change direction unilaterally.

What limits that in practice: the Apache 2.0 licence permits forking without
permission; every decision of consequence is made in a public pull request; and
the ADR requirement means divergences must be argued in writing rather than
merged quietly.

The project's intent is to add maintainers from outside Amagi as contribution
history justifies it. Until that happens, this section stays as written.

## Trademarks and endorsement

The Apache License 2.0 grants copyright and patent rights. Section 6 of that
licence grants **no trademark rights**, and this project does not extend any.

- "Amagi" and the Amagi logo are trademarks of Amagi Media Labs Ltd. You may
  fork, modify, and redistribute OpenTAMS freely. You may not name your fork or
  a derived product in a way that suggests it is Amagi's, nor use the Amagi name
  or logo to imply endorsement of it.
- Describing your product as "built on OpenTAMS" or "compatible with OpenTAMS"
  is fine, and is what the trademark carve-out exists to permit.
- **OpenTAMS is an independent implementation.** TAMS is a specification the
  BBC publishes; OpenTAMS implements it. Conformance is self-assessed, and its
  known gaps are documented in [`docs/conformance.md`](docs/conformance.md).

## AI-assisted contributions

AI-assisted contributions are welcome. They are held to exactly the same
standard as any other contribution, and that standard is the point of this
section: **you are the author of what you submit, whatever produced the
first draft.**

Concretely, before you open a pull request containing AI-generated code:

- **Understand it.** You must be able to explain what every line does and why,
  in review. "The model wrote it" is not an answer to a review question.
- **Verify it, don't trust it.** Run the tests. Generated code is confidently
  wrong in ways that read as plausible — invented API signatures, tests that
  assert what the implementation happens to do rather than what the requirement
  says, comments that describe behaviour the code does not have.
- **Check the licensing.** Do not submit code the tool reproduced verbatim from
  an incompatible source. Apache 2.0 inbound=outbound (see
  [Licensing](CONTRIBUTING.md#licensing)) requires that you have the right to
  contribute it.
- **Do not let a tool file issues or reviews on your behalf** unattended.
  Automated bulk reports without a human reading them first waste maintainer
  time, and will be closed.

You are not required to disclose that you used AI assistance. If a change is
large or unusual enough that provenance affects how it should be reviewed, say
so in the pull request description — that helps the reviewer, and it is the
only reason we would ask.

Agent-specific repository conventions are in [`AGENTS.md`](AGENTS.md).

## Code of Conduct

All participation is governed by [`CODE_OF_CONDUCT.md`](CODE_OF_CONDUCT.md).
Enforcement is a maintainer responsibility; reports go to `conduct@amagi.com`.
A maintainer who is the subject of a report recuses themselves from handling it.
