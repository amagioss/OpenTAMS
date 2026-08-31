---
status: "accepted"
date: 2026-08-31
decision-makers: OpenTAMS maintainers
consulted: OpenTAMS maintainers
informed: OpenTAMS contributors
---

# Put authentication behind a `Provider` interface

## Context and Problem Statement

OpenTAMS has two kinds of caller. Human and API clients arrive with tokens from an external
identity provider. In-cluster services arrive with tokens from the cluster's own issuer,
and those are service accounts rather than people.

Local development has a third need: running the server without an identity provider at all.

Authentication also has to be replaceable. An operator's identity provider is theirs, not
ours.

How is authentication structured?

## Considered Options

* One JWT implementation, wired directly into the middleware
* A `Provider` interface, chosen at startup
* A plugin mechanism loaded at runtime

## Decision Outcome

Chosen option: "A `Provider` interface, chosen at startup".

`auth.Provider` has one method: `Authenticate(ctx, rawToken) (*Principal, error)`. Two
implementations exist.

`JWTProvider` validates against `jwtauth.Validator`, which supports several issuers. It
distinguishes them: a token accepted by the issuer named `internal` is additionally checked
against an allowlist of subjects, so a Kubernetes service account must be listed by name to
be accepted. An empty allowlist rejects every internal caller. External tokens are not
gated this way, because their authorization comes from the external issuer.

It also translates errors into the Problem Details vocabulary. An expired token becomes
`token-expired`, an internal subject that is not allowlisted becomes `forbidden`, and
anything else becomes `unauthorized`. A client can tell "refresh your token" apart from
"you are not permitted".

`DevProvider` accepts any token and returns a fixed principal. It is selected only when
`APP_ENV` is `development`, at `cmd/opentams/serve.go:295`. `APP_ENV` is validated as an
enum with `production` as the default, so the insecure provider cannot be reached by
mistyping the variable.

We rejected runtime plugins. Loading authentication code at runtime is a large attack
surface for a need nobody has stated.

### Consequences

* Good, because service-to-service access is an explicit allowlist of subjects rather than
  a trusted network position.
* Good, because a failure reason reaches the client accurately, which is the difference
  between a client that retries correctly and one that gives up.
* Good, because local development needs no identity provider, and the middleware chain is
  the same one production runs.
* Good, because a new provider is one interface method.
* Bad, because `DevProvider` disables authentication completely. Its safety rests entirely
  on `APP_ENV`, so one misconfigured deployment variable is the whole distance between a
  secured and an open server.
* Bad, because the internal allowlist is static configuration. Adding a service means a
  configuration change and a restart.
* Bad, because the interface takes a raw token, which assumes a bearer scheme. A scheme
  that is not token-shaped would not fit.

## More Information

* Providers and error translation: [`internal/auth/auth.go`](../../internal/auth/auth.go).
* Selection: [`cmd/opentams/serve.go`](../../cmd/opentams/serve.go).
* Multi-issuer validation: [`pkg/jwtauth/`](../../pkg/jwtauth/).
