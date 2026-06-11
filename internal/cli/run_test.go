package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	o, err := parseRunFlags([]string{"fix tests", "--agent", "codex", "--write", "./a", "--write=./b", "--dry-run", "--network=open"})
	if err != nil {
		t.Fatal(err)
	}
	if o.task != "fix tests" {
		t.Errorf("task=%q", o.task)
	}
	if o.agent != "codex" {
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

func TestParseRunFlagsEngine(t *testing.T) {
	o, err := parseRunFlags([]string{"task", "--engine", "podman"})
	if err != nil {
		t.Fatal(err)
	}
	if o.engine != "podman" {
		t.Errorf("engine = %q, want podman", o.engine)
	}
}

func TestRunInvalidEngineFlag(t *testing.T) {
	setupProject(t)
	r := NewRoot("test", "none", "now")
	err := r.cmdRun(context.Background(), []string{"task", "--engine", "rocketship", "--dry-run"})
	if CodeFor(err) != ExitInvalidConfig {
		t.Errorf("invalid --engine should exit %d, got %d (err=%v)", ExitInvalidConfig, CodeFor(err), err)
	}
}

// noTestsPolicy is a minimal echo-agent policy with no test commands, used by
// real-run tests so they do not trigger `go test` in a bare temp dir.
const noTestsPolicy = "agent:\n  default: custom\n  custom:\n    command: echo\n    args:\n      - \"{{ task }}\"\ntests:\n  commands: []\n"

func TestRunCleanupRemovesWorkspace(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "agentbox.yaml"), []byte(noTestsPolicy), 0o644); err != nil {
		t.Fatal(err)
	}
	chdir(t, dir)
	r := NewRoot("test", "none", "now")
	if err := r.cmdRun(context.Background(), []string{"hello", "--runtime", "local", "--yes-unsafe"}); err != nil {
		t.Fatalf("run: %v (code %d)", err, CodeFor(err))
	}
	// cleanup defaults to true: the disposable work dir must be gone.
	entries, _ := os.ReadDir(filepath.Join(dir, ".agentbox", "work"))
	if len(entries) != 0 {
		t.Errorf("expected .agentbox/work to be cleaned, found %d entries", len(entries))
	}
	// Session artifacts are always kept.
	if _, err := session.List(dir); err != nil {
		t.Errorf("session should survive cleanup: %v", err)
	}
}

func TestRunCleanupFalseKeepsWorkspace(t *testing.T) {
	dir := t.TempDir()
	pol := noTestsPolicy + "runtime:\n  isolation: container\n  engine: docker\n  image: img\n  cleanup: false\n"
	if err := os.WriteFile(filepath.Join(dir, "agentbox.yaml"), []byte(pol), 0o644); err != nil {
		t.Fatal(err)
	}
	chdir(t, dir)
	r := NewRoot("test", "none", "now")
	if err := r.cmdRun(context.Background(), []string{"hello", "--runtime", "local", "--yes-unsafe"}); err != nil {
		t.Fatalf("run: %v (code %d)", err, CodeFor(err))
	}
	entries, _ := os.ReadDir(filepath.Join(dir, ".agentbox", "work"))
	if len(entries) == 0 {
		t.Error("cleanup: false should keep the workspace copy for debugging")
	}
}

// Regression for SEC-CLEANUP-DELETES-COMMIT: a --commit branch must survive
// workspace cleanup by being propagated back into the source repository.
func TestRunCommitSurvivesCleanup(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	// Hermetic git environment: no host/global identity, like a CI runner.
	// The commit must still succeed via the AgentBox fallback identity.
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	dir := t.TempDir()
	gitc := func(args ...string) {
		t.Helper()
		full := append([]string{"-C", dir, "-c", "user.email=a@b.c", "-c", "user.name=t", "-c", "commit.gpgsign=false"}, args...)
		if out, err := exec.Command("git", full...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	gitc("init")
	gitc("checkout", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitc("add", "-A")
	gitc("commit", "-m", "init")
	// Agent creates a file; default cleanup=true; commit requested.
	pol := "agent:\n  default: custom\n  custom:\n    command: sh\n    args: [\"-c\", \"echo agent > agent.txt\"]\ntests:\n  commands: []\n"
	if err := os.WriteFile(filepath.Join(dir, "agentbox.yaml"), []byte(pol), 0o644); err != nil {
		t.Fatal(err)
	}
	gitc("add", "-A")
	gitc("commit", "-m", "policy")
	chdir(t, dir)

	r := NewRoot("test", "none", "now")
	if err := r.cmdRun(context.Background(), []string{"add agent file", "--runtime", "local", "--yes-unsafe", "--commit"}); err != nil {
		t.Fatalf("run: %v (code %d)", err, CodeFor(err))
	}

	// The workspace must be cleaned AND the branch must exist in the source repo.
	if entries, _ := os.ReadDir(filepath.Join(dir, ".agentbox", "work")); len(entries) != 0 {
		t.Errorf("workspace should be cleaned after successful propagation, found %d entries", len(entries))
	}
	out, err := exec.Command("git", "-C", dir, "branch", "--list", "agentbox/*").Output()
	if err != nil || !strings.Contains(string(out), "agentbox/") {
		t.Errorf("agentbox branch missing from source repo after cleanup; branches: %q err=%v", out, err)
	}
	// And the commit on that branch must contain the agent's file.
	s := latestSession(t, dir)
	if s.Branch == "" {
		t.Fatal("session should record the branch")
	}
	show, err := exec.Command("git", "-C", dir, "show", "--stat", s.Branch).Output()
	if err != nil || !strings.Contains(string(show), "agent.txt") {
		t.Errorf("propagated branch should contain agent.txt, got: %q err=%v", show, err)
	}
}

// Regression for GO-CORRECTNESS-005: budget enforcement must be testable and
// report an actionable budget error, not a generic agent failure.
func TestRunBudgetExceeded(t *testing.T) {
	old := budgetWindow
	budgetWindow = func(int) time.Duration { return 150 * time.Millisecond }
	defer func() { budgetWindow = old }()

	dir := t.TempDir()
	pol := "agent:\n  default: custom\n  custom:\n    command: sleep\n    args: [\"5\"]\nbudget:\n  max_runtime_minutes: 1\ntests:\n  commands: []\n"
	if err := os.WriteFile(filepath.Join(dir, "agentbox.yaml"), []byte(pol), 0o644); err != nil {
		t.Fatal(err)
	}
	chdir(t, dir)

	r := NewRoot("test", "none", "now")
	start := time.Now()
	err := r.cmdRun(context.Background(), []string{"sleep forever", "--runtime", "local", "--yes-unsafe"})
	if time.Since(start) > 3*time.Second {
		t.Fatalf("budget window not applied; run took %v", time.Since(start))
	}
	if CodeFor(err) != ExitAgentFailed {
		t.Fatalf("budget kill should exit %d, got %d (err=%v)", ExitAgentFailed, CodeFor(err), err)
	}
	if err == nil || !strings.Contains(err.Error(), "budget exceeded") {
		t.Errorf("error should mention the budget, got: %v", err)
	}
	// The session must carry the budget policy event.
	s := latestSession(t, dir)
	found := false
	for _, e := range s.PolicyEvents {
		if strings.Contains(e.Detail, "max_runtime_minutes") {
			found = true
		}
	}
	if !found {
		t.Errorf("budget policy event missing; events: %v", s.PolicyEvents)
	}
}

func TestRunInvalidRuntimeFlag(t *testing.T) {
	setupProject(t)
	r := NewRoot("test", "none", "now")
	err := r.cmdRun(context.Background(), []string{"task", "--runtime", "hypervisor", "--dry-run"})
	if CodeFor(err) != ExitInvalidConfig {
		t.Errorf("invalid --runtime should exit %d, got %d (err=%v)", ExitInvalidConfig, CodeFor(err), err)
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
