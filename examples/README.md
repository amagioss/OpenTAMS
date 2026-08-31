# OpenTAMS Examples

Two minimal Go programs that exercise OpenTAMS's TAMS v8.0 API end-to-end. Each is a single `main.go` using only `net/http`, `encoding/json`, and the standard library — no generated client, no SDK. The point is to make the wire protocol obvious.

| Example | What it does |
|---|---|
| [`write-segment/`](write-segment/) | Creates a Source → creates a Flow → allocates storage → registers a Segment |
| [`read-segments/`](read-segments/) | Lists segments on a Flow filtered by time-range |

Each program walks one end-to-end TAMS conversation in single-screen Go: same request bodies, response shapes, and idempotency-key discipline you would write against any TAMS v8.0 server.

## Prerequisites

A running OpenTAMS stack listening on `http://localhost:8080`. The simplest path is the [Quickstart in the README](../README.md#quickstart):

```bash
make run
```

The dev configuration accepts `Authorization: Bearer dev` for any caller — there's no real OIDC issuer to satisfy. Production configurations require valid JWTs; see [`docs/configuration.md`](../docs/configuration.md) for the auth knobs.

## Running

```bash
# Write side: source → flow → storage → segment
go run ./examples/write-segment

# Read side: list segments on a flow by time-range
# (the flow ID printed by write-segment is what you pass here)
FLOW_ID=<uuid-from-write-segment> go run ./examples/read-segments
```

Both programs exit non-zero on any error and print structured progress on stderr so they're suitable as the basis for a smoke-test harness, not just learning material.

## What's intentionally NOT in these examples

- **Auth complexity.** The dev token (`Authorization: Bearer dev`) works against the local stack only. In production you'd swap in your OIDC issuer's token-acquisition flow; the examples honour an `OPENTAMS_TOKEN` environment variable so you can drop in a real token without touching the code.
- **Retry / backoff.** A real client wraps every call in exponential-backoff with jitter, especially for `POST /segments` where the idempotency key makes retries safe. We omit it here so the read flow stays single-screen.
- **Pagination.** The read example shows the first page only. OpenTAMS surfaces the next-page cursor through two parallel headers — `X-Paging-NextKey` (an opaque cursor string you pass back as the `key` query parameter) and a non-canonical `Link: <>; rel="next"; key="<cursor>"` (RFC-8288-shaped but with the cursor in `key=` rather than the URI slot). See [`docs/conformance.md#pagination-cursor-format`](../docs/conformance.md#pagination-cursor-format) for the full specification.
- **Generated clients.** `gen/api/opentams.gen.go` is the in-tree oapi-codegen client. For production code, prefer it over hand-rolled `net/http` calls. We avoid it here because it would obscure the wire shape these examples are trying to teach.

## What you should look at next

- **[`api/opentams-api-bundled.yaml`](../api/opentams-api-bundled.yaml)** — the full TAMS v8.0 OpenAPI spec OpenTAMS implements. Render it with Redoc or Swagger UI to browse every endpoint.
- **[`docs/conformance.md`](../docs/conformance.md)** — what's implemented today vs. deferred to a future release.
- **[`docs/architecture/`](../docs/architecture/)** — system diagrams and API flows, including the TAMS-specific decisions that surface in these examples.
- **[`docs/development/codebase.md`](../docs/development/codebase.md)** — implementation boundaries and contributor-oriented reading order.
