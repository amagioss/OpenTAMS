---
status: "accepted"
date: 2026-08-31
decision-makers: OpenTAMS maintainers
consulted: OpenTAMS maintainers
informed: OpenTAMS contributors
---

# Reach media storage through an S3-compatible interface

## Context and Problem Statement

OpenTAMS needs somewhere to put media bytes, and [ADR-0001](0001-cloud-agnostic-substrate.md)
rules out anything a single cloud owns. It also needs presigned URLs, because the server
stays out of the media path — see [ADR-0002](0002-server-outside-the-media-path.md).

Object stores differ, but the S3 API is the one interface almost all of them speak. AWS S3,
Google Cloud Storage, MinIO, Ceph, and most on-premise appliances accept S3 requests.

What does OpenTAMS talk to, and how tightly is it bound?

## Considered Options

* Write against AWS S3 directly, using AWS-only features
* Write against the S3 API, restricted to the operations every compatible store implements
* Define a storage abstraction and implement several backends from the start

## Decision Outcome

Chosen option: "Write against the S3 API, restricted to the operations every compatible
store implements".

`internal/objectstore` declares a `Store` interface and one implementation, built on
`aws-sdk-go-v2`. The endpoint is configurable, so pointing at MinIO or another compatible
store is configuration rather than code. Integration tests run against MinIO, which keeps
the "compatible" claim exercised rather than assumed.

The surface is deliberately small. `Store` covers presigned upload and download URL
generation, a batch delete, and a health check. It does not use S3 lifecycle rules, storage
classes, replication, or event notifications, because compatible stores implement those
inconsistently or not at all.

The interface exists mainly for two reasons: tests need a substitute that does not require
a real store, and the boundary makes the operations OpenTAMS depends on explicit.

A superseded implementation is kept behind the `legacy_objectstore` build tag. It is not
built by default and exists for comparison.

### Consequences

* Good, because operators choose their store. The same binary runs against AWS S3, MinIO,
  or an on-premise appliance.
* Good, because the small surface keeps the compatibility claim credible. Fewer operations
  means fewer ways for a backend to differ.
* Good, because the interface makes the service layer testable without a network.
* Bad, because "S3-compatible" is a spectrum. Presigning and checksum behaviour vary in
  practice, and CI tests one backend, not all of them.
* Bad, because we cannot use lifecycle rules or storage classes to manage cost. That work
  falls to the operator, configured outside OpenTAMS.
* Bad, because the AWS SDK is a large dependency, and its types leak into the internal
  interfaces that wrap it.

## More Information

* Interface and implementation: [`internal/objectstore/objectstore.go`](../../internal/objectstore/objectstore.go).
* Configuration: [`../configuration.md`](../configuration.md).
* Who calls the delete primitive: [ADR-0020](0020-gc-owns-object-lifetime.md).
