#!/usr/bin/env bash
# OpenTAMS hands-on demo orchestrator.
#
# Wraps the tools/ binaries (opentamspub, opentamsedit, opentamsplay,
# opentamsassemble) with friendlier verbs so the five examples can be
# exercised without remembering each binary's flag set.
#
# Binaries are run via `go run` so there is no `bin/` directory to
# manage or gitignore. Go's build cache makes subsequent invocations
# near-instant; first-time compilation adds ~1s per tool.
#
# Usage:
#   ./scripts/demo.sh setup
#   ./scripts/demo.sh file <input> [--copy]
#   ./scripts/demo.sh edit <flow_id> <timerange> [slate-text | --slate <file>]
#   ./scripts/demo.sh live <input>
#   ./scripts/demo.sh play <flow_id> [--live] [--range "<tams-range>"] [--open]
#   ./scripts/demo.sh gateway                  # run opentamsplay in fg
#   ./scripts/demo.sh assemble <flow_id> [--range <tams>] [-o out.ts]
#
# All commands honour OPENTAMS_BASE_URL / OPENTAMS_TOKEN.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BASE_URL="${OPENTAMS_BASE_URL:-http://localhost:8080}"
TOKEN="${OPENTAMS_TOKEN:-dev}"
GATEWAY_PORT="${OPENTAMSPLAY_PORT:-8090}"

H() { printf '\n── %s ──\n' "$*" >&2; }
die() { echo "error: $*" >&2; exit 1; }

# run_tool dispatches `go run ./tools/<name>` from the repo root so the
# package path resolves regardless of the caller's cwd, and threads
# OPENTAMS_* env vars through to the binary.
run_tool() {
  local tool="$1"; shift
  ( cd "$REPO_ROOT" && \
    OPENTAMS_BASE_URL="$BASE_URL" OPENTAMS_TOKEN="$TOKEN" \
    go run "./tools/$tool" "$@" )
}

preflight() {
  H "preflight"
  if ! command -v ffmpeg >/dev/null; then
    die "ffmpeg not in PATH — run 'make install-demo-deps' to install it via your package manager"
  fi
  if ! command -v vlc >/dev/null; then
    echo "warning: vlc not in PATH (playback URLs will print only — run 'make install-demo-deps' to add it)"
  fi
  command -v go >/dev/null || die "go not in PATH — install from https://go.dev/dl/"
  curl -sf -o /dev/null "$BASE_URL/healthz" || die "OpenTAMS not reachable at $BASE_URL — run 'make run' from the repo root (see README Quickstart) or set OPENTAMS_BASE_URL to an existing instance"
  echo "ok: ffmpeg + go + OpenTAMS reachable"
}

cmd_setup() {
  preflight
}

cmd_file() {
  [[ $# -ge 1 ]] || die "usage: $0 file <input> [--copy]"
  local input="$1"; shift
  H "publishing $input"
  run_tool opentamspub file "$input" "$@"
}

cmd_edit() {
  [[ $# -ge 2 ]] || die "usage: $0 edit <from_flow_id> <timerange> [slate-text | --slate <file>]"
  local from="$1"; local rng="$2"; shift 2
  local slate_args=()
  if [[ "${1:-}" == "--slate" ]]; then
    [[ $# -ge 2 ]] || die "--slate needs a file path"
    slate_args=(--slate "$2")
  else
    slate_args=(--slate-text "${1:-BREAK}")
  fi
  H "forking flow $from with slate over $rng"
  run_tool opentamsedit fork --from "$from" --replace "$rng" "${slate_args[@]}"
}

cmd_live() {
  [[ $# -ge 1 ]] || die "usage: $0 live <input>"
  local input="$1"; shift
  H "live-publishing from $input (Ctrl-C to stop)"
  run_tool opentamspub live --input "$input" "$@"
}

cmd_gateway() {
  H "starting HLS gateway on :$GATEWAY_PORT"
  ( cd "$REPO_ROOT" && \
    OPENTAMS_BASE_URL="$BASE_URL" OPENTAMS_TOKEN="$TOKEN" \
    OPENTAMSPLAY_LISTEN=":$GATEWAY_PORT" \
    go run ./tools/opentamsplay )
}

cmd_assemble() {
  [[ $# -ge 1 ]] || die "usage: $0 assemble <flow_id> [--range <tams>] [-o out.ts]"
  H "downloading + assembling segments"
  run_tool opentamsassemble "$@"
}

cmd_play() {
  [[ $# -ge 1 ]] || die "usage: $0 play <flow_id> [--live] [--range <tams>] [--open]"
  local flow="$1"; shift
  local live=""
  local rng=""
  local open=""
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --live)  live="true"; shift ;;
      --range) rng="$2"; shift 2 ;;
      --open)  open="true"; shift ;;
      *) die "unknown play flag: $1" ;;
    esac
  done
  ensure_gateway_running
  local url="http://localhost:$GATEWAY_PORT/play/$flow.m3u8"
  local qs=""
  [[ -n "$live" ]] && qs="${qs}&live=true"
  [[ -n "$rng"  ]] && qs="${qs}&range=$rng"
  qs="${qs#&}"
  [[ -n "$qs" ]] && url="$url?$qs"
  echo "$url"
  if [[ -n "$open" ]]; then
    if command -v vlc >/dev/null; then
      vlc --quiet "$url" >/dev/null 2>&1 &
    else
      echo "warning: --open passed but vlc not in PATH" >&2
    fi
  fi
}

# ensure_gateway_running starts opentamsplay in the background if nothing
# is listening on $GATEWAY_PORT yet. The readiness wait is generous to
# cover the first-run `go build` step under `go run`. Stale instances
# aren't tracked — operator can `pkill -f opentamsplay` if needed.
ensure_gateway_running() {
  if curl -sf -o /dev/null "http://localhost:$GATEWAY_PORT/healthz"; then
    return
  fi
  H "starting HLS gateway in background on :$GATEWAY_PORT"
  ( cd "$REPO_ROOT" && \
    OPENTAMS_BASE_URL="$BASE_URL" OPENTAMS_TOKEN="$TOKEN" \
    OPENTAMSPLAY_LISTEN=":$GATEWAY_PORT" \
    nohup go run ./tools/opentamsplay >/tmp/opentamsplay.log 2>&1 & )
  for _ in {1..60}; do
    if curl -sf -o /dev/null "http://localhost:$GATEWAY_PORT/healthz"; then
      return
    fi
    sleep 0.25
  done
  die "gateway failed to start; see /tmp/opentamsplay.log"
}

main() {
  [[ $# -ge 1 ]] || { cat <<EOF
OpenTAMS demo orchestrator. Subcommands:
  setup                                    Preflight: check ffmpeg/go/stack
  file    <input> [--copy]                 Publish a file end-to-end (--copy: no transcode)
  edit    <from> <range> [slate-text | --slate <file>]
                                           Fork a flow with a break slate
  live    <input>                          Continuous publish (SIGINT to stop)
  gateway                                  Run HLS gateway in foreground
  play    <flow> [--live] [--range <tr>] [--open]
                                           Print the HLS URL (--open: also launch VLC)
  assemble <flow> [--range <tr>] [-o ts]    Download + assemble all segments (passthrough)
EOF
    exit 1
  }
  local verb="$1"; shift
  case "$verb" in
    setup)   cmd_setup "$@" ;;
    file)    cmd_file "$@" ;;
    edit)    cmd_edit "$@" ;;
    live)    cmd_live "$@" ;;
    gateway) cmd_gateway "$@" ;;
    play)    cmd_play "$@" ;;
    assemble) cmd_assemble "$@" ;;
    *)       die "unknown verb: $verb" ;;
  esac
}

main "$@"
