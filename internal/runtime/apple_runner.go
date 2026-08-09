package runtime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	goruntime "runtime"
	"strconv"
	"strings"
	"time"
)

// appleStatusTimeout bounds the readiness probe. `container system status`
// contacts the background service; a wedged service must not hang a run.
const appleStatusTimeout = 5 * time.Second

// appleVersionTimeout bounds the macOS version lookup. It is a local, fast
// command, but nothing Andbo shells out to during a gate may hang a run.
const appleVersionTimeout = 3 * time.Second

// AppleMinMacOSMajor is the oldest macOS major version Apple Container runs on.
// Only the MAJOR is compared: upstream states the requirement that way, and a
// finer comparison would reject point releases there is no evidence against.
const AppleMinMacOSMajor = 26

// swVersPath is the absolute path to the version tool. Absolute on purpose: a
// PATH lookup would let a directory earlier in PATH decide whether a security
// gate passes. Its argument is a fixed literal and no shell is involved.
const swVersPath = "/usr/bin/sw_vers"

// appleRunner executes commands via Apple Container's `container` CLI.
//
// Everything it depends on in the environment — build platform, macOS version,
// PATH lookup, service readiness — is injected, so the availability logic is
// unit-testable on a host with no `container` binary. That includes every CI
// runner Andbo builds on, and this development host.
type appleRunner struct {
	goos, goarch string
	macOSVersion func(context.Context) (string, error)
	lookPath     func(string) (string, error)
	systemStatus func(context.Context) error
}

// NewAppleRunner returns a Runner backed by the Apple Container CLI.
//
// It is never selected automatically: only an explicit runtime.engine: apple or
// --engine apple reaches it, and docker remains Andbo's default engine.
func NewAppleRunner() Runner {
	return appleRunner{
		goos:         goruntime.GOOS,
		goarch:       goruntime.GOARCH,
		macOSVersion: MacOSProductVersion,
		lookPath:     exec.LookPath,
		systemStatus: appleSystemStatus,
	}
}

// Name reports the ENGINE name users write in policy and --engine ("apple"),
// not the binary it invokes ("container"). Session records and `andbo doctor`
// must agree with the policy vocabulary.
func (appleRunner) Name() string { return "apple" }

// applePlatformSupported reports whether this build can drive Apple Container.
// PURE, so every branch is table-testable on any host.
//
// The OS and architecture rejections are deliberately separate messages: a
// Linux user and an Intel-Mac user have different situations, and a single
// merged message would leave one of them guessing.
func applePlatformSupported(goos, goarch string) error {
	if goos != "darwin" {
		return fmt.Errorf(
			"the apple engine requires macOS; this host is %s/%s. "+
				"Use --engine docker, --engine podman, or --dry-run.", goos, goarch)
	}
	if goarch != "arm64" {
		return fmt.Errorf(
			"the apple engine requires Apple silicon (arm64); this host is darwin/%s. "+
				"Use --engine docker, --engine podman, or --dry-run.", goarch)
	}
	return nil
}

// MacOSProductVersion returns this host's macOS version, e.g. "26.5.2".
//
// Nothing is interpolated: the binary is an absolute literal, its one argument
// is a literal, and no shell is involved. The lookup is bounded so a hung
// process cannot stall the availability gate that calls it.
//
// Only stdout is read. sw_vers prints the bare version there; keeping stderr
// out avoids a diagnostic line being parsed as a version.
func MacOSProductVersion(ctx context.Context) (string, error) {
	vctx, cancel := context.WithTimeout(ctx, appleVersionTimeout)
	defer cancel()
	cmd := exec.CommandContext(vctx, swVersPath, "-productVersion")
	cmd.WaitDelay = 2 * time.Second
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// MacOSVersionSupported reports whether version satisfies Apple Container's
// macOS requirement. PURE, so every branch is table-testable on any host.
//
// Every input it cannot read as a major version is REFUSED. Andbo cannot
// confirm a hard requirement from a version it cannot parse, and treating the
// unknown as "probably new enough" would turn a named refusal into a cryptic
// failure inside the engine.
func MacOSVersionSupported(version string) error {
	v := strings.TrimSpace(version)
	if v == "" {
		return fmt.Errorf(
			"the apple engine requires macOS %d or newer, and this host reported an empty macOS version, "+
				"so Andbo cannot confirm the requirement and refuses rather than assume. "+
				"Use --engine docker, --engine podman, or --dry-run.", AppleMinMacOSMajor)
	}
	parts := strings.Split(v, ".")
	for _, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return fmt.Errorf(
				"the apple engine requires macOS %d or newer, and Andbo could not parse the macOS version %q "+
					"reported by `sw_vers -productVersion`, so it refuses rather than assume. "+
					"Use --engine docker, --engine podman, or --dry-run.", AppleMinMacOSMajor, v)
		}
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil || major < 0 {
		return fmt.Errorf(
			"the apple engine requires macOS %d or newer, and Andbo could not parse the macOS version %q "+
				"reported by `sw_vers -productVersion`, so it refuses rather than assume. "+
				"Use --engine docker, --engine podman, or --dry-run.", AppleMinMacOSMajor, v)
	}
	if major < AppleMinMacOSMajor {
		return fmt.Errorf(
			"the apple engine requires macOS %d or newer; this host is macOS %s. "+
				"Upgrade macOS, or use --engine docker, --engine podman, or --dry-run.",
			AppleMinMacOSMajor, v)
	}
	return nil
}

// appleSystemStatus asks the Apple Container service whether it is up.
//
// Unlike the docker/podman runner — which deliberately does NOT contact the
// daemon — this check IS made, because the CLI is useless without its
// background service and the user's fix (`container system start`) is specific
// enough to be worth naming. It is bounded so a wedged service cannot hang a
// run.
//
// What `container system status` prints or exits with when the service is
// stopped is not something Andbo has verified, so this keys on NOTHING but the
// command failing: any non-nil error is reported as "not responding". It must
// never parse output or test for a specific exit code.
func appleSystemStatus(ctx context.Context) error {
	sctx, cancel := context.WithTimeout(ctx, appleStatusTimeout)
	defer cancel()
	cmd := exec.CommandContext(sctx, appleEngineBin, "system", "status")
	cmd.WaitDelay = 2 * time.Second
	out, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	if excerpt := appleExcerpt(string(out), 160); excerpt != "" {
		return fmt.Errorf("%w: %s", err, excerpt)
	}
	return err
}

// appleExcerpt trims and bounds CLI output for inclusion in an error message.
func appleExcerpt(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}

// Available gates in priority order: platform, then macOS version, then binary,
// then service. The order is enforced by short-circuit, so no subprocess is
// spawned on a host that cannot run this engine at all, and an OS too old to
// run it is named as such instead of as a missing or broken install.
func (r appleRunner) Available(ctx context.Context) error {
	if err := applePlatformSupported(r.goos, r.goarch); err != nil {
		return err
	}
	version, err := r.macOSVersion(ctx)
	if err != nil {
		return fmt.Errorf(
			"the apple engine requires macOS %d or newer, and Andbo could not determine this host's version "+
				"(`sw_vers -productVersion` failed: %w). Nothing was started. "+
				"Use --engine docker, --engine podman, or --dry-run.", AppleMinMacOSMajor, err)
	}
	if err := MacOSVersionSupported(version); err != nil {
		return err
	}
	if _, err := r.lookPath(appleEngineBin); err != nil {
		return errors.New(appleNotInstalledMsg)
	}
	if err := r.systemStatus(ctx); err != nil {
		return fmt.Errorf(
			"the `container` CLI is installed but its background service is not responding: %w.\n"+
				"Start it with `container system start`, then retry; "+
				"or switch to --engine docker, --engine podman, or --dry-run.", err)
	}
	return nil
}

// Run executes a command via Apple Container.
//
// Argument order is load-bearing: `container run` captures everything after
// the image as process arguments. Options therefore remain before the image,
// while the agent command and its arguments remain after it.
func (r appleRunner) Run(ctx context.Context, spec RuntimeSpec, command CommandSpec) (RunResult, error) {
	if err := r.Available(ctx); err != nil {
		return RunResult{ExitCode: -1}, err
	}

	if command.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, command.Timeout)
		defer cancel()
	}

	// Refuse an unsupported configuration before anything is started. The
	// builder's error is returned as-is: it already names the fix.
	args, err := BuildAppleArgs(spec, command)
	if err != nil {
		return RunResult{ExitCode: -1}, err
	}

	// Name the container so cancellation can delete it in the service. Killing
	// only the CLI client would leave the container running — the exact failure
	// mode a budget deadline exists to prevent.
	name := containerName()
	args = insertContainerName(args, name)

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, appleEngineBin, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// Security: on cancellation (budget deadline, Ctrl-C), force-delete the
	// container in the service FIRST, then kill the client process. A fresh
	// context is required because ctx is already cancelled at this point.
	cmd.Cancel = func() error {
		killCtx, c := context.WithTimeout(context.Background(), 15*time.Second)
		defer c()
		_ = exec.CommandContext(killCtx, appleEngineBin, "delete", "--force", name).Run()
		return cmd.Process.Kill()
	}
	// Bound Wait so it cannot hang on lingering pipe readers after the kill.
	cmd.WaitDelay = 5 * time.Second

	runErr := cmd.Run()
	result := RunResult{Stdout: stdout.String(), Stderr: stderr.String()}
	if runErr == nil {
		result.ExitCode = 0
		return result, nil
	}

	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
		// HONEST MAPPING, deliberately smaller than the docker runner's.
		//
		// The docker/podman path attributes exit 125 plus a recognized stderr
		// marker to the ENGINE ("the agent never ran"), which rests on docker
		// and podman documenting 125 as reserved for engine failure. This
		// engine passes the container process's own exit code straight
		// through, and reports its OWN failures as exit 1 — indistinguishable
		// from an agent that legitimately exits 1. So the docker heuristic is
		// not merely unported here, it is unimplementable: any attempt would
		// confidently mis-attribute one case or the other.
		//
		// Every non-zero exit is therefore passed through as the command's
		// own, with the CLI's stderr reaching the session verbatim. The
		// commonest engine failure — the service being down — is already
		// caught by Available() before the run, with a named fix.
		return result, nil
	}

	result.ExitCode = -1
	return result, appleStartFailure(ctx.Err(), runErr)
}

// appleStartFailure wraps a failure that is not the command's own exit: either
// the run was cancelled/deadlined, or `container` could not be started at all.
// PURE (ctxErr is passed in) so both branches are testable without the binary.
func appleStartFailure(ctxErr, runErr error) error {
	if ctxErr != nil {
		return fmt.Errorf("the apple run was interrupted (%v); the command may have exceeded its timeout: %w", ctxErr, runErr)
	}
	return fmt.Errorf("failed to execute `container`; ensure Apple Container's service is running (`container system start`): %w", runErr)
}

// ProbeBinary reports whether bin is resolvable inside image by running a
// throwaway, fully-isolated probe container. See the Runner interface for the
// (present, err) contract.
func (r appleRunner) ProbeBinary(ctx context.Context, image, bin string) (bool, error) {
	// An unavailable engine is INCONCLUSIVE, never (false, nil): the caller
	// reads a bare false as "conclusively absent" and would block the run with
	// an "install the agent in your image" error that misdiagnoses the problem.
	if err := r.Available(ctx); err != nil {
		return false, err
	}
	// Bound the probe so a wedged service cannot hang the whole run.
	pctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	name := containerName()
	args := insertContainerName(AppleProbeBinaryArgs(image, bin), name)

	var stderr bytes.Buffer
	cmd := exec.CommandContext(pctx, appleEngineBin, args...)
	cmd.Stderr = &stderr
	cmd.Cancel = func() error {
		killCtx, c := context.WithTimeout(context.Background(), 15*time.Second)
		defer c()
		_ = exec.CommandContext(killCtx, appleEngineBin, "delete", "--force", name).Run()
		return cmd.Process.Kill()
	}
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return probeResult(exitErr.ExitCode(), stderr.String(), appleEngineBin, image)
		}
		// Could not even start the CLI: inconclusive.
		return false, fmt.Errorf("probing image %q for %q via %s: %w", image, bin, appleEngineBin, err)
	}
	return true, nil
}
