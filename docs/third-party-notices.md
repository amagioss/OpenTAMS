# Third-Party Notices

OpenTAMS is distributed under the [Apache License 2.0](../LICENSE). It links
against the third-party Go modules listed below, each under its own licence.

This page lists **direct** dependencies — the modules named in `go.mod` without
an `// indirect` marker. Those are the ones whose code OpenTAMS calls directly.
The transitive set is larger; reproduce it in full with:

```bash
go list -m all
```

Licences were read from the module cache at the pinned versions below, not
from upstream documentation. To re-derive this table after a dependency bump:

```bash
go list -m -f '{{.Path}} {{.Version}} {{.Dir}}' all
```

## Runtime dependencies

| Module | Version | Licence |
|---|---|---|
| `github.com/auth0/go-jwt-middleware/v2` | v2.3.1 | MIT |
| `github.com/aws/aws-sdk-go-v2` | v1.41.6 | Apache-2.0 |
| `github.com/aws/aws-sdk-go-v2/config` | v1.32.16 | Apache-2.0 |
| `github.com/aws/aws-sdk-go-v2/credentials` | v1.19.15 | Apache-2.0 |
| `github.com/aws/smithy-go` | v1.25.0 | Apache-2.0 |
| `github.com/aws/aws-sdk-go-v2/service/s3` | v1.99.1 | Apache-2.0 |
| `github.com/getkin/kin-openapi` | v0.133.0 | MIT |
| `github.com/gin-gonic/gin` | v1.12.0 | MIT |
| `github.com/golang-migrate/migrate/v4` | v4.19.1 | MIT |
| `github.com/google/uuid` | v1.6.0 | BSD-3-Clause |
| `github.com/jackc/pgx/v5` | v5.9.2 | MIT |
| `github.com/oapi-codegen/gin-middleware` | v1.0.2 | Apache-2.0 |
| `github.com/oapi-codegen/runtime` | v1.4.0 | Apache-2.0 |
| `github.com/prometheus/client_golang` | v1.23.2 | Apache-2.0 |
| `github.com/prometheus/client_model` | v0.6.2 | Apache-2.0 |
| `github.com/spf13/cobra` | v1.10.2 | Apache-2.0 |
| `go.uber.org/zap` | v1.27.1 | MIT |
| `golang.org/x/sync` | v0.21.0 | BSD-3-Clause |
| `gopkg.in/yaml.v3` | v3.0.1 | MIT AND Apache-2.0 (dual) |

## Build- and test-only dependencies

These are not linked into the released binaries. They run during code
generation, testing, or local development.

| Module | Version | Licence | Used for |
|---|---|---|---|
| `github.com/oapi-codegen/oapi-codegen/v2` | v2.6.0 | Apache-2.0 | Generating `gen/api/` from the OpenAPI contract |
| `github.com/stretchr/testify` | v1.11.1 | MIT | Test assertions |
| `github.com/testcontainers/testcontainers-go` | v0.42.0 | MIT | Integration tests against real Postgres and S3 |
| `github.com/testcontainers/testcontainers-go/modules/postgres` | v0.42.0 | MIT | Integration tests against real Postgres |
| `golang.org/x/tools` | v0.47.0 | BSD-3-Clause | Code generation tooling |

`github.com/oapi-codegen/runtime` is the exception in that pairing: it is a
library the generated code calls at request-handling time, so it *is* linked
into the binary even though the generator that emitted those calls is not.

## Specification and schemas

The JSON Schema files under `api/schemas/` are derived from the BBC TAMS
specification (Apache-2.0), tag `8.0`, and are **modified**. Provenance,
the full catalogue of modifications, and the independence statement are
in [`api/schemas/README.md`](../api/schemas/README.md), [`NOTICE`](../NOTICE),
and [ADR-0005](adr/0005-vendored-tams-schemas-are-modified.md).

## Container base images

Release images are built `FROM gcr.io/distroless/static-debian12:nonroot`
(see [`build/Dockerfile`](../build/Dockerfile)). That base image carries its own
upstream licences, which are not reproduced here. Every release publishes an
SBOM alongside the image, so enumerate them from the artefact rather than from
this page:

```bash
docker sbom ghcr.io/amagioss/opentams:latest
```

See [`docs/deployment/released-images.md`](deployment/released-images.md) for the
signature and provenance attestations published with each image.

## Reporting a licensing problem

If you believe a dependency is misattributed here, or that OpenTAMS ships code
under an incompatible licence, open an issue — or, if you would rather not
raise it publicly, use the channels in [`SECURITY.md`](../SECURITY.md).
