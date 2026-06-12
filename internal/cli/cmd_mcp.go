package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/qosikz/andbo/internal/config"
	"github.com/qosikz/andbo/internal/mcpguard"
	"github.com/qosikz/andbo/internal/mcpserve"
)

func (r *Root) cmdMCP(args []string) error {
	if len(args) == 0 {
		return codedf(ExitGeneral, "usage: andbo mcp <scan|list|serve> [path] [--json]")
	}
	switch args[0] {
	case "scan":
		return mcpScan(args[1:])
	case "list":
		return mcpList(args[1:])
	case "serve":
		return r.mcpServe()
	default:
		return codedf(ExitGeneral, "unknown mcp command: %s", args[0])
	}
}

// mcpServe runs the MCP stdio server until stdin closes. Only protocol
// messages go to stdout; all diagnostics go to stderr.
func (r *Root) mcpServe() error {
	fmt.Fprintln(os.Stderr, "andbo MCP server listening on stdio (policy: andbo.yaml in CWD)")
	srv := mcpserve.New(r.Version)
	if err := srv.Serve(context.Background(), os.Stdin, os.Stdout); err != nil {
		return coded(ExitGeneral, err)
	}
	return nil
}

func mcpScan(args []string) error {
	jsonOut := hasFlag(args, "--json")
	var target string
	for _, a := range args {
		if a != "" && a[0] != '-' {
			target = a
			break
		}
	}
	if target == "" {
		return codedf(ExitGeneral, "usage: andbo mcp scan <path> [--json]")
	}
	// Prefer a local path: only reject as "remote" when the target does not
	// exist on disk and looks like a remote repository reference.
	if _, statErr := os.Stat(target); statErr != nil && looksLikeRepo(target) {
		return codedf(ExitGeneral, "remote MCP scanning is not supported yet.\nClone the server locally and scan the path:\n  git clone %s && andbo mcp scan ./<dir>", target)
	}

	report, err := mcpguard.Scan(target)
	if err != nil {
		return coded(ExitGeneral, err)
	}

	if jsonOut {
		data, jerr := report.JSON()
		if jerr != nil {
			return coded(ExitGeneral, jerr)
		}
		fmt.Println(string(data))
	} else {
		fmt.Print(report.Human())
	}

	// Non-zero exit when the server is unsafe, so CI can gate on it.
	if report.Result == "unsafe" {
		return &CodedError{Code: ExitPolicyViolation, Err: errors.New("")}
	}
	return nil
}

func mcpList(args []string) error {
	jsonOut := hasFlag(args, "--json")
	path := flagValue(args, "--policy", "andbo.yaml")
	cfg, err := config.LoadPolicy(path)
	if err != nil {
		return coded(ExitInvalidConfig, err)
	}
	if jsonOut {
		return printJSON(os.Stdout, cfg.MCP)
	}
	fmt.Printf("MCP policy (mode: %s)\n", cfg.MCP.Mode)
	fmt.Printf("  allow: %s\n", orNoneList(cfg.MCP.Allow))
	fmt.Printf("  deny:  %s\n", orNoneList(cfg.MCP.Deny))
	return nil
}

func orNoneList(items []string) string {
	if len(items) == 0 {
		return "(none)"
	}
	return strings.Join(items, ", ")
}
