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
		Image:       "ghcr.io/qosikz/agentbox:latest",
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
	args := BuildDockerArgs(secureSpec(), CommandSpec{Executable: "agent-cli", Args: []string{"--help"}})

	if len(args) < 2 || args[0] != "run" || args[1] != "--rm" {
		t.Fatalf("expected args to start with [run --rm], got %v", args)
	}
	// Hardening flags must always be present (valid for docker and podman).
	if !containsPair(args, "--cap-drop", "ALL") {
		t.Errorf("expected --cap-drop ALL, got %v", args)
	}
	if !containsPair(args, "--security-opt", "no-new-privileges") {
		t.Errorf("expected --security-opt no-new-privileges, got %v", args)
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
	if args[len(args)-3] != "ghcr.io/qosikz/agentbox:latest" {
		t.Errorf("expected image before executable, got %v", args)
	}
	if args[len(args)-2] != "agent-cli" || args[len(args)-1] != "--help" {
		t.Errorf("expected exec [agent-cli --help] at end, got %v", args)
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
		Executable: "agent-cli",
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
	if !strings.Contains(joined, "exec: agent-cli --model gpt-4o") {
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

func TestPodmanRunner_Name(t *testing.T) {
	if got := NewPodmanRunner().Name(); got != "podman" {
		t.Errorf("Name() = %q, want podman", got)
	}
}

func TestPodmanRunner_Available(t *testing.T) {
	r := NewPodmanRunner()
	err := r.Available(context.Background())
	if _, lookErr := exec.LookPath("podman"); lookErr != nil {
		// podman not installed: Available must return an actionable error that
		// tells the user what to do instead.
		if err == nil {
			t.Fatal("expected non-nil error when podman is not on PATH")
		}
		for _, want := range []string{"podman is not available", "--engine docker", "--dry-run"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("Available() error %q missing actionable hint %q", err, want)
			}
		}
		return
	}
	// podman present: Available must succeed (it does not contact the service).
	if err != nil {
		t.Errorf("Available() = %v, want nil when podman is on PATH", err)
	}
}

func TestEngineFailureError(t *testing.T) {
	const image = "ghcr.io/qosikz/agentbox:latest"
	tests := []struct {
		name     string
		engine   string
		exitCode int
		stderr   string
		wantErr  bool
	}{
		{"exit 0 success", "docker", 0, "", false},
		{"exit 1 agent failure", "docker", 1, "test suite failed", false},
		{"exit 126 not executable", "docker", 126, "permission denied", false},
		{"exit 127 command not found", "podman", 127, "agent-cli: not found", false},
		{"exit 125 docker engine failure", "docker", 125, "Unable to find image 'ghcr.io/qosikz/agentbox:latest' locally", true},
		{"exit 125 podman engine failure", "podman", 125, "Error: ghcr.io/qosikz/agentbox:latest: image not known", true},
		{"exit 125 daemon failure", "docker", 125, "docker: Error response from daemon: something broke", true},
		// 125 WITHOUT a recognizable engine marker is the agent's own exit code
		// inside a working container: not an engine failure.
		{"exit 125 empty stderr is agent exit", "docker", 125, "", false},
		{"exit 125 agent stderr is agent exit", "docker", 125, "my-agent: fatal: budget spent", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := engineFailureError(tt.engine, image, tt.exitCode, tt.stderr)
			if !tt.wantErr {
				if err != nil {
					t.Fatalf("engineFailureError(%d) = %v, want nil", tt.exitCode, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("engineFailureError(%d) = nil, want error", tt.exitCode)
			}
			msg := err.Error()
			if !strings.Contains(msg, image) {
				t.Errorf("error must mention image %q, got: %s", image, msg)
			}
			if !strings.Contains(msg, tt.engine) {
				t.Errorf("error must mention engine %q, got: %s", tt.engine, msg)
			}
			if excerpt := strings.TrimSpace(tt.stderr); excerpt != "" && !strings.Contains(msg, excerpt) {
				t.Errorf("error must include stderr excerpt %q, got: %s", excerpt, msg)
			}
			if !strings.Contains(msg, "--dry-run") {
				t.Errorf("error must hint at --dry-run, got: %s", msg)
			}
		})
	}
}

func TestEngineFailureError_TruncatesStderr(t *testing.T) {
	// Marker prefix makes it a genuine engine failure; the long tail must be cut.
	longStderr := "Error response from daemon: " + strings.Repeat("x", 1000)
	err := engineFailureError("docker", "img:latest", 125, longStderr)
	if err == nil {
		t.Fatal("expected error for exit code 125 with engine marker")
	}
	msg := err.Error()
	if strings.Contains(msg, longStderr) {
		t.Errorf("stderr excerpt should be truncated to ~200 chars, full stderr found in: %s", msg)
	}
	if !strings.Contains(msg, "...") {
		t.Errorf("expected truncated excerpt with ellipsis, got: %s", msg)
	}
}

func TestInsertContainerName(t *testing.T) {
	args := insertContainerName([]string{"run", "--rm", "--cap-drop", "ALL", "img", "echo"}, "agentbox-abc123")
	want := []string{"run", "--rm", "--name", "agentbox-abc123", "--cap-drop", "ALL", "img", "echo"}
	if len(args) != len(want) {
		t.Fatalf("len = %d, want %d (%v)", len(args), len(want), args)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Errorf("args[%d] = %q, want %q", i, args[i], want[i])
		}
	}
}

func TestContainerNameUnique(t *testing.T) {
	a, b := containerName(), containerName()
	if a == b {
		t.Errorf("container names should be unique, got %q twice", a)
	}
	if !strings.HasPrefix(a, "agentbox-") {
		t.Errorf("container name should be agentbox-prefixed, got %q", a)
	}
}

// --- ProbeBinary helpers (pure, no engine required) ---

func TestProbeBinaryArgs_Hardened(t *testing.T) {
	args := ProbeBinaryArgs("agentbox/codex:latest", "codex")
	// Same hardening as a real run, plus full network isolation and self-removal.
	if len(args) < 2 || args[0] != "run" || args[1] != "--rm" {
		t.Fatalf("probe must start with `run --rm`, got %v", args)
	}
	for _, want := range [][2]string{
		{"--cap-drop", "ALL"},
		{"--security-opt", "no-new-privileges"},
		{"--network", "none"},
		{"--user", "10001:10001"}, // non-root, same as the run path
		{"--entrypoint", "sh"},
	} {
		if !containsPair(args, want[0], want[1]) {
			t.Errorf("probe args missing %q %q: %v", want[0], want[1], args)
		}
	}
	// The probe must NEVER mount anything, expose the docker socket, run
	// privileged, or run as root.
	for _, banned := range []string{"-v", "--privileged", "/var/run/docker.sock", "0:0"} {
		if contains(args, banned) {
			t.Errorf("probe args must not contain %q: %v", banned, args)
		}
	}
	// Image precedes the shell command, which tests the binary (with `--` so a
	// dash-prefixed name is an operand, not a flag) and emits the shell-agnostic
	// sentinel exit code when the binary is absent.
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "agentbox/codex:latest -c command -v -- 'codex'") {
		t.Errorf("probe command shape unexpected: %v", args)
	}
	if !strings.Contains(joined, "exit 42") {
		t.Errorf("probe must emit the sentinel absent exit code (42): %v", args)
	}
}

func TestProbeBinaryArgs_QuotesBinary(t *testing.T) {
	// A hostile/malformed binary name must not break out of the sh -c string.
	args := ProbeBinaryArgs("img", "x'; rm -rf /; '")
	last := args[len(args)-1]
	if strings.Contains(last, "rm -rf /;") && !strings.Contains(last, `'\''`) {
		t.Errorf("binary name was not safely single-quoted: %q", last)
	}
}

func TestProbeResult_Mapping(t *testing.T) {
	tests := []struct {
		name        string
		exit        int
		stderr      string
		wantPresent bool
		wantErr     bool
	}{
		{"present", 0, "", true, false},
		{"absent sentinel", probeAbsentExit, "", false, false},
		{"engine error 125", 125, "Unable to find image", false, true},
		{"no shell 127", 127, "exec: \"sh\": not found", false, true},
		{"command-v dash 127", 127, "", false, true},
		{"weird 126", 126, "permission denied", false, true},
		{"stray exit 1 inconclusive", 1, "", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			present, err := probeResult(tt.exit, tt.stderr, "docker", "img")
			if present != tt.wantPresent {
				t.Errorf("present = %v, want %v", present, tt.wantPresent)
			}
			if (err != nil) != tt.wantErr {
				t.Errorf("err = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestDryRunRunner_ProbeBinaryAlwaysPresent(t *testing.T) {
	ok, err := NewDryRunRunner().ProbeBinary(context.Background(), "any", "anything")
	if !ok || err != nil {
		t.Errorf("dry-run probe = (%v,%v), want (true,nil)", ok, err)
	}
}

func TestLocalRunner_ProbeBinaryUsesHostPath(t *testing.T) {
	r := NewLocalRunner()
	// A binary that is essentially always present on the host PATH.
	if ok, err := r.ProbeBinary(context.Background(), "ignored", "sh"); err != nil || !ok {
		t.Errorf("local probe for sh = (%v,%v), want (true,nil)", ok, err)
	}
	if ok, err := r.ProbeBinary(context.Background(), "ignored", "definitely-not-a-real-binary-xyzzy"); err != nil || ok {
		t.Errorf("local probe for missing binary = (%v,%v), want (false,nil)", ok, err)
	}
}
