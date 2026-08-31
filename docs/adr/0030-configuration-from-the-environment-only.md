---
status: "accepted"
date: 2026-08-31
decision-makers: OpenTAMS maintainers
consulted: OpenTAMS maintainers
informed: OpenTAMS contributors
---

# Configure from the environment only, and validate everything at startup

## Context and Problem Statement

OpenTAMS needs around forty settings: database connection, object store endpoint and
credentials, authentication issuers, timeouts, and pool sizes. It runs as a container, so
its configuration comes from whatever the orchestrator injects.

Files and flags are alternatives, and supporting several sources means a precedence order
that everyone has to learn. Deferring validation means a typo surfaces as a failure hours
later, on the first request that needs the setting.

Where does configuration come from, and when is it checked?

## Considered Options

* A configuration file, with an environment override for each key
* Command-line flags
* Environment variables only, parsed and validated at startup
* Several sources merged by a library such as Viper

## Decision Outcome

Chosen option: "Environment variables only, parsed and validated at startup".

`internal/config` reads about forty variables through small typed helpers — `getInt`,
`getString`, `getEnum`, `getBool`, `getDuration` — each carrying its default. There are 41
such calls. No file is read and no flag is parsed.

Validation happens while loading, not on first use. `getEnum` rejects a value outside its
list, so `APP_ENV` cannot hold a typo. That check is load-bearing: it is the only thing
stopping `DevProvider` from being selected in production — see
[ADR-0029](0029-authentication-behind-a-provider-interface.md).

Some defaults depend on other settings. `DB_SSLMODE` defaults to `require` in production
and relaxes in development, so the safe value is the one you get by saying nothing.

No configuration library is used. Forty variables with typed helpers is less code than the
library, and the precedence rules stay in one readable function.

### Consequences

* Good, because the process fails at startup with a named variable when configuration is
  wrong. A crash-looping pod is a clear signal, where a runtime failure hours later is not.
* Good, because there is one source, so there is no precedence order to explain or
  debug.
* Good, because it fits how Kubernetes, Helm, and Compose already inject settings, with no
  config-file mount and no reload path.
* Good, because production defaults are the safe ones, and relaxing them takes a deliberate
  `APP_ENV`.
* Bad, because reconfiguring means a restart. Nothing can be changed while running.
* Bad, because secrets arrive as environment variables, which are visible in a process
  listing and in pod specifications. Reading a secret from a file is not supported.
* Bad, because forty flat variables is a large surface with no structure, and related
  settings are only grouped by naming convention.

## More Information

* Loader, helpers, and defaults: [`internal/config/config.go`](../../internal/config/config.go).
* Every variable, with its default and meaning: [`../configuration.md`](../configuration.md).
