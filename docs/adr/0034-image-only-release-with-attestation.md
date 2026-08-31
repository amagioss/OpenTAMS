---
status: "accepted"
date: 2026-08-31
decision-makers: OpenTAMS maintainers
consulted: OpenTAMS maintainers
informed: OpenTAMS contributors
---

# Release the server as a signed image only, and `tamsctl` as archives

## Context and Problem Statement

A release can publish raw binaries, archives, container images, or all three. Every
published artefact is one a consumer might run, and one whose provenance we then have to
let them verify.

The server and the CLI are used differently. The server runs in Kubernetes, where the unit
of deployment is an image. `tamsctl` runs on a laptop, where a downloaded archive is the
normal thing.

What is published, and how is it verified?

## Considered Options

* Publish everything: binaries, archives, and images, for both programs
* Publish images only, for both
* Publish the server as an image only, and the CLI as archives

## Decision Outcome

Chosen option: "Publish the server as an image only, and the CLI as archives".

`.goreleaser.yml` sets `release.ids` to `tamsctl-archive` alone. The server binary is built
and put into an image, and it is never attached to the release page. The image is
`ghcr.io/amagioss/opentams`, tagged with the version, with `major.minor`, and with `latest`
for stable releases. Per-architecture images are built for amd64 and arm64 and joined into
one manifest.

Three independent attestations back the image:

* A **cosign signature** on the manifest, keyless, using the workflow's OIDC identity. No
  private key is stored anywhere.
* An **SBOM**, produced by syft and attached to the registry as a cosign attestation, so
  the component inventory travels with the image.
* **SLSA build provenance**, from `actions/attest-build-provenance`, recording which
  workflow at which commit produced the artefact.

The signing tools are pinned. cosign is fixed at `v2.4.1`, because an unpinned upgrade can
change attestation format in ways a consumer's verification does not expect.

The release job runs under a GitHub environment named `release`, and the workflow's default
permission is `contents: read`. The job widens that to what it needs and no more:
`contents`, `packages`, `id-token`, and `attestations`.

### Consequences

* Good, because the server has one distribution channel, so there is one artefact to sign,
  scan, and verify.
* Good, because keyless signing removes the private key, and with it key storage,
  rotation, and compromise.
* Good, because a consumer can verify the image came from this repository's workflow at a
  known commit, which is what provenance is for.
* Good, because CLI users get an ordinary archive and do not need a container runtime.
* Bad, because anyone who wants the server outside a container has to build it. There is no
  supported binary.
* Bad, because verification depends on Sigstore's public infrastructure. Verifying offline
  or air-gapped needs extra work.
* Bad, because three attestation mechanisms is three things that can break independently,
  and each has its own verification command for a consumer to learn.

## More Information

* Release configuration: [`.goreleaser.yml`](../../.goreleaser.yml).
* Workflow and its permissions: [`.github/workflows/release.yml`](../../.github/workflows/release.yml).
* Verification commands: the "Verifying release artefacts" section of [`../../README.md`](../../README.md).
