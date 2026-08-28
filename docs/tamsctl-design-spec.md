# tamsctl — operator CLI for OpenTAMS

## Goal

A `kubectl`-style command-line client for the TAMS v1 API so operators can drive
flows, segments, storage allocation, and source queries against any endpoint
(local dev, hosted, AWS) without hand-rolling `curl`.

## Decisions

- **Packaging**: separate binary `cmd/tamsctl`, same module
(`github.com/amagioss/opentams`). Reusable logic in `internal/tamsctl/`.
- **Config**: kubectl-style named contexts in `~/.tamsctl/config` (YAML, mode
`0600`).
- **Output**: pretty JSON by default; `-o table` for list outputs.
- **No new dependencies**: `spf13/cobra` (already direct) + `gopkg.in/yaml.v3`.
- **No generated client** exists; follow the hand-rolled thin-client pattern
from `tools/scaletest/loadgen/client.go`.

## Config file

```yaml
current-context: local
contexts:
  local:  { endpoint: http://localhost:8080/tams/v1, token: <jwt> }
  hosted: { endpoint: https://tams.hosted.internal/tams/v1, token: <jwt> }
  aws:    { endpoint: https://tams.aws.example/api/v1, token: <jwt> }
```

The endpoint is the **full API root including the deployment's version
prefix**. The prefix is deployment-specific — our implementation mounts at
`/tams/v1`, but another TAMS deployment may use `/api/v1` or anything else — so
it is part of the configured endpoint, not hardcoded. The client appends only
the spec-relative resource paths (`/flows`, `/sources`, `/service`,
`/flow-delete-requests`).

Resolution per invocation:

- endpoint: `--endpoint` flag → `TAMSCTL_ENDPOINT` env → context endpoint
- token:    `--token` flag → `TAMSCTL_TOKEN` env → context token
- context:  `--context` flag → `current-context`

File written `0600`; `~/.tamsctl/` created `0700`.

## Packages

```
internal/tamsctl/
  config.go        Load/Save/Resolve(context, env overrides); File struct
  config_test.go
  token.go         ParseExpiry(jwt) (time.Time, ok); IsExpired(jwt, now)
  token_test.go
  client.go        Client{base, token, hc}; flow/segment/storage/source methods
  client_test.go   httptest-driven
cmd/tamsctl/
  main.go root.go config_cmd.go flow.go segment.go storage.go source.go
```

## Token expiry

Decode the JWT payload (middle segment, base64url, no signature verification)
and read `exp`. Before any authed call, if expired, fail fast:

```
token for context "aws" expired at 2026-06-15T10:00:00Z;
run 'tamsctl config set-token --context aws' or set TAMSCTL_TOKEN
```

A server `401` maps to the same hint (covers tokens with no/!exp claim).

## Command tree

```
tamsctl config set-context <name> --endpoint <url> [--token <jwt>]
tamsctl config use-context <name>
tamsctl config set-token [--context <name>]      # flag/env/stdin
tamsctl config current-context
tamsctl config view                              # tokens redacted

tamsctl flow create   [--id] (--source-id | --new-source) --codec --format --frame-width --frame-height (--frame-rate N[/D] | --vfr)
tamsctl flow update   --id [--tag... --label --read-only --avg-bit-rate --flow-collection file]
tamsctl flow delete   --id
tamsctl flow get      --id

tamsctl segment register --flow-id --object-id --timerange [--ts-offset --object-timerange
                          --last-duration --key-frame-count --sample-offset --sample-count --get-url...]
tamsctl segment delete   --flow-id [--timerange] [--object-id]
tamsctl segment get      --flow-id [--timerange --object-id --reverse-order --verbose-storage
                          --accept-get-urls --accept-storage-ids --presigned
                          --include-object-timerange --limit --page-token]

tamsctl storage create --flow-id (--limit N | --object-id a --object-id b ...)  # exactly one

tamsctl source get [--label --format --tag name=value... --tags-exist a,b --limit --page-token --all]

tamsctl version                                  # plain text; -o json|yaml for structured
```

The command tree above is grouped by resource for reference. The typical media
lifecycle threads through it in this order:

1. `flow create` — define the flow (and its source).
2. `storage create` — allocate object ids + presigned PUT URLs for the bytes.
3. *(external)* `PUT` the media to each `put_url` — uploads go straight to the
   object store, not through OpenTAMS; set `Content-Type` to the flow codec.
4. `segment register` — bind each uploaded `object_id` to a timerange on the flow.
5. `segment get --timerange` — read the flow's media back by time (add
   `--presigned --accept-get-urls https` for download URLs).

`version` prints build metadata (`version`/`commit`/`date` injected via
`-ldflags` by `make build-cli`, defaulting to `dev`/`none`/`unknown` for plain
`go build`) plus Go version and platform. It reads as text by default and emits
a structured object only when `-o json`/`-o yaml` is explicitly passed.

`storage create` enforces exactly-one-of `--limit`/`--object-id` before any
network call.

Flag-naming convention: repeatable `StringArray` flags (one value per
occurrence, no comma splitting) are **singular** — `--object-id`, `--tag`,
`--get-url`. Comma-separated `StringSlice` flags are **plural** — `--tags-exist`.

## API mapping


Paths below are spec-relative (appended to the configured endpoint, which
already carries the version prefix).

| command                 | method | path (relative to endpoint)  |
| ----------------------- | ------ | ---------------------------- |
| flow create/update      | PUT    | /flows/{id}                  |
| flow delete             | DELETE | /flows/{id}                  |
| flow get                | GET    | /flows/{id}                  |
| segment register        | POST   | /flows/{flowId}/segments     |
| segment delete          | DELETE | /flows/{flowId}/segments     |
| segment get             | GET    | /flows/{flowId}/segments     |
| storage create          | POST   | /flows/{flowId}/storage      |
| source get              | GET    | /sources                     |


`flow create` builds the minimal video-flow payload (`id`, `source_id`,
`format`, `codec`, `essence_parameters.frame_width/height`, and a frame rate)
by hand rather than constructing the heavy generated `Flow` `oneOf` union. A
video flow requires a frame rate: pass `--frame-rate N[/D]` (fixed →
`essence_parameters.frame_rate={numerator,denominator}`) or `--vfr` (variable →
`essence_parameters.vfr=true`); the two are mutually exclusive and one is
required.

## Internally-generated headers

These are never user input — the client sets them:

- `X-Request-ID`: a fresh UUID on **every** request, for log correlation.
The server echoes it and includes it in error bodies.
- `X-Idempotency-Key`: a fresh UUID on **POST /segments only** (the API
requires it; missing key → 400). One CLI invocation is one logical write,
so a new key per call is correct; GETs/DELETEs/PUTs carry none.

Paging (`page`/`limit`) stays as user input — the `page` cursor comes from a
prior response's `X-Paging-NextKey`.

## Pagination

List commands (`segment get`, `source get`) always capture the
`X-Paging-NextKey` response header (`client.Page{Items, NextToken}`).

- **Output**: `-o json` (default) and `-o yaml` emit a `{items, nextPageToken}`
envelope on stdout (`nextPageToken` omitted when absent). `-o table` prints
rows on stdout and, when more pages exist, a next-page hint + ready-to-run
command on **stderr**.
- `--quiet`: suppress the stderr hint (table mode).
- `--all`: follow cursors and concatenate every page into one result
(envelope's `nextPageToken` ends empty); the suggested next-command drops
`--all`/old `--page-token`.
- `--max-pages` (default 10000): safety cap for `--all`. A non-advancing
cursor (server bug / hostile endpoint) is rejected rather than looped on;
exceeding the cap errors with the cursor to resume from. `<=0` = unlimited.

`-o` is validated up front (`json`/`yaml`/`table` only). `config set-context`
preserves an existing token when `--token` is omitted and clears it when
`--token ""` is passed explicitly.

## Id handling

- `flow create --id` is optional: a UUIDv4 is minted when omitted. The source
  is either an explicit `--source-id` or `--new-source` (mints one); the two are
  mutually exclusive and one is required. Generated ids are printed to stderr
  (suppressed by `--quiet`).
- UUID-shaped flags (`--id`, `--source-id`, `--flow-id`) are validated locally
  before any request, so a malformed id fails fast instead of round-tripping a
  server 400. The check uses the server's **exact** pattern
  (`api/schemas/uuid.json`: canonical lowercase RFC-9562, version 1-5) rather
  than `google/uuid`'s lenient `Parse` — so the client rejects precisely what
  the server would. Generated ids (`uuid.NewString()`, v4) always satisfy it.
  `--object-id` is intentionally **not** validated — TAMS object ids are
  free-form strings, not UUIDs.

## Testing

- config: round-trip save/load, env-override precedence, perms, missing file.
- token: valid exp, expired exp, malformed/no-exp.
- client: httptest server asserts method/path/body/headers; 401→hint;
expiry→fail before request.
- cobra: storage exactly-one-of validation; output formatting.

