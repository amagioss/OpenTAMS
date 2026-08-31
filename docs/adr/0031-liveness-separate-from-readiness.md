---
status: "accepted"
date: 2026-08-31
decision-makers: OpenTAMS maintainers
consulted: OpenTAMS maintainers
informed: OpenTAMS contributors
---

# Separate liveness from readiness

## Context and Problem Statement

Kubernetes asks two different questions. Liveness asks whether the process is broken and
must be restarted. Readiness asks whether it can serve traffic right now.

Answering both with one check is a common and damaging mistake. If a health check probes
the database and that check is wired to liveness, a database outage restarts every replica.
Restarting does not fix a database, and it destroys the warm connections and in-flight
requests that would have recovered on their own.

What does each endpoint check?

## Considered Options

* One endpoint used for both probes
* Two endpoints that run the same checks
* Two endpoints with deliberately different checks

## Decision Outcome

Chosen option: "Two endpoints with deliberately different checks".

`/healthz` is liveness. It returns `200` unconditionally, with no body and no parameters. If
the process can answer, the process is alive. Nothing about a dependency can make it fail.

`/readyz` is readiness. `health.Checker.Ready` probes the database and the object store
concurrently under `ReadyTimeout`, and returns `503` when either fails. A dependency
outage takes the replica out of the load balancer and leaves it running, so it returns to
service when the dependency does.

`/health/details` is the third endpoint and is authenticated. It reports per-probe detail
for an operator, which is diagnostic information rather than a probe answer.

Probe construction is strict. `health.New` returns a sentinel error if a required probe is
missing, and the caller treats that as fatal. A misconfigured readiness check fails at
startup rather than reporting a comforting `200` forever.

### Consequences

* Good, because a database outage cannot cause a restart storm. Replicas leave the load
  balancer and stay alive.
* Good, because liveness cannot produce a false negative. It has no dependency to be wrong
  about.
* Good, because probes run concurrently under a timeout, so readiness answers in bounded
  time however slow a dependency is.
* Good, because a missing probe is a startup failure, not a silently permissive check.
* Bad, because liveness cannot detect a deadlocked or wedged process. A server that answers
  `/healthz` but serves nothing else is never restarted.
* Bad, because readiness is only as good as its probe list. A dependency nobody added is a
  dependency readiness does not know about.

## More Information

* Checker and its probes: [`internal/httpx/health/health.go`](../../internal/httpx/health/health.go).
* Handlers: [`internal/httpx/health/endpoints.go`](../../internal/httpx/health/endpoints.go).
* Why these endpoints bypass codegen: [ADR-0011](0011-health-and-metrics-outside-codegen.md).
