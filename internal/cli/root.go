package cli

import (
	"fmt"
	"strings"

	"github.com/qosi/agentbox/internal/config"
)

type Root struct {
	Version string
	Commit  string
	Date    string
}

func NewRoot(version, commit, date string) *Root {
	return &Root{Version: version, Commit: commit, Date: date}
}

func (r *Root) Run(args []string) error {
	if len(args) == 0 {
		return r.help()
	}

	switch args[0] {
	case "version":
		fmt.Printf("agentbox %s commit=%s date=%s\n", r.Version, r.Commit, r.Date)
		return nil
	case "init":
		return config.WriteDefaultPolicy("agentbox.yaml")
	case "doctor":
		fmt.Println("AgentBox doctor")
		fmt.Println("TODO: check git, docker, policy, agents")
		return nil
	case "policy":
		if len(args) > 1 && args[1] == "check" {
			_, err := config.LoadPolicy("agentbox.yaml")
			if err != nil {
				return err
			}
			fmt.Println("✓ Policy valid")
			return nil
		}
		return fmt.Errorf("unknown policy command: %s", strings.Join(args[1:], " "))
	case "run":
		if len(args) < 2 {
			return fmt.Errorf("missing task: agentbox run \"fix failing tests\"")
		}
		fmt.Printf("AgentBox dry scaffold run: %s\n", strings.Join(args[1:], " "))
		fmt.Println("TODO: create workspace, apply policy, run adapter, save session")
		return nil
	case "session":
		fmt.Println("TODO: session commands")
		return nil
	case "mcp":
		fmt.Println("TODO: MCP Guard scanner")
		return nil
	default:
		return fmt.Errorf("unknown command: %s", args[0])
	}
}

func (r *Root) help() error {
	fmt.Println(`AgentBox — safe workspaces for AI coding agents

Usage:
  agentbox init
  agentbox run "fix failing tests"
  agentbox policy check
  agentbox doctor
  agentbox version`)
	return nil
}
