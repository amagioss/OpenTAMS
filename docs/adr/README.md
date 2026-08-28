# Architecture Decision Records

This directory holds OpenTAMS's architecture decision records (ADRs). An ADR captures a
single decision, the alternatives that were rejected, and why — so the reasoning behind
the code is reviewable and survives maintainer turnover.

An ADR is **not** a specification. A spec describes *what to build* and is superseded by
the code once the code exists. An ADR describes *why this option and not that one*, and
stays useful long after the feature ships.

## Index

| ADR | Title | Status |
|---|---|---|
| [0000](0000-use-madr-for-decision-records.md) | Use MADR for architecture decision records | accepted |
| [0001](0001-whole-batch-reject-on-segment-overlap.md) | Reject the whole batch when Flow Segments overlap | accepted |
| [0002](0002-rfc9457-problem-details-for-per-segment-failures.md) | Report per-segment failures as an RFC 9457 Problem Details subset | accepted |
| [0003](0003-vendored-tams-schemas-are-modified.md) | Vendor the TAMS JSON schemas as a modified copy | accepted |

<!-- Add a row per ADR, in number order. Keep the status column in sync with the file's front matter. -->

## Writing a new ADR

1. Copy [`adr-template.md`](adr-template.md) to `NNNN-kebab-case-title.md`, where `NNNN`
   is the next unused number, zero-padded to four digits.
2. Fill in the front matter and the body. Delete optional sections that add nothing —
   a short, honest ADR beats a padded one.
3. Open it as `proposed` in the pull request that implements or precedes the decision,
   so the rationale is reviewed alongside the change.
4. On merge, set the status to `accepted` and add a row to the index above.

## Rules

- **Numbers are never reused or renumbered**, including for rejected ADRs. A gap in the
  sequence is expected, not a defect.
- **Status** is one of `proposed`, `rejected`, `accepted`, `deprecated`, or
  `superseded by ADR-NNNN`.
- **An accepted ADR is immutable in substance.** To change a decision, write a new ADR
  and set the old one's status to `superseded by ADR-NNNN`. Typo and link fixes are fine.
- **An ADR records a decision the code implements.** Write one when a design spec or
  requirement was resolved a particular way and the code now reflects that resolution.
  Intentions, roadmap items, and unimplemented designs do not get an ADR — they belong
  in the requirements document, marked as not implemented.
- **When documents disagree, the code is the tie-breaker.** If an ADR, a requirement, a
  design document, and the code describe different behaviour, the code is what the system
  does. Correct the documents, and if the divergence was deliberate, write the ADR that
  says so.
- **What needs an ADR**: any change to a public package API, the database schema, an HTTP
  response shape, the API contract under `api/`, a documented behavioural rule, or a
  build/release-toolchain choice. Bug fixes, doc updates, and isolated test additions do
  not.

## Format

MADR 4.0.0 minimal, plus a metadata block. See
[ADR-0000](0000-use-madr-for-decision-records.md) for why, and
[adr.github.io/madr](https://adr.github.io/madr/) for the upstream project.
