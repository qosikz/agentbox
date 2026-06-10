// Package runtime abstracts the execution backend for an agent command.
//
// This file is the FROZEN CONTRACT for the package: the types and interface
// every other package compiles against. Implementations (dry-run runner,
// docker runner, security-property helpers) live in sibling files and must not
// redefine these symbols.
package runtime

import (
	"context"
	"time"
)

// RuntimeSpec describes the isolated environment an agent command runs in.
// The security-relevant fields encode AgentBox's secure defaults: no docker
// socket, non-root user, no privileged mode.
type RuntimeSpec struct {
	Engine            string            // docker | podman | dryrun
	Image             string            // container image
	NetworkMode       string            // none | bridge (enforced mapping of policy)
	Workdir           string            // working directory inside the container
	Env               map[string]string // allowlisted environment only
	User              string            // non-root user, e.g. "10001:10001"
	Privileged        bool              // must be false by default
	MountDockerSocket bool              // must be false by default
	ReadOnlyPaths     []string          // host paths mounted read-only
	WritePaths        []string          // host paths mounted read-write
}

// CommandSpec is the agent command to execute inside the runtime.
type CommandSpec struct {
	Executable string
	Args       []string
	Env        map[string]string
	WorkingDir string
	Timeout    time.Duration
}

// RunResult is the outcome of executing a command.
type RunResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
	DryRun   bool
	// Description holds the human-readable plan of intended runtime actions
	// (used by the dry-run runner so users can inspect what *would* happen).
	Description []string
}

// Runner executes a command under a RuntimeSpec.
type Runner interface {
	// Name identifies the runner (e.g. "dryrun", "docker").
	Name() string
	// Available returns nil if the backend can run, or an actionable error
	// (e.g. Docker not installed) otherwise.
	Available(ctx context.Context) error
	// Run executes command under spec.
	Run(ctx context.Context, spec RuntimeSpec, command CommandSpec) (RunResult, error)
}
