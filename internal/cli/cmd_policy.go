package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/qosikz/andbo/internal/adapters"
	"github.com/qosikz/andbo/internal/config"
	"github.com/qosikz/andbo/internal/policy"
)

// checkAgentDefault refuses a policy whose agent.default names no adapter.
//
// The verdict comes from adapters.Get — the same resolution `andbo run` and
// `andbo k8s render` already perform — and not from a second list of names kept
// beside the registry, so the set these gates accept cannot drift from the set
// that actually runs. The adapter is resolved and discarded; the custom config
// is threaded through only because Get's signature takes it.
//
// It returns a plain error rather than a coded one because both callers use the
// TEXT: `policy check` folds it into check.Errors under that command's own
// invalid-config exit, and doctor reports it. run and k8s render keep refusing
// at adapters.Get with ExitAgentFailed. This closes the gap in front of them; it
// does not renumber what they already refuse.
func checkAgentDefault(agent string, custom config.CustomAgentConfig, policyPath string) error {
	if _, err := adapters.Get(agent, custom); err == nil {
		return nil
	}
	return fmt.Errorf(
		"agent.default %q is invalid (expected: %s).\nSet agent.default in %s to one of those, or pass --agent NAME to choose one for a single run.",
		agent, strings.Join(adapters.SupportedNames(), ", "), policyPath)
}

func (r *Root) cmdPolicy(args []string) error {
	if len(args) == 0 || args[0] != "check" {
		return codedf(ExitGeneral, "usage: andbo policy check [--policy FILE] [--json]")
	}
	rest := args[1:]
	path := flagValue(rest, "--policy", "andbo.yaml")
	jsonOut := hasFlag(rest, "--json")

	cfg, err := config.LoadPolicy(path)
	if err != nil {
		return coded(ExitInvalidConfig, err)
	}
	check := cfg.Check()
	ep := policy.BuildEffectivePolicy(cfg, path, policy.Overrides{})

	// This command is the gate a pipeline runs BEFORE anything executes, so a
	// budget `andbo run`/`andbo exec` will refuse has to be an error here too.
	// Reporting the policy valid and then dying on the first real run is the
	// same silent gap between surfaces that checkBudgetMinutes exists to close.
	// It is reported through check.Errors so it reaches --json and the human
	// output alike, under this command's own invalid-policy exit code.
	if err := checkBudgetMinutes(ep.Budget.MaxRuntimeMinutes, path); err != nil {
		check.Errors = append(check.Errors, err.Error())
	}
	// Same gap, one field over: an agent.default no adapter answers to passed
	// this gate and then killed `andbo run` and `andbo k8s render` at exit 4.
	if err := checkAgentDefault(ep.Agent.Default, cfg.Agent.Custom, path); err != nil {
		check.Errors = append(check.Errors, err.Error())
	}

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
			// Errors are multi-line when they carry a fix; indent the
			// continuation so it stays visibly part of the same bullet.
			fmt.Printf("  ✗ %s\n", strings.ReplaceAll(e, "\n", "\n    "))
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
