package main

import (
	"fmt"
	"os"
	"runtime/debug"

	"github.com/qosikz/andbo/internal/cli"
)

// Build-time variables injected via -ldflags.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

// resolveVersion prefers ldflags-injected version (make build/release).
// When installing via `go install`, ldflags are absent so fall back to the
// module version recorded in the binary's build info.
func resolveVersion(v string) string {
	if v != "" && v != "dev" {
		return v
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		if mv := bi.Main.Version; mv != "" && mv != "(devel)" {
			return mv
		}
	}
	if v == "" {
		return "dev"
	}
	return v
}

func main() {
	root := cli.NewRoot(resolveVersion(version), commit, date)
	err := root.Run(os.Args[1:])
	if err != nil {
		if msg := err.Error(); msg != "" {
			fmt.Fprintln(os.Stderr, msg)
		}
	}
	os.Exit(cli.CodeFor(err))
}
