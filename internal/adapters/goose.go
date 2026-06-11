package adapters

import (
	"context"
	"fmt"
	"os/exec"
)

// GooseAdapter drives the goose CLI (Agentic AI Foundation, aaif-goose/goose)
// in headless run mode:
//
//	goose run --no-session -t "<task>"
//
// --no-session skips session persistence inside the throwaway workspace.
// GOOSE_MODE=auto is merged into the command environment (unless the caller
// already set GOOSE_MODE) because goose's approval modes refuse to run
// non-interactively.
//
// Auth note: goose selects its backend via GOOSE_PROVIDER/GOOSE_MODEL plus
// the matching provider API key env var (e.g. ANTHROPIC_API_KEY); allowlist
// those in secrets.allow for container runs.
type GooseAdapter struct{}

// Name returns the adapter identifier.
func (a GooseAdapter) Name() string { return "goose" }

// Check verifies the goose binary is resolvable on PATH.
//
// Security: we only resolve the binary for availability here; we never run it.
// Execution is delegated to the runtime under the active sandbox policy.
func (a GooseAdapter) Check(ctx context.Context) error {
	if _, err := exec.LookPath("goose"); err != nil {
		return fmt.Errorf("goose not found on PATH.\nInstall it: brew install block-goose-cli — or choose another --agent (%v)", err)
	}
	return nil
}

// BuildCommand renders the goose invocation for the given input. The task is
// passed via "-t" for a single headless run; input.ExtraArgs are appended
// after. The returned env is a copy of input.Env with GOOSE_MODE defaulted to
// "auto" — input.Env is never mutated, and a caller-provided GOOSE_MODE wins.
func (a GooseAdapter) BuildCommand(ctx context.Context, input Input) (Command, error) {
	args := make([]string, 0, 4+len(input.ExtraArgs))
	args = append(args, "run", "--no-session", "-t", input.Task)
	args = append(args, input.ExtraArgs...)

	env := make(map[string]string, len(input.Env)+1)
	for k, v := range input.Env {
		env[k] = v
	}
	if _, ok := env["GOOSE_MODE"]; !ok {
		env["GOOSE_MODE"] = "auto"
	}

	return Command{
		Executable: "goose",
		Args:       args,
		Env:        env,
		WorkingDir: input.WorkspacePath,
	}, nil
}
