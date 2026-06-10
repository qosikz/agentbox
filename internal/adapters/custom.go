package adapters

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// CustomAdapter invokes a user-defined CLI agent. The user supplies the
// executable and its arguments; AgentBox substitutes the task into the args
// via the "{{ task }}" / "{{task}}" placeholders.
type CustomAdapter struct {
	Command string
	Args    []string
}

// Name returns the adapter identifier.
func (a CustomAdapter) Name() string { return "custom" }

// Check verifies the configured command is resolvable on PATH.
//
// Security: we only resolve the command for availability here; we never run it.
// Execution is delegated to the runtime under the active sandbox policy.
func (a CustomAdapter) Check(ctx context.Context) error {
	if a.Command == "" {
		return fmt.Errorf("custom agent has no command configured: set agent.custom.command in your policy")
	}
	if _, err := exec.LookPath(a.Command); err != nil {
		return fmt.Errorf("custom agent command %q not found on PATH: install it or fix agent.custom.command in your policy", a.Command)
	}
	return nil
}

// BuildCommand renders the custom command for the given input. Each configured
// arg has the task placeholders substituted, then input.ExtraArgs are appended.
func (a CustomAdapter) BuildCommand(ctx context.Context, input Input) (Command, error) {
	if a.Command == "" {
		return Command{}, fmt.Errorf("custom agent has no command configured: set agent.custom.command in your policy")
	}

	args := make([]string, 0, len(a.Args)+len(input.ExtraArgs))
	for _, arg := range a.Args {
		args = append(args, substituteTask(arg, input.Task))
	}
	args = append(args, input.ExtraArgs...)

	return Command{
		Executable: a.Command,
		Args:       args,
		Env:        input.Env,
		WorkingDir: input.WorkspacePath,
	}, nil
}

// substituteTask replaces the supported task placeholders with the task text.
func substituteTask(arg, task string) string {
	out := strings.ReplaceAll(arg, "{{ task }}", task)
	out = strings.ReplaceAll(out, "{{task}}", task)
	return out
}
