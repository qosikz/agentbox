package adapters

import (
	"context"
	"fmt"
	"os/exec"
)

// CodexAdapter drives the OpenAI Codex CLI in headless exec mode:
//
//	codex exec --sandbox workspace-write "<task>"
//
// "exec" runs a single non-interactive turn and exits; --sandbox
// workspace-write lets the agent edit files in the workspace without
// interactive approval prompts.
//
// Auth note: Codex authenticates via ChatGPT OAuth (~/.codex/auth.json) or
// CODEX_API_KEY. OAuth state lives on the host, so container runs need
// CODEX_API_KEY allowlisted in secrets.allow and the codex CLI installed in
// the runtime image.
//
// Sandbox note: Codex ships its own Landlock/Seatbelt sandbox, which can
// conflict with container runtimes. Inside a fully isolated AgentBox
// container, users may pass --dangerously-bypass-approvals-and-sandbox via
// ExtraArgs; AgentBox's container isolation is then the enforcement boundary.
type CodexAdapter struct{}

// Name returns the adapter identifier.
func (a CodexAdapter) Name() string { return "codex" }

// Check verifies the codex binary is resolvable on PATH.
//
// Security: we only resolve the binary for availability here; we never run it.
// Execution is delegated to the runtime under the active sandbox policy.
func (a CodexAdapter) Check(ctx context.Context) error {
	if _, err := exec.LookPath("codex"); err != nil {
		return fmt.Errorf("codex (OpenAI Codex CLI) not found on PATH.\nInstall it: npm install -g @openai/codex — or choose another --agent (%v)", err)
	}
	return nil
}

// BuildCommand renders the codex invocation for the given input. The task is
// passed to "codex exec" for a single headless run; input.ExtraArgs are
// appended after.
func (a CodexAdapter) BuildCommand(ctx context.Context, input Input) (Command, error) {
	args := make([]string, 0, 4+len(input.ExtraArgs))
	args = append(args, "exec", "--sandbox", "workspace-write", input.Task)
	args = append(args, input.ExtraArgs...)

	return Command{
		Executable: "codex",
		Args:       args,
		Env:        input.Env,
		WorkingDir: input.WorkspacePath,
	}, nil
}
