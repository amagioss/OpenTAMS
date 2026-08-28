# M18 build/Dockerfile — NFR Requirements

| ID | Category | Requirement |
|----|----------|-------------|
| NFR-DOC-S1 | Security | Non-root UID at runtime (uid 65532 — distroless nonroot) |
| NFR-DOC-S2 | Security | No shell in runtime image — removes `/bin/sh` attack surface |
| NFR-DOC-S3 | Security | Static binary with no libc — no shared library injection possible |
| NFR-DOC-O1 | Build performance | `go.mod`/`go.sum` layer before source — module cache survives source changes |
| NFR-DOC-O2 | Binary size | `-ldflags="-s -w"` strips DWARF debug info and symbol table |
| NFR-DOC-O3 | Portability | `CGO_ENABLED=0` — no host libc dependency; required for distroless static runtime |
| NFR-DOC-M1 | Size | Runtime image < 50 MB (achieved: 11.6 MB) |
| NFR-DOC-M2 | Maintainability | Single Dockerfile — no per-arch variants |

## Compatibility Notes

- **Bottlerocket**: compatible — OCI image, non-root, static binary, standard syscalls only, no shell required
- **Chainguard static**: drop-in replacement for `distroless/static` if CVE-free runtime is required
- **Ubuntu CI runners**: compatible — Docker is OS-agnostic; builder image libc is irrelevant with CGO=0
- **Multi-arch CI memory**: parallel amd64+arm64 builds require ≥4 GB VM RAM; colima default (2 GB) OOMs
