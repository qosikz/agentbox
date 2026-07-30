package k8s

import (
	"fmt"
	"math"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/qosikz/andbo/internal/runtime"
)

// FromRuntimeSpec maps Andbo's container RuntimeSpec onto a JobSpec, using base
// (normally DefaultJobSpec with Name and Namespace filled in) for everything
// Kubernetes needs that RuntimeSpec does not carry.
//
// It is a pure mapping with no cluster interaction, and its value is mostly in
// what it REFUSES. Anything RuntimeSpec can express that this renderer cannot
// enforce is a hard error, never a silent downgrade, so only these fields cross
// the boundary: Image, NetworkMode, User, Executable, Args, Timeout, and the
// workspace (as a PATH REMAPPING — see mapWorkspace). Every other host-derived
// value — extra bind mounts and the resolved host environment — fails closed,
// because a Job has no access to the host and this slice has no secret
// transport. On any error the returned JobSpec is the zero value, so a caller
// that ignores the error cannot render a spec that silently lost a control.
func FromRuntimeSpec(base JobSpec, rs runtime.RuntimeSpec, cs runtime.CommandSpec) (JobSpec, error) {
	out := base

	if rs.Privileged {
		return JobSpec{}, fmt.Errorf("runtime spec requests privileged mode, which the Kubernetes renderer never emits; remove it or run this workload on the container runtime")
	}
	if rs.MountDockerSocket {
		return JobSpec{}, fmt.Errorf("runtime spec mounts the docker socket, which the Kubernetes renderer never emits (it grants control of the host daemon); remove it or run this workload on the container runtime")
	}

	hostWorkspace, err := mapWorkspace(out, rs, cs)
	if err != nil {
		return JobSpec{}, err
	}
	env, err := mapEnv(out, rs, cs, hostWorkspace)
	if err != nil {
		return JobSpec{}, err
	}
	out.Env = env

	switch rs.NetworkMode {
	case "none", "deny":
		out.NetworkMode = NetworkDeny
	case "":
		return JobSpec{}, fmt.Errorf("runtime spec has no network mode set; set it to \"deny\" (or \"none\") — the Kubernetes renderer will not guess")
	default:
		return JobSpec{}, fmt.Errorf("runtime network mode %q is not implemented for Kubernetes; only default-deny is supported here, and allowlisted egress requires the container runtime's egress proxy", rs.NetworkMode)
	}

	// An egress allowlist alongside a deny-all mode is contradictory input. The
	// rendered manifest would deny everything, so silently accepting it would
	// let a caller believe an allowlist had been applied.
	if len(rs.AllowedDomains) > 0 || len(rs.AllowedPorts) > 0 {
		return JobSpec{}, fmt.Errorf("runtime spec carries an egress allowlist (%d domain(s), %d port(s)) that the Kubernetes renderer cannot honour; clear it, or run this workload on the container runtime where the egress proxy enforces it", len(rs.AllowedDomains), len(rs.AllowedPorts))
	}

	if rs.Image != "" {
		out.Image = rs.Image
	}

	if rs.User != "" {
		uid, err := parseRunAsUser(rs.User)
		if err != nil {
			return JobSpec{}, err
		}
		out.RunAsUser = uid
	}

	// Command and args move together: taking a new executable while leaving the
	// base spec's arguments in place would build argv nobody asked for.
	if cs.Executable != "" {
		out.Command = []string{cs.Executable}
		out.Args = append([]string(nil), cs.Args...)
	}

	if cs.Timeout != 0 {
		deadline, err := deadlineSeconds(cs.Timeout)
		if err != nil {
			return JobSpec{}, err
		}
		out.ActiveDeadlineSeconds = deadline
	}

	return out, nil
}

// mapWorkspace validates the host-side workspace inputs and returns the host
// workspace path, or "" when the run has none. The path is used ONLY to
// recognise which inputs refer to the workspace so they can be remapped to the
// pod path; it never reaches a manifest, where it would leak the host's
// username and directory layout into an object applied to a shared cluster.
func mapWorkspace(base JobSpec, rs runtime.RuntimeSpec, cs runtime.CommandSpec) (string, error) {
	// Read-only host mounts have no equivalent and, unlike the workspace, there
	// is nothing to remap them to: a hostPath volume would expose the node
	// filesystem, which is the whole reason it is absent from the volume type.
	if len(rs.ReadOnlyPaths) > 0 {
		return "", fmt.Errorf("runtime spec mounts %d read-only host path(s), which have no safe Kubernetes equivalent (a hostPath volume would expose the node filesystem); bake what the agent needs into the image, or run this workload on the container runtime", len(rs.ReadOnlyPaths))
	}

	// RuntimeSpec.Workdir is a HOST path: the container runtime bind-mounts the
	// sanitized workspace copy at that same path, which is the only reason it
	// means anything there. Andbo builds exactly this shape — Workdir is the
	// workspace copy and WritePaths holds that one directory (buildRuntimeSpec
	// in internal/cli).
	host := rs.Workdir
	if host != "" {
		// Every comparison below is literal, so a second spelling of the same
		// directory would be classified as an unrelated host mount and rejected
		// — or worse, a HOME that is really the workspace would look foreign.
		// Reject rather than canonicalise: the caller's own strings must agree.
		if !strings.HasPrefix(host, "/") {
			return "", fmt.Errorf("runtime workdir %q is not an absolute path; the Kubernetes renderer matches the workspace path literally against the mount list, HOME, and the command working directory, and will not guess what a relative path resolves to on the host", host)
		}
		if host != path.Clean(host) {
			return "", fmt.Errorf("runtime workdir %q is not a clean absolute path (it resolves to %q); the Kubernetes renderer matches the workspace path literally, so a second spelling of the same directory would be treated as an unrelated host mount", host, path.Clean(host))
		}
	}

	switch {
	case len(rs.WritePaths) == 0:
	case host != "" && len(rs.WritePaths) == 1 && rs.WritePaths[0] == host:
		// The sanitized workspace copy, delivered by the transport checked below.
	default:
		return "", fmt.Errorf("runtime spec mounts %d host path(s) beyond the workspace copy; a hostPath volume would expose the node filesystem, so the only directory that can cross into a Job is the one named by RuntimeSpec.Workdir (currently %q). Drop the extra mounts, or run this workload on the container runtime", len(rs.WritePaths), host)
	}

	// CommandSpec.WorkingDir is likewise a host directory (the local runner
	// chdirs into it, and every adapter sets it to the workspace path). It is
	// accepted only when it IS the workspace, and the pod path replaces it.
	if cs.WorkingDir != "" && cs.WorkingDir != host {
		return "", fmt.Errorf("command spec sets host working directory %q, which is not the workspace this run declares (%q); a Kubernetes Job cannot reach the host filesystem, so the agent can only start inside the workspace, which the renderer maps to the pod path %q", cs.WorkingDir, host, base.WorkingDir)
	}

	if host == "" {
		return "", nil
	}

	// This is the check that prevents the silent failure. A Job has no path to
	// the host, so the workspace can only arrive by a declared transport; with
	// WorkspaceEmpty the manifest renders perfectly well and the agent starts on
	// an empty directory while the caller believes the repository is there.
	if base.WorkspaceTransport != WorkspaceFromImage {
		return "", fmt.Errorf("runtime spec carries a host workspace at %q, but the JobSpec declares workspaceTransport %q, so the agent would run against an empty volume; a Kubernetes Job cannot reach the host filesystem, so set WorkspaceTransport to %q and ImageWorkspacePath to the directory inside the image that holds the workspace", host, base.WorkspaceTransport, WorkspaceFromImage)
	}

	return host, nil
}

// mapEnv merges the runtime and command environments (command wins, matching
// the container runtime) and refuses everything except HOME.
//
// The environment carried by a RuntimeSpec/CommandSpec is RESOLVED HOST
// ENVIRONMENT: Andbo populates it by reading every name in policy.secrets.allow
// out of the host, and feeds those same values to the log redactor — the
// codebase classifies them as secrets. A rendered manifest is plain text stored
// in etcd and readable by anyone who can get the Job, and envVar has no
// valueFrom, so there is no safe way to carry them. Errors name no value.
//
// HOME is the one exception, and it belongs to the workspace rather than to the
// secret set: internal/cli points it at the workspace copy so git and package
// managers work under a non-root UID. It is REWRITTEN to the pod working
// directory — keeping the host value would leak the host layout, and dropping
// it would leave HOME on the read-only root filesystem, where those same tools
// fail. It is bridged ONLY when it is exactly the workspace path; a HOME
// pointing anywhere else is an arbitrary host directory and is refused.
func mapEnv(base JobSpec, rs runtime.RuntimeSpec, cs runtime.CommandSpec, hostWorkspace string) (map[string]string, error) {
	merged := make(map[string]string, len(rs.Env)+len(cs.Env))
	for k, v := range rs.Env {
		merged[k] = v
	}
	for k, v := range cs.Env {
		merged[k] = v
	}

	home, hasHome := merged["HOME"]
	bridgeHome := hasHome && hostWorkspace != "" && home == hostWorkspace
	unbridgeable := len(merged)
	if bridgeHome {
		unbridgeable--
	}
	if unbridgeable > 0 {
		return nil, fmt.Errorf("runtime/command environment carries %d variable(s) that cannot be bridged: Andbo resolves host secrets into it, and this renderer would inline them as plain text in the manifest; deliver them through a Kubernetes Secret (not supported in this slice), or set JobSpec.Env yourself with non-secret literals only. HOME is the only bridged name, and only when it points at the workspace", unbridgeable)
	}
	if !bridgeHome {
		return base.Env, nil
	}

	// JobSpec is copied by value but its Env map is not, so writing HOME in
	// place would edit the caller's map behind its back.
	env := make(map[string]string, len(base.Env)+1)
	for k, v := range base.Env {
		env[k] = v
	}
	env["HOME"] = base.WorkingDir
	return env, nil
}

// parseRunAsUser converts a docker-style "uid" or "uid:gid" string to a UID.
// Names are rejected: Kubernetes needs a numeric UID to enforce runAsNonRoot,
// and resolving a name would require reading the image's /etc/passwd.
func parseRunAsUser(user string) (int64, error) {
	uidPart, gidPart, hasGID := strings.Cut(user, ":")

	uid, err := strconv.ParseInt(uidPart, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("runtime user %q is not numeric; Kubernetes needs a numeric UID to enforce runAsNonRoot (e.g. \"10001:10001\")", user)
	}
	if err := checkUID(uid); err != nil {
		return 0, fmt.Errorf("runtime user %q is invalid: %w", user, err)
	}

	if hasGID {
		gid, gerr := strconv.ParseInt(gidPart, 10, 64)
		if gerr != nil {
			return 0, fmt.Errorf("runtime user %q has a non-numeric group; Kubernetes needs a numeric GID (e.g. \"10001:10001\")", user)
		}
		// JobSpec carries a single UID that is also used as the GID and fsGroup.
		// Accepting a different GID would silently drop it from the manifest.
		if gid != uid {
			return 0, fmt.Errorf("runtime user %q sets a GID that differs from its UID; the Kubernetes renderer runs the agent with matching UID, GID, and fsGroup, so use \"%d:%d\"", user, uid, uid)
		}
	}

	return uid, nil
}

// checkUID enforces the non-root guarantee across the whole representable
// range. The upper bound matters: Linux uid_t is 32 bits, so a value such as
// 2^32 is positive here but truncates to 0 — root — for anything that narrows
// it, which is exactly the outcome the non-root rule exists to prevent.
func checkUID(uid int64) error {
	if uid <= 0 {
		return fmt.Errorf("UID %d is not a non-root UID; agents must never run as root", uid)
	}
	if uid > math.MaxInt32 {
		return fmt.Errorf("UID %d is outside the 32-bit UID range (1..%d) and would truncate, potentially to root", uid, math.MaxInt32)
	}
	return nil
}

// deadlineSeconds converts a command timeout to whole seconds, rounding up so a
// sub-second timeout never becomes an unbounded run.
func deadlineSeconds(d time.Duration) (int64, error) {
	if d < 0 {
		return 0, fmt.Errorf("command timeout %s is negative; set a positive budget or leave it unset to use the default", d)
	}
	// The cap is checked BEFORE rounding up: for a duration near the int64
	// limit, adding a second first would overflow to a negative value and
	// silently produce an unbounded deadline.
	if d > MaxActiveDeadlineSeconds*time.Second {
		return 0, fmt.Errorf("command timeout %s exceeds the %d-second cap on activeDeadlineSeconds; lower the budget", d, MaxActiveDeadlineSeconds)
	}
	return int64((d + time.Second - 1) / time.Second), nil
}
