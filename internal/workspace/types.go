// Package workspace resolves the filesystem layout for a run: which paths are
// writable, read-only, or denied, and (optionally) materializes an isolated
// copy of the repository honoring those rules.
//
// In the MVP, filesystem restrictions are enforced primarily by copying the
// repository into a workspace and excluding denied paths, rather than by
// kernel-level sandboxing. This file is the FROZEN CONTRACT for the package.
package workspace

// Plan is the resolved filesystem layout derived from an EffectivePolicy.
type Plan struct {
	RepoRoot       string
	WritePaths     []string
	ReadOnlyPaths  []string
	DeniedPaths    []string
	ExcludedMounts []string // sensitive paths that exist but are excluded
	Warnings       []string
}
