package cli

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/qosikz/andbo/internal/config"
	"github.com/qosikz/andbo/internal/netproxy"
)

type doctorCheck struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

func (r *Root) cmdDoctor(args []string) error {
	jsonOut := hasFlag(args, "--json")
	var checks []doctorCheck

	checks = append(checks, doctorCheck{"os", true, runtime.GOOS + "/" + runtime.GOARCH})
	checks = append(checks, doctorCheck{"andbo", true, r.Version})

	for _, bin := range []string{"docker", "podman", "git", "gh"} {
		path, err := exec.LookPath(bin)
		if err != nil {
			checks = append(checks, doctorCheck{bin, false, "not found on PATH"})
		} else {
			checks = append(checks, doctorCheck{bin, true, path})
		}
	}

	// Config file.
	if _, err := os.Stat("andbo.yaml"); err == nil {
		if _, lerr := config.LoadPolicy("andbo.yaml"); lerr != nil {
			checks = append(checks, doctorCheck{"config", false, "andbo.yaml present but invalid"})
		} else {
			checks = append(checks, doctorCheck{"config", true, "andbo.yaml valid"})
		}
	} else {
		checks = append(checks, doctorCheck{"config", false, "no andbo.yaml (run 'andbo init')"})
	}

	// Known agent CLIs.
	for _, bin := range []string{"claude", "codex", "gemini", "goose", "opencode"} {
		if path, err := exec.LookPath(bin); err == nil {
			checks = append(checks, doctorCheck{"agent:" + bin, true, path})
		} else {
			checks = append(checks, doctorCheck{"agent:" + bin, false, "not installed"})
		}
	}

	// Egress proxy: required for network.mode=allowlist enforcement. Released
	// binaries embed it; plain `go build` dev binaries do not.
	var embedded []string
	for _, arch := range []string{"amd64", "arm64"} {
		if netproxy.Embedded(arch) {
			embedded = append(embedded, arch)
		}
	}
	if len(embedded) > 0 {
		checks = append(checks, doctorCheck{"egress-proxy", true, "embedded (" + strings.Join(embedded, ", ") + "); network allowlist enforceable"})
	} else {
		checks = append(checks, doctorCheck{"egress-proxy", false, "not embedded (dev build?); network allowlist cannot be enforced — build with `make build`"})
	}

	// Write access to .andbo/.
	if err := os.MkdirAll(".andbo", 0o755); err != nil {
		checks = append(checks, doctorCheck{".andbo", false, err.Error()})
	} else {
		checks = append(checks, doctorCheck{".andbo", true, "writable"})
	}

	if jsonOut {
		return printJSON(os.Stdout, checks)
	}

	fmt.Println("Andbo doctor")
	fmt.Println()
	for _, c := range checks {
		mark := "✓"
		if !c.OK {
			mark = "✗"
		}
		fmt.Printf("  %s %-14s %s\n", mark, c.Name, c.Detail)
	}
	fmt.Println()
	if _, err := exec.LookPath("docker"); err != nil {
		fmt.Println("Docker is not available. You can still use --dry-run, or install Docker for isolated runs.")
	}
	return nil
}
