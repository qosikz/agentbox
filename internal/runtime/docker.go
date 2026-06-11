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

// ProbeBinary reports whether bin is resolvable inside image by running a
// throwaway, fully-isolated probe container. See the Runner interface for the
// (present, err) contract. It is used to validate a baked-in agent before a
// container run — the host PATH is irrelevant when the agent lives in the
// image.
func (r containerRunner) ProbeBinary(ctx context.Context, image, bin string) (bool, error) {
	if err := r.Available(ctx); err != nil {
		return false, err
	}
	// Bound the probe so a wedged daemon cannot hang the whole run.
	pctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// Name the probe container so cancellation can force-remove it in the
	// daemon. With `docker run`, killing only the CLI client leaves the
	// container (and its --rm cleanup) behind under the daemon — the same leak
	// the Run path guards against; the preflight probe must not be weaker.
	name := containerName()
	args := insertContainerName(ProbeBinaryArgs(image, bin), name)

	var stderr bytes.Buffer
	cmd := exec.CommandContext(pctx, r.engine, args...)
	cmd.Stderr = &stderr
	cmd.Cancel = func() error {
		killCtx, c := context.WithTimeout(context.Background(), 15*time.Second)
		defer c()
		_ = exec.CommandContext(killCtx, r.engine, "rm", "-f", name).Run()
		return cmd.Process.Kill()
	}
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return probeResult(exitErr.ExitCode(), stderr.String(), r.engine, image)
		}
		// Could not even start the engine (e.g. context cancelled): inconclusive.
		return false, fmt.Errorf("probing image %q for %q via %s: %w", image, bin, r.engine, err)
	}
	return true, nil
}

// probeAbsentExit is a sentinel exit code our probe shell emits when the binary
// is conclusively absent. Using an explicit sentinel (rather than `command -v`'s
// own exit status) makes the result shell-agnostic: POSIX shells disagree on the
// failure code of `command -v` (bash returns 1, dash returns 127), and 127 also
// means "no shell in image", so relying on it would be ambiguous.
const probeAbsentExit = 42

// ProbeBinaryArgs returns the engine CLI arguments for a probe container that
// checks whether bin is resolvable inside image. The probe carries the same
// hardening as a real run (capabilities dropped, no privilege escalation, no
// network) and removes itself; it never mounts anything. PURE and testable.
func ProbeBinaryArgs(image, bin string) []string {
	return []string{
		"run", "--rm",
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		"--network", "none",
		// Non-root, matching the run path (buildRuntimeSpec). The host-PATH check
		// it replaces ran as the host user; the probe must not silently run as
		// root in images that declare no USER. command -v needs no privileges.
		"--user", "10001:10001",
		// Override any custom entrypoint so the probe is the shell test below,
		// not the agent itself (which could be long-running or interactive).
		"--entrypoint", "sh",
		image,
		// `--` ends option parsing so a binary name beginning with "-" is treated
		// as an operand, not a flag to `command`. The sentinel exit keeps the
		// present/absent decision shell-agnostic.
		"-c", "command -v -- " + shellSingleQuote(bin) + " >/dev/null 2>&1 && exit 0 || exit " + fmt.Sprint(probeAbsentExit),
	}
}

// probeResult maps a probe container's exit onto the ProbeBinary contract.
// PURE so the mapping can be asserted without an engine.
//
//   - 0                 shell ran and found bin              -> present.
//   - probeAbsentExit   shell ran and did NOT find bin        -> conclusively absent.
//   - else (125 engine, 127 no shell, ...)                    -> inconclusive: do
//     not block; the real run surfaces any genuine failure.
func probeResult(exitCode int, stderr, engine, image string) (bool, error) {
	switch exitCode {
	case 0:
		return true, nil
	case probeAbsentExit:
		return false, nil
	default:
		excerpt := strings.TrimSpace(stderr)
		const maxExcerpt = 160
		if len(excerpt) > maxExcerpt {
			excerpt = excerpt[:maxExcerpt] + "..."
		}
		msg := fmt.Sprintf("agent probe in image %q was inconclusive (%s exit %d)", image, engine, exitCode)
		if excerpt != "" {
			msg += ": " + excerpt
		}
		return false, errors.New(msg)
	}
}

// shellSingleQuote wraps s in single quotes, escaping any embedded single
// quotes, so it is safe to interpolate into a `sh -c` string. Agent binary
// names are normally plain, but a malformed policy must never let a value break
// out of the probe command.
func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
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
