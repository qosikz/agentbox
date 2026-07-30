package k8s

import (
	"fmt"
	"math"
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
// the boundary: Image, NetworkMode, User, Executable, Args, and Timeout.
// Everything host-derived — bind mounts, the host working directory, and the
// resolved host environment — fails closed, because a Job has no access to the
// host and this slice has no workspace or secret transport. On any error the
// returned JobSpec is the zero value, so a caller that ignores the error cannot
// render a spec that silently lost a control.
func FromRuntimeSpec(base JobSpec, rs runtime.RuntimeSpec, cs runtime.CommandSpec) (JobSpec, error) {
	out := base

	if rs.Privileged {
		return JobSpec{}, fmt.Errorf("runtime spec requests privileged mode, which the Kubernetes renderer never emits; remove it or run this workload on the container runtime")
	}
	if rs.MountDockerSocket {
		return JobSpec{}, fmt.Errorf("runtime spec mounts the docker socket, which the Kubernetes renderer never emits (it grants control of the host daemon); remove it or run this workload on the container runtime")
	}
	if len(rs.ReadOnlyPaths) > 0 || len(rs.WritePaths) > 0 {
		return JobSpec{}, fmt.Errorf("runtime spec mounts %d host path(s), which have no safe Kubernetes equivalent (a hostPath volume would expose the node filesystem); ship the workspace inside the image or fetch it in the container instead", len(rs.ReadOnlyPaths)+len(rs.WritePaths))
	}

	// RuntimeSpec.Workdir is a HOST path: the container runtime bind-mounts the
	// workspace at the same path, which is the only reason it means anything
	// there. This renderer refuses that mount, so keeping the path would show a
	// reviewer a workspace directory backed by an empty volume — and would leak
	// the host's username and layout into a manifest applied to a shared
	// cluster. The pod working directory comes from JobSpec.WorkingDir instead.
	if rs.Workdir != "" {
		return JobSpec{}, fmt.Errorf("runtime spec sets host working directory %q; a Kubernetes Job cannot reach the host filesystem, so set the pod working directory on the JobSpec (default %q) and leave RuntimeSpec.Workdir empty", rs.Workdir, DefaultWorkingDir)
	}
	// CommandSpec.WorkingDir is likewise a host directory (the local runner
	// chdirs into it, and every adapter sets it to the workspace path).
	if cs.WorkingDir != "" {
		return JobSpec{}, fmt.Errorf("command spec sets host working directory %q, but a Kubernetes Job cannot reach the host filesystem and this renderer has no workspace transport yet; deliver the workspace into the pod (baked into the image, or fetched by the agent) and leave CommandSpec.WorkingDir empty", cs.WorkingDir)
	}

	// The environment carried by a RuntimeSpec/CommandSpec is RESOLVED HOST
	// ENVIRONMENT: Andbo populates it by reading every name in
	// policy.secrets.allow out of the host, and feeds those same values to the
	// log redactor — the codebase classifies them as secrets. A rendered
	// manifest is plain text stored in etcd and readable by anyone who can get
	// the Job, and envVar here has no valueFrom, so there is no safe way to
	// carry them. Fail closed rather than inline a live credential. The error
	// deliberately names no value.
	if len(rs.Env) > 0 || len(cs.Env) > 0 {
		return JobSpec{}, fmt.Errorf("runtime/command environment (%d variable(s)) cannot be bridged: Andbo resolves host secrets into it, and this renderer would inline them as plain text in the manifest; deliver them through a Kubernetes Secret (not supported in this slice), or set JobSpec.Env yourself with non-secret literals only", len(rs.Env)+len(cs.Env))
	}

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
