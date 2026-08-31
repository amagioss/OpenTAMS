# Running a released image

This is the no-build path: pull a tagged OpenTAMS container image, verify its
supply-chain attestations, and confirm the server is up. For the build-from-clone
dev flow, see the [Quickstart](../../README.md#quickstart) in the README.

## Pull the image

Requires a tagged release.

```bash
# Latest stable release
docker pull ghcr.io/amagioss/opentams:latest

# Pin to an exact version (recommended for production)
docker pull ghcr.io/amagioss/opentams:v0.1.0
```

Before deploying a release image to production, [verify its
attestations](#verifying-release-artefacts).

## What you get

Once the logs settle on `server listening on :8080`, you have:

| Endpoint | URL |
|---|---|
| TAMS API | http://localhost:8080/tams/v1 |
| Liveness | http://localhost:8080/healthz |
| Readiness | http://localhost:8080/readyz |
| Prometheus | http://localhost:8080/metrics |
| MinIO console | http://localhost:9001 (credentials from `deployments/docker/.env`) |

URLs assume a local run; substitute your deployed host/port otherwise.

## Sanity-check it's up

```bash
# Liveness probe: empty body, HTTP 200 is the success signal.
curl -s -o /dev/null -w "HTTP %{http_code}\n" http://localhost:8080/healthz
# HTTP 200

# Service root: JSON array of the top-level resource names this service exposes.
curl -s -H "Authorization: Bearer dev" http://localhost:8080/tams/v1/ | jq .
# [
#   "service",
#   "flows",
#   "sources",
#   "flow-delete-requests"
# ]

# Service info: API version, type URN, presigned-URL TTL.
curl -s -H "Authorization: Bearer dev" http://localhost:8080/tams/v1/service | jq .
# {
#   "api_version": "8.0",
#   "type": "urn:x-tams:service:tams",
#   "min_object_timeout": "3600:0"
# }
```

For a runnable client walkthrough, see [`examples/`](../../examples/);
for a hands-on tour of five scenarios, see [`docs/demo.md`](../demo.md).

## Verifying release artefacts

Every tagged release of OpenTAMS publishes a multi-arch container image to
`ghcr.io/amagioss/opentams` along with three discoverable supply-chain
attestations: a [cosign](https://github.com/sigstore/cosign) keyless signature
on the manifest, a [syft](https://github.com/anchore/syft)-generated SPDX SBOM,
and a [SLSA v1.0](https://slsa.dev/) build-provenance attestation. All three are
signed with the workflow's GitHub OIDC identity
(`https://token.actions.githubusercontent.com`) — no long-lived signing keys are
stored anywhere.

You can verify any of them before pulling the image into production. The
examples below assume cosign 2.4+ on your `PATH`.

```bash
# 1. Manifest signature: confirms the image came from this repo's release
#    workflow on a tag matching `refs/tags/v*`.
cosign verify \
  --certificate-identity-regexp '^https://github\.com/amagioss/opentams/\.github/workflows/release\.yml@refs/tags/v.*$' \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' \
  ghcr.io/amagioss/opentams:v0.1.0

# 2. SBOM (SPDX): list the software bill of materials.
cosign verify-attestation \
  --type spdxjson \
  --certificate-identity-regexp '^https://github\.com/amagioss/opentams/\.github/workflows/release\.yml@refs/tags/v.*$' \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' \
  ghcr.io/amagioss/opentams:v0.1.0

# 3. SLSA build provenance: confirms which workflow / commit / runner
#    produced the image.
cosign verify-attestation \
  --type slsaprovenance1 \
  --certificate-identity-regexp '^https://github\.com/amagioss/opentams/\.github/workflows/release\.yml@refs/tags/v.*$' \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' \
  ghcr.io/amagioss/opentams:v0.1.0
```

Replace `v0.1.0` with the tag you intend to deploy. All three commands hit the
public Sigstore transparency log
([Rekor](https://docs.sigstore.dev/logging/overview/)) and will fail loudly if
the artefact has been tampered with.

If your environment has policy tooling (Kyverno, Gatekeeper,
sigstore-policy-controller, Connaisseur), the same identity and issuer pair is
what you wire into the admission policy.
