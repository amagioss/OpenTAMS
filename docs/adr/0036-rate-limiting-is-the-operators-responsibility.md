---
status: "accepted"
date: 2026-08-31
decision-makers: OpenTAMS maintainers
consulted: OpenTAMS maintainers
informed: OpenTAMS contributors
---

# Leave rate limiting to the operator

## Context and Problem Statement

An unprotected API can be overwhelmed by request volume, whether deliberately or by a
client with a retry loop and no backoff.

An in-process limiter is easy to add and hard to get right for a service that scales
horizontally. Each replica sees only its own traffic, so a per-replica limit of N becomes a
cluster limit of N times the replica count, and that number changes when the autoscaler
acts. A shared limit needs shared state, which means another dependency on the request
path.

Meanwhile every deployment target for OpenTAMS already has a limiter in front of it. An
ingress controller, a service mesh, and an API gateway all do this, with a cluster-wide
view that a replica does not have.

Does OpenTAMS limit request rates?

## Considered Options

* A per-replica in-process limiter, using `golang.org/x/time/rate`
* A distributed limiter backed by Redis
* No limiter, and document it as the operator's responsibility

## Decision Outcome

Chosen option: "No limiter, and document it as the operator's responsibility".

No limiter exists. `SECURITY.md` states the position directly: denial of service through
unbounded request volume is the operator's responsibility, per the deployment guide.

Two pieces of the shape are in place, and they are deliberate rather than leftovers:

* `SERVER_RATE_LIMIT_RPS` and `SERVER_RATE_LIMIT_BURST` are parsed and validated at
  startup, then ignored. `../configuration.md` marks both "Currently ignored", so an
  operator who sets them is not surprised when nothing happens.
* `apperror` catalogues `rate-limited`, mapped to HTTP 429 with its own Problem Details
  type URI. A limiter placed in front of OpenTAMS can therefore return a `429` in the same
  error vocabulary as the rest of the API, and a limiter added later needs no new error
  code.

We rejected a per-replica limiter because a limit that changes with the replica count is
not a limit an operator can reason about. We rejected a distributed limiter because it puts
another network dependency on every request, to solve a problem the ingress already solves.

### Consequences

* Good, because limiting happens where the traffic view is complete, and where an operator
  already configures it.
* Good, because there is no shared state on the request path, and no dependency that can
  fail open or fail closed.
* Good, because the `429` vocabulary is defined, so a proxy's rejection looks like an
  OpenTAMS error to a client.
* Bad, because a deployment with no proxy has no protection at all. Running OpenTAMS
  directly on a public address is unsafe, and nothing in the software says so at runtime.
* Bad, because two configuration variables exist and do nothing. They read as implemented
  to anyone who does not check the documentation.
* Bad, because per-tenant or per-operation limits are not expressible at the ingress in the
  way they would be in the application, which knows the caller's principal.

## More Information

* Position statement: [`../../SECURITY.md`](../../SECURITY.md).
* The ignored variables, marked as such: [`../configuration.md`](../configuration.md).
* Error catalogue: [`internal/apperror/apperror.go`](../../internal/apperror/apperror.go).
