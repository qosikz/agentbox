// Package skill ships the cross-harness "andbo-sandbox" agent skill: a
// single SKILL.md that teaches any agent harness (Claude Code, OpenClaw,
// Hermes Agent, and anything honoring the agentskills.io standard) to use
// the andbo CLI as its safety sandbox.
//
// The skill content is embedded at build time so the andbo binary can
// install it offline into the well-known skill directories of each harness.
package skill

import (
	_ "embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Name is the canonical skill directory name used by every harness:
// <skills-dir>/andbo-sandbox/SKILL.md.
const Name = "andbo-sandbox"

//go:embed content/SKILL.md
var content []byte

// Content returns the embedded SKILL.md.
func Content() []byte {
	return content
}

// Target is a harness-specific skill installation directory. Dir is the
// skills root: the andbo-sandbox/ subdirectory is created inside it.
// Dir may contain "~" when constructed by callers; Targets itself returns
// paths already expanded with the provided home directory.
type Target struct {
	Name        string
	Description string
	Dir         string
}

// Targets returns the known installation targets, with home expanded into
// each user-level path. The claude-project target is relative to the
// current working directory by design.
func Targets(home string) []Target {
	return []Target{
		{
			Name:        "claude-project",
			Description: "Claude Code (this project)",
			Dir:         filepath.Join(".claude", "skills"),
		},
		{
			Name:        "claude-user",
			Description: "Claude Code (all projects for this user)",
			Dir:         filepath.Join(home, ".claude", "skills"),
		},
		{
			Name:        "openclaw",
			Description: "OpenClaw workspace skills",
			Dir:         filepath.Join(home, ".openclaw", "workspace", "skills"),
		},
		{
			Name:        "hermes",
			Description: "Hermes Agent skills",
			Dir:         filepath.Join(home, ".hermes", "skills"),
		},
		{
			Name:        "agents",
			Description: "Cross-agent standard (agentskills.io compatible harnesses)",
			Dir:         filepath.Join(home, ".agents", "skills"),
		},
	}
}

// Install writes the embedded skill to <dir>/andbo-sandbox/SKILL.md and
// returns the written path. It refuses to overwrite an existing SKILL.md
// unless force is true, so a locally customized skill is never clobbered
// silently.
func Install(dir string, force bool) (string, error) {
	skillDir := filepath.Join(dir, Name)
	path := filepath.Join(skillDir, "SKILL.md")

	if !force {
		if _, err := os.Stat(path); err == nil {
			return "", fmt.Errorf(
				"skill already installed at %s: refusing to overwrite a possibly customized SKILL.md; re-run with --force to replace it",
				path,
			)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf(
				"cannot check existing skill at %s: %v; fix the path permissions and retry",
				path, err,
			)
		}
	}

	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		return "", fmt.Errorf(
			"cannot create skill directory %s: %v; check that the parent directory is writable",
			skillDir, err,
		)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		return "", fmt.Errorf(
			"cannot write skill file %s: %v; check directory permissions and free disk space",
			path, err,
		)
	}
	return path, nil
}
