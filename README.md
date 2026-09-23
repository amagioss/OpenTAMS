# OpenTAMS

[![Test](https://github.com/amagioss/OpenTAMS/actions/workflows/test.yml/badge.svg?branch=main)](https://github.com/amagioss/OpenTAMS/actions/workflows/test.yml)
[![Lint](https://github.com/amagioss/OpenTAMS/actions/workflows/lint.yml/badge.svg?branch=main)](https://github.com/amagioss/OpenTAMS/actions/workflows/lint.yml)
[![Build](https://github.com/amagioss/OpenTAMS/actions/workflows/build.yml/badge.svg?branch=main)](https://github.com/amagioss/OpenTAMS/actions/workflows/build.yml)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/amagioss/opentams/badge)](https://scorecard.dev/viewer/?uri=github.com/amagioss/opentams)
[![License: Apache 2.0](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)
[![Go Version](https://img.shields.io/badge/go-1.26-00ADD8?logo=go)](go.mod)
[![TAMS Spec](https://img.shields.io/badge/TAMS-v8.0-5C2D91)](https://bbc.github.io/tams/main/index.html)

> **Cloud-agnostic, open-source implementation of the BBC Time-Addressable Media Store (TAMS) v8.0 API.**

> Specification source: [bbc/tams](https://github.com/bbc/tams) ([rendered docs](https://bbc.github.io/tams/main/index.html)).

OpenTAMS lets you address media essence by time. You upload chunks of audio, video, or data to an object store, register them against a flow with a precise time-range, and query them back the same way — `give me everything between 10:30:15.500 and 10:30:18.000` — without ever touching the underlying objects directly. A TAMS v8.0 client works against OpenTAMS for every surface OpenTAMS implements; see [`docs/conformance.md`](docs/conformance.md) for the endpoint-by-endpoint status, including the surfaces that are not implemented.

It is built for media pipelines that already think in terms of time: live ingest, archive, contribution, time-shifted playout, frame-accurate retrieval.

---

## Why OpenTAMS

- **Spec-faithful**. Implements the [BBC TAMS v8.0 API](https://bbc.github.io/tams/main/index.html) as specified, not a re-interpretation. Spec compliance is the primary product requirement.
- **Cloud-agnostic**. PostgreSQL for metadata, any S3-compatible object store for media (AWS S3, GCS, Azure Blob via S3 API, MinIO, Ceph). No managed-service lock-in.
- **Operationally honest**. Health probes, structured JSON logs, Prometheus metrics, request IDs, idempotency keys, schema startup probe, expand-contract migrations, graceful shutdown — production patterns from day one, not retrofitted later.
- **Apache 2.0**. Use it, fork it, ship it inside commercial products. No copyleft, no contributor-license-agreement gymnastics.

---

## Architecture

```
                  ┌──────────────────────┐
   client ──────► │       OpenTAMS       │
   (TAMS v8.0)    │       HTTP API       │
                  │ (gin + oapi-codegen) │
                  └──────────────────────┘
                   │                  │
                   │ metadata         │ presigned URLs
                   ▼                  ▼
            ┌──────────────┐  ┌────────────────┐
            │  PostgreSQL  │  │   S3-compat    │
            │  (metastore) │  │ (object store) │
            └──────────────┘  └────────────────┘
```

OpenTAMS keeps three concerns separate:

- **Metadata** — sources, flows, segments, idempotency keys; PostgreSQL today.
- **Media bytes** — stored in an S3-compatible object store and accessed by clients through presigned URLs.
- **Caller identity** — validated through external and optional internal OIDC issuers.

The API server coordinates metadata, auth, and signed object-store access. Media bytes do not flow through OpenTAMS.

For system diagrams, storage ownership, and API flows, see [`docs/architecture/`](docs/architecture/). For package boundaries and contributor-oriented implementation guidance, see [`docs/development/codebase.md`](docs/development/codebase.md).

---

## Status

**No release has been cut yet.** Breaking API and configuration changes are possible until the first stable release.

The core API is implemented: Sources/Flows/Segments CRUD, storage allocation, idempotent segment registration, auth (external + optional internal OIDC), health probes, metrics/structured logs, and schema migrations. Webhooks/event streaming, MXF container mapping, and a client library are deferred to a later phase (clients poll and use the OpenAPI spec directly for now).

Two things an operator will expect and not find: **garbage collection** (`opentams gc`
exits with an error, so orphaned objects accumulate) and **rate limiting** (no limiter,
no 429 — put one in front of the service).

For the full TAMS v8.0 endpoint-by-endpoint status — what's implemented today, what's deferred, and what's intentionally out of scope — see [`docs/conformance.md`](docs/conformance.md).

---

## Quickstart

Two `make` targets bring the dev stack up: `make stack` provisions the storage dependencies (PostgreSQL + MinIO) in Docker Compose, and `make run` applies migrations and starts the OpenTAMS server on the host. `make run` depends on `make stack`, so a fresh shell only needs one command.

**Prerequisites**: Docker 24+ with the `compose` plugin, Go (toolchain matching `go.mod`), `curl`, and a UUID generator (`uuidgen` on macOS/Linux, or any equivalent).

```bash
git clone https://github.com/amagioss/opentams.git
cd opentams

make run
```

`make run` bootstraps `deployments/docker/.env` from the template on first use (existing edits are preserved), brings up Postgres + MinIO via Docker Compose, applies migrations, and starts the server.

The server runs on the host (not in Docker) so its presigned object-storage URLs use `localhost:9000` — reachable both from the server and from any client on the same machine. See [`scripts/start.sh`](scripts/start.sh) for the exact env-var setup `make run` performs.

Once the logs settle on `server listening on :8080`, the API is at
`http://localhost:8080/tams/v1` (health at `/healthz`, metrics at `/metrics`,
MinIO console at `http://localhost:9001`). To stop: Ctrl-C the `make run`
terminal, then `make stack-down` to drop the storage volumes.

**Next steps:**

- **Running a released image** (no build) + supply-chain verification: [`docs/deployment/released-images.md`](docs/deployment/released-images.md).
- **Endpoint reference + first requests** (sanity-check curls): [`docs/deployment/released-images.md`](docs/deployment/released-images.md).
- **Hands-on tour** (slate swap, HLS, live-to-VOD, and more): [`docs/demo.md`](docs/demo.md).

---

## Configuration

OpenTAMS is configured exclusively through environment variables — no config
files. The full reference (every variable, defaults, dev-vs-prod differences,
validation rules, and cross-variable constraints) is at
[`docs/configuration.md`](docs/configuration.md). `opentams serve --help` lists
every flag and the env-var precedence rules.

---

## API

The TAMS v8.0 OpenAPI spec, bundled into a single self-contained YAML, lives at [`api/opentams-api-bundled.yaml`](api/opentams-api-bundled.yaml).

Browse the rendered reference (Redoc, auto-deployed from `main`):

- **[https://amagioss.github.io/OpenTAMS/](https://amagioss.github.io/OpenTAMS/)**

You can also render it locally with [Redoc](https://github.com/Redocly/redoc) or [Swagger UI](https://github.com/swagger-api/swagger-ui), or import it into Postman / Bruno / `httpie`.

For the wider TAMS conceptual model — flows, sources, segments, time-ranges, the difference between a flow and an essence — read the upstream BBC documentation: [https://bbc.github.io/tams/main/index.html](https://bbc.github.io/tams/main/index.html). OpenTAMS does not re-explain TAMS concepts; it implements them.

---

## Examples

Runnable programs that show what a time-addressable media store does that a file-based workflow cannot:

- [`examples/regional-blackout/`](examples/regional-blackout/) — withhold a time window from one region. Same media on disk, two Flows, **0 bytes written**.

A rights deal says one region must not receive six seconds of a programme. The usual answer is a second package: run the packager again, write a second set of segment files, ship a second manifest. In TAMS a Segment is a reference to an immutable Media Object, so the regional Flow lists the same `object_id`s and omits the ones it must not carry. Because a Flow's segment list is the only path to a presigned URL, the restricted media is not hidden from the regional client — it is unreachable by it.

Each example is a single `main.go` using only the Go standard library — no generated client, no SDK — so the wire protocol is obvious. See [`examples/README.md`](examples/README.md) for prerequisites and what is deliberately left out.

---

## CLI (`tamsctl`)

`tamsctl` is a `kubectl`-style command-line client for the TAMS API — manage flows, segments, storage allocation, and sources against local, hosted, or cloud TAMS instances without hand-rolling `curl`.

`tamsctl` talks to a running TAMS server — it does not start one. Point it at any reachable instance; for a local one, bring up the stack first with [`make run`](#quickstart).

### Install

Simplest, with a Go toolchain — builds straight onto your PATH, no clone needed:

```bash
go install github.com/amagioss/opentams/cmd/tamsctl@latest
# lands in "$(go env GOPATH)/bin" — make sure that's on your PATH
```

From a clone, build then install onto your PATH so you can run `tamsctl` from anywhere (build as your user, install separately — the install step only copies):

```bash
make build-cli                                    # builds dist/tamsctl
sudo make install-cli                             # copies into /usr/local/bin
# or user-local, no sudo (ensure the dir is on your PATH):
make install-cli INSTALL_DIR="$HOME/.local/bin"
```

Verify:

```bash
tamsctl version
```

### Configure and use

`tamsctl` keeps named contexts (endpoint + token) in `~/.tamsctl/config`. The endpoint is the full API root **including the deployment's version prefix** (`/tams/v1` for OpenTAMS; another implementation may differ).

```bash
tamsctl config set-context local --endpoint http://localhost:8080/tams/v1 --token "$TOKEN"
tamsctl config use-context local
# per-call overrides: --context / --endpoint / --token, or env TAMSCTL_ENDPOINT / TAMSCTL_TOKEN
```

### Walkthrough: write then read media by time

The media lifecycle is **create flow → allocate storage → upload the bytes to the
returned URL → register the segment → read back by time-range**. `tamsctl` drives
every metadata step; the byte upload goes straight to the object store via the
presigned URL, so media never passes through OpenTAMS.

```bash
# 1. Create a flow (and mint its source). Ids are canonical lowercase UUIDs.
FLOW_ID=$(uuidgen | tr '[:upper:]' '[:lower:]')
tamsctl flow create --id "$FLOW_ID" --new-source \
  --codec video/h264 --frame-width 1920 --frame-height 1080 --frame-rate 25

# 2. Allocate storage: each slot is one object id + a presigned PUT URL.
ALLOC=$(tamsctl storage create --flow-id "$FLOW_ID" --limit 1 -o json)
OBJECT_ID=$(echo "$ALLOC" | jq -r '.media_objects[0].object_id')
PUT_URL=$(echo "$ALLOC"  | jq -r '.media_objects[0].put_url.url')

# 3. Upload the media bytes externally, straight to object storage.
#    Content-Type must match the flow codec (echoed in put_url.content-type).
curl -X PUT -H "Content-Type: video/h264" --upload-file ./chunk.ts "$PUT_URL"

# 4. Register the uploaded object as a segment on the flow's timeline.
tamsctl segment register --flow-id "$FLOW_ID" --object-id "$OBJECT_ID" --timerange "[0:0_10:0)"

# 5. Read it back by time-range. Each segment carries its get_urls (download
#    URLs) by default; add --presigned to keep only presigned ones, or --all
#    to follow pagination across many segments.
tamsctl segment get --flow-id "$FLOW_ID" --timerange "[0:0_10:0)"
```

Other handy commands: `tamsctl flow get --id <uuid>` (flow metadata),
`tamsctl source get --label cam1` (find sources), and `-o json|yaml|table` on any
command. Full command tree and design notes: [`docs/tamsctl-design-spec.md`](docs/tamsctl-design-spec.md).

---

## Development

OpenTAMS is plain Go 1.26+ with no codegen build steps required at run-time (codegen is committed to the repo).

```bash
go test ./...                       # full suite, including testcontainer-backed integration tests
go test -race ./...                 # race detector
go vet ./...
go build -o opentams ./cmd/opentams # local server binary
make build                          # both binaries (opentams + tamsctl) → dist/, version-stamped
```

Integration tests spin up real PostgreSQL containers via [testcontainers-go](https://golang.testcontainers.org/) — a working Docker daemon is required for `go test` to pass.

A multi-stage, distroless, multi-arch Dockerfile is at [`build/Dockerfile`](build/Dockerfile):

```bash
docker buildx build \
  --platform linux/amd64,linux/arm64 \
  -f build/Dockerfile \
  -t opentams:dev .
```

---

## Deployment

- **Docker Compose** — [`deployments/docker/docker-compose.yml`](deployments/docker/docker-compose.yml) (used by the quickstart above; suitable for local evaluation, not production).
- **Helm chart** — [`deployments/helm/opentams/`](deployments/helm/opentams/) packages the server for Kubernetes (templates, `values.yaml` + `values.schema.json`, examples). See [`deployments/helm/opentams/README.md`](deployments/helm/opentams/README.md).
- **Kubernetes (raw manifests)** — [`deployments/kubernetes/`](deployments/kubernetes/) covers Deployment, Service, ConfigMap, Secret, and a one-shot migration Job. See [`deployments/kubernetes/README.md`](deployments/kubernetes/README.md).
- **Terraform (IaC)** — [`deployments/terraform/`](deployments/terraform/) provisions cloud infrastructure: a runnable AWS root config in [`aws/`](deployments/terraform/aws/) and reusable modules in [`modules/`](deployments/terraform/modules/). See [`deployments/terraform/README.md`](deployments/terraform/README.md).

The Kubernetes manifests cover the common case. A broader production deployment guide — TLS termination patterns, secret-management wiring, observability stack (Grafana dashboards / Prometheus alert rules), rate-limit sizing, pool tuning, multi-region considerations — is tracked as a follow-up; if you need any of those topics in your evaluation, please open an issue and we'll prioritise.

---

## Documentation


| Audience                                   | Time   | Read                                                                                                                                |
| ------------------------------------------ | ------ | ----------------------------------------------------------------------------------------------------------------------------------- |
| Scanner — *is this relevant?*              | 30 s   | This README                                                                                                                         |
| Evaluator — *does it run?*                 | 5 min  | [Quickstart](#quickstart) above, then [`examples/`](examples/) for the API in Go                                                    |
| Evaluator — *what does it do hands-on?*    | 15 min | [`docs/demo.md`](docs/demo.md) — five examples: slate swap, HLS playback, near-live (live-to-VOD), BBC-style fork, download-and-cat |
| Evaluator — *what's actually implemented?* | 10 min | [`docs/conformance.md`](docs/conformance.md) — TAMS v8.0 surface, endpoint by endpoint                                              |
| Operator — *drive the API from the shell?* | 5 min  | [CLI (`tamsctl`)](#cli-tamsctl) above; full reference in [`docs/tamsctl-design-spec.md`](docs/tamsctl-design-spec.md)               |
| Operator — *run a released image?*         | 5 min  | [`docs/deployment/released-images.md`](docs/deployment/released-images.md) — pull, verify attestations, sanity-check                |
| Operator — *how do I deploy?*              | 15 min | [`deployments/`](deployments/) and [`docs/configuration.md`](docs/configuration.md) for the full env-var surface                    |
| Contributor — *how do I extend it?*        | 30 min | [`docs/development/codebase.md`](docs/development/codebase.md), then [`CONTRIBUTING.md`](CONTRIBUTING.md)                           |
| Maintainer — *how do I cut a release?*     | 5 min  | The "Releasing" section of [`CONTRIBUTING.md`](CONTRIBUTING.md)                                                                     |


---

## Contributing

Code contributions are accepted only from Amagi Media Labs Limited personnel. We do not currently accept pull requests or code contributions from outside the organisation. Bug reports and feature requests are welcome from anyone through GitHub issues.

Amagi engineers should start with [`CONTRIBUTING.md`](CONTRIBUTING.md) — it covers the one-command local setup, build/test commands, commit conventions, and the PR review process.

- **Bugs**: [open a bug report](https://github.com/amagioss/opentams/issues/new?template=bug_report.yml).
- **Features**: [open a feature request](https://github.com/amagioss/opentams/issues/new?template=feature_request.yml). The project tracks every behavioural decision against the requirements spec, so the issue is where a proposal gets discussed.
- **Security issues**: please **do not** open a public issue. See [`SECURITY.md`](SECURITY.md) for the private reporting channels (GitHub Security Advisories preferred; email fallback to `security@amagi.com`).
- **Code-of-Conduct concerns**: see [`CODE_OF_CONDUCT.md`](CODE_OF_CONDUCT.md). Reports go to `akshay.narayan@amagi.com`.

You are welcome to fork this repository to add features, address issues, or adapt the implementation to your own requirements, under the [Apache 2.0 license](LICENSE). Amagi trademarks are not licensed with it — see the Trademarks section of [`NOTICE`](NOTICE).

---

## Acknowledgements

OpenTAMS exists because the BBC published TAMS as an open specification under
Apache 2.0, together with the design discussion and ADRs behind it. We are
grateful for that work. OpenTAMS is an independent implementation — see
[`NOTICE`](NOTICE).

---

## License

Copyright © 2026 Amagi Media Labs Limited.

Licensed under the Apache License, Version 2.0. See [`LICENSE`](LICENSE) for the full text.
