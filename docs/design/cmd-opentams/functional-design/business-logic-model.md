# M15 cmd/opentams — Functional Design: Business Logic Model

## Package Surface

```
cmd/opentams/
├── main.go       — main(): newRootCmd().Execute(); fatal stderr + os.Exit(1) on error
├── root.go       — newRootCmd(): cobra root; registers serve/gc/migrate subcommands
├── serve.go      — newServeCmd() + serve(ctx) dep wiring + buildAuth()
├── gc.go         — newGCCmd() stub (M16)
├── migrate.go    — newMigrateCmd() stub (M17)
└── run.go        — serverRunner interface + runServer(ctx, srv, shutdownPeriod, sigCh)
```

## `runServer` Logic

```
runServer(ctx, srv, shutdownPeriod, sigCh):
  go srv.Start(ctx) → serveErr channel

  select:
    case err = <-serveErr:   return err          // Start failed before signal
    case <-sigCh:            // OS signal received
    case <-ctx.Done():       // caller cancelled

  shutCtx = context.WithTimeout(background, shutdownPeriod)
  if err = srv.Shutdown(shutCtx); err != nil:
    return fmt.Errorf("shutdown: %w", err)
  return <-serveErr                              // drain Start goroutine
```

## `serve` Error Handling Model

| Phase | Logger available | Output |
|---|---|---|
| `config.Load()` fails | No | `zap.NewProduction()` to stderr (JSON) |
| Any subsequent failure | Yes | `log.Error(msg, retry_safe, check)` to stdout + return err |
| Shutdown error | Yes | Propagated from `runServer` (caller logs if needed) |

## Test Plan

| TC | Description | Expected |
|---|---|---|
| TC-CMD-ROOT-01 | `newRootCmd()` Use field and subcommand names | `opentams`, `serve`/`gc`/`migrate` registered |
| TC-CMD-ROOT-02 | Root `--help` output | Contains `serve`, `gc`, `migrate` |
| TC-CMD-GC-01 | `gc` subcommand execution | Returns non-nil error containing "not" |
| TC-CMD-MIGRATE-01 | `migrate` subcommand execution | Returns non-nil error containing "not" |
| TC-CMD-SERVE-01 | `serve` with all required env vars unset | Returns error (config failure) |
| TC-CMD-SERVE-02 | `serve --help` | Contains "SIGTERM" and "migrate" |
| TC-CMD-RUN-01 | `runServer` with SIGTERM | Calls Shutdown, returns nil |
| TC-CMD-RUN-02 | `runServer` with Start error | Returns Start error, no Shutdown called |
| TC-CMD-RUN-03 | `runServer` with ctx cancel | Calls Shutdown, returns nil |
| TC-CMD-RUN-04 | `runServer` with Shutdown error | Returns Shutdown error wrapped |
