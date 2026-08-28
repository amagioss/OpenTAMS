# M15 cmd/opentams — Functional Design: Business Rules

## Scope

Process entrypoint for OpenTAMS. Provides three subcommands: `serve` (API server), `gc` (GC worker stub — M16), `migrate` (migrations stub — M17).

## Functional Requirements Mapped

| Req ID | Requirement | Implementation |
|---|---|---|
| REQ-USE-03 | `--help` on each subcommand describes its lifecycle role — not just flags | `Long` field on each cobra command explains when to run it and what it affects |
| REQ-USE-04 | `serve` emits structured startup log listing effective config with secrets redacted | `log.Info("opentams starting", zap.Any("config", cfg.Redacted()))` after logger init |
| REQ-USE-05 | Every non-zero exit emits a structured log: what failed, retry_safe, what to check | `log.Error(...)` with `retry_safe` + `check` fields before every `return err` in `serve`; temporary `zap.NewProduction()` for pre-logger config failures |
| REQ-CFG-03 | Config validated at startup; fail fast with clear error | `config.Load()` returns `errors.Join` of all failures; `serve` returns immediately |
| REQ-REL-09 | SIGTERM/SIGINT: drain in-flight requests within `SERVER_GRACEFUL_SHUTDOWN_PERIOD` | `runServer` selects on `sigCh`/`ctx.Done()`, calls `srv.Shutdown(shutCtx)` with bounded deadline |
| REQ-REL-10 | Connection pools established before readiness probe can report healthy | pgxpool + objectstore initialised before `server.New` and before `runServer` is called |
| config table | SIGHUP reloads `LOG_LEVEL` from environment without restart | Background goroutine selects on `sighupCh`; calls `atom.SetLevel(newLevel)` |
| REQ-ARCH-13 | Schema migrations are a separate step, not auto-run at startup | `migrate` is a distinct subcommand stub; `serve` does not run migrations |
| REQ-ARCH-15 | Validate schema version on startup | **Deferred to M17** — requires migrations module to be implemented first |

## Business Rules

**BR-CMD-01** The root command `opentams` does nothing when invoked without a subcommand — cobra prints help automatically.

**BR-CMD-02** `serve` reads all configuration from environment variables via `config.Load()`. No CLI flags override env vars in Phase 1.

**BR-CMD-03** If `config.Load()` fails, `serve` writes a structured JSON error line to stderr (via temporary production zap logger) and exits non-zero. The error lists every invalid parameter.

**BR-CMD-04** After the application logger is initialised, all subsequent fatal paths log a structured error with `retry_safe` (bool) and `check` (string) fields before returning.

**BR-CMD-05** SIGHUP reloads `LOG_LEVEL` by calling `os.Getenv("LOG_LEVEL")` at signal time. If the value is invalid the current level is kept and a WARN is logged.

**BR-CMD-06** SIGTERM and SIGINT both trigger graceful shutdown. `runServer` calls `srv.Shutdown` with a context bounded by `cfg.ServerGracefulShutdownPeriod`.

**BR-CMD-07** Auth provider selection: `APP_ENV=development` → `auth.DevProvider` (always accepts any token). All other values → `auth.NewJWTProvider` with a `jwtauth.MultiIssuerValidator` configured for external + internal issuers.

**BR-CMD-08** `gc` and `migrate` subcommands return "not implemented" errors in Phase 1. Their `--help` text describes the full intended lifecycle role per REQ-USE-03.

## Dep Wiring Order

```
config.Load()
  → logger.New()           (stdout, parsed level, stacktrace at Error)
  → metrics.New()          (namespace="opentams")
  → pgxpool.NewWithConfig() (MinConns/MaxConns from config)
  → objectstore.NewS3Store()
  → metastore.New(pool)
  → idempotency.NewStore(pool)
  → service.NewAppServiceMetrics(reg.NamespacedRegisterer())
  → flow.New() / segment.New() / storage.New()
  → health.New(DB=ms, Object=obj)
  → buildAuth()            (DevProvider or JWTProvider)
  → handlers.New()
  → server.New(Deps{...})
  → runServer(ctx, srv, shutdownPeriod, sigCh)
```

## Out of Scope

- CLI flags (all config from env vars in Phase 1)
- Schema version check at startup (REQ-ARCH-15 — deferred to M17)
- `gc` implementation (M16)
- `migrate` implementation (M17)
- TLS configuration beyond what `server.New` already handles
