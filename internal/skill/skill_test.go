package skill

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// frontmatter extracts the YAML frontmatter block from the embedded skill.
func frontmatter(t *testing.T) string {
	t.Helper()
	s := string(Content())
	if !strings.HasPrefix(s, "---\n") {
		t.Fatalf("SKILL.md must start with a --- frontmatter fence, got %q", s[:min(len(s), 20)])
	}
	rest := s[len("---\n"):]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		t.Fatal("SKILL.md frontmatter has no closing --- fence")
	}
	return rest[:end]
}

func TestContent(t *testing.T) {
	c := Content()
	if len(c) == 0 {
		t.Fatal("Content() returned empty bytes; SKILL.md embed is broken")
	}

	fm := frontmatter(t)

	tests := []struct {
		name string
		want string
	}{
		{"frontmatter name", "name: agentbox-sandbox"},
		{"frontmatter version", "version: 0.2.0"},
		{"openclaw bins requirement", `"bins": ["agentbox"]`},
		{"hermes metadata", `"hermes": {"category": "devops"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !strings.Contains(fm, tt.want) {
				t.Errorf("frontmatter missing %q:\n%s", tt.want, fm)
			}
		})
	}

	bodyChecks := []struct {
		name string
		want string
	}{
		{"exec command", "agentbox exec"},
		{"mcp scan command", "agentbox mcp scan"},
		{"session audit", "agentbox session list"},
		{"json result fields", "exit_code"},
		{"unsafe flag warning", "NEVER pass `--unsafe`"},
		{"local runtime warning", "--runtime local"},
		{"doctor hint", "agentbox doctor"},
	}
	body := string(c)
	for _, tt := range bodyChecks {
		t.Run(tt.name, func(t *testing.T) {
			if !strings.Contains(body, tt.want) {
				t.Errorf("SKILL.md body missing %q", tt.want)
			}
		})
	}
}

func TestContentDescriptionLength(t *testing.T) {
	fm := frontmatter(t)
	var desc string
	for _, line := range strings.Split(fm, "\n") {
		if rest, ok := strings.CutPrefix(line, "description:"); ok {
			desc = strings.TrimSpace(rest)
			break
		}
	}
	if desc == "" {
		t.Fatal("frontmatter has no description line")
	}
	if n := len([]rune(desc)); n >= 160 {
		t.Errorf("description is %d chars, must be under 160: %q", n, desc)
	}
}

func TestTargets(t *testing.T) {
	const home = "/home/dev"
	targets := Targets(home)
	if len(targets) != 5 {
		t.Fatalf("Targets() returned %d targets, want 5", len(targets))
	}

	want := map[string]string{
		"claude-project": filepath.Join(".claude", "skills"),
		"claude-user":    filepath.Join(home, ".claude", "skills"),
		"openclaw":       filepath.Join(home, ".openclaw", "workspace", "skills"),
		"hermes":         filepath.Join(home, ".hermes", "skills"),
		"agents":         filepath.Join(home, ".agents", "skills"),
	}
	for _, tgt := range targets {
		wantDir, ok := want[tgt.Name]
		if !ok {
			t.Errorf("unexpected target %q", tgt.Name)
			continue
		}
		delete(want, tgt.Name)
		if tgt.Dir != wantDir {
			t.Errorf("target %q: Dir = %q, want %q", tgt.Name, tgt.Dir, wantDir)
		}
		if tgt.Description == "" {
			t.Errorf("target %q has empty Description", tgt.Name)
		}
		if strings.Contains(tgt.Dir, "~") {
			t.Errorf("target %q: Dir %q must be expanded, not contain ~", tgt.Name, tgt.Dir)
		}
	}
	for name := range want {
		t.Errorf("missing target %q", name)
	}
}

func TestInstall(t *testing.T) {
	dir := t.TempDir()

	path, err := Install(dir, false)
	if err != nil {
		t.Fatalf("first Install() failed: %v", err)
	}
	wantPath := filepath.Join(dir, Name, "SKILL.md")
	if path != wantPath {
		t.Errorf("Install() path = %q, want %q", path, wantPath)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cannot read installed skill: %v", err)
	}
	if !bytes.Equal(got, Content()) {
		t.Error("installed SKILL.md differs from embedded content")
	}

	// Second install without force must refuse to overwrite.
	if _, err := Install(dir, false); err == nil {
		t.Fatal("second Install() without force succeeded, want refusal")
	} else if !strings.Contains(err.Error(), "--force") {
		t.Errorf("refusal error should mention --force, got: %v", err)
	}

	// Force overwrites, even after local modification.
	if err := os.WriteFile(path, []byte("modified"), 0o644); err != nil {
		t.Fatalf("cannot modify installed skill: %v", err)
	}
	forcedPath, err := Install(dir, true)
	if err != nil {
		t.Fatalf("Install() with force failed: %v", err)
	}
	if forcedPath != wantPath {
		t.Errorf("forced Install() path = %q, want %q", forcedPath, wantPath)
	}
	got, err = os.ReadFile(path)
	if err != nil {
		t.Fatalf("cannot read reinstalled skill: %v", err)
	}
	if !bytes.Equal(got, Content()) {
		t.Error("forced reinstall did not restore embedded content")
	}
}

func TestInstallPermissions(t *testing.T) {
	dir := t.TempDir()
	path, err := Install(dir, false)
	if err != nil {
		t.Fatalf("Install() failed: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("cannot stat installed skill: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o644 {
		t.Errorf("SKILL.md permissions = %o, want 644", perm)
	}

	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("cannot stat skill directory: %v", err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o755 {
		t.Errorf("skill directory permissions = %o, want 755", perm)
	}
}
