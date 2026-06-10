package policy

import (
	"strings"
	"testing"

	"github.com/qosi/agentbox/internal/config"
)

func TestMandatoryDeniesAlwaysPresent(t *testing.T) {
	cfg := config.DefaultPolicy()
	cfg.Filesystem.Deny = []string{"foo"} // user wiped the sensitive defaults
	ep := BuildEffectivePolicy(cfg, "agentbox.yaml", Overrides{})
	for _, want := range []string{".env", "~/.ssh", "~/.aws", "~/.kube", "~/.config/gh"} {
		if !containsStr(ep.Filesystem.Deny, want) {
			t.Errorf("mandatory deny %q missing from %v", want, ep.Filesystem.Deny)
		}
	}
}

func TestDenyOverridesAllowFilesystem(t *testing.T) {
	cfg := config.DefaultPolicy()
	cfg.Filesystem.Write = []string{"./src", "./tests"}
	cfg.Filesystem.Deny = []string{"./tests"}
	ep := BuildEffectivePolicy(cfg, "", Overrides{})
	if containsStr(ep.Filesystem.Write, "./tests") {
		t.Errorf("denied path ./tests should be removed from write, got %v", ep.Filesystem.Write)
	}
	if !containsStr(ep.Filesystem.Write, "./src") {
		t.Errorf("./src should remain writable, got %v", ep.Filesystem.Write)
	}
}

func TestEnvDeniedEvenIfWritten(t *testing.T) {
	cfg := config.DefaultPolicy()
	cfg.Filesystem.Write = []string{".env"}
	ep := BuildEffectivePolicy(cfg, "", Overrides{})
	if containsStr(ep.Filesystem.Write, ".env") {
		t.Error(".env must never be writable by default")
	}
}

func TestDenyOverridesAllowSecrets(t *testing.T) {
	cfg := config.DefaultPolicy()
	cfg.Secrets.Allow = []string{"GITHUB_TOKEN", "AWS_SECRET_ACCESS_KEY"}
	cfg.Secrets.Deny = []string{"AWS_SECRET_ACCESS_KEY"}
	ep := BuildEffectivePolicy(cfg, "", Overrides{})
	if containsStr(ep.Secrets.Allow, "AWS_SECRET_ACCESS_KEY") {
		t.Error("denied secret should not remain allowed")
	}
	if !containsStr(ep.Secrets.Allow, "GITHUB_TOKEN") {
		t.Error("GITHUB_TOKEN should remain allowed")
	}
}

func TestSecretDenyWildcardDeniesAll(t *testing.T) {
	cfg := config.DefaultPolicy()
	cfg.Secrets.Allow = []string{"GITHUB_TOKEN"}
	cfg.Secrets.Deny = []string{"*"}
	ep := BuildEffectivePolicy(cfg, "", Overrides{})
	if len(ep.Secrets.Allow) != 0 {
		t.Errorf("deny \"*\" should clear allow, got %v", ep.Secrets.Allow)
	}
}

func TestUnsafeReasonsAndGating(t *testing.T) {
	cfg := config.DefaultPolicy()
	ep := BuildEffectivePolicy(cfg, "", Overrides{Network: "open", Runtime: "local", AllowDockerSocket: true})
	if !ep.RequiresUnsafeConfirmation() {
		t.Fatal("open network + local runtime + docker socket must require confirmation")
	}
	reasons := strings.Join(ep.UnsafeReasons(), " ")
	for _, want := range []string{"network", "local", "Docker"} {
		if !strings.Contains(reasons, want) {
			t.Errorf("unsafe reasons %q missing %q", reasons, want)
		}
	}
}

func TestDefaultPolicyIsSafe(t *testing.T) {
	ep := BuildEffectivePolicy(config.DefaultPolicy(), "", Overrides{})
	if ep.RequiresUnsafeConfirmation() {
		t.Errorf("default policy should be safe, reasons: %v", ep.UnsafeReasons())
	}
	if ep.EnforcedNetwork() != "deny" {
		t.Errorf("default enforced network = %q, want deny", ep.EnforcedNetwork())
	}
}

func TestAllowlistNotEnforced(t *testing.T) {
	cfg := config.DefaultPolicy()
	cfg.Network.Mode = "allowlist"
	ep := BuildEffectivePolicy(cfg, "", Overrides{})
	if ep.EnforcedNetwork() != "deny" {
		t.Errorf("allowlist should collapse to deny enforcement, got %q", ep.EnforcedNetwork())
	}
	notes := strings.Join(ep.EnforcementNotes(), " ")
	if !strings.Contains(notes, "allowlist") {
		t.Errorf("expected honesty note about allowlist, got %q", notes)
	}
}

func TestAllowHostHomeRelaxesHomeDenies(t *testing.T) {
	ep := BuildEffectivePolicy(config.DefaultPolicy(), "", Overrides{AllowHostHome: true})
	if containsStr(ep.Filesystem.Deny, "~/.ssh") {
		t.Error("--allow-host-home should drop mandatory home denies")
	}
	// .env (non-home) must still be denied.
	if !containsStr(ep.Filesystem.Deny, ".env") {
		t.Error(".env must still be denied even with --allow-host-home")
	}
}
