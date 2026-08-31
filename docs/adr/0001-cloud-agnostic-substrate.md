---
status: "accepted"
date: 2026-08-31
decision-makers: OpenTAMS maintainers
consulted: OpenTAMS maintainers
informed: OpenTAMS contributors
---

# Target a cloud-agnostic substrate

## Context and Problem Statement

OpenTAMS implements the BBC TAMS v8.0 API. A TAMS server needs three things: a store for
metadata, a store for media objects, and a runtime. Every major cloud sells a managed
product for all three, and using those products directly makes the implementation
shorter. It also ties every operator of OpenTAMS to the cloud we picked.

Which substrate does OpenTAMS target?

## Considered Options

* One cloud, using its managed services directly (DynamoDB, S3, Lambda)
* A cloud-agnostic substrate: PostgreSQL, an S3-compatible object store, and a container
  runtime
* Pluggable backends behind interfaces, with two or more implementations of each from the
  start

## Decision Outcome

Chosen option: "A cloud-agnostic substrate", because all three components have both a
managed offering on every major cloud and a credible self-hosted option. An operator can
run OpenTAMS on AWS, on another cloud, or in their own data centre, and the code does not
change.

The substrate is:

* Metadata in PostgreSQL — see [ADR-0012](0012-postgresql-as-the-metadata-store.md).
* Media in an S3-compatible object store — see
  [ADR-0021](0021-s3-compatible-object-store-interface.md).
* The server as a container image — see [ADR-0033](0033-distroless-static-image.md).

We rejected the third option as premature. The interfaces exist, because
`metastore.FlowStore` and `objectstore.Store` are needed for testing anyway. Building a
second implementation of each before an operator asks for one costs design freedom and a
larger test matrix, and buys nothing today.

### Consequences

* Good, because the deployment target is a decision the operator makes, not one we make
  for them.
* Good, because both dependencies are ordinary, well-understood infrastructure that most
  teams already run.
* Bad, because we cannot use a cloud-native feature that has no portable equivalent. Two
  concrete losses are cross-region replication policy and per-object lifecycle rules,
  which operators must configure outside OpenTAMS.
* Bad, because "cloud-agnostic" is a claim we only partly verify. CI exercises PostgreSQL
  and MinIO. It does not exercise every S3-compatible backend, and
  `deployments/terraform/` ships AWS modules only.

## More Information

* Deployment material for each layer: [`deployments/`](../../deployments/).
* The requirements this satisfies: `REQ-ARCH-*` in [`../requirements.md`](../requirements.md).
