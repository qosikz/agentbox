// Package adapters translates an AgentBox task + policy into an agent-specific
// command. AgentBox is not itself an agent; adapters are the seam where real
// coding agents (custom, claude, codex, ...) plug in.
//
// This file is the FROZEN CONTRACT for the package. Implementations
// (custom adapter, the per-agent adapters, registry) live in sibling files and
// must not redefine these symbols.
package adapters

import "context"

// Input is everything an adapter needs to build a command.
type Input struct {
	Task          string
	WorkspacePath string
	Env           map[string]string
	ExtraArgs     []string
}

// Command is a runnable agent invocation. The runtime maps it onto a
// runtime.CommandSpec.
type Command struct {
	Executable string
	Args       []string
	Env        map[string]string
	WorkingDir string
}

// Result is a parsed adapter outcome.
type Result struct {
	Success bool
	Summary string
}

// Adapter adapts a coding agent to AgentBox's invocation model.
type Adapter interface {
	// Name is the adapter identifier (e.g. "custom", "claude").
	Name() string
	// Check returns nil if the agent is available/usable, else an actionable
	// error (missing binary, missing credentials, ...).
	Check(ctx context.Context) error
	// BuildCommand renders the agent command for the given input.
	BuildCommand(ctx context.Context, input Input) (Command, error)
}
