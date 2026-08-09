package runtime

import (
	"context"
	"fmt"
	"runtime"
	"sort"
	"strings"

	"github.com/qosikz/andbo/internal/netproxy"
)

// dryRunRunner is a Runner that never executes anything. Instead it renders a
// human-readable description of the intended runtime actions so users can
// inspect what *would* happen before committing to a real run.
//
// Security note: the description intentionally lists environment variable KEYS
// only and never their VALUES, so secrets are not leaked into logs or terminal
// output when a user inspects a dry run.
type dryRunRunner struct{}

// NewDryRunRunner returns a Runner that plans, but never executes, a command.
func NewDryRunRunner() Runner { return dryRunRunner{} }

func (dryRunRunner) Name() string { return "dryrun" }

// Available always succeeds: planning has no external dependencies.
func (dryRunRunner) Available(ctx context.Context) error { return nil }

// ProbeBinary always reports present: a dry run never executes the agent and
// must not require a container engine or daemon to plan.
func (dryRunRunner) ProbeBinary(ctx context.Context, image, bin string) (bool, error) {
	return true, nil
}

// Run produces a RunResult describing the intended actions without executing
// anything. ExitCode is 0 and DryRun is true.
func (dryRunRunner) Run(ctx context.Context, spec RuntimeSpec, command CommandSpec) (RunResult, error) {
	var lines []string

	lines = append(lines, fmt.Sprintf("engine: %s", spec.Engine))
	lines = append(lines, fmt.Sprintf("image: %s", spec.Image))
	if spec.NetworkMode == "allowlist" {
		// Don't assert enforcement unconditionally: the claim depends on both
		// the ENGINE and this build. Deciding it from the network mode alone
		// would describe an egress proxy that never runs.
		enforce := "would be enforced via egress proxy"
		switch {
		case spec.Engine == "apple":
			// The apple engine refuses allowlist mode outright, so the real run
			// fails closed. Saying "would be enforced" here would promise
			// protection the run will never reach.
			enforce = "NOT supported on the apple engine — the run fails closed; use --engine docker or podman"
		case !netproxy.Embedded(runtime.GOARCH):
			enforce = "NOT enforceable in this build — proxy not embedded; see `andbo doctor`"
		}
		lines = append(lines, fmt.Sprintf("network: allowlist (%s: %s)", enforce, strings.Join(spec.AllowedDomains, ", ")))
	} else {
		lines = append(lines, fmt.Sprintf("network: %s", spec.NetworkMode))
	}

	if spec.User != "" {
		lines = append(lines, fmt.Sprintf("user: %s (non-root)", spec.User))
	} else {
		lines = append(lines, "user: <default>")
	}

	// Security-sensitive defaults: surface them explicitly so a reviewer can
	// spot an unsafe configuration at a glance.
	if spec.MountDockerSocket {
		lines = append(lines, "docker socket: MOUNTED (unsafe)")
	} else {
		lines = append(lines, "docker socket: not mounted")
	}
	lines = append(lines, fmt.Sprintf("privileged: %t", spec.Privileged))

	for _, p := range spec.ReadOnlyPaths {
		lines = append(lines, fmt.Sprintf("mount (ro): %s", p))
	}
	for _, p := range spec.WritePaths {
		lines = append(lines, fmt.Sprintf("mount (rw): %s", p))
	}

	// Merge spec.Env and command.Env (command overrides). Only KEYS are shown;
	// values are deliberately hidden to avoid leaking secrets.
	for _, k := range sortedEnvKeys(spec.Env, command.Env) {
		lines = append(lines, fmt.Sprintf("env: %s (value hidden)", k))
	}

	exec := command.Executable
	for _, a := range command.Args {
		exec += " " + a
	}
	lines = append(lines, fmt.Sprintf("exec: %s", exec))

	return RunResult{
		ExitCode:    0,
		DryRun:      true,
		Description: lines,
	}, nil
}

// sortedEnvKeys returns the union of keys from the provided env maps in sorted
// order, for deterministic output. Later maps do not change the key set, only
// (conceptually) the value, which is never printed.
func sortedEnvKeys(maps ...map[string]string) []string {
	seen := make(map[string]struct{})
	for _, m := range maps {
		for k := range m {
			seen[k] = struct{}{}
		}
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
