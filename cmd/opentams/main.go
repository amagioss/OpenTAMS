package main

import (
	"fmt"
	"os"
)

// Build-time variables populated by goreleaser via -ldflags on tagged
// release builds. They default to "dev" / "none" / "unknown" so plain
// `go build`, `go run`, and `go test` produce a sensible value without
// any extra flags.
//
// These are intentionally exported as package-level globals so that a
// `version` subcommand (tracked separately as a follow-up issue) can
// surface them without touching this file again.
var (
	version = "dev"     //nolint:gochecknoglobals // build-time injection point
	commit  = "none"    //nolint:gochecknoglobals // build-time injection point
	date    = "unknown" //nolint:gochecknoglobals // build-time injection point
)

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}

// Until a `version` subcommand is wired up, the linter would flag these
// as unused. Touching them in init() is the smallest possible no-op
// that keeps the symbols live without changing any runtime behaviour.
//
//nolint:gochecknoinits // see comment above; removed when `opentams version` lands
func init() {
	_ = version
	_ = commit
	_ = date
}
