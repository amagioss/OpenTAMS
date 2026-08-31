package main

import "testing"

// TC-LOADGEN-CMD-01: constructing the command tree must not panic. The ramp
// command previously redefined the common --idem-keys flag, which pflag panics
// on at registration time (before any subcommand runs).
func TestNewRoot_NoDuplicateFlags(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("building command tree panicked: %v", r)
		}
	}()

	root := newRoot()
	if root == nil {
		t.Fatal("newRoot returned nil")
	}
}
