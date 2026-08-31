# M18 build/Dockerfile — Functional Design

## Functional Requirements

| ID | Requirement |
|----|-------------|
| FR-DOC-01 | Multi-stage build: builder stage compiles binary; runtime stage contains only the binary |
| FR-DOC-02 | Builder uses `golang:1.26-alpine` pinned to project Go version |
| FR-DOC-03 | Runtime uses `gcr.io/distroless/static-debian12:nonroot` — no shell, no package manager |
| FR-DOC-04 | No SQL files in runtime image — migrations are `embed.FS` baked at compile time |
| FR-DOC-05 | Default `CMD` is `serve`; `ENTRYPOINT` is `/opentams` |
| FR-DOC-06 | `EXPOSE 8080` (matches `SERVER_PORT` default) |
| FR-DOC-07 | Run as non-root user at runtime (distroless `nonroot` = uid 65532) |
| FR-DOC-08 | Multi-arch: image builds for `linux/amd64` and `linux/arm64` |
| FR-DOC-09 | Cross-compilation: builder runs on native platform (`$BUILDPLATFORM`), compiles to target (`$TARGETOS/$TARGETARCH`) — avoids QEMU emulation |

## Design Decisions

| Decision | Choice | Rejected | Rationale |
|----------|--------|----------|-----------|
| Builder base | `golang:1.26-alpine` | `golang:1.26` (Debian) | Smaller pull; CGO_ENABLED=0 makes builder libc irrelevant |
| Runtime base | `gcr.io/distroless/static-debian12:nonroot` | `chainguard/static`, `alpine` | Industry standard; no shell reduces attack surface; Google-maintained |
| Chainguard for builder | Rejected | — | CVEs in builder don't reach runtime in multi-stage builds |
| Multi-arch strategy | `--platform=$BUILDPLATFORM` + `TARGETOS/TARGETARCH` | Single-arch, QEMU | Native compile per arch; QEMU is slow and OOMs on constrained VMs |
| CGO | `CGO_ENABLED=0` | default (CGO enabled) | Pure static binary; required for cross-compilation and distroless runtime |

## Build Args

| ARG | Set by | Purpose |
|-----|--------|---------|
| `BUILDPLATFORM` | Docker Buildx | Builder image platform (run on native host) |
| `TARGETOS` | Docker Buildx | `GOOS` for Go cross-compilation |
| `TARGETARCH` | Docker Buildx | `GOARCH` for Go cross-compilation |

## Layer Cache Strategy

1. `COPY go.mod go.sum ./` — module layer; invalidated only on dependency changes
2. `RUN go mod download` — cached across source changes
3. `COPY . .` — source layer; invalidated on any source change
4. `RUN go build ...` — compile; only runs when source or deps change
