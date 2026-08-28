#!/usr/bin/env bash
# Fail on any govulncheck advisory reachable from OpenTAMS code that is not
# recorded in .govulncheck-allow.yaml.
#
# The plain `govulncheck` exit status is not usable as a gate on its own: it
# returns 3 whenever anything is reachable, so a known-blocked upgrade would
# hold CI red indefinitely and the signal would be ignored — which is how the
# 23 advisories this repository started with went unnoticed. Filtering against
# an explicit, justified accept-list keeps the gate meaningful: a NEW reachable
# advisory fails the build, an accepted one does not.
#
# Advisories in modules we require but never call are reported by govulncheck
# and ignored here. They are a judgement call about dependency hygiene, not a
# gate.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ALLOW_FILE="$ROOT/.govulncheck-allow.yaml"
GOVULNCHECK="${GOVULNCHECK:-golang.org/x/vuln/cmd/govulncheck@v1.1.4}"

cd "$ROOT"

raw="$(mktemp)"
trap 'rm -f "$raw"' EXIT

# -format json streams one JSON object per line. Exit status is ignored here:
# a non-zero status just means findings exist, which is what we are about to
# classify. A genuine tool failure shows up as unparseable output below.
go run "$GOVULNCHECK" -format json ./... >"$raw" 2>/dev/null || true

if [ ! -s "$raw" ]; then
  echo "govulncheck produced no output — treating as a tool failure" >&2
  exit 1
fi

# A finding is reachable from our code when it carries a trace frame with a
# function. Findings without one are import- or module-level only.
reachable="$(
  python3 - "$raw" <<'PYEOF'
import json, sys

# govulncheck -format json emits concatenated pretty-printed objects, not
# one object per line, so decode as a stream rather than line by line.
decoder = json.JSONDecoder()
text = open(sys.argv[1]).read()
pos, ids = 0, set()
while pos < len(text):
    while pos < len(text) and text[pos].isspace():
        pos += 1
    if pos >= len(text):
        break
    obj, pos = decoder.raw_decode(text, pos)
    finding = obj.get("finding")
    if not finding:
        continue
    # A trace frame naming a function means govulncheck traced a call from
    # our code into the vulnerable symbol. Frames carrying only a module are
    # import- or module-level findings.
    if any(frame.get("function") for frame in finding.get("trace") or []):
        ids.add(finding["osv"])
print("\n".join(sorted(ids)))
PYEOF
)"

accepted="$(grep -oE '^[[:space:]]*-[[:space:]]*id:[[:space:]]*GO-[0-9]{4}-[0-9]+' "$ALLOW_FILE" \
  | grep -oE 'GO-[0-9]{4}-[0-9]+' | sort -u)"

unexpected="$(comm -23 <(printf '%s\n' "$reachable" | grep -v '^$' | sort -u) \
                        <(printf '%s\n' "$accepted"  | grep -v '^$' | sort -u))"

# An accepted advisory that is no longer reachable is stale: the upgrade
# landed and the entry should go, so the next real one is not buried.
stale="$(comm -13 <(printf '%s\n' "$reachable"  | grep -v '^$' | sort -u) \
                  <(printf '%s\n' "$accepted"   | grep -v '^$' | sort -u))"

if [ -n "$stale" ]; then
  echo "note: accepted but no longer reachable — remove from $(basename "$ALLOW_FILE"):" >&2
  printf '  %s\n' $stale >&2
fi

if [ -n "$unexpected" ]; then
  echo >&2
  echo "FAIL: reachable advisories not accepted in $(basename "$ALLOW_FILE"):" >&2
  printf '  %s\n' $unexpected >&2
  echo >&2
  echo "Fix the dependency, or add an entry stating why it cannot be fixed yet." >&2
  echo "Full detail: make vuln" >&2
  exit 1
fi

n_accepted="$(printf '%s\n' "$accepted" | grep -c . || true)"
echo "ok: no unaccepted reachable advisories (${n_accepted} accepted, see .govulncheck-allow.yaml)"
