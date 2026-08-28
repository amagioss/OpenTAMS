# M19 deployments/docker/docker-compose.yml — NFR Requirements

| ID | Category | Requirement |
|----|----------|-------------|
| NFR-DC-S1 | Security | No plaintext secrets in compose YAML — `.env` file gitignored; compose uses `${VAR}` references |
| NFR-DC-O1 | Dev ergonomics | `DB_SSLMODE=disable` for local dev — no TLS config needed between containers |
| NFR-DC-M1 | Usability | Single `docker compose up` brings up full working stack |
| NFR-DC-M2 | Teardown | `docker compose down -v` removes all data volumes cleanly |
| NFR-DC-S2 | Security | Image tags pinned — `minio/minio` and `minio/mc` use specific `RELEASE.*` tags, not `latest` |

## `.env.example` Contract

`deployments/docker/.env.example` must be committed and kept in sync with all `${VAR}` references in `docker-compose.yml`. `.env` itself is gitignored.
