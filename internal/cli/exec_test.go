package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func writeExecPolicy(t *testing.T, dir string, extra string) {
	t.Helper()
	pol := "tests:\n  commands: []\n" + extra
	if err := os.WriteFile(filepath.Join(dir, "agentbox.yaml"), []byte(pol), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestExecDryRunCreatesSession(t *testing.T) {
	dir := t.TempDir()
	writeExecPolicy(t, dir, "")
	chdir(t, dir)
	r := NewRoot("test", "none", "now")

	if err := r.cmdExec(context.Background(), []string{"echo hi", "--dry-run"}); err != nil {
		t.Fatalf("exec dry-run: %v (code %d)", err, CodeFor(err))
	}
	s := latestSession(t, dir)
	if s.Agent != "exec" {
		t.Errorf("agent = %q, want exec", s.Agent)
	}
	if !s.DryRun {
		t.Error("session should be dry-run")
	}
}

func TestExecLocalPassesThroughExitCode(t *testing.T) {
	dir := t.TempDir()
	writeExecPolicy(t, dir, "")
	chdir(t, dir)
	r := NewRoot("test", "none", "now")

	// Success path: exit 0.
	if err := r.cmdExec(context.Background(), []string{"--runtime", "local", "--yes-unsafe", "true"}); err != nil {
		t.Fatalf("exec true: %v", err)
	}
	// Failure path: the command's exit code 7 must pass through verbatim.
	err := r.cmdExec(context.Background(), []string{"--runtime", "local", "--yes-unsafe", "exit 7"})
	if CodeFor(err) != 7 {
		t.Errorf("exit code = %d, want pass-through 7 (err=%v)", CodeFor(err), err)
	}
}

func TestExecArgvForm(t *testing.T) {
	dir := t.TempDir()
	writeExecPolicy(t, dir, "")
	chdir(t, dir)
	r := NewRoot("test", "none", "now")

	if err := r.cmdExec(context.Background(), []string{"--runtime", "local", "--yes-unsafe", "--", "echo", "argv-form"}); err != nil {
		t.Fatalf("exec argv form: %v", err)
	}
	s := latestSession(t, dir)
	if !strings.Contains(s.Task, "echo argv-form") {
		t.Errorf("task = %q, want the argv command", s.Task)
	}
}

func TestExecRejectsCommitFlags(t *testing.T) {
	dir := t.TempDir()
	writeExecPolicy(t, dir, "")
	chdir(t, dir)
	r := NewRoot("test", "none", "now")
	err := r.cmdExec(context.Background(), []string{"--commit", "echo hi"})
	if err == nil || !strings.Contains(err.Error(), "agentbox run") {
		t.Errorf("exec --commit should be rejected with a pointer to run, got: %v", err)
	}
}

func TestExecMissingCommand(t *testing.T) {
	dir := t.TempDir()
	writeExecPolicy(t, dir, "")
	chdir(t, dir)
	r := NewRoot("test", "none", "now")
	err := r.cmdExec(context.Background(), []string{"--dry-run"})
	if CodeFor(err) != ExitGeneral || !strings.Contains(err.Error(), "missing command") {
		t.Errorf("missing command should be a clear general error, got: %v", err)
	}
}

// Regression: a pre-existing untracked DIRECTORY (e.g. a just-installed
// .claude/skills/... file) must not be attributed to the command. git status
// collapses an untracked dir to the dir path; the post-run diff expands it to
// files — baseline normalization (MarkIntentToAdd) must bridge that.
func TestExecDoesNotAttributePreexistingUntrackedDir(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	dir := t.TempDir()
	gitc := func(args ...string) {
		t.Helper()
		full := append([]string{"-C", dir, "-c", "user.email=a@b.c", "-c", "user.name=t"}, args...)
		if out, err := exec.Command("git", full...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	gitc("init")
	writeExecPolicy(t, dir, "")
	gitc("add", "-A")
	gitc("commit", "-m", "init")
	// Pre-existing untracked NESTED file, created after the commit.
	if err := os.MkdirAll(filepath.Join(dir, ".claude", "skills", "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude", "skills", "x", "SKILL.md"), []byte("pre\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	chdir(t, dir)
	r := NewRoot("test", "none", "now")
	if err := r.cmdExec(context.Background(), []string{"--runtime", "local", "--yes-unsafe", "echo nochange"}); err != nil {
		t.Fatalf("exec: %v (code %d)", err, CodeFor(err))
	}
	s := latestSession(t, dir)
	for _, f := range s.ChangedFiles {
		if strings.Contains(f, "SKILL.md") || strings.Contains(f, ".claude") {
			t.Errorf("pre-existing untracked file wrongly attributed to command: %v", s.ChangedFiles)
		}
	}
}

func TestExecExcludesSensitiveFiles(t *testing.T) {
	dir := t.TempDir()
	writeExecPolicy(t, dir, "")
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("S=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	chdir(t, dir)
	r := NewRoot("test", "none", "now")

	// The workspace copy must not contain .env: `cat .env` fails => exit != 0.
	err := r.cmdExec(context.Background(), []string{"--runtime", "local", "--yes-unsafe", "cat .env"})
	if CodeFor(err) == 0 {
		t.Error(".env must be excluded from the exec workspace (cat should fail)")
	}
	s := latestSession(t, dir)
	found := false
	for _, e := range s.PolicyEvents {
		if strings.Contains(e.Detail, ".env") {
			found = true
		}
	}
	if !found {
		t.Errorf(".env exclusion should be recorded, events: %v", s.PolicyEvents)
	}
}
