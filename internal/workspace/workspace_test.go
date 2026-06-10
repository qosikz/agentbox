package workspace

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/qosi/agentbox/internal/config"
	"github.com/qosi/agentbox/internal/policy"
)

// newRepo builds a temp repo with a known layout and returns its root.
func newRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		filepath.Join("src", "main.go"):          "package main\n",
		".env":                                   "SECRET=1\n",
		"README.md":                              "# readme\n",
		filepath.Join(".git", "HEAD"):            "ref: refs/heads/main\n",
		filepath.Join(".agentbox", "state.json"): "{}\n",
	}
	for rel, content := range files {
		abs := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", abs, err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", abs, err)
		}
	}
	return root
}

func defaultEffectivePolicy() policy.EffectivePolicy {
	return policy.BuildEffectivePolicy(config.DefaultPolicy(), "", policy.Overrides{})
}

func contains(set []string, v string) bool {
	for _, s := range set {
		if s == v {
			return true
		}
	}
	return false
}

func TestBuildPlan(t *testing.T) {
	root := newRepo(t)
	ep := defaultEffectivePolicy()

	plan, err := BuildPlan(root, ep)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}

	if plan.RepoRoot != filepath.Clean(root) {
		t.Errorf("RepoRoot = %q, want %q", plan.RepoRoot, filepath.Clean(root))
	}

	// ExcludedMounts must surface the existing sensitive .env file.
	if !contains(plan.ExcludedMounts, ".env") {
		t.Errorf("ExcludedMounts = %v, want to contain %q", plan.ExcludedMounts, ".env")
	}

	// WritePaths reflect the policy's write list.
	for _, w := range ep.Filesystem.Write {
		if !contains(plan.WritePaths, w) {
			t.Errorf("WritePaths = %v, missing policy write path %q", plan.WritePaths, w)
		}
	}
	if len(plan.WritePaths) != len(ep.Filesystem.Write) {
		t.Errorf("WritePaths len = %d, want %d", len(plan.WritePaths), len(ep.Filesystem.Write))
	}

	// DeniedPaths and ReadOnlyPaths mirror the policy.
	if len(plan.DeniedPaths) != len(ep.Filesystem.Deny) {
		t.Errorf("DeniedPaths len = %d, want %d", len(plan.DeniedPaths), len(ep.Filesystem.Deny))
	}
	if len(plan.ReadOnlyPaths) != len(ep.Filesystem.ReadOnly) {
		t.Errorf("ReadOnlyPaths len = %d, want %d", len(plan.ReadOnlyPaths), len(ep.Filesystem.ReadOnly))
	}

	// Returned slices must be independent copies, not aliases of the policy's.
	if len(plan.WritePaths) > 0 {
		plan.WritePaths[0] = "MUTATED"
		if ep.Filesystem.Write[0] == "MUTATED" {
			t.Error("BuildPlan returned an alias of ep.Filesystem.Write, not a copy")
		}
	}
}

func TestBuildPlanWarnsMissingWritePath(t *testing.T) {
	root := t.TempDir() // empty: ./src, ./tests, ./docs all missing
	ep := defaultEffectivePolicy()

	plan, err := BuildPlan(root, ep)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if len(plan.Warnings) == 0 {
		t.Errorf("expected warnings for missing write paths, got none")
	}
}

func TestBuildPlanExcludedMountsGlob(t *testing.T) {
	root := t.TempDir()
	// .env.local matches the mandatory ".env.*" deny pattern.
	if err := os.WriteFile(filepath.Join(root, ".env.local"), []byte("X=1\n"), 0o644); err != nil {
		t.Fatalf("write .env.local: %v", err)
	}
	plan, err := BuildPlan(root, defaultEffectivePolicy())
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if !contains(plan.ExcludedMounts, ".env.local") {
		t.Errorf("ExcludedMounts = %v, want to contain %q", plan.ExcludedMounts, ".env.local")
	}
}

func TestBuildPlanNonexistentRoot(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does", "not", "exist")
	if _, err := BuildPlan(missing, defaultEffectivePolicy()); err == nil {
		t.Fatalf("BuildPlan(%q) = nil error, want error", missing)
	}
}

func TestBuildPlanRootIsFile(t *testing.T) {
	f := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := BuildPlan(f, defaultEffectivePolicy()); err == nil {
		t.Fatalf("BuildPlan(file) = nil error, want error")
	}
}

func TestPrepare(t *testing.T) {
	root := newRepo(t)
	plan, err := BuildPlan(root, defaultEffectivePolicy())
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}

	dest := filepath.Join(t.TempDir(), "ws")
	if err := Prepare(plan, dest); err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	present := []string{
		"README.md",
		filepath.Join("src", "main.go"),
	}
	for _, rel := range present {
		if _, err := os.Stat(filepath.Join(dest, rel)); err != nil {
			t.Errorf("expected %q copied into workspace: %v", rel, err)
		}
	}

	absent := []string{
		".env",
		".git",
		".agentbox",
		filepath.Join(".git", "HEAD"),
		filepath.Join(".agentbox", "state.json"),
	}
	for _, rel := range absent {
		if _, err := os.Stat(filepath.Join(dest, rel)); !os.IsNotExist(err) {
			t.Errorf("expected %q NOT in workspace, stat err = %v", rel, err)
		}
	}
}

func TestPreparePreservesMode(t *testing.T) {
	root := t.TempDir()
	script := filepath.Join(root, "run.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	plan, err := BuildPlan(root, defaultEffectivePolicy())
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	dest := filepath.Join(t.TempDir(), "ws")
	if err := Prepare(plan, dest); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	info, err := os.Stat(filepath.Join(dest, "run.sh"))
	if err != nil {
		t.Fatalf("stat copied script: %v", err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Errorf("copied mode = %o, want 0755", info.Mode().Perm())
	}
}

func TestPrepareCreatesDest(t *testing.T) {
	root := newRepo(t)
	plan, err := BuildPlan(root, defaultEffectivePolicy())
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	// dest does not exist yet (nested path under a fresh temp dir).
	dest := filepath.Join(t.TempDir(), "a", "b", "ws")
	if err := Prepare(plan, dest); err != nil {
		t.Fatalf("Prepare should create dest: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "README.md")); err != nil {
		t.Errorf("README.md not copied into created dest: %v", err)
	}
}

func TestDenyMatch(t *testing.T) {
	tests := []struct {
		name   string
		rel    string
		denies []string
		want   bool
	}{
		{"exact literal", ".env", []string{".env"}, true},
		{"glob env suffix", ".env.local", []string{".env.*"}, true},
		{"inside denied dir", filepath.Join("secrets", "key.pem"), []string{"secrets"}, true},
		{"unrelated", filepath.Join("src", "main.go"), []string{".env", "secrets"}, false},
		{"home deny ignored at match", ".ssh", []string{"~/.ssh"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Filter home denies the way Prepare does.
			var denies []string
			for _, d := range tt.denies {
				if !isHomePath(d) {
					denies = append(denies, d)
				}
			}
			if got := denyMatch(tt.rel, denies); got != tt.want {
				t.Errorf("denyMatch(%q, %v) = %v, want %v", tt.rel, denies, got, tt.want)
			}
		})
	}
}
