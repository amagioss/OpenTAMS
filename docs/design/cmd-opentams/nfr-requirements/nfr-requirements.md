# M15 cmd/opentams — NFR Requirements

## Performance

**NFR-CMD-P1** Process startup must complete (logger + metrics + pool + objectstore + services wired) before `server.Start` is called. Connection pool establishment is synchronous — `pgxpool.NewWithConfig` blocks until `MinConns` connections are established or the context is cancelled.

## Reliability

**NFR-CMD-R1** `runServer` is the single point where Start/Shutdown lifecycle is coordinated. It holds the `serveErr` channel open until `<-serveErr` is explicitly drained after Shutdown — no goroutine leak.

**NFR-CMD-R2** SIGHUP handler goroutine is bounded by a `hupDone` channel closed via `defer` in `serve`. It cannot outlive the serve call.

**NFR-CMD-R3** `signal.Stop` is deferred for both `sigCh` (SIGTERM/SIGINT) and `sighupCh` (SIGHUP) — prevents signal delivery to a closed channel after `serve` returns.

**NFR-CMD-R4** `pool.Close()` is deferred immediately after successful pool construction — guaranteed cleanup on any subsequent error path.

**NFR-CMD-R5** `log.Sync()` is deferred immediately after logger construction to flush buffered log lines before the process exits.

## Observability

**NFR-CMD-O1** Every fatal exit path after logger init logs: `msg`, `error`, `retry_safe` (bool), `check` (string). Operators can diagnose from the log line alone without reading source code.

**NFR-CMD-O2** Startup log includes the full redacted config (all secrets replaced with `[redacted]`) at INFO level — one log line captures the entire runtime configuration.

**NFR-CMD-O3** SIGHUP level changes are confirmed with an INFO log at the new level, so operators can verify the change took effect.

## Security

**NFR-CMD-S1** `cfg.Redacted()` is the only config value passed to the logger. Raw `cfg` is never logged.

**NFR-CMD-S2** `APP_ENV=development` enables `auth.DevProvider` which accepts any token. This must not be set in production deployments.

## Maintainability

**NFR-CMD-M1** `runServer` accepts a `serverRunner` interface — testable without a real network listener. No Gin or HTTP stack is imported in `run.go`.

**NFR-CMD-M2** `serve` is a plain function (not a method) — testable by calling directly with a controlled `context.Context`.

**NFR-CMD-M3** Adding a new startup step (e.g. schema version check in M17) requires only inserting it in `serve` between pool construction and `server.New` — no interface changes.

## Constraints

**NFR-CMD-C1** No CLI flags in Phase 1 — all configuration from environment variables only (config.Load).

**NFR-CMD-C2** REQ-ARCH-15 (schema version check) deferred to M17. `serve` does not validate migration state.
