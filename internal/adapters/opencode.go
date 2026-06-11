package adapters

import (
	"context"
	"fmt"
	"os/exec"
)

// OpenCodeAdapter drives the opencode CLI in non-interactive run mode:
//
//	opencode run "<task>"
//
// "run" executes a single task and exits. Inside a fully isolated AgentBox
// container, users may pass --dangerously-skip-permissions via ExtraArgs;
// AgentBox's container isolation is then the enforcement boundary.
//
// Auth note: opencode reads provider API keys from the environment
// (ANTHROPIC_API_KEY, OPENAI_API_KEY, ...); allowlist the one for your
// provider in secrets.allow for container runs.
type OpenCodeAdapter struct{}

// Name returns the adapter identifier.
func (a OpenCodeAdapter) Name() string { return "opencode" }

// Check verifies the opencode binary is resolvable on PATH.
//
// Security: we only resolve the binary for availability here; we never run it.
// Execution is delegated to the runtime under the active sandbox policy.
func (a OpenCodeAdapter) Check(ctx context.Context) error {
	if _, err := exec.LookPath("opencode"); err != nil {
		return fmt.Errorf("opencode not found on PATH.\nInstall it: curl -fsSL https://opencode.ai/install | bash (or brew install anomalyco/tap/opencode) — or choose another --agent (%v)", err)
	}
	return nil
}

// BuildCommand renders the opencode invocation for the given input. The task
// is passed to "opencode run" for a single non-interactive run;
// input.ExtraArgs are appended after.
func (a OpenCodeAdapter) BuildCommand(ctx context.Context, input Input) (Command, error) {
	args := make([]string, 0, 2+len(input.ExtraArgs))
	args = append(args, "run", input.Task)
	args = append(args, input.ExtraArgs...)

	return Command{
		Executable: "opencode",
		Args:       args,
		Env:        input.Env,
		WorkingDir: input.WorkspacePath,
	}, nil
}
