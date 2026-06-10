package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qosi/agentbox/internal/config"
	"github.com/qosi/agentbox/internal/session"
)

// chdir changes into dir for the duration of the test (Go 1.23 compatible;
// avoids testing.T.Chdir which requires Go 1.24).
func chdir(t *testing.T, dir string) {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
}

// setupProject creates a temp project (policy + a source file + a .env) and
// chdirs into it. It returns the project dir.
func setupProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := config.WriteDefaultPolicy(filepath.Join(dir, "agentbox.yaml")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("OPENAI_API_KEY=sk-shouldnotleak\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	chdir(t, dir)
	return dir
}

func latestSession(t *testing.T, base string) session.Session {
	t.Helper()
	list, err := session.List(base)
	if err != nil || len(list) == 0 {
		t.Fatalf("no sessions recorded under %s (err=%v)", base, err)
	}
	return list[0]
}

func TestRunDryRunCreatesSession(t *testing.T) {
	dir := setupProject(t)
	r := NewRoot("test", "none", "now")

	err := r.cmdRun(context.Background(), []string{"do a thing", "--dry-run"})
	if err != nil {
		t.Fatalf("dry-run should succeed, got err=%v code=%d", err, CodeFor(err))
	}

	s := latestSession(t, dir)
	if !s.DryRun {
		t.Error("session should be marked dry-run")
	}
	if s.Result.Status != "success" {
		t.Errorf("status = %q, want success", s.Result.Status)
	}
	if s.Agent != "custom" {
		t.Errorf("agent = %q, want custom", s.Agent)
	}
	// The dry run must not execute the agent: no real changes.
	if len(s.ChangedFiles) != 0 {
		t.Errorf("dry-run should produce no changed files, got %v", s.ChangedFiles)
	}
}

func TestRunRecordsEnvExclusionAndNetworkBlock(t *testing.T) {
	dir := setupProject(t)
	r := NewRoot("test", "none", "now")
	if err := r.cmdRun(context.Background(), []string{"task", "--dry-run"}); err != nil {
		t.Fatalf("run: %v", err)
	}

	s := latestSession(t, dir)
	var sawEnv, sawNet bool
	for _, e := range s.PolicyEvents {
		if strings.Contains(e.Detail, ".env") {
			sawEnv = true
		}
		if strings.Contains(e.Detail, "network") {
			sawNet = true
		}
	}
	if !sawEnv {
		t.Errorf(".env exclusion not recorded; events=%v", s.PolicyEvents)
	}
	if !sawNet {
		t.Errorf("network block not recorded; events=%v", s.PolicyEvents)
	}
}

func TestRunMissingTask(t *testing.T) {
	setupProject(t)
	r := NewRoot("test", "none", "now")
	err := r.cmdRun(context.Background(), []string{"--dry-run"})
	if CodeFor(err) != ExitGeneral {
		t.Errorf("missing task should be a general error, got code %d", CodeFor(err))
	}
}

func TestRunInvalidPolicyExit7(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "agentbox.yaml"), []byte("network:\n  mode: nonsense\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	chdir(t, dir)
	r := NewRoot("test", "none", "now")
	err := r.cmdRun(context.Background(), []string{"task", "--dry-run"})
	if CodeFor(err) != ExitInvalidConfig {
		t.Errorf("invalid policy should exit %d, got %d", ExitInvalidConfig, CodeFor(err))
	}
}

func TestParseRunFlags(t *testing.T) {
	o, err := parseRunFlags([]string{"fix tests", "--agent", "aider", "--write", "./a", "--write=./b", "--dry-run", "--network=open"})
	if err != nil {
		t.Fatal(err)
	}
	if o.task != "fix tests" {
		t.Errorf("task=%q", o.task)
	}
	if o.agent != "aider" {
		t.Errorf("agent=%q", o.agent)
	}
	if len(o.write) != 2 || o.write[0] != "./a" || o.write[1] != "./b" {
		t.Errorf("write=%v", o.write)
	}
	if !o.dryRun || o.network != "open" {
		t.Errorf("flags not parsed: %+v", o)
	}
}

func TestParseRunFlagsRepoPositional(t *testing.T) {
	o, err := parseRunFlags([]string{"github.com/org/repo", "--task", "add tests"})
	if err != nil {
		t.Fatal(err)
	}
	if o.repo != "github.com/org/repo" {
		t.Errorf("repo=%q", o.repo)
	}
	if o.task != "add tests" {
		t.Errorf("task=%q", o.task)
	}
}

func TestLooksLikeRepo(t *testing.T) {
	cases := map[string]bool{
		"github.com/org/repo":             true,
		"https://github.com/org/repo.git": true,
		"git@github.com:org/repo.git":     true,
		"fix failing tests":               false,
		"refactor the parser":             false,
	}
	for in, want := range cases {
		if got := looksLikeRepo(in); got != want {
			t.Errorf("looksLikeRepo(%q) = %v, want %v", in, got, want)
		}
	}
}
