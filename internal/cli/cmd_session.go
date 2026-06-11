package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/qosikz/agentbox/internal/session"
)

func (r *Root) cmdSession(args []string) error {
	if len(args) == 0 {
		return codedf(ExitGeneral, "usage: agentbox session <list|show|replay> [id|latest] [--json]")
	}
	base, _ := os.Getwd()
	jsonOut := hasFlag(args, "--json")

	switch args[0] {
	case "list":
		return sessionList(base, jsonOut)
	case "show":
		return sessionShow(base, sessionID(args[1:]), jsonOut)
	case "replay":
		return sessionReplay(base, sessionID(args[1:]))
	default:
		return codedf(ExitGeneral, "unknown session command: %s", args[0])
	}
}

// sessionID returns the first non-flag argument, defaulting to "latest".
func sessionID(args []string) string {
	for _, a := range args {
		if a != "" && a[0] != '-' {
			return a
		}
	}
	return "latest"
}

func sessionList(base string, jsonOut bool) error {
	list, err := session.List(base)
	if err != nil {
		return coded(ExitGeneral, err)
	}
	if jsonOut {
		return printJSON(os.Stdout, list)
	}
	if len(list) == 0 {
		fmt.Println("No sessions yet. Run: agentbox run \"fix failing tests\" --dry-run")
		return nil
	}
	fmt.Printf("%-26s %-8s %-9s %s\n", "ID", "AGENT", "STATUS", "TASK")
	for _, s := range list {
		fmt.Printf("%-26s %-8s %-9s %s\n", s.ID, s.Agent, s.Result.Status, s.Task)
	}
	return nil
}

func sessionShow(base, id string, jsonOut bool) error {
	s, err := session.Load(base, id)
	if err != nil {
		return coded(ExitGeneral, err)
	}
	if jsonOut {
		return printJSON(os.Stdout, s)
	}
	fmt.Print(session.RenderShow(s))
	fmt.Printf("\nArtifacts: %s\n", filepath.Join(session.SessionsDir(base), s.ID))
	return nil
}

func sessionReplay(base, id string) error {
	s, err := session.Load(base, id)
	if err != nil {
		return coded(ExitGeneral, err)
	}
	dir := filepath.Join(session.SessionsDir(base), s.ID)

	fmt.Printf("Replay of session %s\n\nTimeline:\n", s.ID)
	for _, ev := range s.Timeline {
		fmt.Printf("  %s  %s\n", ev.Time.Format("15:04:05"), ev.Event)
	}

	if logs, err := os.ReadFile(filepath.Join(dir, "logs.txt")); err == nil && len(logs) > 0 {
		fmt.Printf("\nLogs:\n%s\n", string(logs))
	}
	if diff, err := os.ReadFile(filepath.Join(dir, "diff.patch")); err == nil && len(diff) > 0 {
		fmt.Printf("\nDiff:\n%s\n", string(diff))
	}
	fmt.Println("\n(MVP replay re-displays the recorded run; it does not re-execute the agent.)")
	return nil
}
