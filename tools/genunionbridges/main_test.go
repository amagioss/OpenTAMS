package main

import (
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

const fixture = `package api

import "encoding/json"

// Union type with a custom MarshalJSON — classic oapi-codegen polymorphic
// schema shape.
type Flow struct{ union json.RawMessage }
func (t Flow) MarshalJSON() ([]byte, error)      { return t.union, nil }
func (t *Flow) UnmarshalJSON(b []byte) error     { t.union = b; return nil }

// additionalProperties-style generated type with pointer-receiver MarshalJSON.
type Tags struct{ AdditionalProperties map[string]string ` + "`json:\"-\"`" + ` }
func (t *Tags) MarshalJSON() ([]byte, error)  { return nil, nil }

// Plain struct, no custom marshaler.
type Source struct{ ID string }

// Strict-server response type definitions.
type GetFlow200JSONResponse Flow                 // SHOULD bridge
type PutFlow201JSONResponse Flow                 // SHOULD bridge
type GetSource200JSONResponse Source             // should NOT bridge
type GetTags200JSONResponse Tags                 // SHOULD bridge (pointer-receiver MarshalJSON)

// Type alias — inherits methods, must NOT be bridged (and won't trigger Go's
// "method redeclared" error because the alias already has MarshalJSON).
type FlowAlias = Flow
`

func TestCollectMarshalJSONReceivers(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "fixture.go", fixture, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got := collectMarshalJSONReceivers(f)

	want := map[string]bool{"Flow": true, "Tags": true}
	if len(got) != len(want) {
		t.Fatalf("expected %d entries, got %d: %v", len(want), len(got), got)
	}
	for k := range want {
		if !got[k] {
			t.Errorf("missing %q in marshal-bearing set: %v", k, got)
		}
	}
}

func TestCollectDefinitionAliases(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "fixture.go", fixture, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	marshalBearing := collectMarshalJSONReceivers(f)
	got := collectDefinitionAliases(f, marshalBearing)

	wantPairs := map[string]string{
		"GetFlow200JSONResponse": "Flow",
		"PutFlow201JSONResponse": "Flow",
		"GetTags200JSONResponse": "Tags",
	}
	if len(got) != len(wantPairs) {
		t.Fatalf("expected %d bridges, got %d: %+v", len(wantPairs), len(got), got)
	}
	for _, b := range got {
		w, ok := wantPairs[b.Named]
		if !ok {
			t.Errorf("unexpected bridge: %+v", b)
			continue
		}
		if b.Underlying != w {
			t.Errorf("bridge %s: expected underlying %s, got %s", b.Named, w, b.Underlying)
		}
	}
	for _, b := range got {
		if b.Named == "GetSource200JSONResponse" {
			t.Errorf("Source (no MarshalJSON) incorrectly bridged: %+v", b)
		}
		if b.Named == "FlowAlias" {
			t.Errorf("alias FlowAlias incorrectly bridged: %+v", b)
		}
	}
}

func TestRender_producesValidGo(t *testing.T) {
	bridges := []bridge{
		{Named: "GetFlow200JSONResponse", Underlying: "Flow"},
		{Named: "PutFlow201JSONResponse", Underlying: "Flow"},
	}
	out, err := render("api", bridges)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	src := string(out)
	for _, substr := range []string{
		"package api",
		"DO NOT EDIT",
		"oapi-codegen issue #1250",
		"func (r GetFlow200JSONResponse) MarshalJSON() ([]byte, error)",
		"return Flow(r).MarshalJSON()",
		"func (r PutFlow201JSONResponse) MarshalJSON() ([]byte, error)",
	} {
		if !strings.Contains(src, substr) {
			t.Errorf("expected output to contain %q; got:\n%s", substr, src)
		}
	}
	if _, err := parser.ParseFile(token.NewFileSet(), "out.go", out, parser.SkipObjectResolution); err != nil {
		t.Fatalf("generated source failed to parse: %v\n--- source ---\n%s", err, src)
	}
}

func TestRender_emptyInput(t *testing.T) {
	out, err := render("api", nil)
	if err != nil {
		t.Fatalf("render empty: %v", err)
	}
	src := string(out)
	if !strings.Contains(src, "package api") {
		t.Errorf("empty output missing package decl:\n%s", src)
	}
	if strings.Contains(src, "func (r ") {
		t.Errorf("empty output unexpectedly contains bridge methods:\n%s", src)
	}
	if _, err := parser.ParseFile(token.NewFileSet(), "out.go", out, parser.SkipObjectResolution); err != nil {
		t.Fatalf("empty generated source failed to parse: %v\n--- source ---\n%s", err, src)
	}
}
