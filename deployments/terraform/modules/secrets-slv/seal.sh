#!/usr/bin/env bash
# Seal the given secret map into an SLV CR and apply it to the cluster.
# Inputs (env): SLV_PUBLIC_KEY, SECRET_NAME, NAMESPACE, SECRET_JSON, KUBECONFIG, KUBE_CONTEXT
set -euo pipefail

for bin in slv kubectl jq; do
  command -v "$bin" >/dev/null || { echo "ERROR: '$bin' not found in PATH" >&2; exit 1; }
done

export KUBECONFIG="${KUBECONFIG/#\~/$HOME}"
KCTX=()
[[ -n "${KUBE_CONTEXT:-}" ]] && KCTX=(--context "$KUBE_CONTEXT")

workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"' EXIT
vault="$workdir/${SECRET_NAME}.slv.yaml"

# Build the SLV vault as a k8s CR (apiVersion slv.sh/v1, kind SLV).
slv vault new -v "$vault" \
  --env-pubkey "$SLV_PUBLIC_KEY" \
  --name "$SECRET_NAME" \
  --k8s-namespace "$NAMESPACE"

# Seal each key. Values are piped via stdin so they never appear in the process list.
while IFS= read -r key; do
  jq -rj --arg k "$key" '.[$k]' <<<"$SECRET_JSON" \
    | slv vault put -v "$vault" -n "$key" --value - --force
done < <(jq -r 'keys[]' <<<"$SECRET_JSON")

kubectl "${KCTX[@]+"${KCTX[@]}"}" apply -f "$vault"

# Wait for the operator to materialize the native Secret.
echo "Waiting for Secret/${SECRET_NAME} in ${NAMESPACE}..."
for _ in $(seq 1 60); do
  if kubectl "${KCTX[@]+"${KCTX[@]}"}" -n "$NAMESPACE" get secret "$SECRET_NAME" >/dev/null 2>&1; then
    echo "Secret/${SECRET_NAME} is ready."
    exit 0
  fi
  sleep 2
done

echo "ERROR: timed out waiting for Secret/${SECRET_NAME}" >&2
exit 1
