---
status: "accepted"
date: 2026-08-31
decision-makers: OpenTAMS maintainers
consulted: OpenTAMS maintainers
informed: OpenTAMS contributors
---

# Restore union marshalling with a generated bridge file

## Context and Problem Statement

TAMS models Flow as a discriminated `oneOf`. `oapi-codegen` represents such a type as a
struct holding an unexported `union json.RawMessage` field, plus a hand-written
`MarshalJSON` that returns the raw bytes.

Strict-server response types are emitted as `type GetFlow200JSONResponse Flow`. That is a
type *definition*, not an alias. Go definitions do not inherit methods from the underlying
type, so the response type loses `MarshalJSON`. `encoding/json` then falls back to default
struct encoding and serialises the union as `{}`.

This is [oapi-codegen issue #1250](https://github.com/oapi-codegen/oapi-codegen/issues/1250).
It fails silently: the status code is right, the body is empty. Schemas using
`additionalProperties` lose their map the same way.

## Considered Options

* Wait for the upstream fix
* Change the contract to avoid `oneOf`
* Hand-write the missing `MarshalJSON` methods
* Patch the generator's output in place
* Generate a companion file of passthrough methods

## Decision Outcome

Chosen option: "Generate a companion file of passthrough methods".

`tools/genunionbridges` parses the generated file. For every `type X Y` where `Y` declares
its own `MarshalJSON`, it emits into a sibling file:

```go
func (r X) MarshalJSON() ([]byte, error) { return Y(r).MarshalJSON() }
```

The methods sit in the same package as the generated types, so Go's method-set rules
apply and `encoding/json` finds them. The tool is idempotent: the same input produces
byte-identical output.

We rejected changing the contract, because the `oneOf` comes from the TAMS specification
and is not ours to remove. We rejected patching the generator's output, because an
in-place edit is lost on the next regeneration and makes `make api-check` unusable as a
drift detector. Hand-writing the methods has the same maintenance problem as any manual
mirror of generated code.

### Consequences

* Good, because post-processing stays additive. The generator's output is never edited, so
  regeneration remains reproducible and drift detection keeps working.
* Good, because coverage is automatic. A new union in the contract gets its bridge without
  anyone remembering to add one.
* Good, because the workaround is contained. Deleting the tool and its `go:generate` line
  is the whole removal when upstream fixes #1250.
* Bad, because we maintain a Go AST tool to work around someone else's bug.
* Bad, because the failure it prevents is silent. Anyone who bypasses the bridge sees a
  valid `200` with an empty body, which is hard to spot in a test that only asserts the
  status code.

## More Information

* The tool, with the full explanation in its package comment: [`tools/genunionbridges/main.go`](../../tools/genunionbridges/main.go).
* Its current output: [`gen/api/opentams_json_bridges.gen.go`](../../gen/api/opentams_json_bridges.gen.go).
* Why generated code is committed: [ADR-0008](0008-commit-generated-code.md).
