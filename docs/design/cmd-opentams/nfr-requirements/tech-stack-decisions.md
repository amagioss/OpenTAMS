# M15 cmd/opentams — Tech Stack Decisions

## CLI Framework

**Decision: `github.com/spf13/cobra`**

Chosen because REQ-USE-03 names three subcommands (`serve`, `gc`, `migrate`) — cobra's subcommand model maps directly to this structure with zero custom dispatch logic. `SilenceUsage` + `SilenceErrors` suppress cobra's default printing; each subcommand handles its own structured error output per REQ-USE-05.

Rejected: `stdlib flag` — no native subcommand support; would require manual `os.Args` dispatch.

## Signal Handling

**Decision: `os/signal.Notify` on a buffered channel**

`runServer` accepts `sigCh <-chan os.Signal` as a parameter so tests can inject a plain `chan os.Signal` and send signals programmatically without involving the OS signal table. `signal.Stop` is always deferred to prevent signal delivery to a closed channel.

## Pre-Logger Error Output

**Decision: `zap.Must(zap.NewProduction())` for config failures**

`zap.NewProduction()` writes JSON to stderr at Warn+ level — satisfies REQ-USE-05's "structured log line" requirement even before the application logger is initialised. Avoided `fmt.Fprintf` with hand-rolled JSON to keep the output format consistent with the rest of the log stream.

## Auth Provider Selection

**Decision: runtime branch on `APP_ENV`**

`APP_ENV=development` → `auth.DevProvider{}` (no network calls, no JWKS). All other values → `jwtauth.MultiIssuerValidator` with external + internal issuers. This keeps the development loop fast while ensuring production always uses real JWT validation.

## New Dependencies

None. All packages used by `cmd/opentams` were already present in `go.mod` from earlier modules — `cobra` was already a transitive dependency and was upgraded to v1.10.2 to make it a direct dependency.
