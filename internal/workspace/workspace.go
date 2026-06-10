package workspace

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/qosi/agentbox/internal/policy"
)

// skipDirs are repository-internal directories never copied into a workspace:
// .git carries history and remotes, .agentbox carries run state and would let
// an agent observe or tamper with its own sandbox.
var skipDirs = map[string]bool{
	".git":      true,
	".agentbox": true,
}

// BuildPlan resolves the filesystem layout for a run from an EffectivePolicy.
//
// It copies the policy's write/readonly/deny lists into the Plan, records which
// denied sensitive files actually exist under repoRoot (ExcludedMounts), and
// warns about write paths that are missing. It errors only when repoRoot itself
// is not an existing directory.
func BuildPlan(repoRoot string, ep policy.EffectivePolicy) (Plan, error) {
	root := filepath.Clean(repoRoot)

	info, err := os.Stat(root)
	if err != nil {
		return Plan{}, fmt.Errorf("workspace: repo root %q is not accessible: %w\nVerify the path exists and you have permission to read it.", root, err)
	}
	if !info.IsDir() {
		return Plan{}, fmt.Errorf("workspace: repo root %q is not a directory\nPoint --repo at the top-level directory of your project.", root)
	}

	plan := Plan{
		RepoRoot:      root,
		WritePaths:    cloneSlice(ep.Filesystem.Write),
		ReadOnlyPaths: cloneSlice(ep.Filesystem.ReadOnly),
		DeniedPaths:   cloneSlice(ep.Filesystem.Deny),
	}

	// Security: discover which denied sensitive files actually exist so the
	// caller can report concrete evidence (e.g. "found .env, excluded"). Home
	// (~) denies live outside the repo and are handled by the runtime, not by
	// the workspace copy, so they are skipped here.
	seen := map[string]bool{}
	for _, d := range plan.DeniedPaths {
		if isHomePath(d) {
			continue
		}
		for _, rel := range matchRepoRelative(root, d) {
			if !seen[rel] {
				seen[rel] = true
				plan.ExcludedMounts = append(plan.ExcludedMounts, rel)
			}
		}
	}

	// Warn (do not error) about write paths that do not exist under repoRoot:
	// the agent may create them, but a typo is worth surfacing.
	for _, w := range plan.WritePaths {
		abs := resolveUnderRoot(root, w)
		if abs == "" {
			continue // absolute or escaping path; not a repo-relative write target
		}
		if _, err := os.Stat(abs); err != nil {
			plan.Warnings = append(plan.Warnings, fmt.Sprintf("write path %q does not exist under %s; it will be created if the agent writes to it", w, root))
		}
	}

	return plan, nil
}

// Prepare materializes an isolated copy of the repository at dest, honoring the
// plan's deny rules. It recursively copies plan.RepoRoot into dest, skipping the
// .git and .agentbox directories and any path matching a denied repo-relative
// pattern. File mode bits are preserved.
//
// This realizes the MVP "enforce by workspace copy" model: denied sensitive
// files are simply never present in the copy the agent runs against.
func Prepare(plan Plan, dest string) error {
	root := filepath.Clean(plan.RepoRoot)
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return fmt.Errorf("workspace: cannot prepare from repo root %q: not an accessible directory\nRun BuildPlan first and pass its RepoRoot.", root)
	}

	dest = filepath.Clean(dest)
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return fmt.Errorf("workspace: creating destination %q: %w", dest, err)
	}

	// Precompute repo-relative deny patterns once. Home (~) denies cannot match
	// a path inside the repo and are ignored for the copy.
	var denies []string
	for _, d := range plan.DeniedPaths {
		if !isHomePath(d) {
			denies = append(denies, d)
		}
	}

	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}

		rel, err := filepath.Rel(root, path)
		if err != nil {
			return fmt.Errorf("workspace: relativizing %q: %w", path, err)
		}

		// Security: skip runtime-internal dirs and anything denied by policy.
		if info.IsDir() {
			if skipDirs[info.Name()] {
				return filepath.SkipDir
			}
			if denyMatch(rel, denies) {
				return filepath.SkipDir
			}
		} else if denyMatch(rel, denies) {
			return nil
		}

		target := filepath.Join(dest, rel)
		if info.IsDir() {
			if err := os.MkdirAll(target, info.Mode().Perm()); err != nil {
				return fmt.Errorf("workspace: creating directory %q: %w", target, err)
			}
			return nil
		}

		// Skip non-regular files (symlinks, sockets, devices): copying them into
		// the sandbox could re-introduce a path outside the workspace.
		if !info.Mode().IsRegular() {
			return nil
		}
		return copyFile(path, target, info.Mode().Perm())
	})
}

// matchRepoRelative returns the repo-relative paths under root matched by deny
// pattern d. Glob patterns are expanded with filepath.Glob; literals are tested
// with os.Stat. Only existing entries are returned.
func matchRepoRelative(root, d string) []string {
	d = strings.TrimSpace(d)
	if d == "" {
		return nil
	}
	clean := filepath.Clean(d)
	if isGlob(d) {
		matches, err := filepath.Glob(filepath.Join(root, clean))
		if err != nil {
			return nil
		}
		var out []string
		for _, m := range matches {
			if rel, err := filepath.Rel(root, m); err == nil {
				out = append(out, rel)
			}
		}
		return out
	}
	abs := filepath.Join(root, clean)
	if _, err := os.Stat(abs); err == nil {
		return []string{clean}
	}
	return nil
}

// denyMatch reports whether the repo-relative path rel is covered by any deny
// pattern: an exact/clean match, a filepath.Match glob hit, or rel sitting
// inside a denied directory.
func denyMatch(rel string, denies []string) bool {
	crel := filepath.Clean(rel)
	for _, d := range denies {
		cd := filepath.Clean(strings.TrimSpace(d))
		if cd == "" || cd == "." {
			continue
		}
		if crel == cd {
			return true
		}
		if isGlob(d) {
			if ok, _ := filepath.Match(cd, crel); ok {
				return true
			}
			// Also match the basename so a top-level glob like ".env.*" matches
			// regardless of directory depth used in the pattern.
			if ok, _ := filepath.Match(cd, filepath.Base(crel)); ok {
				return true
			}
			continue
		}
		// rel lives inside a denied directory.
		if strings.HasPrefix(crel+string(os.PathSeparator), cd+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

// resolveUnderRoot joins a repo-relative write path to root. It returns "" for
// absolute paths or paths that escape root, which the workspace does not own.
func resolveUnderRoot(root, p string) string {
	p = strings.TrimSpace(p)
	if p == "" || filepath.IsAbs(p) || strings.HasPrefix(p, "~") {
		return ""
	}
	joined := filepath.Join(root, filepath.Clean(p))
	if joined != root && !strings.HasPrefix(joined, root+string(os.PathSeparator)) {
		return ""
	}
	return joined
}

// isHomePath reports whether a deny entry refers to a home-directory (~) path,
// which lives outside the repository copy.
func isHomePath(p string) bool {
	return strings.HasPrefix(strings.TrimSpace(p), "~")
}

func isGlob(p string) bool {
	return strings.ContainsAny(p, "*?[")
}

func cloneSlice(in []string) []string {
	if in == nil {
		return nil
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}

func copyFile(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("workspace: opening source %q: %w", src, err)
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return fmt.Errorf("workspace: creating %q: %w", dst, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return fmt.Errorf("workspace: copying %q -> %q: %w", src, dst, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("workspace: closing %q: %w", dst, err)
	}
	return nil
}
