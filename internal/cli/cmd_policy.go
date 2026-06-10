package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/qosi/agentbox/internal/config"
	"github.com/qosi/agentbox/internal/policy"
)

func (r *Root) cmdPolicy(args []string) error {
	if len(args) == 0 || args[0] != "check" {
		return codedf(ExitGeneral, "usage: agentbox policy check [--policy FILE] [--json]")
	}
	rest := args[1:]
	path := flagValue(rest, "--policy", "agentbox.yaml")
	jsonOut := hasFlag(rest, "--json")

	cfg, err := config.LoadPolicy(path)
	if err != nil {
		return coded(ExitInvalidConfig, err)
	}
	check := cfg.Check()
	ep := policy.BuildEffectivePolicy(cfg, path, policy.Overrides{})

	if jsonOut {
		out := map[string]any{
			"valid":             check.OK(),
			"errors":            check.Errors,
			"warnings":          check.Warnings,
			"unsafe_options":    check.UnsafeOptions,
			"unsupported_modes": check.UnsupportedModes,
			"unsafe":            ep.UnsafeReasons(),
			"enforcement_notes": ep.EnforcementNotes(),
			"effective":         effectiveJSON(ep),
		}
		if err := printJSON(os.Stdout, out); err != nil {
			return coded(ExitGeneral, err)
		}
		if !check.OK() {
			return &CodedError{Code: ExitInvalidConfig, Err: errors.New("")}
		}
		return nil
	}

	fmt.Print(ep.Human())
	fmt.Println()

	if len(check.Errors) > 0 {
		fmt.Println("Errors:")
		for _, e := range check.Errors {
			fmt.Printf("  ✗ %s\n", e)
		}
		fmt.Println()
	}
	if reasons := ep.UnsafeReasons(); len(reasons) > 0 {
		fmt.Println("Unsafe options (require explicit confirmation at run time):")
		for _, u := range reasons {
			fmt.Printf("  ! %s\n", u)
		}
		fmt.Println()
	}
	if notes := ep.EnforcementNotes(); len(notes) > 0 {
		fmt.Println("Enforcement notes (honest limitations):")
		for _, n := range notes {
			fmt.Printf("  • %s\n", n)
		}
		fmt.Println()
	}

	if !check.OK() {
		return codedf(ExitInvalidConfig, "policy %s is invalid", path)
	}
	fmt.Println("✓ Policy valid")
	return nil
}

// effectiveJSON renders the effective policy as a plain map for --json output.
func effectiveJSON(ep policy.EffectivePolicy) map[string]any {
	return map[string]any{
		"runtime": map[string]any{
			"isolation": ep.Runtime.Isolation,
			"engine":    ep.Runtime.Engine,
			"image":     ep.Runtime.Image,
			"cleanup":   ep.Runtime.Cleanup,
		},
		"agent":             ep.Agent.Default,
		"network":           ep.Network.Mode,
		"network_enforced":  ep.EnforcedNetwork(),
		"filesystem_write":  ep.Filesystem.Write,
		"filesystem_deny":   ep.Filesystem.Deny,
		"secrets_allow":     ep.Secrets.Allow,
		"secrets_deny":      ep.Secrets.Deny,
		"mcp":               ep.MCP.Mode,
		"budget_max_usd":    ep.Budget.MaxUSD,
		"budget_max_tokens": ep.Budget.MaxTokens,
	}
}
