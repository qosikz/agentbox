package runtime

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// containerRunner executes commands inside a container via a docker-compatible
// engine CLI ("docker" or "podman" — podman is CLI-compatible with every
// argument AgentBox generates).
//
// Security note: argument construction is delegated to the pure function
// BuildDockerArgs so that AgentBox's secure defaults (capabilities dropped,
// no privilege escalation, no docker socket mount, non-root user, no
// privileged mode, no host network) can be asserted by tests without invoking
// the engine.
type containerRunner struct {
	engine         string // CLI binary to invoke: "docker" or "podman"
	unavailableMsg string // actionable error shown when the binary is missing
}

// NewDockerRunner returns a Runner backed by the docker CLI.
func NewDockerRunner() Runner {
	return containerRunner{
		engine:         "docker",
		unavailableMsg: "Docker is not available. Install Docker, or use --dry-run, or run with --runtime local --unsafe (not recommended).",
	}
}

func (r containerRunner) Name() string { return r.engine }

// Available reports whether the engine CLI is on PATH. It deliberately does
// NOT contact the engine daemon (which can block for a long time when the
// daemon is unreachable); a LookPath check is fast and sufficient to give an
// actionable error early.
func (r containerRunner) Available(ctx context.Context) error {
	if _, err := exec.LookPath(r.engine); err != nil {
		return errors.New(r.unavailableMsg)
	}
	return nil
}

// Run executes the command inside a container using the engine CLI. It honors
// command.Timeout (if > 0) by deriving a context with a deadline.
func (r containerRunner) Run(ctx context.Context, spec RuntimeSpec, command CommandSpec) (RunResult, error) {
	if err := r.Available(ctx); err != nil {
		return RunResult{ExitCode: -1}, err
	}

	if command.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, command.Timeout)
		defer cancel()
	}

	// Name the container so cancellation can kill it authoritatively. Killing
	// only the CLI client leaves the container running under the daemon — the
	// exact failure mode a budget deadline exists to prevent.
	name := containerName()
	args := insertContainerName(BuildDockerArgs(spec, command), name)

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, r.engine, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// Security: on context cancellation (budget deadline, Ctrl-C), force-remove
	// the container in the daemon FIRST, then kill the client process. A fresh
	// context is required because ctx is already cancelled at this point.
	cmd.Cancel = func() error {
		killCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = exec.CommandContext(killCtx, r.engine, "rm", "-f", name).Run()
		return cmd.Process.Kill()
	}
	// Bound Wait so it cannot hang on lingering pipe readers after the kill.
	cmd.WaitDelay = 5 * time.Second

	err := cmd.Run()

	result := RunResult{
		Stdout: stdout.String(),
		Stderr: stderr.String(),
	}

	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			result.ExitCode = exitErr.ExitCode()
			// Exit code 125 means the ENGINE failed (missing image, daemon
			// error): the agent never ran, so surface it as a runner error
			// instead of letting callers misreport it as an agent failure.
			if engErr := engineFailureError(r.engine, spec.Image, result.ExitCode, result.Stderr); engErr != nil {
				return result, engErr
			}
			// Other non-zero exits (including 126/127) come from the agent
			// command inside a working container: surface the exit code and
			// let the caller decide.
			return result, nil
		}
		// Context deadline/cancellation or a failure to start the engine itself.
		result.ExitCode = -1
		if ctxErr := ctx.Err(); ctxErr != nil {
			return result, fmt.Errorf("%s run was interrupted (%v); the command may have exceeded its timeout: %w", r.engine, ctxErr, err)
		}
		return result, fmt.Errorf("failed to execute %s; ensure the %s daemon/service is running: %w", r.engine, r.engine, err)
	}

	result.ExitCode = 0
	return result, nil
}

// engineFailureMarkers are stderr fragments (lowercase) that identify a
// container-engine failure as opposed to the agent's own output. Exit code 125
// alone is ambiguous: docker/podman reserve it for engine failures, but an
// agent process can also legitimately exit 125 inside a working container.
var engineFailureMarkers = []string{
	"unable to find image",                // docker: image missing locally
	"error response from daemon",          // docker: daemon-side failure
	"pull access denied",                  // docker/podman: image pull denied
	"manifest unknown",                    // registry: tag does not exist
	"cannot connect to the docker daemon", // docker: daemon down
	"no such image",                       // docker/podman
	"short-name",                          // podman: unqualified image resolution
	"initializing source",                 // podman: image pull failure
	"image not known",                     // podman: image missing locally
	"docker: 'docker run' requires",       // docker: malformed argv
}

// engineFailureError maps a container-engine CLI exit onto an error.
//
// It returns a non-nil, actionable error only when the exit code is 125 AND
// stderr carries a recognizable engine-failure marker — meaning the agent
// never ran. Every other case — including 125 without a marker, 126 (command
// not executable), and 127 (command not found) — is attributed to the agent
// command inside a working container and returns nil, leaving the exit code
// for the caller.
//
// This is a PURE function so tests can assert the mapping without an engine.
func engineFailureError(engine, image string, exitCode int, stderr string) error {
	if exitCode != 125 {
		return nil
	}
	lower := strings.ToLower(stderr)
	matched := false
	for _, m := range engineFailureMarkers {
		if strings.Contains(lower, m) {
			matched = true
			break
		}
	}
	if !matched {
		return nil
	}
	excerpt := strings.TrimSpace(stderr)
	const maxExcerpt = 200
	if len(excerpt) > maxExcerpt {
		excerpt = excerpt[:maxExcerpt] + "..."
	}
	msg := fmt.Sprintf("%s could not start the container with image %q (exit code 125); the agent never ran", engine, image)
	if excerpt != "" {
		msg += ": " + excerpt
	}
	msg += fmt.Sprintf(". Build or pull the runtime image, e.g. `%s build -t %s -f examples/runtime.Dockerfile examples/`, or run with --dry-run.", engine, image)
	return errors.New(msg)
}

// BuildDockerArgs returns the engine CLI argument list (everything after the
// leading "docker"/"podman" token, beginning with "run") that runs command
// inside a container described by spec. Podman accepts the exact same
// arguments, so both engines share this builder.
//
// This is a PURE function with no side effects: it is the single place where
// AgentBox's secure container defaults are encoded, and security tests assert
// directly on its output. Notably it NEVER adds "--privileged" unless explicitly
// requested, NEVER mounts the docker socket unless explicitly requested, and
// maps unknown/empty network modes to the isolated "none" network.
func BuildDockerArgs(spec RuntimeSpec, command CommandSpec) []string {
	// Security hardening, always on: drop every Linux capability and forbid
	// privilege escalation (setuid/setgid binaries cannot regain privileges).
	// An agent has no legitimate need for kernel capabilities, and these flags
	// are supported identically by docker and podman.
	args := []string{
		"run", "--rm",
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
	}

	// Network: default-deny. Only an explicit open/bridge request enables the
	// bridge network; everything else (including unknown values) is isolated.
	switch spec.NetworkMode {
	case "open", "bridge":
		args = append(args, "--network", "bridge")
	default: // "none", "deny", "" and any unrecognized value
		args = append(args, "--network", "none")
	}

	if spec.User != "" {
		args = append(args, "--user", spec.User)
	}

	// Security: privileged mode and the docker socket mount are opt-in only.
	// Mounting the socket grants full control of the host docker daemon, so it
	// is never added implicitly.
	if spec.Privileged {
		args = append(args, "--privileged")
	}
	if spec.MountDockerSocket {
		args = append(args, "-v", "/var/run/docker.sock:/var/run/docker.sock")
	}

	if spec.Workdir != "" {
		args = append(args, "-w", spec.Workdir)
	}

	// Environment is passed in sorted key order for deterministic, testable
	// output. Merge command.Env over spec.Env (command wins on conflict).
	for _, k := range sortedEnvKeysForDocker(spec.Env, command.Env) {
		v := spec.Env[k]
		if cv, ok := command.Env[k]; ok {
			v = cv
		}
		args = append(args, "-e", k+"="+v)
	}

	for _, p := range spec.ReadOnlyPaths {
		args = append(args, "-v", p+":"+p+":ro")
	}
	for _, p := range spec.WritePaths {
		args = append(args, "-v", p+":"+p)
	}

	args = append(args, spec.Image)
	args = append(args, command.Executable)
	args = append(args, command.Args...)

	return args
}

// containerName returns a unique container name so cancellation can target
// the container in the daemon (see cmd.Cancel in Run).
func containerName() string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand should never fail; a fixed fallback only risks a name
		// collision, which the engine reports as a startup error.
		return "agentbox-run"
	}
	return "agentbox-" + hex.EncodeToString(b)
}

// insertContainerName injects "--name <name>" right after "run --rm" in an
// argument list produced by BuildDockerArgs. Pure and testable.
func insertContainerName(args []string, name string) []string {
	if len(args) < 2 {
		return args
	}
	out := make([]string, 0, len(args)+2)
	out = append(out, args[0], args[1], "--name", name)
	out = append(out, args[2:]...)
	return out
}

// sortedEnvKeysForDocker returns the union of keys across the given maps in
// sorted order, for deterministic argument generation.
func sortedEnvKeysForDocker(maps ...map[string]string) []string {
	seen := make(map[string]struct{})
	for _, m := range maps {
		for k := range m {
			seen[k] = struct{}{}
		}
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
