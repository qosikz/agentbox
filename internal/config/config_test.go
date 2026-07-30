package config

import (
	"os"
	"path/filepath"
	"strconv"
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

// A negative wall-clock budget is not a duration, and no surface agreed on what
// it meant: run and exec gate the deadline on `> 0` and so ran with NO deadline,
// while `k8s render` gated the same way and fell through to the renderer's own
// 1800s activeDeadlineSeconds — a bound the policy never asked for. Refusing it
// in Check is what makes the answer the same everywhere, because Check is the
// one validation every surface funnels through.
func TestCheckRejectsANegativeRuntimeBudget(t *testing.T) {
	// -30 is the shape that matters most: a sign typo on the default budget,
	// which reads as "30 minutes" to everyone but the code.
	for _, minutes := range []int{-1, -30, -1800} {
		p := DefaultPolicy()
		p.Budget.MaxRuntimeMinutes = minutes
		r := p.Check()
		if r.OK() {
			t.Errorf("budget.max_runtime_minutes = %d must be an error, got none", minutes)
			continue
		}
		// The exit code alone would be satisfied by any unrelated validation
		// failure, so the message has to name the field and the value written.
		var found string
		for _, e := range r.Errors {
			if strings.Contains(e, "budget.max_runtime_minutes") {
				found = e
			}
		}
		if found == "" {
			t.Errorf("errors for %d do not name the field: %v", minutes, r.Errors)
			continue
		}
		if !strings.Contains(found, strconv.Itoa(minutes)) {
			t.Errorf("error does not quote the value %d back: %q", minutes, found)
		}
	}
}

// Check errors have to be single-line. The CLI surfaces that act on a policy —
// run, exec, k8s render — print each one with warn(), which prefixes "! " and
// renders the string flat: a newline inside the message becomes a line with no
// prefix and no indent, reading as an unattributed stray line rather than part
// of its own error. (`andbo policy check` indents continuations; the other
// three do not, so the message cannot rely on that.)
//
// The invariant is layer-local, and deliberately so: it covers the errors Check
// itself produces, NOT everything that ends up in a CheckResult.Errors slice.
// cmd_policy appends checkBudgetMinutes' deliberately multi-line error to that
// same slice, which is fine because only the indenting printer ever sees it —
// but route that one through a warn() surface later and it grows the identical
// orphan line, somewhere this test does not look.
func TestCheckErrorsAreSingleLine(t *testing.T) {
	p := DefaultPolicy()
	// Provoke as many errors at once as the checker can produce, so this covers
	// every message rather than whichever one a future edit happens to add.
	p.Runtime.Isolation = "bogus"
	p.Runtime.Engine = "bogus"
	p.Runtime.Image = ""
	p.Network.Mode = "bogus"
	p.Network.Ports = []int{0, 70000}
	p.Secrets.Mode = "bogus"
	p.MCP.Mode = "bogus"
	p.Budget.MaxRuntimeMinutes = -30
	r := p.Check()
	if len(r.Errors) < 8 {
		t.Fatalf("expected every checked field to error, got %d: %v", len(r.Errors), r.Errors)
	}
	for _, e := range r.Errors {
		if strings.Contains(e, "\n") {
			t.Errorf("error contains a newline, which warn() renders as an orphan line:\n%q", e)
		}
	}
}

// Zero is the documented way to say "no deadline" and stays valid. Pinned next
// to the refusal so the boundary between them is a decision, not an accident.
func TestCheckAcceptsZeroAndPositiveRuntimeBudgets(t *testing.T) {
	for _, minutes := range []int{0, 1, 30, 1440} {
		p := DefaultPolicy()
		p.Budget.MaxRuntimeMinutes = minutes
		if r := p.Check(); !r.OK() {
			t.Errorf("budget.max_runtime_minutes = %d must be valid, errors: %v", minutes, r.Errors)
		}
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
