package runtime

import (
	"errors"
	"fmt"
	"strings"
)

// Refusal messages. Andbo refuses these configurations instead of approximating
// them: running with silently-reduced isolation would give the user the security
// posture they did not ask for, under the name of the one they did.
const (
	applePrivilegedMsg = "privileged mode is not supported by the apple engine: `container run` has no --privileged flag, " +
		"and Andbo will not run silently without the isolation you asked to drop. Nothing was started. " +
		"Use --engine docker or --engine podman if you need it."

	appleDockerSocketMsg = "mounting the docker socket is not supported by the apple engine: there is no Docker daemon " +
		"socket to mount, and Andbo will not substitute another one. Nothing was started. " +
		"Use --engine docker with --allow-docker-socket if you really need it (unsafe)."

	appleNotInstalledMsg = "the `container` CLI is not available. Install Apple Container " +
		"(https://github.com/apple/container/releases), or switch to --engine docker, " +
		"--engine podman, or --dry-run."
)

// Apple Container support (engine name "apple", CLI binary "container").
//
// Apple's `container` CLI is Docker-SHAPED but not Docker-compatible, so this
// file deliberately does NOT reuse BuildDockerArgs. Two flags Andbo's docker
// path always emits do not exist in the apple CLI at all:
//
//   - --security-opt no-new-privileges: no equivalent exists. Capability
//     dropping (--cap-drop ALL) carries over, but setuid privilege escalation
//     inside the container is NOT blocked the way it is on docker/podman.
//     This is a genuine reduction in hardening and is documented as such in
//     README.md and CHANGELOG.md — it is not papered over here.
//   - --privileged: no equivalent exists; a spec requesting it is refused.
//
// The apple CLI is built on swift-argument-parser, which REJECTS unknown
// options rather than ignoring them, so passing docker-only flags would fail
// every run at argument parsing. A separate, pure argument builder keeps the
// docker/podman contract frozen and makes the apple differences explicit.
const (
	// appleEngineBin is the CLI binary name. Note it differs from the
	// user-facing engine name ("apple"), which is what policy and --engine use.
	appleEngineBin = "container"

	// appleNoNetwork is upstream's reserved network name meaning "no network
	// attachment" (NetworkClient.swift: "The reserved name that indicates a
	// container should have no network attachment"). Upstream's network
	// service refuses to CREATE a network with this name, so a user-defined
	// network cannot shadow it — the fail-closed default is structural.
	appleNoNetwork = "none"

	// appleDefaultNetwork is the network the apple CLI attaches to implicitly
	// when --network is omitted. Andbo therefore ALWAYS emits an explicit
	// --network: omitting it would silently grant the default (connected)
	// network, which is the opposite of Andbo's default-deny posture.
	appleDefaultNetwork = "default"
)

// BuildAppleArgs returns the Apple Container CLI arguments for a run.
//
// It is pure and returns nil arguments for every unsupported configuration.
// Refusing before execution prevents a requested security property from being
// silently weakened by the backend.
func BuildAppleArgs(spec RuntimeSpec, command CommandSpec) ([]string, error) {
	if spec.Privileged {
		return nil, errors.New(applePrivilegedMsg)
	}
	if spec.MountDockerSocket {
		return nil, errors.New(appleDockerSocketMsg)
	}
	network, err := appleNetworkArg(spec.NetworkMode)
	if err != nil {
		return nil, err
	}
	// Reject mount paths the engine's --mount parser cannot represent, so the
	// user gets a named path instead of a cryptic "unknown directive" from the
	// CLI. The fail direction is CLOSED, so this is robustness and UX rather
	// than a vulnerability: the parser has no "rw" or negating directive
	// (readonly/ro are append-only), so a crafted path can only ADD read-only,
	// never strip it.
	for _, p := range spec.ReadOnlyPaths {
		if err := appleCheckMountPath(p); err != nil {
			return nil, err
		}
	}
	for _, p := range spec.WritePaths {
		if err := appleCheckMountPath(p); err != nil {
			return nil, err
		}
	}

	// Security hardening. --cap-drop ALL is the only capability control this
	// engine offers: there is NO --security-opt no-new-privileges equivalent,
	// and Andbo does not silently substitute something weaker in its place.
	//
	// --network is emitted UNCONDITIONALLY, and that is load-bearing: omitting
	// it does NOT mean "no network" on this engine. `container system start`
	// creates a vmnet network named "default" that containers attach to unless
	// told otherwise, so an omitted flag falls OPEN.
	args := []string{
		"run", "--rm",
		"--cap-drop", "ALL",
		"--network", network,
	}

	if spec.User != "" {
		args = append(args, "--user", spec.User)
	}

	// Root filesystem read-only, always. Writes must arrive as explicit bind
	// mounts below, so anything the policy did not grant is not writable.
	//
	// --tmpfs /tmp is emitted WITH it, never separately. Andbo does not set
	// --read-only on docker/podman, so --read-only alone would mean the same
	// policy and image succeed on docker and fail on apple with an opaque write
	// error the moment anything touches /tmp.
	args = append(args, "--read-only", "--tmpfs", "/tmp")

	if spec.Workdir != "" {
		args = append(args, "-w", spec.Workdir)
	}

	// Environment in sorted key order for deterministic, testable output.
	// Merge command.Env over spec.Env (command wins on conflict).
	for _, k := range sortedEnvKeysForDocker(spec.Env, command.Env) {
		v := spec.Env[k]
		if cv, ok := command.Env[k]; ok {
			v = cv
		}
		// Security: ALWAYS emit "K=V", never a bare "K". Apple's -e treats a
		// bare key as "inherit this variable from the HOST environment" —
		// exactly the host-env passthrough Andbo exists to prevent. An empty
		// value must still emit "K=", not "K".
		args = append(args, "-e", k+"="+v)
	}

	// Bind mounts. Apple has no docker-style "-v host:ctr:ro" third field; the
	// read-only marker is a key in --mount, so a naive port of the docker form
	// would mount a read-only path WRITABLE.
	for _, p := range spec.ReadOnlyPaths {
		args = append(args, "--mount", appleBindMount(p, true))
	}
	for _, p := range spec.WritePaths {
		args = append(args, "--mount", appleBindMount(p, false))
	}

	args = append(args, spec.Image)
	args = append(args, command.Executable)
	args = append(args, command.Args...)

	return args, nil
}

// AppleProbeBinaryArgs returns the `container` CLI arguments for a probe
// container that checks whether bin is resolvable inside image. It mirrors
// ProbeBinaryArgs: same intent, same shell-agnostic sentinel exit, and it
// mounts nothing.
//
// The hardening is deliberately one flag short of the docker probe: Apple
// Container has no --security-opt, so this probe cannot forbid setuid privilege
// regain. It still drops all capabilities, takes no network, and runs non-root.
// Stated plainly rather than glossed: the isolation here is weaker.
func AppleProbeBinaryArgs(image, bin string) []string {
	return []string{
		"run", "--rm",
		"--cap-drop", "ALL",
		"--network", appleNoNetwork,
		// Non-root, matching the run path. command -v needs no privileges.
		"--user", "10001:10001",
		// Override any custom entrypoint so the probe is the shell test below,
		// not the agent itself (which could be long-running or interactive).
		"--entrypoint", "sh",
		image,
		// `--` ends option parsing so a binary name beginning with "-" is
		// treated as an operand. The sentinel exit keeps the present/absent
		// decision shell-agnostic.
		"-c", "command -v -- " + shellSingleQuote(bin) + " >/dev/null 2>&1 && exit 0 || exit " + fmt.Sprint(probeAbsentExit),
	}
}

// appleCheckMountPath rejects a path that Apple Container's comma-separated
// --mount syntax cannot represent.
func appleCheckMountPath(path string) error {
	for _, bad := range []string{",", "="} {
		if strings.Contains(path, bad) {
			return fmt.Errorf(
				"mount path %q contains %q, which the apple engine's --mount parser treats as a separator, "+
					"so the path cannot be expressed on this engine. Nothing was started. "+
					"Rename or relocate the path, or use --engine docker or --engine podman.",
				path, bad)
		}
	}
	return nil
}

// appleBindMount renders a host path as an apple --mount operand. The path is a
// single argv element and is never shell-interpolated.
//
// Two upstream constraints apply to the source path, and neither is enforced
// here (the engine reports both): it must be an existing DIRECTORY — unlike
// docker, this engine rejects binding a single file — and it must not contain
// "," or "=", which are the separators --mount is parsed with. A path carrying
// either is rejected by BuildAppleArgs rather than left to a cryptic CLI error.
func appleBindMount(path string, readonly bool) string {
	m := "type=bind,source=" + path + ",target=" + path
	if readonly {
		m += ",readonly"
	}
	return m
}

// appleNetworkArg maps a policy network mode onto an apple `--network` value.
//
// Security: the default branch maps every unrecognized value — including "" —
// to the no-attachment network, so an unknown or malformed mode fails closed
// rather than inheriting the connected default network.
//
// "allowlist" is refused outright. Andbo enforces egress allowlists with a
// dual-homed proxy sidecar, which requires attaching one container to a second
// network after it starts; the apple CLI has no `container network connect`
// (or any documented multi-attachment path), so the enforcement mechanism
// cannot be constructed. Refusing is the only honest option: silently running
// without enforcement would be a fail-open, and silently denying all egress
// would misrepresent the policy the user wrote.
func appleNetworkArg(mode string) (string, error) {
	switch mode {
	case "open", "bridge":
		return appleDefaultNetwork, nil
	case "allowlist":
		return "", errors.New(
			"network.mode=allowlist is not supported on the apple engine in this release, so the run fails closed. " +
				"Andbo enforces an egress allowlist with a proxy sidecar that must be attached to a second network, " +
				"and the apple `container` CLI provides no way to express that (it has no `network connect` command). " +
				"Nothing was started. " +
				"Use --engine docker or --engine podman for allowlist enforcement, or set network.mode=deny.")
	default: // "deny", "none", "" and any unrecognized value: fail closed.
		return appleNoNetwork, nil
	}
}
