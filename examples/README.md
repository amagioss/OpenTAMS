# OpenTAMS Examples

Runnable programs that show what a time-addressable media store does
that a file-based workflow cannot.

OpenTAMS is a server, so every example here is **client** code: a Go
program that speaks the TAMS v8.0 HTTP API. Each one is a single
`main.go` on the standard library alone — no SDK, no generated client —
so the wire protocol stays visible.

| Example | What it shows |
|---|---|
| [`regional-blackout/`](regional-blackout/) | Withhold a time window from one region. Same media on disk, two Flows, **0 bytes written**. |

## Prerequisites

A running stack on `http://localhost:8080`, and a published clip. Both
come from the [Quickstart](../README.md#quickstart):

```bash
make run                                              # in another terminal
FLOW_ID=$(./scripts/demo.sh file ~/Movies/clip.mp4 | tail -1)
```

The dev configuration accepts `Authorization: Bearer dev` from any
caller, because there is no OIDC issuer to satisfy locally. Production
requires a real JWT. Every example reads `OPENTAMS_TOKEN`, so a real
token drops in without a code change. See
[`docs/configuration.md`](../docs/configuration.md).

## Deliberately left out

- **Retry and backoff.** A real client wraps every call in exponential
  backoff with jitter. `POST /segments` carries an idempotency key,
  which is what makes those retries safe.
- **Typed clients.** `gen/api/opentams.gen.go` holds the generated
  server stubs and the request/response models. It is not a client —
  `.oapi-codegen.yaml` does not generate one. A Go client library is
  deferred to a later phase, so these examples call the API directly.

## Related

- [`docs/demo.md`](../docs/demo.md) — five hands-on scenarios driven by
  the `tools/` binaries: file ingest, HLS playback over an arbitrary
  range, live-to-VOD, slate variants, and assembling a Flow into one
  playable file.
- [`docs/conformance.md`](../docs/conformance.md) — endpoint-by-endpoint
  status against TAMS v8.0, the timerange grammar, and the pagination
  cursor format.
- [`api/opentams-api-bundled.yaml`](../api/opentams-api-bundled.yaml) —
  the full spec. Rendered at
  [amagioss.github.io/OpenTAMS](https://amagioss.github.io/OpenTAMS/).
