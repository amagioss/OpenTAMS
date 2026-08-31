---
status: "accepted"
date: 2026-08-31
decision-makers: OpenTAMS maintainers
consulted: OpenTAMS maintainers
informed: OpenTAMS contributors
---

# Evolve the schema expand-contract, and treat the required version as a minimum

## Context and Problem Statement

A rolling upgrade runs two versions of the binary at once. A canary deploy does the same on
purpose. Both mean the schema and the code are briefly out of step, and the direction of
the mismatch is not always the same.

A migration that drops or renames a column in one step breaks the old binary the moment it
runs. A binary that assumes a new column exists breaks when it starts against an older
schema.

How do schema and code change without a maintenance window?

## Considered Options

* Require an exact schema version, and take downtime for every migration
* Change the schema freely, and rely on deploy ordering
* Expand-contract migrations, with the required version treated as a minimum

## Decision Outcome

Chosen option: "Expand-contract migrations, with the required version treated as a
minimum".

Expand-contract splits a breaking change across releases. First add the new column and
write to both. Then move readers. Then, in a later release, remove the old one. Each step
leaves both the old and the new binary able to run.

`metastore.ExpectedSchemaVersion` states which direction of mismatch is tolerated. It is a
**minimum**, not an equality: `version >= ExpectedSchemaVersion` passes. An older binary
against a newer schema is allowed, which is the rolling-upgrade case. A newer binary
against an older schema is not.

`VerifySchema` implements that check and distinguishes three failures, each naming its
recovery:

* `ErrSchemaNotMigrated` — `schema_migrations` is absent or empty. Run the migration.
* `ErrSchemaDirty` — a previous migration did not finish. The schema shape is unknown.
  Reconcile by hand, then `migrate force`.
* `ErrSchemaTooOld` — the version is below the minimum. Apply pending migrations.

### Consequences

* Good, because a rolling upgrade needs no downtime and no strict ordering between the
  migration and the deploy.
* Good, because the tolerated direction of mismatch is written down as a constant, not
  left to convention.
* Bad, because a breaking change takes three releases. Removing a column is slow, and the
  intermediate state has to be carried and remembered.
* Bad, because `ExpectedSchemaVersion` is a hand-maintained constant. Adding a migration
  whose absence would break a request path means bumping it, and nothing enforces that.
* Bad, because dual writes during the expand phase cost write throughput and can drift if
  one of the two writes is missed.

## More Information

* The check and its sentinel errors: [`internal/metastore/schema.go`](../../internal/metastore/schema.go).
* How migrations are applied: [ADR-0018](0018-migrations-as-an-explicit-step.md).
