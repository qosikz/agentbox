// Package cli implements Andbo command parsing and routing. It contains no
// business logic beyond dispatch and presentation; the work lives in the
// internal packages it orchestrates.
package cli

import (
	"context"
	"fmt"
	"strings"
)

// Root is the top-level command dispatcher.
type Root struct {
	Version string
	Commit  string
	Date    string
}

// NewRoot constructs the dispatcher.
func NewRoot(version, commit, date string) *Root {
	return &Root{Version: version, Commit: commit, Date: date}
}

// Run dispatches args (os.Args[1:]) and returns an error that may carry an
// exit code (see CodedError / CodeFor).
func (r *Root) Run(args []string) error {
	if len(args) == 0 {
		return r.help()
	}

	ctx := context.Background()
	switch args[0] {
	case "version", "--version", "-v":
		return r.cmdVersion()
	case "help", "--help", "-h":
		return r.help()
	case "init":
		return r.cmdInit(args[1:])
	case "doctor":
		return r.cmdDoctor(args[1:])
	case "policy":
		return r.cmdPolicy(args[1:])
	case "run":
		return r.cmdRun(ctx, args[1:])
	case "exec":
		return r.cmdExec(ctx, args[1:])
	case "session":
		return r.cmdSession(args[1:])
	case "mcp":
		return r.cmdMCP(args[1:])
	case "skill":
		return r.cmdSkill(args[1:])
	case "shell":
		return codedf(ExitGeneral, "andbo shell is not implemented in the MVP.\nUse 'andbo run \"<task>\"' to run an agent in a workspace.")
	default:
		return codedf(ExitGeneral, "unknown command: %s\n\nRun 'andbo help' to see available commands.", args[0])
	}
}

func (r *Root) cmdVersion() error {
	fmt.Printf("andbo %s commit=%s date=%s\n", r.Version, r.Commit, r.Date)
	return nil
}

func (r *Root) help() error {
	fmt.Println(strings.TrimSpace(helpText))
	return nil
}

const helpText = `
Andbo — disposable sandboxes for AI coding agents

Usage:
  andbo <command> [flags]

Commands:
  init                       Create andbo.yaml and .andbo/
  run "<task>"               Run an agent in an isolated, policy-controlled workspace
  exec "<command>"           Run a command in an isolated workspace (for agents/harnesses)
  policy check               Validate policy and show the effective configuration
  mcp scan <path>            Statically scan an MCP server for dangerous capabilities
  mcp serve                  Serve Andbo sandbox tools over MCP (stdio)
  skill install              Install the Andbo skill into an agent harness
  session list               List recorded sessions
  session show [id|latest]   Show a session record
  session replay [id|latest] Replay a session timeline, logs, and diff
  doctor                     Diagnose local setup (git, docker, agents, config)
  version                    Print version information

Examples:
  andbo init
  andbo run "fix failing tests" --dry-run
  andbo run github.com/org/repo --task "add tests" --open-pr
  andbo run "refactor parser" --network deny --write ./src --write ./tests
  andbo mcp scan ./mcp-server

Unsafe modes (--network open, --runtime local, --allow-host-home,
--allow-docker-socket) require explicit confirmation or --yes-unsafe in CI.

Global:
  --json   Machine-readable output (supported by most commands)
`
