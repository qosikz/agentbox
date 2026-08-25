package main

import "testing"

func TestResolveVersion_ldflagsWins(t *testing.T) {
	t.Parallel()
	if got := resolveVersion("v1.2.3"); got != "v1.2.3" {
		t.Fatalf("ldflags version lost: got %q", got)
	}
}

func TestResolveVersion_devFallback(t *testing.T) {
	t.Parallel()
	got := resolveVersion("dev")
	if got == "" {
		t.Fatal("empty version")
	}
}

func TestResolveVersion_empty(t *testing.T) {
	t.Parallel()
	got := resolveVersion("")
	if got == "" {
		t.Fatal("empty version")
	}
}
