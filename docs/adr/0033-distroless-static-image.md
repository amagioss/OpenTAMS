---
status: "accepted"
date: 2026-08-31
decision-makers: OpenTAMS maintainers
consulted: OpenTAMS maintainers
informed: OpenTAMS contributors
---

# Ship a distroless static image, running as a non-root user

## Context and Problem Statement

The server is deployed as a container. The base image decides most of the image's
vulnerability surface, because the Go binary is one static file and everything else in the
image is inherited.

A general-purpose base such as Debian or Alpine brings a shell, a package manager, and
dozens of libraries. None of them is used by a static Go binary, and every one of them is
something a scanner reports and an operator must patch.

Which base image, and which user?

## Considered Options

* A general-purpose base such as `debian:slim` or `alpine`
* `scratch`
* `gcr.io/distroless/static`

## Decision Outcome

Chosen option: "`gcr.io/distroless/static-debian12:nonroot`".

The binary is built with `CGO_ENABLED=0` and `-ldflags="-s -w"`, so it needs no libc and no
dynamic loader. Distroless static supplies the few things `scratch` lacks and a networked
service does need: CA certificates for TLS verification, `/etc/passwd` for the non-root
user, and timezone data.

The image declares `USER nonroot:nonroot` explicitly. The `:nonroot` tag already defaults
to uid 65532, and stating it means a base-image change cannot silently re-root the
container, and an image scanner can see the intent rather than infer it.

There are two Dockerfiles on purpose, and both use the same base:

* `build/Dockerfile` cross-compiles with buildx, using `$BUILDPLATFORM`, `$TARGETOS`, and
  `$TARGETARCH`. CI uses it to verify a pull request builds for every target.
* `build/Dockerfile.goreleaser` receives an already-built binary from goreleaser and never
  invokes the Go toolchain. Release builds use it, and it is much faster than emulating
  another architecture.

### Consequences

* Good, because there is no shell in the image. An attacker with code execution has no
  interactive tooling, and no package manager to fetch any.
* Good, because the vulnerability surface is roughly the Go binary. Base-image findings
  are close to zero, so a scanner report is about our code.
* Good, because running as uid 65532 satisfies a restricted pod security standard with no
  extra configuration.
* Good, because the image is small, so pulls are fast and a cold start is short.
* Bad, because you cannot `exec` into a running container to debug it. Diagnosis has to
  work through logs, metrics, and an ephemeral debug container.
* Bad, because two Dockerfiles can drift. They share a base and a user today, and only
  review keeps them aligned.
* Bad, because the base is a Google-hosted image, which is a supply-chain dependency
  outside our control.

## More Information

* CI build: [`build/Dockerfile`](../../build/Dockerfile).
* Release build: [`build/Dockerfile.goreleaser`](../../build/Dockerfile.goreleaser).
* What is published: [ADR-0034](0034-image-only-release-with-attestation.md).
