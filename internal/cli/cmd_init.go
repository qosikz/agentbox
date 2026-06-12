package cli

import (
	"fmt"
	"os"

	"github.com/qosikz/andbo/internal/config"
)

func (r *Root) cmdInit(args []string) error {
	jsonOut := hasFlag(args, "--json")
	path := flagValue(args, "--policy", "andbo.yaml")

	if err := config.WriteDefaultPolicy(path); err != nil {
		return codedf(ExitGeneral, "%v\n\nEdit the existing file, or remove it first if you want a fresh default.", err)
	}
	if err := os.MkdirAll(".andbo", 0o755); err != nil {
		return codedf(ExitGeneral, "creating .andbo/: %v", err)
	}

	if jsonOut {
		return printJSON(os.Stdout, map[string]any{"created": []string{path, ".andbo/"}})
	}
	ok(os.Stdout, "Created "+path)
	ok(os.Stdout, "Created .andbo/")
	fmt.Println("Next: andbo run \"fix failing tests\"")
	return nil
}
