package netproxy

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Embedded linux builds of cmd/netproxy, produced by `make proxy` before the
// main binary is compiled. They are extracted at runtime and mounted read-only
// into the egress-proxy sidecar container, so users never need a separate
// proxy image or binary. Containers are linux-only, hence only linux builds.
//
// A development build made with plain `go build` (no `make proxy`) embeds
// nothing; Extract then returns an actionable error and allowlist enforcement
// is unavailable until the proxy is built.
//
//go:embed all:embedded
var embeddedFS embed.FS

// binaryName returns the embedded path for a container architecture
// ("amd64" or "arm64").
func binaryName(arch string) string {
	return "embedded/netproxy_linux_" + arch
}

// Embedded reports whether the proxy binary for arch is embedded in this
// build. Used by `agentbox doctor` to report allowlist enforceability.
func Embedded(arch string) bool {
	f, err := embeddedFS.Open(binaryName(arch))
	if err != nil {
		return false
	}
	f.Close()
	return true
}

// Extract writes the embedded linux proxy binary for arch into dir with mode
// 0755 and returns its path. The caller mounts it read-only into the sidecar.
func Extract(arch, dir string) (string, error) {
	return extractFrom(embeddedFS, arch, dir)
}

// extractFrom is Extract with an injectable FS so tests can supply fstest
// fixtures without release artifacts.
func extractFrom(fsys fs.FS, arch, dir string) (string, error) {
	if arch != "amd64" && arch != "arm64" {
		return "", fmt.Errorf("unsupported container architecture %q (expected amd64 or arm64)", arch)
	}
	data, err := fs.ReadFile(fsys, binaryName(arch))
	if err != nil {
		return "", fmt.Errorf(
			"the egress proxy binary for linux/%s is not embedded in this agentbox build, so network.mode=allowlist cannot be enforced.\nUse a released binary, or build with `make build` (which embeds the proxy via `make proxy`).\nAlternatively set network.mode to deny, or open (unsafe).", arch)
	}
	path := filepath.Join(dir, "agentbox-netproxy")
	if err := os.WriteFile(path, data, 0o755); err != nil {
		return "", fmt.Errorf("writing egress proxy binary to %s: %w", path, err)
	}
	return path, nil
}
