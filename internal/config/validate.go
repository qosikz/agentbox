package config

import "fmt"

// CheckResult is the outcome of validating a policy. Errors make the policy
// invalid; warnings and unsafe options are advisory but must be surfaced.
type CheckResult struct {
	Errors           []string `json:"errors"`
	Warnings         []string `json:"warnings"`
	UnsafeOptions    []string `json:"unsafe_options"`
	UnsupportedModes []string `json:"unsupported_modes"`
}

// OK reports whether the policy is free of hard errors.
func (c CheckResult) OK() bool { return len(c.Errors) == 0 }

func contains(set []string, v string) bool {
	for _, s := range set {
		if s == v {
			return true
		}
	}
	return false
}

// Check validates enum values and surfaces unsafe options and enforcement
// limitations. It never mutates the policy.
func (p Policy) Check() CheckResult {
	var r CheckResult

	// Runtime.
	if !contains([]string{"container", "local"}, p.Runtime.Isolation) {
		r.Errors = append(r.Errors, fmt.Sprintf("runtime.isolation %q is invalid (expected: container, local)", p.Runtime.Isolation))
	}
	if !contains([]string{"docker", "podman"}, p.Runtime.Engine) {
		r.Errors = append(r.Errors, fmt.Sprintf("runtime.engine %q is invalid (expected: docker, podman)", p.Runtime.Engine))
	}
	if p.Runtime.Image == "" {
		r.Errors = append(r.Errors, "runtime.image must not be empty")
	}
	if p.Runtime.Isolation == "local" {
		r.UnsafeOptions = append(r.UnsafeOptions, "runtime.isolation=local runs the agent directly on the host without container isolation")
	}

	// Network.
	if !contains([]string{"deny", "allowlist", "open"}, p.Network.Mode) {
		r.Errors = append(r.Errors, fmt.Sprintf("network.mode %q is invalid (expected: deny, allowlist, open)", p.Network.Mode))
	}
	switch p.Network.Mode {
	case "open":
		r.UnsafeOptions = append(r.UnsafeOptions, "network.mode=open allows unrestricted outbound network access")
	case "allowlist":
		if len(p.Network.Allow) == 0 {
			r.Warnings = append(r.Warnings, "network.mode=allowlist with an empty network.allow list denies all egress; add the domains your agent needs")
		}
		if p.Runtime.Isolation == "local" {
			r.UnsupportedModes = append(r.UnsupportedModes, "network.mode=allowlist cannot be enforced with runtime.isolation=local; container isolation is required for egress enforcement")
		}
	}

	// Secrets.
	if p.Secrets.Mode != "explicit" {
		r.Errors = append(r.Errors, fmt.Sprintf("secrets.mode %q is invalid (expected: explicit)", p.Secrets.Mode))
	}
	for _, name := range p.Secrets.Allow {
		if name == "*" {
			r.UnsafeOptions = append(r.UnsafeOptions, "secrets.allow contains \"*\", which would expose all environment variables")
		}
	}

	// MCP.
	if !contains([]string{"allowlist", "denyall", "advisory"}, p.MCP.Mode) {
		r.Errors = append(r.Errors, fmt.Sprintf("mcp.mode %q is invalid (expected: allowlist, denyall, advisory)", p.MCP.Mode))
	}
	if p.MCP.Mode == "advisory" {
		r.UnsupportedModes = append(r.UnsupportedModes, "mcp.mode=advisory only reports risk; it does not block MCP servers at runtime")
	}

	// Commands: enforcement is best-effort in the MVP.
	if len(p.Commands.Deny) > 0 {
		r.UnsupportedModes = append(r.UnsupportedModes, "commands.deny is best-effort; an agent that spawns shells indirectly may bypass it")
	}

	// Budget: token/usd caps depend on adapter support.
	if p.Budget.MaxTokens > 0 || p.Budget.MaxUSD > 0 {
		r.UnsupportedModes = append(r.UnsupportedModes, "budget.max_tokens and budget.max_usd depend on adapter/provider support; reported as unknown when unavailable")
	}

	return r
}
