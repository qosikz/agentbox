package runtime

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// secureSpec returns a RuntimeSpec representing AgentBox's secure defaults.
func secureSpec() RuntimeSpec {
	return RuntimeSpec{
		Engine:      "docker",
		Image:       "ghcr.io/qosi/agentbox:latest",
		NetworkMode: "none",
		Workdir:     "/work",
		User:        "10001:10001",
	}
}

// contains reports whether args contains the given single token.
func contains(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

// containsPair reports whether args contains a, immediately followed by b.
func containsPair(args []string, a, b string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == a && args[i+1] == b {
			return true
		}
	}
	return false
}

// containsSubstr reports whether any arg contains the given substring.
func containsSubstr(args []string, sub string) bool {
	for _, a := range args {
		if strings.Contains(a, sub) {
			return true
		}
	}
	return false
}

func TestBuildDockerArgs_SecureDefaults(t *testing.T) {
	args := BuildDockerArgs(secureSpec(), CommandSpec{Executable: "aider", Args: []string{"--help"}})

	if len(args) < 2 || args[0] != "run" || args[1] != "--rm" {
		t.Fatalf("expected args to start with [run --rm], got %v", args)
	}
	if !containsPair(args, "--network", "none") {
		t.Errorf("expected --network none, got %v", args)
	}
	if !contains(args, "--user") {
		t.Errorf("expected --user to be present, got %v", args)
	}
	if contains(args, "--privileged") {
		t.Errorf("must NOT contain --privileged by default, got %v", args)
	}
	if containsSubstr(args, "docker.sock") {
		t.Errorf("must NOT mount docker.sock by default, got %v", args)
	}
	// Image and exec must come after the flags, in order.
	if args[len(args)-3] != "ghcr.io/qosi/agentbox:latest" {
		t.Errorf("expected image before executable, got %v", args)
	}
	if args[len(args)-2] != "aider" || args[len(args)-1] != "--help" {
		t.Errorf("expected exec [aider --help] at end, got %v", args)
	}
}

func TestBuildDockerArgs_NetworkMode(t *testing.T) {
	tests := []struct {
		name    string
		mode    string
		wantNet string
	}{
		{"empty defaults to none", "", "none"},
		{"none", "none", "none"},
		{"deny maps to none", "deny", "none"},
		{"open maps to bridge", "open", "bridge"},
		{"bridge", "bridge", "bridge"},
		{"unknown defaults to none", "weird-value", "none"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := secureSpec()
			spec.NetworkMode = tt.mode
			args := BuildDockerArgs(spec, CommandSpec{Executable: "sh"})
			if !containsPair(args, "--network", tt.wantNet) {
				t.Errorf("mode %q: expected --network %s, got %v", tt.mode, tt.wantNet, args)
			}
		})
	}
}

func TestBuildDockerArgs_DockerSocket(t *testing.T) {
	tests := []struct {
		name  string
		mount bool
		want  bool
	}{
		{"default not mounted", false, false},
		{"explicitly mounted", true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := secureSpec()
			spec.MountDockerSocket = tt.mount
			args := BuildDockerArgs(spec, CommandSpec{Executable: "sh"})
			if got := containsSubstr(args, "docker.sock"); got != tt.want {
				t.Errorf("MountDockerSocket=%t: docker.sock present=%t, want %t (args=%v)", tt.mount, got, tt.want, args)
			}
		})
	}
}

func TestBuildDockerArgs_Privileged(t *testing.T) {
	tests := []struct {
		name string
		priv bool
		want bool
	}{
		{"default not privileged", false, false},
		{"explicitly privileged", true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := secureSpec()
			spec.Privileged = tt.priv
			args := BuildDockerArgs(spec, CommandSpec{Executable: "sh"})
			if got := contains(args, "--privileged"); got != tt.want {
				t.Errorf("Privileged=%t: --privileged present=%t, want %t (args=%v)", tt.priv, got, tt.want, args)
			}
		})
	}
}

func TestBuildDockerArgs_EnvDeterministicOrder(t *testing.T) {
	spec := secureSpec()
	spec.Env = map[string]string{
		"ZED":     "z-val",
		"ALPHA":   "a-val",
		"MIDDLE":  "m-val",
		"BRAVO":   "b-val",
		"OPENAI":  "secret-key",
		"CHARLIE": "c-val",
	}

	// Run repeatedly; output must be identical each time (deterministic).
	var first []string
	for i := 0; i < 5; i++ {
		args := BuildDockerArgs(spec, CommandSpec{Executable: "sh"})
		if first == nil {
			first = args
			continue
		}
		if strings.Join(args, "\x00") != strings.Join(first, "\x00") {
			t.Fatalf("BuildDockerArgs is not deterministic:\n run0=%v\n run%d=%v", first, i, args)
		}
	}

	// Each env entry must appear as "-e" "KEY=VALUE".
	if !containsPair(first, "-e", "ALPHA=a-val") {
		t.Errorf("expected -e ALPHA=a-val, got %v", first)
	}
	if !containsPair(first, "-e", "OPENAI=secret-key") {
		t.Errorf("expected -e OPENAI=secret-key, got %v", first)
	}

	// Verify keys are emitted in sorted order.
	var keys []string
	for i := 0; i+1 < len(first); i++ {
		if first[i] == "-e" {
			keys = append(keys, strings.SplitN(first[i+1], "=", 2)[0])
		}
	}
	want := []string{"ALPHA", "BRAVO", "CHARLIE", "MIDDLE", "OPENAI", "ZED"}
	if strings.Join(keys, ",") != strings.Join(want, ",") {
		t.Errorf("env keys not in sorted order: got %v, want %v", keys, want)
	}
}

func TestBuildDockerArgs_CommandEnvOverrides(t *testing.T) {
	spec := secureSpec()
	spec.Env = map[string]string{"SHARED": "from-spec", "ONLY_SPEC": "x"}
	cmd := CommandSpec{
		Executable: "sh",
		Env:        map[string]string{"SHARED": "from-command", "ONLY_CMD": "y"},
	}
	args := BuildDockerArgs(spec, cmd)

	if !containsPair(args, "-e", "SHARED=from-command") {
		t.Errorf("command env should override spec env, got %v", args)
	}
	if !containsPair(args, "-e", "ONLY_SPEC=x") {
		t.Errorf("expected spec-only env preserved, got %v", args)
	}
	if !containsPair(args, "-e", "ONLY_CMD=y") {
		t.Errorf("expected command-only env present, got %v", args)
	}
}

func TestBuildDockerArgs_Mounts(t *testing.T) {
	spec := secureSpec()
	spec.ReadOnlyPaths = []string{"/etc/ssl/certs"}
	spec.WritePaths = []string{"/work"}
	args := BuildDockerArgs(spec, CommandSpec{Executable: "sh"})

	if !containsPair(args, "-v", "/etc/ssl/certs:/etc/ssl/certs:ro") {
		t.Errorf("expected read-only mount, got %v", args)
	}
	if !containsPair(args, "-v", "/work:/work") {
		t.Errorf("expected read-write mount, got %v", args)
	}
}

func TestDryRunRunner_Run(t *testing.T) {
	r := NewDryRunRunner()
	if r.Name() != "dryrun" {
		t.Errorf("Name() = %q, want dryrun", r.Name())
	}
	if err := r.Available(context.Background()); err != nil {
		t.Errorf("Available() = %v, want nil", err)
	}

	spec := secureSpec()
	const secretVal = "sk-super-secret-value"
	spec.Env = map[string]string{"OPENAI_API_KEY": secretVal}
	cmd := CommandSpec{
		Executable: "aider",
		Args:       []string{"--model", "gpt-4o"},
		Env:        map[string]string{"ANTHROPIC_API_KEY": "another-secret"},
	}

	res, err := r.Run(context.Background(), spec, cmd)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !res.DryRun {
		t.Error("expected DryRun=true")
	}
	if res.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", res.ExitCode)
	}
	if len(res.Description) == 0 {
		t.Fatal("expected non-empty Description")
	}

	joined := strings.Join(res.Description, "\n")

	// Security: env values must never appear in the plan.
	if strings.Contains(joined, secretVal) {
		t.Errorf("Description leaked env value %q:\n%s", secretVal, joined)
	}
	if strings.Contains(joined, "another-secret") {
		t.Errorf("Description leaked command env value:\n%s", joined)
	}
	// But the keys should be listed.
	if !strings.Contains(joined, "OPENAI_API_KEY") {
		t.Errorf("expected env key listed, got:\n%s", joined)
	}
	if !strings.Contains(joined, "ANTHROPIC_API_KEY") {
		t.Errorf("expected command env key listed, got:\n%s", joined)
	}
	// Exec line should reflect the command.
	if !strings.Contains(joined, "exec: aider --model gpt-4o") {
		t.Errorf("expected exec line, got:\n%s", joined)
	}
}

func TestDryRunRunner_DockerSocketDescription(t *testing.T) {
	tests := []struct {
		name  string
		mount bool
		want  string
	}{
		{"safe default", false, "docker socket: not mounted"},
		{"unsafe", true, "docker socket: MOUNTED (unsafe)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := secureSpec()
			spec.MountDockerSocket = tt.mount
			res, err := NewDryRunRunner().Run(context.Background(), spec, CommandSpec{Executable: "sh"})
			if err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			if !strings.Contains(strings.Join(res.Description, "\n"), tt.want) {
				t.Errorf("expected %q in description, got:\n%s", tt.want, strings.Join(res.Description, "\n"))
			}
		})
	}
}

func TestDockerRunner_Name(t *testing.T) {
	if got := NewDockerRunner().Name(); got != "docker" {
		t.Errorf("Name() = %q, want docker", got)
	}
}

func TestDockerRunner_Available(t *testing.T) {
	r := NewDockerRunner()
	err := r.Available(context.Background())
	if _, lookErr := exec.LookPath("docker"); lookErr != nil {
		// docker not installed: Available must return an actionable error.
		if err == nil {
			t.Error("expected non-nil error when docker is not on PATH")
		}
		return
	}
	// docker present: Available must succeed (it does not contact the daemon).
	if err != nil {
		t.Errorf("Available() = %v, want nil when docker is on PATH", err)
	}
}

func TestDockerRunner_Run_SkipsWithoutDocker(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker binary not found; skipping real docker run")
	}
	// Use a trivially short timeout and a no-op image-less command path: we only
	// assert that the runner wires through to docker and returns a result. To
	// avoid pulling images or needing the daemon, run "docker --version"-style
	// behavior is not exercised here; instead we just confirm Available passes.
	r := NewDockerRunner()
	if err := r.Available(context.Background()); err != nil {
		t.Skipf("docker present but Available failed: %v", err)
	}

	// Exercise timeout plumbing with an immediately-cancelled context so we do
	// not depend on the daemon being reachable.
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	spec := secureSpec()
	spec.Image = "hello-world"
	_, err := r.Run(ctx, spec, CommandSpec{Executable: "true"})
	// Either a context error wrap or a docker start error is acceptable; we only
	// require that Run does not panic and returns.
	_ = err
}
