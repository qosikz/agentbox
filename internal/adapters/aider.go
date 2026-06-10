package adapters

import (
	"context"
	"fmt"
	"os/exec"
)

// AiderAdapter invokes the aider coding agent (https://aider.chat).
//
// Limitation: aider is interactive by nature. We drive it non-interactively via
// "--message", which runs a single instruction and exits, but features that
// expect a TTY/REPL (interactive confirmations, in-session follow-ups) are not
// available through AgentBox.
type AiderAdapter struct{}

// Name returns the adapter identifier.
func (a AiderAdapter) Name() string { return "aider" }

// Check verifies the aider binary is resolvable on PATH.
//
// Security: we only resolve the binary for availability here; we never run it.
// Execution is delegated to the runtime under the active sandbox policy.
func (a AiderAdapter) Check(ctx context.Context) error {
	if _, err := exec.LookPath("aider"); err != nil {
		return fmt.Errorf("aider not found. Install it (e.g. pipx install aider-chat) or choose another --agent.")
	}
	return nil
}

// BuildCommand renders the aider invocation for the given input. The task is
// passed via "--message" for a single non-interactive run; input.ExtraArgs are
// appended after.
func (a AiderAdapter) BuildCommand(ctx context.Context, input Input) (Command, error) {
	args := make([]string, 0, 2+len(input.ExtraArgs))
	args = append(args, "--message", input.Task)
	args = append(args, input.ExtraArgs...)

	return Command{
		Executable: "aider",
		Args:       args,
		Env:        input.Env,
		WorkingDir: input.WorkspacePath,
	}, nil
}
