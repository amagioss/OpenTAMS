# M20 deployments/kubernetes/ — NFR Requirements

| ID | Category | Requirement |
|----|----------|-------------|
| NFR-K8S-S1 | Security | Credentials only in `Secret` — never in `ConfigMap` or `Deployment` env literals |
| NFR-K8S-S2 | Security | `runAsNonRoot: true`, `runAsUser: 65532` (distroless nonroot UID) on pod and job |
| NFR-K8S-S3 | Security | `readOnlyRootFilesystem: true` — compatible with static binary + distroless |
| NFR-K8S-S4 | Security | `allowPrivilegeEscalation: false`, `capabilities: drop: ["ALL"]` on all containers |
| NFR-K8S-S5 | Security | `seccompProfile: RuntimeDefault` on all pod specs |
| NFR-K8S-O1 | Availability | `RollingUpdate` with `maxUnavailable: 0`, `maxSurge: 1` — zero-downtime deploys |
| NFR-K8S-O2 | Reliability | Readiness probe on `/readyz` gates traffic; liveness probe on `/healthz` restarts hung pods |
| NFR-K8S-O3 | Reliability | Migrate Job `backoffLimit: 3` — retries on transient DB connectivity failures |
| NFR-K8S-O4 | Cleanup | `ttlSecondsAfterFinished: 300` on migrate Job — auto-delete completed Job after 5 min |
| NFR-K8S-O5 | Reliability | `activeDeadlineSeconds: 300` on migrate Job — hard timeout kills hung Job before backoffLimit exhausted |
| NFR-K8S-M1 | Maintainability | Plain YAML, no Helm — single source of truth, no template rendering required |
| NFR-K8S-M2 | Maintainability | Namespace declared in every manifest — `kubectl apply -f dir/` works without context assumptions |
| NFR-K8S-M3 | Maintainability | `README.md` documents ordered apply sequence with `kubectl wait` — no Helm lifecycle hooks to enforce ordering |

## Resource Sizing (Initial)

| Container | CPU Request | CPU Limit | Mem Request | Mem Limit |
|-----------|-------------|-----------|-------------|-----------|
| opentams serve | 100m | 500m | 128Mi | 256Mi |
| opentams migrate | 100m | 200m | 64Mi | 128Mi |

Tune based on load testing; these are conservative starting values for a Go API server with a 11.6 MB binary.
