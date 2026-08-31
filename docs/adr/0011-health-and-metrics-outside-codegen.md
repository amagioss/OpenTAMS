---
status: "accepted"
date: 2026-08-31
decision-makers: OpenTAMS maintainers
consulted: OpenTAMS maintainers
informed: OpenTAMS contributors
---

# Keep `/healthz`, `/readyz`, and `/metrics` in the spec but out of codegen

## Context and Problem Statement

Three operational endpoints have to be reachable without a bearer token, because a
Kubernetes probe and a Prometheus scraper carry none. Every other operation is
authenticated.

`oapi-codegen` registers all generated handlers onto whichever router group it is given.
Giving it the authenticated group would put the probes behind authentication. Giving it
the public group would expose the whole API.

`/metrics` has a second problem. A strict handler receives no `*http.Request`, and
`promhttp` needs one to negotiate the response encoding.

Do these three operations stay in the contract, and are they generated?

## Considered Options

* Remove them from the contract and mount them by hand
* Keep them in the contract, generate them, and make authentication conditional per route
* Keep them in the contract, exclude them from codegen, and mount them by hand
* Serve them from a second HTTP listener on a separate port

## Decision Outcome

Chosen option: "Keep them in the contract, exclude them from codegen, and mount them by
hand".

`.oapi-codegen.yaml` lists `GetMetrics`, `GetHealthz`, and `GetReadyz` under
`exclude-operation-ids`. `internal/server` mounts all three on the public group, and then
registers the generated handlers on the authenticated group. The two groups cannot
collide, because the excluded three are the only overlap.

`/health/details` stays in codegen. It is authenticated, and its response body is
non-trivial, so the strict response type earns its ceremony.

Keeping the operations in the specification matters: the published API documentation stays
complete, and an operator reading the contract still finds the probe endpoints.

### Consequences

* Good, because probes and scrapes work with no credentials, and the rest of the API needs
  one.
* Good, because `promhttp` gets a real `http.ResponseWriter` through `gin.WrapH`, so
  content negotiation works.
* Good, because the exclusion is data in a configuration file, not a patch to generated
  output.
* Good, because one port serves everything, so no extra listener, probe target, or network
  policy is needed.
* Bad, because the exclusion list is a manual invariant. Adding a fourth public operation
  means editing the generator config and the router, and nothing enforces the pair.
* Bad, because these three handlers are hand-written, so the compiler does not check them
  against the contract. Drift between the spec and their behaviour is possible.
* Bad, because `/metrics` is unauthenticated on the main listener. An operator who does not
  want it public must block it at the ingress or with a network policy.

## More Information

* The exclusion list and its rationale: [`.oapi-codegen.yaml`](../../.oapi-codegen.yaml).
* Route-group construction: [`internal/server/server.go`](../../internal/server/server.go).
* Handlers: [`internal/httpx/health/endpoints.go`](../../internal/httpx/health/endpoints.go) and
  [`internal/httpx/metrics/metrics.go`](../../internal/httpx/metrics/metrics.go).
* Why the two groups exist: [ADR-0028](0028-gin-with-split-route-groups.md).
* Liveness against readiness: [ADR-0031](0031-liveness-separate-from-readiness.md).
