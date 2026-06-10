package cli

import (
	"fmt"
	"os"

	"github.com/qosi/agentbox/internal/config"
)

func (r *Root) cmdInit(args []string) error {
	jsonOut := hasFlag(args, "--json")
	path := flagValue(args, "--policy", "agentbox.yaml")

	if err := config.WriteDefaultPolicy(path); err != nil {
		return codedf(ExitGeneral, "%v\n\nEdit the existing file, or remove it first if you want a fresh default.", err)
	}
	if err := os.MkdirAll(".agentbox", 0o755); err != nil {
		return codedf(ExitGeneral, "creating .agentbox/: %v", err)
	}

	if jsonOut {
		return printJSON(os.Stdout, map[string]any{"created": []string{path, ".agentbox/"}})
	}
	ok(os.Stdout, "Created "+path)
	ok(os.Stdout, "Created .agentbox/")
	fmt.Println("Next: agentbox run \"fix failing tests\"")
	return nil
}
