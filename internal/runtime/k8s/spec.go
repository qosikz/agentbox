// Package k8s renders hardened Kubernetes manifests for one agent run: a Job
// plus a default-deny NetworkPolicy that selects that Job's pod.
//
// The package RENDERS AND VALIDATES ONLY. It never contacts a cluster, reads a
// kubeconfig, or applies anything — the output is a YAML string an external
// scheduler (or a future Andbo Kubernetes backend) applies itself. Keeping the
// contract pure is what lets Andbo's security defaults be asserted by tests
// without a live cluster.
//
// Security model:
//
//   - JobSpec is the ONLY input. It carries no field for privileged mode, host
//     namespaces, host paths, service-account token mounting, added
//     capabilities, or non-emptyDir volumes, so no caller input can render
//     them. The hardened values are constants inside the manifest structs.
//   - Manifests are produced by marshalling typed structs, never by string
//     templating, so user data (task text, env values) can only ever become a
//     YAML scalar — it cannot inject manifest structure.
//   - Everything unbounded is rejected at the boundary: resources, deadline,
//     TTL, and scratch space must all be set and within caps.
//   - Unsupported network modes fail closed (see NetworkMode).
package k8s

// NetworkMode selects the egress posture of the rendered Job.
type NetworkMode string

const (
	// NetworkDeny renders a NetworkPolicy that denies all ingress and egress
	// for the Job's pod. It is the only mode this package implements.
	NetworkDeny NetworkMode = "deny"

	// NetworkAllowlist is NOT implemented here. Andbo's domain allowlist is
	// enforced by a per-run egress proxy in the container runtime; Kubernetes
	// NetworkPolicy selects by IP/namespace/pod, not by domain, so there is no
	// equivalent. Requesting it is a validation error, never a silent downgrade.
	NetworkAllowlist NetworkMode = "allowlist"

	// NetworkOpen is NOT implemented here: unrestricted egress from a cluster
	// workload is an unsafe mode that this renderer does not offer.
	NetworkOpen NetworkMode = "open"
)

// Bounds and secure defaults. Every cap exists so a rendered Job cannot occupy
// a cluster indefinitely if the applying scheduler forgets to supervise it.
const (
	// MaxNameLength leaves room for the ~6-character suffix Kubernetes appends
	// when deriving pod names from a Job name, within the 63-character limit
	// that also applies to the label values the NetworkPolicy selects on.
	MaxNameLength = 52

	// MaxNamespaceLength is the Kubernetes limit on a namespace name. The
	// DNS-1123 character rules alone do not bound length.
	MaxNamespaceLength = 63

	MaxActiveDeadlineSeconds   = 24 * 60 * 60
	MaxTTLSecondsAfterFinished = 24 * 60 * 60

	DefaultActiveDeadlineSeconds   = 1800
	DefaultTTLSecondsAfterFinished = 600

	// DefaultRunAsUser matches the non-root UID used by the container runtime.
	DefaultRunAsUser = 10001

	DefaultWorkingDir = "/work"
	tmpDir            = "/tmp"

	workspaceVolume = "workspace"
	tmpVolume       = "tmp"
	containerName   = "agent"

	labelName      = "app.kubernetes.io/name"
	labelInstance  = "app.kubernetes.io/instance"
	labelManagedBy = "app.kubernetes.io/managed-by"
	labelValueName = "andbo"
)

// JobSpec is the complete, explicit input contract for a rendered run.
//
// There is deliberately no field here for any privileged capability: the
// absence of the field is the enforcement mechanism, not a default that could
// be overridden later.
type JobSpec struct {
	// Name is the Job name and the instance label the NetworkPolicy selects on.
	Name string
	// Namespace must already exist; the renderer does not create it.
	Namespace string
	// Image is the agent image. Pin it by digest in production.
	Image string
	// Command is the container entrypoint (argv[0] and any fixed arguments).
	// It is required: relying on the image's entrypoint would make the rendered
	// manifest an incomplete description of what runs.
	Command []string
	// Args are appended to Command.
	Args []string
	// Env holds literal environment values only. Do not put secrets here — the
	// manifest is plain text and usually ends up in version control or CI logs.
	Env map[string]string
	// WorkingDir must be absolute and is backed by a writable emptyDir.
	WorkingDir string

	CPURequest    string
	CPULimit      string
	MemoryRequest string
	MemoryLimit   string

	// WorkspaceSizeLimit and TmpSizeLimit bound the emptyDir volumes that make
	// readOnlyRootFilesystem workable. Unbounded emptyDir can fill a node's disk.
	WorkspaceSizeLimit string
	TmpSizeLimit       string

	// ActiveDeadlineSeconds is the wall-clock budget; Kubernetes terminates the
	// Job when it elapses.
	ActiveDeadlineSeconds int64
	// TTLSecondsAfterFinished is when the finished Job (and its pod) is garbage
	// collected. It requires the TTL-after-finished controller.
	TTLSecondsAfterFinished int64

	// RunAsUser is the non-root UID the agent runs as. Zero is rejected.
	RunAsUser int64

	// NetworkMode must be NetworkDeny; anything else fails closed.
	NetworkMode NetworkMode

	// RuntimeClassName optionally selects a sandboxed runtime (e.g. "gvisor",
	// "kata"). Rendered only when explicitly set.
	RuntimeClassName string
	// ServiceAccountName optionally names a service account. Rendered only when
	// explicitly set, and never with token automounting: naming an identity is
	// for image pulls and admission, not for Kubernetes API access.
	ServiceAccountName string
}

// DefaultJobSpec returns the secure defaults. Name, Namespace, Image, and
// Command have no safe default and must be supplied by the caller.
func DefaultJobSpec() JobSpec {
	return JobSpec{
		WorkingDir:              DefaultWorkingDir,
		CPURequest:              "100m",
		CPULimit:                "1",
		MemoryRequest:           "256Mi",
		MemoryLimit:             "1Gi",
		WorkspaceSizeLimit:      "1Gi",
		TmpSizeLimit:            "64Mi",
		ActiveDeadlineSeconds:   DefaultActiveDeadlineSeconds,
		TTLSecondsAfterFinished: DefaultTTLSecondsAfterFinished,
		RunAsUser:               DefaultRunAsUser,
		NetworkMode:             NetworkDeny,
	}
}

// EnforcementNotes states what the rendered manifests do NOT guarantee. Andbo
// does not claim controls it has not implemented, and every one of these is a
// property the renderer cannot verify on its own.
func (s JobSpec) EnforcementNotes() []string {
	return []string{
		"manifests are rendered only; this package never contacts a cluster — whoever applies them is responsible for applying the NetworkPolicy before (or with) the Job",
		"the default-deny NetworkPolicy is inert unless the cluster CNI implements NetworkPolicy (Calico, Cilium, and similar do; plain flannel does not) — verify enforcement on your cluster. Some CNIs also program policy shortly AFTER the pod gets an IP, leaving a brief window at startup",
		"NetworkPolicies are ADDITIVE and this one cannot subtract from another: any other NetworkPolicy in the namespace that also selects this pod, and any cluster-scoped AdminNetworkPolicy, GlobalNetworkPolicy, or CiliumClusterwideNetworkPolicy Allow rule, grants egress this policy cannot remove. A namespace-wide allow-dns-egress policy is a common baseline and would hand the agent DNS, a well-known exfiltration channel. Run agents in a dedicated namespace and audit the namespaced and cluster-scoped policy objects before applying",
		"default-deny egress covers DNS too, so this policy on its own leaves the agent with no name resolution and no outbound path — subject to the two notes above, which are what decide whether it holds in your cluster",
		"the NetworkPolicy must exist for the WHOLE lifetime of the pod. It carries no ownerReference, so it is neither garbage-collected with the Job nor protected from removal: deleting it (or pruning by label while a run is in flight) restores full egress to a running agent immediately. A persistent namespace-wide default-deny is the durable backstop",
		"domain allowlisting is not available in this renderer: NetworkPolicy selects by IP, namespace, and pod, not by domain — use the container runtime for network.mode=allowlist",
		"the NetworkPolicy restricts the pod, not the node: image pulls are performed by the kubelet and are unaffected by it",
		"pod-level hardening does not contain a container escape by itself; add a sandboxed RuntimeClassName (gvisor, kata) and cluster admission policy (Pod Security Standards: restricted) for defense in depth",
		"backoffLimit 0 prevents retries after failure, but Kubernetes does not guarantee at-most-once execution: node failure, preemption, or pod deletion can start the same run a second time. Agent side effects must be idempotent or keyed by a run ID",
		"resource limits, activeDeadlineSeconds, and ttlSecondsAfterFinished depend on cluster controllers being healthy; ttlSecondsAfterFinished additionally requires the TTL-after-finished controller",
		"CPU and memory limits are required and checked for form and ordering, but this renderer sets no ceiling on how large they may be — use a namespace ResourceQuota or LimitRange for that",
		"only the working directory and " + tmpDir + " are writable. In particular $HOME is NOT: most images point it at the read-only root filesystem, so git, npm, and pip will fail until you redirect HOME (and any XDG cache/config paths) into the working directory via JobSpec.Env",
		"env values are rendered literally into plain-text manifests that live in etcd and are readable by anyone who can get the Job: never place secrets in JobSpec.Env",
	}
}

// jobLabels are set on the Job, the pod template, and the NetworkPolicy
// selector. Render asserts the selector and the pod template agree.
func (s JobSpec) jobLabels() map[string]string {
	return map[string]string{
		labelName:      labelValueName,
		labelInstance:  s.Name,
		labelManagedBy: labelValueName,
	}
}

// selectorLabels are the subset the NetworkPolicy matches on. They must be a
// subset of jobLabels or the policy silently stops applying.
func (s JobSpec) selectorLabels() map[string]string {
	return map[string]string{
		labelName:     labelValueName,
		labelInstance: s.Name,
	}
}
