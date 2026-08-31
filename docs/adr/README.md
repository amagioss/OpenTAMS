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
| [0001](0001-cloud-agnostic-substrate.md) | Target a cloud-agnostic substrate | accepted |
| [0002](0002-server-outside-the-media-path.md) | Keep the server out of the media path | accepted |
| [0003](0003-pkg-holds-context-agnostic-libraries.md) | Reserve `pkg/` for context-agnostic libraries | accepted |
| [0004](0004-single-go-module.md) | Ship the server, the CLI, and the tools as one Go module | accepted |
| [0005](0005-vendored-tams-schemas-are-modified.md) | Vendor the TAMS JSON schemas as a modified copy | accepted |
| [0006](0006-openapi-303-source-spec-with-committed-bundle.md) | Author the contract at OpenAPI 3.0.3 and commit the bundled document | accepted |
| [0007](0007-strict-server-code-generation.md) | Generate the server from the OpenAPI contract in strict-server mode | accepted |
| [0008](0008-commit-generated-code.md) | Commit the generated code and pin the generator through `go.mod` | accepted |
| [0009](0009-union-marshalling-bridge.md) | Restore union marshalling with a generated bridge file | accepted |
| [0010](0010-spec-driven-request-validation.md) | Validate requests from the embedded spec with kin-openapi | accepted |
| [0011](0011-health-and-metrics-outside-codegen.md) | Keep `/healthz`, `/readyz`, and `/metrics` in the spec but out of codegen | accepted |
| [0012](0012-postgresql-as-the-metadata-store.md) | Use PostgreSQL as the metadata store | accepted |
| [0013](0013-pgx-without-database-sql.md) | Use pgx directly rather than through `database/sql` | accepted |
| [0014](0014-timerange-stored-as-string-and-bounds.md) | Store a timerange as its wire string plus normalised integer bounds | accepted |
| [0015](0015-gist-exclusion-constraint.md) | Enforce Segment non-overlap with a GiST exclusion constraint | accepted |
| [0016](0016-keyset-pagination.md) | Paginate with opaque keyset cursors, never with `OFFSET` | accepted |
| [0017](0017-expand-contract-schema-evolution.md) | Evolve the schema expand-contract, and treat the required version as a minimum | accepted |
| [0018](0018-migrations-as-an-explicit-step.md) | Apply migrations as an explicit step, never at server startup | accepted |
| [0019](0019-lazy-object-registration.md) | Register an Object lazily, when the first Segment references it | accepted |
| [0020](0020-gc-owns-object-lifetime.md) | Give the garbage collector sole ownership of Object lifetime | accepted |
| [0021](0021-s3-compatible-object-store-interface.md) | Reach media storage through an S3-compatible interface | accepted |
| [0022](0022-rfc9457-as-the-single-error-contract.md) | Serve every error as an RFC 9457 Problem Details document | accepted |
| [0023](0023-rfc9457-problem-details-for-per-segment-failures.md) | Report per-segment failures as an RFC 9457 Problem Details subset | accepted |
| [0024](0024-whole-batch-reject-on-segment-overlap.md) | Reject the whole batch when Flow Segments overlap | accepted |
| [0025](0025-idempotency-keys-in-postgres.md) | Require an idempotency key on Segment registration, backed by PostgreSQL | accepted |
| [0026](0026-get-urls-projected-in-the-handler.md) | Project `get_urls` in the handler | accepted |
| [0027](0027-two-type-families.md) | Keep wire types and domain types apart, with one adapter between them | accepted |
| [0028](0028-gin-with-split-route-groups.md) | Use Gin, and split routing into a public group and an authenticated group | accepted |
| [0029](0029-authentication-behind-a-provider-interface.md) | Put authentication behind a `Provider` interface | accepted |
| [0030](0030-configuration-from-the-environment-only.md) | Configure from the environment only, and validate everything at startup | accepted |
| [0031](0031-liveness-separate-from-readiness.md) | Separate liveness from readiness | accepted |
| [0032](0032-zap-and-a-private-metrics-registry.md) | Log with zap, and hold metrics in a private registry | accepted |
| [0033](0033-distroless-static-image.md) | Ship a distroless static image, running as a non-root user | accepted |
| [0034](0034-image-only-release-with-attestation.md) | Release the server as a signed image only, and `tamsctl` as archives | accepted |
| [0035](0035-ci-on-hosted-runners-with-pinned-actions.md) | Run CI on GitHub-hosted runners, with every action pinned by commit SHA | accepted |
| [0036](0036-rate-limiting-is-the-operators-responsibility.md) | Leave rate limiting to the operator | accepted |
| [0037](0037-object-garbage-collection.md) | Reclaim Object storage from a separate worker | accepted |

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
  sequence is expected, not a defect. The founding set `0000`-`0037` is the one
  exception — see [ADR-0000](0000-use-madr-for-decision-records.md).
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
