package cli

import (
	"fmt"
	"os"

	"github.com/qosi/agentbox/internal/skill"
)

func (r *Root) cmdSkill(args []string) error {
	if len(args) == 0 {
		return codedf(ExitGeneral, "usage: agentbox skill <install|show|targets> [--target NAME] [--dir PATH] [--force]")
	}
	switch args[0] {
	case "show":
		fmt.Print(string(skill.Content()))
		return nil
	case "targets":
		return skillTargets()
	case "install":
		return skillInstall(args[1:])
	default:
		return codedf(ExitGeneral, "unknown skill command: %s", args[0])
	}
}

func homeDir() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return "~"
	}
	return h
}

func skillTargets() error {
	fmt.Println("Skill install targets (agentbox skill install --target NAME):")
	fmt.Println()
	for _, t := range skill.Targets(homeDir()) {
		fmt.Printf("  %-15s %s\n", t.Name, t.Description)
		fmt.Printf("  %-15s -> %s\n\n", "", t.Dir)
	}
	return nil
}

func skillInstall(args []string) error {
	target := flagValue(args, "--target", "claude-project")
	dir := flagValue(args, "--dir", "")
	force := hasFlag(args, "--force")

	if dir == "" {
		found := false
		for _, t := range skill.Targets(homeDir()) {
			if t.Name == target {
				dir, found = t.Dir, true
				break
			}
		}
		if !found {
			return codedf(ExitGeneral, "unknown skill target %q.\nRun 'agentbox skill targets' to see the options, or pass --dir <path>.", target)
		}
	}

	path, err := skill.Install(dir, force)
	if err != nil {
		return codedf(ExitGeneral, "%v\n\nPass --force to overwrite an existing skill.", err)
	}
	ok(os.Stdout, "Installed skill: "+path)
	fmt.Println()
	fmt.Println("The harness will discover it automatically. To also expose AgentBox as an")
	fmt.Println("MCP server (structured tools instead of shell calls):")
	fmt.Println()
	fmt.Println("  Claude Code:  claude mcp add agentbox -- agentbox mcp serve")
	fmt.Println("  OpenClaw:     openclaw mcp add agentbox --command agentbox --arg mcp --arg serve")
	fmt.Println("  Codex CLI:    codex mcp add agentbox -- agentbox mcp serve")
	fmt.Println("  Gemini CLI:   gemini mcp add agentbox agentbox mcp serve")
	return nil
}
