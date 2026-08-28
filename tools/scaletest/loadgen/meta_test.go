package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveTokenFlagFallback(t *testing.T) {
	got, err := resolveToken("flag-tok", "")
	if err != nil {
		t.Fatalf("resolveToken: %v", err)
	}
	if got != "flag-tok" {
		t.Errorf("got %q, want flag-tok", got)
	}
}

func TestResolveTokenFromFileTrims(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tok")
	// Trailing newline is common (`echo $JWT > tok`); it must be trimmed.
	if err := os.WriteFile(path, []byte("  real.jwt.value\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := resolveToken("flag-tok", path) // file wins over flag
	if err != nil {
		t.Fatalf("resolveToken: %v", err)
	}
	if got != "real.jwt.value" {
		t.Errorf("got %q, want real.jwt.value", got)
	}
}

func TestResolveTokenMissingFileErrors(t *testing.T) {
	if _, err := resolveToken("flag", "/no/such/token/file"); err == nil {
		t.Error("expected error for missing token file")
	}
}
