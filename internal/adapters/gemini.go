package adapters

import (
	"context"
	"fmt"
	"os/exec"
)

// GeminiAdapter drives the Google Gemini CLI in non-interactive prompt mode:
//
//	gemini --approval-mode auto_edit -p "<task>"
//
// auto_edit auto-approves only edit tools — the same conservative posture as
// the claude adapter's acceptEdits. Inside a fully isolated AgentBox
// container, users may pass --approval-mode yolo via ExtraArgs for full
// auto-approval.
//
// Auth note: headless runs authenticate via GEMINI_API_KEY, which must be
// allowlisted in secrets.allow for container runs; the interactive OAuth
// login flow hangs in headless environments.
//
// Exit codes: the Gemini CLI documents fatal exit codes 41 (auth),
// 42 (input), 44 (sandbox), 52 (config), and 53 (turn limit), which is useful
// when diagnosing failed sessions.
type GeminiAdapter struct{}

// Name returns the adapter identifier.
func (a GeminiAdapter) Name() string { return "gemini" }

// Check verifies the gemini binary is resolvable on PATH.
//
// Security: we only resolve the binary for availability here; we never run it.
// Execution is delegated to the runtime under the active sandbox policy.
func (a GeminiAdapter) Check(ctx context.Context) error {
	if _, err := exec.LookPath("gemini"); err != nil {
		return fmt.Errorf("gemini (Google Gemini CLI) not found on PATH.\nInstall it: npm install -g @google/gemini-cli — or choose another --agent (%v)", err)
	}
	return nil
}

// BuildCommand renders the gemini invocation for the given input. The task is
// passed via "-p" for a single non-interactive run; input.ExtraArgs are
// appended after.
func (a GeminiAdapter) BuildCommand(ctx context.Context, input Input) (Command, error) {
	args := make([]string, 0, 4+len(input.ExtraArgs))
	args = append(args, "--approval-mode", "auto_edit", "-p", input.Task)
	args = append(args, input.ExtraArgs...)

	return Command{
		Executable: "gemini",
		Args:       args,
		Env:        input.Env,
		WorkingDir: input.WorkspacePath,
	}, nil
}
