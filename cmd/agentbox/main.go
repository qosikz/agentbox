package main

import (
	"fmt"
	"os"

	"github.com/qosi/agentbox/internal/cli"
)

// Build-time variables injected via -ldflags.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	root := cli.NewRoot(version, commit, date)
	err := root.Run(os.Args[1:])
	if err != nil {
		// Print the message unless it is intentionally empty (the command
		// already produced its own output and only needs to set an exit code).
		if msg := err.Error(); msg != "" {
			fmt.Fprintln(os.Stderr, msg)
		}
	}
	os.Exit(cli.CodeFor(err))
}
