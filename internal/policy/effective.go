// Package policy turns a raw config.Policy plus CLI overrides into an
// EffectivePolicy: the single, security-resolved view that every other module
// consumes. It is where deny-overrides-allow, mandatory sensitive denies, and
// unsafe-mode gating are decided — never in the modules downstream.
package policy

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/qosi/agentbox/internal/config"
)

// mandatoryDenies are always merged into filesystem.deny. The home-directory
// entries can only be relaxed with the explicit --allow-host-home unsafe flag.
var mandatoryDenies = []string{
	".env", ".env.*",
	"~/.ssh", "~/.aws", "~/.kube", "~/.docker", "~/.config/gh",
}

var homeDenyPrefixes = []string{"~/."}

// Overrides carries CLI-flag and environment overrides applied on top of the
// file policy.
type Overrides struct {
	Network           string   // "" | deny | allowlist | open
	Runtime           string   // "" | container | local
	Agent             string   // "" | adapter name
	ExtraWrite        []string // additional --write paths
	Unsafe            bool     // master unsafe switch
	AllowHostHome     bool     // mount host home (unsafe)
	AllowDockerSocket bool     // mount docker socket (unsafe)
	YesUnsafe         bool     // non-interactive unsafe ack (CI)
}

// EffectivePolicy is the resolved, security-hardened policy.
type EffectivePolicy struct {
	Runtime    config.RuntimePolicy
	Agent      config.AgentPolicy
	Network    config.NetworkPolicy
	Filesystem config.FilesystemPolicy
	Secrets    config.SecretsPolicy
	MCP        config.MCPPolicy
	Commands   config.CommandsPolicy
	Budget     config.BudgetPolicy
	Tests      config.TestsPolicy

	PolicyPath string
	Overrides  Overrides

	unsafeReasons []string
	notes         []string
}

// BuildEffectivePolicy applies overrides and security invariants to cfg.
func BuildEffectivePolicy(cfg config.Policy, policyPath string, ov Overrides) EffectivePolicy {
	ep := EffectivePolicy{
		Runtime:    cfg.Runtime,
		Agent:      cfg.Agent,
		Network:    cfg.Network,
		Filesystem: cfg.Filesystem,
		Secrets:    cfg.Secrets,
		MCP:        cfg.MCP,
		Commands:   cfg.Commands,
		Budget:     cfg.Budget,
		Tests:      cfg.Tests,
		PolicyPath: policyPath,
		Overrides:  ov,
	}

	// Apply scalar overrides.
	if ov.Network != "" {
		ep.Network.Mode = ov.Network
	}
	if ov.Runtime != "" {
		ep.Runtime.Isolation = ov.Runtime
	}
	if ov.Agent != "" {
		ep.Agent.Default = ov.Agent
	}

	// Merge mandatory denies, then (if host-home is explicitly allowed) strip
	// home-directory denies entirely — including any that came from the file
	// policy or defaults. Non-home denies like .env always remain.
	deny := append([]string{}, ep.Filesystem.Deny...)
	for _, d := range mandatoryDenies {
		if !containsStr(deny, d) {
			deny = append(deny, d)
		}
	}
	if ov.AllowHostHome {
		var kept []string
		for _, d := range deny {
			if isHomeDeny(d) {
				continue
			}
			kept = append(kept, d)
		}
		deny = kept
	}
	ep.Filesystem.Deny = deny

	// Extra --write paths.
	write := append([]string{}, ep.Filesystem.Write...)
	for _, w := range ov.ExtraWrite {
		if !containsStr(write, w) {
			write = append(write, w)
		}
	}

	// Deny overrides allow (filesystem): drop write paths covered by deny.
	var keptWrite []string
	for _, w := range write {
		if pat, denied := pathDenied(w, ep.Filesystem.Deny); denied {
			ep.notes = append(ep.notes, fmt.Sprintf("write path %q dropped: denied by %q (deny overrides allow)", w, pat))
			continue
		}
		keptWrite = append(keptWrite, w)
	}
	ep.Filesystem.Write = keptWrite

	// Deny overrides allow (secrets): deny names win; "*" denies everything.
	ep.Secrets.Allow = resolveSecretAllow(ep.Secrets.Allow, ep.Secrets.Deny)

	ep.unsafeReasons = computeUnsafeReasons(ep)
	ep.notes = append(ep.notes, enforcementNotes(ep)...)
	return ep
}

func isHomeDeny(d string) bool {
	for _, p := range homeDenyPrefixes {
		if strings.HasPrefix(d, p) {
			return true
		}
	}
	return false
}

// resolveSecretAllow removes any allowed secret that is also denied. A deny of
// "*" denies all and yields an empty allow list.
func resolveSecretAllow(allow, deny []string) []string {
	if containsStr(deny, "*") {
		return []string{}
	}
	denied := map[string]bool{}
	for _, d := range deny {
		denied[d] = true
	}
	var out []string
	for _, a := range allow {
		if a == "*" {
			// Explicit allow-all is gated separately as unsafe; keep it so the
			// unsafe detector can see it, but it is meaningless once denied.
			out = append(out, a)
			continue
		}
		if !denied[a] {
			out = append(out, a)
		}
	}
	return out
}

// pathDenied reports whether write path w is covered by any deny pattern, and
// returns the matching pattern.
func pathDenied(w string, patterns []string) (string, bool) {
	cw := normalizePath(w)
	for _, p := range patterns {
		cp := normalizePath(p)
		if cw == cp {
			return p, true
		}
		// "**" glob: treat as substring containment on the cleaned base.
		if strings.Contains(p, "**") {
			frag := strings.Trim(strings.ReplaceAll(p, "**", ""), "/*")
			if frag != "" && strings.Contains(cw, frag) {
				return p, true
			}
			continue
		}
		// Simple glob on the final element.
		if ok, _ := filepath.Match(cp, cw); ok {
			return p, true
		}
		// w sits inside a denied directory.
		if strings.HasPrefix(cw+"/", cp+"/") {
			return p, true
		}
	}
	return "", false
}

func normalizePath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return p
	}
	// Keep ~ paths and globs recognizable; only clean ordinary relative paths.
	if strings.ContainsAny(p, "*?") || strings.HasPrefix(p, "~") {
		return p
	}
	return filepath.Clean(p)
}

func computeUnsafeReasons(ep EffectivePolicy) []string {
	var reasons []string
	if ep.Network.Mode == "open" {
		reasons = append(reasons, "network.mode=open allows unrestricted outbound access")
	}
	if ep.Runtime.Isolation == "local" {
		reasons = append(reasons, "runtime.isolation=local runs the agent on the host without container isolation")
	}
	if ep.Overrides.AllowDockerSocket {
		reasons = append(reasons, "--allow-docker-socket exposes the Docker daemon to the agent")
	}
	if ep.Overrides.AllowHostHome {
		reasons = append(reasons, "--allow-host-home mounts your home directory into the workspace")
	}
	if containsStr(ep.Secrets.Allow, "*") {
		reasons = append(reasons, "secrets.allow=\"*\" would pass every environment variable to the agent")
	}
	return reasons
}

func enforcementNotes(ep EffectivePolicy) []string {
	var notes []string
	if ep.Network.Mode == "allowlist" {
		notes = append(notes, "network allowlist is not enforced yet; the runtime applies deny and the domain list is advisory")
	}
	if len(ep.Commands.Deny) > 0 {
		notes = append(notes, "command deny list is best-effort and cannot stop an agent that spawns shells indirectly")
	}
	if ep.MCP.Mode == "advisory" {
		notes = append(notes, "MCP mode is advisory: servers are scanned but not blocked at runtime")
	}
	return notes
}

// RequiresUnsafeConfirmation reports whether the policy enables any unsafe mode
// that must be explicitly acknowledged before a real run.
func (e EffectivePolicy) RequiresUnsafeConfirmation() bool {
	return len(e.unsafeReasons) > 0
}

// UnsafeReasons returns human-readable reasons the policy is unsafe.
func (e EffectivePolicy) UnsafeReasons() []string { return e.unsafeReasons }

// EnforcementNotes returns honesty notes about partially-enforced controls.
func (e EffectivePolicy) EnforcementNotes() []string { return e.notes }

// EnforcedNetwork returns the network mode actually enforced by the runtime.
// Because allowlist is not yet enforced, it collapses to deny.
func (e EffectivePolicy) EnforcedNetwork() string {
	if e.Network.Mode == "allowlist" {
		return "deny"
	}
	return e.Network.Mode
}

// Human renders the effective policy for `agentbox policy check`.
func (e EffectivePolicy) Human() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Effective policy\n")
	fmt.Fprintf(&b, "  Runtime:    %s (%s) image=%s cleanup=%t\n", e.Runtime.Isolation, e.Runtime.Engine, e.Runtime.Image, e.Runtime.Cleanup)
	fmt.Fprintf(&b, "  Agent:      %s\n", e.Agent.Default)
	fmt.Fprintf(&b, "  Network:    %s (enforced: %s)\n", e.Network.Mode, e.EnforcedNetwork())
	fmt.Fprintf(&b, "  Write:      %s\n", joinOrNone(e.Filesystem.Write))
	fmt.Fprintf(&b, "  Deny:       %s\n", joinOrNone(e.Filesystem.Deny))
	fmt.Fprintf(&b, "  Secrets:    allow=%s deny=%s\n", joinOrNone(e.Secrets.Allow), joinOrNone(e.Secrets.Deny))
	fmt.Fprintf(&b, "  MCP:        %s\n", e.MCP.Mode)
	fmt.Fprintf(&b, "  Budget:     usd=%g tokens=%d minutes=%d\n", e.Budget.MaxUSD, e.Budget.MaxTokens, e.Budget.MaxRuntimeMinutes)
	return b.String()
}

func joinOrNone(items []string) string {
	if len(items) == 0 {
		return "(none)"
	}
	return strings.Join(items, ", ")
}

func containsStr(set []string, v string) bool {
	for _, s := range set {
		if s == v {
			return true
		}
	}
	return false
}

// SortedDeny returns the deny list sorted, for stable display/testing.
func (e EffectivePolicy) SortedDeny() []string {
	out := append([]string{}, e.Filesystem.Deny...)
	sort.Strings(out)
	return out
}
