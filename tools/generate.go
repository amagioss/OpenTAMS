// Package tools hosts the codegen pipeline wiring for `go generate ./...`.
//
// Pipeline (each directive `cd ..`s into the repo root first so the commands
// are CWD-independent — `go generate ./...` works from any subdirectory):
//
//  1. oapi-codegen consumes api/opentams-api-bundled.yaml (produced manually
//     by `redocly bundle api/opentams-api-v1.yaml -o api/opentams-api-bundled.yaml`
//     before regenerating) and emits gen/api/opentams.gen.go with the
//     strict-server types and handler interfaces.
//  2. tools/genunionbridges post-processes that file and emits
//     gen/api/opentams_json_bridges.gen.go with MarshalJSON passthroughs
//     for every strict-server response type whose underlying schema carries
//     a custom MarshalJSON. Compensates for oapi-codegen issue #1250 — see
//     tools/genunionbridges/main.go.
//
// This file has NO build tag so `go generate` always sees the directives;
// the file has no imports so it costs nothing at build time. Tool pins
// live in tools.go behind `//go:build tools` to keep the import graph clean.
//
// Re-run `go generate ./...` whenever the OpenAPI spec or the generator
// version changes. The handler-package test
// TestGetFlow_wireBodyContainsUnionFields is the CI canary: if step 2 is
// skipped or regresses, that test fails loudly.
package tools

//go:generate sh -c "cd .. && go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen --config .oapi-codegen.yaml api/opentams-api-bundled.yaml"
//go:generate sh -c "cd .. && go run ./tools/genunionbridges -in gen/api/opentams.gen.go -out gen/api/opentams_json_bridges.gen.go -pkg api"
