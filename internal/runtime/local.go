package runtime

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
)

// localRunner executes the agent command directly on the host, with NO
// container isolation. It is reached only after the user explicitly opts into
// unsafe local mode (runtime.isolation=local / --runtime local --unsafe). It
// still passes only the allowlisted environment from the spec/command — it does
// not inherit the host environment.
type localRunner struct{}

// NewLocalRunner returns a Runner that executes on the host (unsafe).
func NewLocalRunner() Runner { return localRunner{} }

func (localRunner) Name() string { return "local" }

// Available always succeeds: local execution has no external dependency.
func (localRunner) Available(ctx context.Context) error { return nil }

func (localRunner) Run(ctx context.Context, spec RuntimeSpec, command CommandSpec) (RunResult, error) {
	if command.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, command.Timeout)
		defer cancel()
	}

	c := exec.CommandContext(ctx, command.Executable, command.Args...)
	c.Dir = command.WorkingDir

	// Security: build the environment from the allowlist only — never inherit
	// the host environment (c.Env nil would inherit os.Environ()).
	env := make([]string, 0, len(spec.Env)+len(command.Env))
	for k, v := range spec.Env {
		env = append(env, k+"="+v)
	}
	for k, v := range command.Env {
		env = append(env, k+"="+v)
	}
	c.Env = env

	var stdout, stderr bytes.Buffer
	c.Stdout = &stdout
	c.Stderr = &stderr

	err := c.Run()
	res := RunResult{Stdout: stdout.String(), Stderr: stderr.String()}
	if exitErr, ok := err.(*exec.ExitError); ok {
		res.ExitCode = exitErr.ExitCode()
		return res, nil
	}
	if err != nil {
		res.ExitCode = -1
		return res, fmt.Errorf("local runner: %s: %w", command.Executable, err)
	}
	return res, nil
}
