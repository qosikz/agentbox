package adapters

import (
	"context"
	"fmt"
	"os/exec"
)

// ClaudeAdapter drives Claude Code in non-interactive print mode:
//
//	claude -p --permission-mode acceptEdits "<task>"
//
// acceptEdits lets the agent edit files in the workspace without interactive
// prompts while still blocking arbitrary command approval escalations.
//
// Auth note: Claude Code authenticates via OS keychain/OAuth (or
// ANTHROPIC_API_KEY). Keychain auth only works in local (unsafe) runtime
// mode; container runs need ANTHROPIC_API_KEY allowlisted in secrets.allow
// and the claude CLI installed in the runtime image.
type ClaudeAdapter struct{}

func (a ClaudeAdapter) Name() string { return "claude" }

func (a ClaudeAdapter) Check(ctx context.Context) error {
	if _, err := exec.LookPath("claude"); err != nil {
		return fmt.Errorf("claude (Claude Code) not found on PATH.\nInstall it: npm install -g @anthropic-ai/claude-code — or choose another --agent (%v)", err)
	}
	return nil
}

func (a ClaudeAdapter) BuildCommand(ctx context.Context, input Input) (Command, error) {
	args := []string{"-p", "--permission-mode", "acceptEdits", input.Task}
	args = append(args, input.ExtraArgs...)
	return Command{
		Executable: "claude",
		Args:       args,
		Env:        input.Env,
		WorkingDir: input.WorkspacePath,
	}, nil
}
