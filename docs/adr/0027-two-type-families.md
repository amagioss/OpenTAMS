---
status: "accepted"
date: 2026-08-31
decision-makers: OpenTAMS maintainers
consulted: OpenTAMS maintainers
informed: OpenTAMS contributors
---

# Keep wire types and domain types apart, with one adapter between them

## Context and Problem Statement

`oapi-codegen` produces a Go type for every schema in the contract. Those types are shaped
by the wire format: pointers for optional fields, generated names, unions held as raw JSON,
and enum types per field.

The service and store layers want different shapes. They want a parsed timerange, not a
string. They want a required field to be a value, not a pointer that every caller checks.

Using the generated types everywhere avoids a mapping layer. It also makes a contract
change ripple into the store, and makes every business rule work around wire ergonomics.

How many type families are there?

## Considered Options

* One family: use the generated types throughout
* One family: hand-write domain types and map them to the contract at generation time
* Two families, with mapping scattered wherever it is convenient
* Two families, with a single package allowed to convert

## Decision Outcome

Chosen option: "Two families, with a single package allowed to convert".

`gen/api` holds the wire types. `internal/domain` holds the domain types. `internal/httpx/conversion`
is the only place that maps between them.

The conversion package is pure by contract. Its documentation states the rule: shape
mappers and request decoders only, with no business logic, no metastore, no logging, no
metrics, and no I/O. It produces wire-shape pieces and chooses neither the HTTP status nor
the strict-server response variant. The handler assembles the response from those pieces.

The rule is enforced rather than remembered. The `conversion-is-pure` depguard rule in
`.golangci.yml` blocks imports of the service, metastore, and store layers. Two scenario
tests, `SCN-CONV-16` and `SCN-HTTP-41`, cover the symbol-level part that depguard cannot
see.

### Consequences

* Good, because a contract change stops at the conversion boundary. The store and service
  layers do not move when a field becomes optional on the wire.
* Good, because domain types express the rules directly. A parsed timerange cannot hold an
  unparseable string, so no layer below conversion revalidates it.
* Good, because the boundary is checked by a linter, not by review.
* Good, because purity makes conversion testable in isolation, with table tests and no
  fixtures.
* Bad, because every field is written twice, and adding one means editing both families and
  the mapper.
* Bad, because the mapping is manual and can be wrong in ways that compile. A field left
  unmapped is silently absent, which is why the union decode order needed a test rather
  than a reading.
* Bad, because the purity rule pushes work into the handler that might look like it belongs
  in conversion. `get_urls` projection is that case — see
  [ADR-0026](0026-get-urls-projected-in-the-handler.md).

## More Information

* The rule, stated in the package: [`internal/httpx/conversion/doc.go`](../../internal/httpx/conversion/doc.go).
* Enforcement: the `conversion-is-pure` depguard rule in [`.golangci.yml`](../../.golangci.yml).
* Where the wire types come from: [ADR-0007](0007-strict-server-code-generation.md).
