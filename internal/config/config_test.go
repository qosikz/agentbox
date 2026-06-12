package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultPolicySecureInvariants(t *testing.T) {
	p := DefaultPolicy()
	if p.Network.Mode != "deny" {
		t.Errorf("default network mode = %q, want deny", p.Network.Mode)
	}
	if p.Runtime.Isolation != "container" {
		t.Errorf("default isolation = %q, want container", p.Runtime.Isolation)
	}
	if !p.Runtime.Cleanup {
		t.Error("default cleanup should be true")
	}
	for _, want := range []string{".env", "~/.ssh", "~/.aws", "~/.kube"} {
		if !contains(p.Filesystem.Deny, want) {
			t.Errorf("default filesystem.deny missing %q", want)
		}
	}
	if len(p.Secrets.Allow) != 0 {
		t.Errorf("default secrets.allow should be empty, got %v", p.Secrets.Allow)
	}
}

func TestLoadMissingFileReturnsDefaults(t *testing.T) {
	p, err := LoadPolicy(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil {
		t.Fatalf("missing file should not error, got %v", err)
	}
	if p.Network.Mode != "deny" {
		t.Errorf("missing file should yield defaults, got network mode %q", p.Network.Mode)
	}
}

func TestLoadDecodesOverDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "andbo.yaml")
	// Omit cleanup and filesystem entirely: defaults must be preserved.
	yaml := "runtime:\n  isolation: container\n  engine: podman\n  image: custom:1\nnetwork:\n  mode: open\n"
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := LoadPolicy(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if p.Runtime.Engine != "podman" {
		t.Errorf("engine = %q, want podman", p.Runtime.Engine)
	}
	if !p.Runtime.Cleanup {
		t.Error("cleanup default true should survive omission")
	}
	if p.Network.Mode != "open" {
		t.Errorf("network mode = %q, want open", p.Network.Mode)
	}
	if !contains(p.Filesystem.Deny, ".env") {
		t.Error("default filesystem deny should survive omission of filesystem block")
	}
}

func TestLoadRejectsUnknownKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "andbo.yaml")
	if err := os.WriteFile(path, []byte("runtime:\n  bogus: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPolicy(path); err == nil {
		t.Fatal("expected error for unknown key, got nil")
	}
}

func TestLoadRejectsInvalidYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "andbo.yaml")
	if err := os.WriteFile(path, []byte("runtime: [unterminated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadPolicy(path)
	if err == nil {
		t.Fatal("expected error for malformed YAML")
	}
	if !strings.Contains(err.Error(), "andbo policy check") {
		t.Errorf("error should be actionable, got: %v", err)
	}
}

func TestWriteDefaultPolicyRefusesOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "andbo.yaml")
	if err := WriteDefaultPolicy(path); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := WriteDefaultPolicy(path); err == nil {
		t.Fatal("second write should refuse to overwrite")
	}
	// The written file must round-trip through the strict loader.
	if _, err := LoadPolicy(path); err != nil {
		t.Fatalf("written default policy failed to reload: %v", err)
	}
}

func TestCheckDetectsErrorsAndUnsafe(t *testing.T) {
	p := DefaultPolicy()
	p.Runtime.Isolation = "local"
	p.Network.Mode = "open"
	p.Secrets.Mode = "bogus"
	r := p.Check()
	if r.OK() {
		t.Error("policy with bad secrets.mode should not be OK")
	}
	if len(r.UnsafeOptions) < 2 {
		t.Errorf("expected unsafe options for local + open, got %v", r.UnsafeOptions)
	}
}

func TestCheckValidDefaultPolicyOK(t *testing.T) {
	if r := DefaultPolicy().Check(); !r.OK() {
		t.Errorf("default policy should be valid, errors: %v", r.Errors)
	}
}

// The shipped example policies must parse under the strict loader and validate.
func TestShippedExamplesAreValid(t *testing.T) {
	for _, name := range []string{"andbo.yaml", "andbo.strict.yaml"} {
		path := filepath.Join("..", "..", "examples", name)
		if _, err := os.Stat(path); err != nil {
			t.Skipf("example %s not present: %v", name, err)
		}
		p, err := LoadPolicy(path)
		if err != nil {
			t.Fatalf("example %s failed strict load: %v", name, err)
		}
		if r := p.Check(); !r.OK() {
			t.Errorf("example %s failed validation: %v", name, r.Errors)
		}
	}
}
