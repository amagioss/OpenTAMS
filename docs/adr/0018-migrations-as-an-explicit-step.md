---
status: "accepted"
date: 2026-08-31
decision-makers: OpenTAMS maintainers
consulted: OpenTAMS maintainers
informed: OpenTAMS contributors
---

# Apply migrations as an explicit step, never at server startup

## Context and Problem Statement

The schema has to be current before the server can serve. The server can apply pending
migrations itself when it boots, which makes a first deploy a single action. It can also
refuse to do so, and require the operator to run them.

Startup migration is convenient with one replica. With several, every replica tries to
migrate at once, and a schema change becomes a race between processes that are also trying
to accept traffic.

Who applies migrations?

## Considered Options

* The server applies pending migrations at startup
* The server applies them at startup, guarded by an advisory lock
* A separate command, run by the operator or by a deployment job

## Decision Outcome

Chosen option: "A separate command".

`opentams migrate` wraps `golang-migrate` and exposes `up`, `down`, `version`, and `force`.
The SQL files under `migrations/` are embedded in the binary, so the migrating image is the
image being deployed and the two cannot disagree.

`opentams serve` applies nothing. Its help text says so directly: *"Run 'opentams migrate'
before the first start to apply the database schema."*

The Helm chart ships this as a Job that runs `["/opentams", "migrate", "up"]`, separate
from the Deployment.

`force` exists for one case: clearing the `dirty` flag after a partial migration has been
reconciled by hand. It is a recovery tool and changes no schema itself.

### Consequences

* Good, because scaling to N replicas needs no coordination. No two servers race to migrate,
  and no advisory lock is needed.
* Good, because migration is a visible deployment step that can be approved, logged, and
  rolled back on its own.
* Good, because embedding the SQL keeps the migration and the binary in one artefact.
* Good, because a migration failure is a failed Job, not a crash-looping server.
* Bad, because a first deploy is two steps, and forgetting the first produces a server that
  starts and then fails on every query. `VerifySchema` was written to catch exactly this
  and is not wired in — see [ADR-0017](0017-expand-contract-schema-evolution.md).
* Bad, because `migrate down` and `migrate force` are dangerous by nature. Both are exposed
  in the shipped binary, so an operator can reach them by accident.
* Bad, because the Helm Job's ordering relative to the Deployment is the operator's
  responsibility.

## More Information

* Command implementation: [`cmd/opentams/migrate.go`](../../cmd/opentams/migrate.go).
* The `golang-migrate` wrapper: [`pkg/dbmigrate/`](../../pkg/dbmigrate/).
* Deployment job: [`deployments/helm/opentams/templates/migrate-job.yaml`](../../deployments/helm/opentams/templates/migrate-job.yaml).
