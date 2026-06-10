package runtime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sort"
)

// dockerRunner executes commands inside a container via the real docker CLI.
//
// Security note: argument construction is delegated to the pure function
// BuildDockerArgs so that AgentBox's secure defaults (no docker socket mount,
// non-root user, no privileged mode, no host network) can be asserted by tests
// without invoking docker.
type dockerRunner struct{}

// NewDockerRunner returns a Runner backed by the docker CLI.
func NewDockerRunner() Runner { return dockerRunner{} }

func (dockerRunner) Name() string { return "docker" }

// Available reports whether the docker CLI is on PATH. It deliberately does NOT
// contact the docker daemon (which can block for a long time when the daemon is
// unreachable); a LookPath check is fast and sufficient to give an actionable
// error early.
func (dockerRunner) Available(ctx context.Context) error {
	if _, err := exec.LookPath("docker"); err != nil {
		return errors.New("Docker is not available. Install Docker, or use --dry-run, or run with --runtime local --unsafe (not recommended).")
	}
	return nil
}

// Run executes the command inside a container using the docker CLI. It honors
// command.Timeout (if > 0) by deriving a context with a deadline.
func (dockerRunner) Run(ctx context.Context, spec RuntimeSpec, command CommandSpec) (RunResult, error) {
	if err := (dockerRunner{}).Available(ctx); err != nil {
		return RunResult{ExitCode: -1}, err
	}

	if command.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, command.Timeout)
		defer cancel()
	}

	args := BuildDockerArgs(spec, command)

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	result := RunResult{
		Stdout: stdout.String(),
		Stderr: stderr.String(),
	}

	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			// Non-zero exit from the container/command is not a runner failure:
			// surface the exit code and let the caller decide.
			result.ExitCode = exitErr.ExitCode()
			return result, nil
		}
		// Context deadline/cancellation or a failure to start docker itself.
		result.ExitCode = -1
		if ctxErr := ctx.Err(); ctxErr != nil {
			return result, fmt.Errorf("docker run was interrupted (%v); the command may have exceeded its timeout: %w", ctxErr, err)
		}
		return result, fmt.Errorf("failed to execute docker; ensure the docker daemon is running: %w", err)
	}

	result.ExitCode = 0
	return result, nil
}

// BuildDockerArgs returns the docker CLI argument list (everything after the
// leading "docker" token, beginning with "run") that runs command inside a
// container described by spec.
//
// This is a PURE function with no side effects: it is the single place where
// AgentBox's secure container defaults are encoded, and security tests assert
// directly on its output. Notably it NEVER adds "--privileged" unless explicitly
// requested, NEVER mounts the docker socket unless explicitly requested, and
// maps unknown/empty network modes to the isolated "none" network.
func BuildDockerArgs(spec RuntimeSpec, command CommandSpec) []string {
	args := []string{"run", "--rm"}

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
