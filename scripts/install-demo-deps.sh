#!/usr/bin/env bash
# Installs the host-side dependencies the OpenTAMS hands-on demo needs:
#   - ffmpeg  (required — the publisher and editor shell out to it)
#   - vlc     (optional — only used by `scripts/demo.sh play --open`)
#
# Detects platform + package manager and prompts before running anything.
# Already-installed tools are skipped so re-running is idempotent.
#
# Usage:
#   ./scripts/install-demo-deps.sh
#   OPENTAMS_INSTALL_DEPS_YES=1 ./scripts/install-demo-deps.sh   # non-interactive
#
# Out of scope: Go and Docker. Both are the project's own dependencies
# (not demo-specific); install those via their canonical channels first.

set -euo pipefail

confirm() {
  [[ "${OPENTAMS_INSTALL_DEPS_YES:-}" == "1" ]] && { echo "→ $*"; return; }
  read -rp "About to run: $*  [y/N] " ans
  [[ "$ans" =~ ^[Yy]$ ]] || { echo "aborted."; exit 1; }
}

install_mac() {
  command -v brew >/dev/null || {
    echo "Homebrew not installed. Install from https://brew.sh, then re-run." >&2
    exit 1
  }
  if ! command -v ffmpeg >/dev/null; then
    confirm "brew install ffmpeg"
    brew install ffmpeg
  fi
  if ! command -v vlc >/dev/null; then
    confirm "brew install --cask vlc"
    brew install --cask vlc
  fi
}

install_linux() {
  local cmd
  if   command -v apt-get >/dev/null; then cmd="sudo apt-get update && sudo apt-get install -y ffmpeg vlc"
  elif command -v dnf     >/dev/null; then cmd="sudo dnf install -y ffmpeg vlc"
  elif command -v pacman  >/dev/null; then cmd="sudo pacman -S --noconfirm ffmpeg vlc"
  elif command -v zypper  >/dev/null; then cmd="sudo zypper install -y ffmpeg vlc"
  else
    echo "No supported package manager found (apt-get / dnf / pacman / zypper)." >&2
    echo "Install ffmpeg + vlc via your distro's tooling and re-run." >&2
    exit 1
  fi
  # Skip if both already present.
  if command -v ffmpeg >/dev/null && command -v vlc >/dev/null; then
    return
  fi
  confirm "$cmd"
  eval "$cmd"
}

case "$(uname -s)" in
  Darwin) install_mac ;;
  Linux)  install_linux ;;
  *)
    echo "Unsupported OS: $(uname -s). Install ffmpeg + vlc manually." >&2
    exit 1
    ;;
esac

echo
echo "✓ demo dependencies present:"
echo "    ffmpeg: $(command -v ffmpeg || echo 'NOT FOUND')"
echo "    vlc:    $(command -v vlc    || echo 'NOT FOUND (optional)')"
