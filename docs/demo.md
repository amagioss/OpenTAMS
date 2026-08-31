# OpenTAMS Hands-On Demo

Walks five product-level TAMS examples against a local OpenTAMS stack.
Uses the four demo binaries under `tools/` (`opentamspub`,
`opentamsedit`, `opentamsplay`, `opentamsassemble`) driven by
`scripts/demo.sh`.

OpenTAMS is a metadata plane: it stores time-addressed pointers to
S3-hosted objects, not the media itself. Clients PUT/GET media directly
to/from S3 via presigned URLs the server hands out. This demo makes
that value visible — you can see two Flows share the same underlying
chunks except for one swapped chunk, and you can play either Flow in
VLC over an HLS gateway that translates segment listings into a
manifest of presigned URLs.

## Prerequisites

- The OpenTAMS stack running locally — easiest via the README
[Quickstart](../README.md#quickstart):
  ```bash
  make run
  ```
  `make run` bootstraps `deployments/docker/.env` from the template on
  first use, brings up PostgreSQL + MinIO in Docker Compose
  (`make stack`), applies migrations, then starts the OpenTAMS server
  on the host. Leave it running in another terminal. Any reachable
  OpenTAMS instance works — set `OPENTAMS_BASE_URL` and
  `OPENTAMS_TOKEN` to point the demo elsewhere.
- `ffmpeg` in PATH.
- `vlc` in PATH (optional; only needed for the `--open` flag on `play`,
otherwise the demo just prints URLs).
- A short test clip on disk — Big Buck Bunny, a phone recording,
anything decodable by ffmpeg. The publisher transcodes to a fixed
H.264 + AAC + MPEG-TS profile so essence parameters stay predictable.

On a fresh machine, `make install-demo-deps` installs both ffmpeg and
vlc via the platform's package manager (Homebrew on macOS;
apt/dnf/pacman/zypper on Linux). It prompts before each install and
skips anything already present.

```bash
./scripts/demo.sh setup
```

Runs a preflight check: verifies `ffmpeg`, `go`, and a reachable
OpenTAMS, and warns if VLC isn't installed. No binaries are built —
the script runs the demo tools via `go run` on demand.

## Conventions used below

- Flow IDs are printed on stdout; examples capture them into `FLOW_A`,
  `FLOW_B`, etc. via `$( … | tail -1 )`. In Example 3 (multi-terminal),
  paste the UUID directly or re-export the variable in each terminal.
- Example outputs assume 30-second clips with 6 s chunks (~5 segments
  per flow). Pick `--replace` ranges from the boundary list the
  publisher prints at the end of `file`.

## A note on chunking

The publisher has two ingest modes, and the chunk-boundary behaviour
differs between them:


| Mode                | What ffmpeg does                                                                                                                 | Chunk durations                                              |
| ------------------- | -------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------ |
| default (transcode) | Re-encodes to H.264 + AAC + MPEG-TS at a fixed profile and inserts an IDR at every multiple of `--chunk` via `-force_key_frames` | Exactly `--chunk` seconds (last one is the remainder)        |
| `--copy`            | Passes input streams through with `-c copy`, no re-encode                                                                        | At least `--chunk`, snapped forward to the next *source* IDR |


The default mode **re-encodes the input** for one reason: the HLS muxer
can only cut at IDR frames, and arbitrary inputs don't necessarily have
IDRs at the cadence we'd like. Forcing IDRs requires an encoder, and an
encoder requires a decode → re-encode pass. The trade-off is essence
fidelity (a generation of compression loss + downscale to the fixed
profile) in exchange for uniform, predictable chunk boundaries that
make ranges like `--replace [12:0_18:0)` line up on a chunk edge.

`--copy` skips the re-encode entirely — the bytes that land in MinIO
are exactly the bytes you uploaded — but chunk durations are then
dictated by wherever the source already has keyframes. The publisher
prints the actual boundaries after publish; pass one of those to
`opentamsedit --replace`.

For a real ingest pipeline you'd normally configure the upstream encoder
to write IDRs at the cadence you want, and then `--copy` gives you both
byte-exact uploads *and* uniform chunks.

## Example 1 — File ingest with mid-timeline slate swap

The headline demo. Publish a clip, fork it with a slate replacing a
6-second window in the middle, play both side-by-side in VLC. Both
Flows reference the same object IDs for every chunk *except* the
overlaid one — a copy-free fork.

```bash
# 1. Publish the clip. Prints the flow id on stdout.
FLOW_A=$(./scripts/demo.sh file ~/Movies/bbb-30s.mp4 | tail -1)

# 2. Fork with a "BREAK" slate over the 12s–18s window. Prints the
#    new flow id on stdout. Flow A is never mutated.
FLOW_B=$(./scripts/demo.sh edit "$FLOW_A" "[12:0_18:0)" "BREAK" | tail -1)

# 3. Print the HLS URLs (add --open to also launch VLC).
./scripts/demo.sh play "$FLOW_A" --open
./scripts/demo.sh play "$FLOW_B" --open
```

Without `--open` the script only prints the URL; paste it into VLC,
`ffplay`, or `curl` manually. The default is print-only so the script
stays well-behaved in CI and other headless contexts.

What to look for:

- `GET /tams/v1/flows/$FLOW_A/segments?timerange=_` returns ~5 segments
(30 s / 6 s each).
- `GET /tams/v1/flows/$FLOW_B/segments?timerange=_` also returns ~5
segments — but the `object_id` for the `[12_18)` chunk differs from
the rest, which share IDs with Flow A.
- In VLC, both timelines look identical except 12–18 s.

## Example 2 — HLS playback over an arbitrary range

The gateway accepts a `range=` query in raw TAMS bracket notation, so
you can scrub to any window in a published flow without re-encoding.

```bash
./scripts/demo.sh play "$FLOW_A" --range "[6:0_24:0)" --open
```

The gateway pages through `GET /flows/{id}/segments?timerange=[6:0_24:0)`
and emits an HLS manifest pointing at the presigned URLs TAMS returned —
VLC fetches each chunk directly from MinIO.

## Example 3 — Near-live publish with parallel consumer (live-to-VOD)

Three terminals. The fork in terminal 3 is non-destructive — it creates
a brand-new Flow B from a snapshot of Flow A's segments; Flow A keeps
growing untouched.

### Terminal 1 — continuous publish

Use an input long enough for the live behaviour to be observable — a
multi-minute clip, an RTMP stream, a capture device (`/dev/video0`).
A short file is fully consumed in seconds and live mode exits before
terminals 2 and 3 have anything to do.

```bash
./scripts/demo.sh live ~/Movies/long-clip.mp4   # or rtmp://…, /dev/video0
```

The publisher prints the flow id on stdout, then keeps ingesting until
Ctrl-C. Copy that UUID for terminals 2 and 3.

Live mode pre-allocates a batch of presigned URLs via one bulk
`POST /flows/{id}/storage`, then refills the pool when it drops below
a quarter. The per-chunk hot path never blocks on a storage round-trip.

### Terminal 2 — follow the growing manifest

```bash
./scripts/demo.sh play <flow-id-from-terminal-1> --live --open
```

VLC reloads the manifest every `target_duration / 2`; on each reload
the gateway re-queries TAMS, so new chunks appear within a poll
interval of when they were registered.

### Terminal 3 (optional) — fork the live flow at a past window

```bash
FLOW_LIVE=<flow-id-from-terminal-1>
FLOW_FORK=$(./scripts/demo.sh edit "$FLOW_LIVE" "[24:0_30:0)" "BREAK" | tail -1)
./scripts/demo.sh play "$FLOW_FORK" --open
```

The live flow keeps growing in terminal 1; the forked flow is frozen
at the moment you ran `edit`, except for the slate-overlaid window.

## Example 4 — BBC-style ad slate / blackout / reframe

Same mechanic as Example 1's `edit`, but Example 4 is about the *kind* of slate.
The two blocks below are alternative invocations — run whichever fits.

### Variant A — text slate (generated)

A solid-black clip with text drawn at the parent's resolution and
codec. No external file needed. This is what Example 1 also uses by default.

```bash
FLOW_B=$(./scripts/demo.sh edit "$FLOW_A" "[12:0_18:0)" "AD BREAK" | tail -1)
./scripts/demo.sh play "$FLOW_B" --open
```

### Variant B — slate from a clip file

A pre-existing clip on disk, re-encoded once to match the parent's
essence (codec, resolution, framerate, audio profile). Useful for
regional feeds, station idents, or a real ad creative.

```bash
FLOW_B=$(./scripts/demo.sh edit "$FLOW_A" "[12:0_18:0)" --slate ~/Movies/regional-clip.mp4 | tail -1)
./scripts/demo.sh play "$FLOW_B" --open
```

Both variants produce the same shape of Flow B: every chunk outside
`[12:0_18:0)` shares its `object_id` with Flow A, only the slate
window points at a new object. This is the BBC "swap one chunk for
a slate in-place at a chunk boundary" pattern — useful for ad-break
replacement, regional blackout, and on-air mistake recovery.

## Example 5 — Assemble a flow into a single playable file

`assemble` walks a flow's segments end-to-end, downloads the bytes for
each one, and concatenates them to stdout or `-o <file>`. For MPEG-TS
flows the output is a complete, decoder-ready stream — no transcode,
no manifest, no player-side logic. Useful for offline review, ffmpeg
post-processing, or sanity-checking what a forked flow actually
contains without launching the HLS gateway.

```bash
# Full flow → file
./scripts/demo.sh assemble "$FLOW_A" -o /tmp/flow-a.ts

# Just one window
./scripts/demo.sh assemble "$FLOW_B" --range "[12:0_18:0)" -o /tmp/slate.ts

# Stream to ffmpeg for a quick re-mux to MP4
./scripts/demo.sh assemble "$FLOW_A" | ffmpeg -i - -c copy /tmp/flow-a.mp4
```

What to look for:

- For a fork (Example 1), `diff <(assemble $FLOW_A)` against `<(assemble $FLOW_B)`
shows the segments diverge only within the slate-replaced window.
- Live flows (Example 3): `assemble` returns the bytes available at request
time. Re-run after waiting to see the growth.

## Cleanup

Stop the gateway (started in the background by `play`):

```bash
pkill -f opentamsplay || true
```

`-f` is needed because the gateway runs under `go run ./tools/opentamsplay`,
so the process name contains the package path rather than just the
binary name.

Stop the server (Ctrl-C in the `make run` terminal) and tear down the
storage stack:

```bash
make stack-down
```

`make stack-down` runs `docker compose down -v`, dropping the
PostgreSQL and MinIO volumes for a clean slate. Use plain
`docker compose -f deployments/docker/docker-compose.yml down` (no
`-v`) if you want to keep the data between runs.

## Source code


| What                               | Where                            |
| ---------------------------------- | -------------------------------- |
| Publisher (file + live)            | `tools/opentamspub/`             |
| Flow-fork editor                   | `tools/opentamsedit/`            |
| HLS gateway                        | `tools/opentamsplay/`            |
| Flow assembler (download + concat) | `tools/opentamsassemble/`        |
| Shared client + timerange helpers  | `tools/internal/opentamsclient/` |
| Orchestrator script                | `scripts/demo.sh`                |


The binaries are intentionally thin — the publisher is ~250 lines, the
gateway under 200. Read them next to `examples/write-segment/main.go`
and `examples/read-segments/main.go` to see how the bare-wire teaching
examples scale up into something demonstrable.