package netproxy

import (
	"os"
	"strings"
	"testing"
	"testing/fstest"
)

func TestExtractFrom(t *testing.T) {
	fsys := fstest.MapFS{
		"embedded/netproxy_linux_arm64": {Data: []byte("fake-arm64-elf")},
	}
	dir := t.TempDir()

	t.Run("present arch extracts 0755", func(t *testing.T) {
		path, err := extractFrom(fsys, "arm64", dir)
		if err != nil {
			t.Fatalf("extractFrom: %v", err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o755 {
			t.Errorf("mode = %v, want 0755 (must be executable in the container)", info.Mode().Perm())
		}
		data, _ := os.ReadFile(path)
		if string(data) != "fake-arm64-elf" {
			t.Errorf("extracted content mismatch")
		}
	})

	t.Run("missing arch is an actionable error", func(t *testing.T) {
		_, err := extractFrom(fsys, "amd64", dir)
		if err == nil {
			t.Fatal("want error for missing embedded binary")
		}
		for _, hint := range []string{"make build", "network.mode", "allowlist"} {
			if !strings.Contains(err.Error(), hint) {
				t.Errorf("error should mention %q, got: %v", hint, err)
			}
		}
	})

	t.Run("unknown arch rejected", func(t *testing.T) {
		if _, err := extractFrom(fsys, "mips", dir); err == nil || !strings.Contains(err.Error(), "unsupported") {
			t.Errorf("want unsupported-arch error, got: %v", err)
		}
	})
}
