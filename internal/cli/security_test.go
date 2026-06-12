package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qosikz/andbo/internal/config"
	"github.com/qosikz/andbo/internal/policy"
	"github.com/qosikz/andbo/internal/runtime"
	"github.com/qosikz/andbo/internal/session"
	"github.com/qosikz/andbo/internal/workspace"
)

// These tests map to Andbo's required security acceptance criteria —
// the secure-by-default boundaries summarized in SECURITY.md.

func defaultSpec(t *testing.T, ov policy.Overrides, o runOptions) runtime.RuntimeSpec {
	t.Helper()
	ep := policy.BuildEffectivePolicy(config.DefaultPolicy(), "andbo.yaml", ov)
	plan := workspace.Plan{RepoRoot: "/repo", WritePaths: ep.Filesystem.Write}
	return buildRuntimeSpec(ep, plan, "/repo", map[string]string{}, o)
}

// §8.1 .env is not in the runtime mount set. §8.2/8.3 ~/.ssh, ~/.aws never
// mounted. §8.5 docker socket not mounted. §8.6 network deny -> none.
func TestRuntimeSpecSecureDefaults(t *testing.T) {
	spec := defaultSpec(t, policy.Overrides{}, runOptions{}) // real run

	if spec.Privileged {
		t.Error("runtime must never be privileged by default")
	}
	if spec.MountDockerSocket {
		t.Error("docker socket must not be mounted by default")
	}
	if spec.NetworkMode != "none" {
		t.Errorf("network mode = %q, want none (deny)", spec.NetworkMode)
	}
	if spec.User == "" || strings.HasPrefix(spec.User, "0:") || spec.User == "root" {
		t.Errorf("runtime must run as non-root, got user %q", spec.User)
	}
	for _, m := range append(append([]string{}, spec.WritePaths...), spec.ReadOnlyPaths...) {
		for _, bad := range []string{".env", ".ssh", ".aws", ".kube"} {
			if strings.Contains(m, bad) {
				t.Errorf("sensitive path %q must not be mounted, found in %q", bad, m)
			}
		}
	}
}

// §8.5 cross-check at the docker-arg layer: no --privileged, no docker.sock,
// and deny maps to --network none.
func TestDockerArgsSecureInvariants(t *testing.T) {
	spec := defaultSpec(t, policy.Overrides{}, runOptions{})
	args := runtime.BuildDockerArgs(spec, runtime.CommandSpec{Executable: "echo", Args: []string{"hi"}})
	joined := strings.Join(args, " ")

	if strings.Contains(joined, "--privileged") {
		t.Error("docker args must not contain --privileged")
	}
	if strings.Contains(joined, "docker.sock") {
		t.Error("docker args must not mount the docker socket")
	}
	if !strings.Contains(joined, "--network none") {
		t.Errorf("deny network must map to --network none, args=%v", args)
	}
}

// §8.8 unsafe network mode must require confirmation; non-interactive refuses.
func TestUnsafeRealRunRefusedNonInteractive(t *testing.T) {
	dir := t.TempDir()
	if err := config.WriteDefaultPolicy(filepath.Join(dir, "andbo.yaml")); err != nil {
		t.Fatal(err)
	}
	chdir(t, dir)
	r := NewRoot("test", "none", "now")
	// Real run (no --dry-run) with an unsafe override, no --yes-unsafe.
	err := r.cmdRun(context.Background(), []string{"task", "--network", "open"})
	if CodeFor(err) != ExitUnsafeRequired {
		t.Errorf("unsafe real run should exit %d, got %d (err=%v)", ExitUnsafeRequired, CodeFor(err), err)
	}
}

// §8.4 secret-like values are redacted from saved logs.
func TestRunRedactsSecretsInSavedLogs(t *testing.T) {
	dir := t.TempDir()
	// Default secure policy but with no test commands, so the local run executes
	// only the echo agent (running go test in a bare temp dir would fail).
	const pol = "agent:\n  default: custom\n  custom:\n    command: echo\n    args:\n      - \"{{ task }}\"\ntests:\n  commands: []\n"
	if err := os.WriteFile(filepath.Join(dir, "andbo.yaml"), []byte(pol), 0o644); err != nil {
		t.Fatal(err)
	}
	chdir(t, dir)
	const secret = "sk-LEAKsecretvalue0123456789"
	t.Setenv("OPENAI_API_KEY", secret)

	r := NewRoot("test", "none", "now")
	// Local unsafe run so the echo agent actually executes and prints the task,
	// which contains the secret value verbatim.
	err := r.cmdRun(context.Background(), []string{"leak " + secret + " now", "--runtime", "local", "--yes-unsafe"})
	if err != nil && CodeFor(err) != ExitOK {
		t.Fatalf("local run failed: %v (code %d)", err, CodeFor(err))
	}

	sessions, lerr := session.List(dir)
	if lerr != nil || len(sessions) == 0 {
		t.Fatalf("no session recorded: %v", lerr)
	}
	logsPath := filepath.Join(session.SessionsDir(dir), sessions[0].ID, "logs.txt")
	data, rerr := os.ReadFile(logsPath)
	if rerr != nil {
		t.Fatalf("read logs: %v", rerr)
	}
	if strings.Contains(string(data), secret) {
		t.Errorf("secret value leaked into logs.txt:\n%s", data)
	}
	if !strings.Contains(string(data), "REDACTED") {
		t.Errorf("expected a redaction placeholder in logs, got:\n%s", data)
	}
}

// Env hygiene: the host PATH/HOME must never be forwarded into a container.
// Containers get a standard Linux PATH; HOME is set to the workspace by
// buildRuntimeSpec, never to the host home.
func TestContainerEnvExcludesHostPathAndHome(t *testing.T) {
	hostPath := os.Getenv("PATH")
	ep := policy.BuildEffectivePolicy(config.DefaultPolicy(), "", policy.Overrides{})
	env := buildAgentEnv(ep)
	if env["PATH"] == hostPath {
		t.Error("container env must not contain the host PATH")
	}
	if env["PATH"] != containerPATH {
		t.Errorf("container PATH = %q, want standard Linux PATH", env["PATH"])
	}
	if _, hasHome := env["HOME"]; hasHome {
		t.Error("buildAgentEnv must not leak host HOME into containers (HOME is set to the workspace later)")
	}

	// The runtime spec sets HOME to the workspace, not the host home.
	spec := buildRuntimeSpec(ep, workspace.Plan{}, "/work/dir", env, runOptions{})
	if spec.Env["HOME"] != "/work/dir" {
		t.Errorf("container HOME = %q, want the workspace /work/dir", spec.Env["HOME"])
	}

	// Local (unsafe) runs do need the host PATH to function.
	epLocal := policy.BuildEffectivePolicy(config.DefaultPolicy(), "", policy.Overrides{Runtime: "local"})
	localEnv := buildAgentEnv(epLocal)
	if got := localEnv["PATH"]; got != hostPath {
		t.Errorf("local-run PATH = %q, want host PATH", got)
	}
	// USER must be forwarded in local mode: OS keychains (e.g. Claude Code's
	// macOS auth) and git identity fallback fail without it. Regression for a
	// real-agent run that died with "Not logged in".
	if hostUser := os.Getenv("USER"); hostUser != "" && localEnv["USER"] != hostUser {
		t.Errorf("local-run USER = %q, want host USER %q", localEnv["USER"], hostUser)
	}
	// Containers must still NOT get the host USER.
	if _, hasUser := env["USER"]; hasUser {
		t.Error("container env must not contain host USER")
	}
}

// §8.9 host-home access only with explicit flag; otherwise home denies hold.
func TestAllowHostHomeRequiresFlag(t *testing.T) {
	without := policy.BuildEffectivePolicy(config.DefaultPolicy(), "", policy.Overrides{})
	hasHomeDeny := false
	for _, d := range without.Filesystem.Deny {
		if d == "~/.ssh" {
			hasHomeDeny = true
		}
	}
	if !hasHomeDeny {
		t.Error("~/.ssh must be denied by default")
	}
}
