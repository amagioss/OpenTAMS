#!/usr/bin/env bash
# Start the OpenTAMS dev server on the host against the storage stack
# brought up by `make stack` (postgres + minio in docker compose).
#
# Runs migrations once, then execs `opentams serve` via `go run`. The
# server is intentionally on the host (not in compose) so its presigned
# URLs use `localhost:9000` — reachable from both this process and from
# any demo client running on the same host.
#
# Usage:
#   ./scripts/start.sh
#
# Or via the Makefile (which also brings up the stack first):
#   make run

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

# Pull secrets/config from the compose .env file so the host run sees the
# same DB_PASSWORD / OBJECT_STORE_ACCESS_KEY_ID etc. as the compose
# stack was started with. The .env file is the same source of truth.
ENV_FILE="deployments/docker/.env"
if [[ ! -f "$ENV_FILE" ]]; then
  echo "error: $ENV_FILE not found." >&2
  echo "       Run 'make env' (or 'make run') from the repo root — it" >&2
  echo "       bootstraps the file from .env.example automatically." >&2
  exit 1
fi
set -a
# shellcheck disable=SC1090
source "$ENV_FILE"
set +a

# Host-context overrides. The compose file set these for the in-container
# server view (DB at `postgres`, MinIO at `minio`, no TLS); on the host
# we reach both via published ports on localhost. APP_ENV=development
# enables the `Bearer dev` token used by the demo and examples/.
export APP_ENV=${APP_ENV:-development}
export DB_HOST=${DB_HOST:-localhost}
export DB_PORT=${DB_PORT:-5432}
export DB_SSLMODE=${DB_SSLMODE:-disable}
export OBJECT_STORE_ENDPOINT=${OBJECT_STORE_ENDPOINT:-http://localhost:9000}

echo "==> Applying migrations"
go run ./cmd/opentams migrate up

echo "==> Starting opentams serve on http://localhost:${SERVER_PORT:-8080}"
exec go run ./cmd/opentams serve
