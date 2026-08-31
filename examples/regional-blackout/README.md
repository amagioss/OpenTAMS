# Regional blackout

A rights deal says one region must not receive six seconds of a
programme. The usual answer is a second package: run the packager again,
write a second set of segment files, ship a second manifest.

TAMS needs neither. A Segment is a reference to an immutable Media
Object, so the regional Flow lists the same `object_id`s as the world
Flow and omits the ones it must not carry.

```
world flow    : [0_6) [6_12) [12_18) [18_24) [24_30)
regional flow : [0_6) [6_12)   ——    [18_24) [24_30)
                                ↑
                    same objects, one reference dropped
```

**Objects created: 0. Bytes written: 0.**

## Why this is stronger than an omitted manifest entry

With HLS you ship a regional manifest that leaves those segments out.
The segment files stay on the CDN. Anyone who guesses the URL can still
fetch them. Omission is a suggestion.

In TAMS a Flow's segment list is the only path to a presigned URL for an
object. No reference, no URL, no access. The example proves this by
asking both Flows the same question:

```
GET /flows/<world>/segments?timerange=[12:0_18:0)
  → object_id=8f2c…  presigned URL returned (1)

GET /flows/<regional>/segments?timerange=[12:0_18:0)
  → [] — no segment, no object_id, no URL to fetch
```

The restricted media is not hidden from the regional client. It is
unreachable by it.

## Run it

Bring up the stack and publish a clip. The publisher prints the Flow ID:

```bash
make run                                              # in another terminal
FLOW_ID=$(./scripts/demo.sh file ~/Movies/clip.mp4 | tail -1)
```

Then derive the regional variant:

```bash
FLOW_ID=$FLOW_ID go run ./examples/regional-blackout
```

The program picks the middle segment as the restricted window. To choose
your own, set `BLACKOUT` to a TAMS timerange:

```bash
FLOW_ID=$FLOW_ID BLACKOUT="[12:0_18:0)" go run ./examples/regional-blackout
```

| Variable | Default | Purpose |
|---|---|---|
| `FLOW_ID` | *required* | The world feed to derive from |
| `BLACKOUT` | middle segment | The restricted window |
| `OPENTAMS_BASE_URL` | `http://localhost:8080` | Server address |
| `OPENTAMS_TOKEN` | `dev` | Local stack only. Production needs a real OIDC token. |

## Two modes

### Proof mode — what this example does

The regional Flow has a hole. The timeline is not re-based, so the
regional clock stays aligned with the world clock and timecodes still
line up. Nothing is rendered and nothing is uploaded.

The proof needs no player. Two `GET` requests show it.

Playback is a different matter. The manifest that `opentamsplay` builds
carries no `#EXT-X-DISCONTINUITY` tag, so the presentation timestamps
jump across the hole. Expect a stutter or a skip, and expect it to
differ between players.

### Polished mode — fill the hole with a slate

To get clean playback, put a slate in the window instead of leaving it
empty. `opentamsedit` renders one that matches the parent's codec,
resolution, frame rate, and audio, and shifts its timestamps to plug
into the parent timeline:

```bash
REGIONAL=$(./scripts/demo.sh edit "$FLOW_ID" "[12:0_18:0)" "NOT AVAILABLE IN YOUR REGION" | tail -1)
./scripts/demo.sh play "$REGIONAL" --open
```

Every chunk outside the window still shares its `object_id` with the
world Flow. Only the slate points at a new object: six seconds of new
media against a programme of any length.

Use a real clip instead of generated text with `--slate <file>`. See
[`docs/demo.md`](../../docs/demo.md) Example 4.

## The one constraint

The restricted window must begin and end on a segment boundary. TAMS
segments reference whole immutable objects, so a window that cuts
through a segment would still hand the regional client the object that
contains the restricted frames.

If the window does not align, the example prints the available
boundaries and exits:

```
error: BLACKOUT window cuts through a segment.
TAMS segments reference whole immutable objects, so the window must
begin and end on one of these boundaries:
  [0:0_6:0)
  [6:0_12:0)
  ...
```

Chunk duration is set at ingest. See the chunking notes in
[`docs/demo.md`](../../docs/demo.md).

## How the objects got there

This example never uploads media. It only references objects that the
publisher already registered. The write path is three calls:

```bash
# 1. Create the Flow (creates its Source implicitly).
curl -X PUT localhost:8080/tams/v1/flows/$FLOW -H 'Authorization: Bearer dev' \
     -H 'Content-Type: application/json' -d @flow.json

# 2. Ask for somewhere to put the bytes. The server returns presigned PUT URLs.
curl -X POST localhost:8080/tams/v1/flows/$FLOW/storage -H 'Authorization: Bearer dev' \
     -H 'Content-Type: application/json' -d '{"limit": 1}'

# 3. PUT the media straight to that URL, then claim a timerange for it.
curl -X PUT "<presigned-url>" --data-binary @chunk.ts
curl -X POST localhost:8080/tams/v1/flows/$FLOW/segments -H 'Authorization: Bearer dev' \
     -H 'X-Idempotency-Key: '$(uuidgen) -H 'Content-Type: application/json' \
     -d '[{"object_id":"<allocated>","timerange":"[0:0_6:0)"}]'
```

The media bytes go from the client to the object store directly. They
never pass through OpenTAMS. For a real ingest that chunks a file with
ffmpeg, see [`tools/opentamspub`](../../tools/opentamspub/) and
[`docs/demo.md`](../../docs/demo.md).

## What to read next

- [`docs/demo.md`](../../docs/demo.md) — five hands-on scenarios: file
  ingest, HLS over an arbitrary range, live-to-VOD, slate variants,
  assemble a flow into one file.
- [`docs/conformance.md`](../../docs/conformance.md) — endpoint status,
  the full timerange grammar, and the pagination cursor format this
  example pages with.
- [`api/opentams-api-bundled.yaml`](../../api/opentams-api-bundled.yaml)
  — the TAMS v8.0 spec OpenTAMS implements. Rendered at
  [amagimedia.github.io/OpenTAMS](https://amagimedia.github.io/OpenTAMS/).
