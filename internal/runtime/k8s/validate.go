package k8s

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

var (
	// dns1123Label is the Kubernetes label-value / namespace form. The Job name
	// is held to it because it is also used as a label value the NetworkPolicy
	// selects on — a name that cannot be a label would break the selector.
	dns1123Label = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	// dns1123Subdomain is the form for ServiceAccount and RuntimeClass names.
	dns1123Subdomain = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)
	// envName is the POSIX-portable environment variable name form.
	envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// reservedNamespacePrefix is the prefix Kubernetes reserves for its own system
// namespaces. Scheduling an agent in one would place it next to control-plane
// workloads and their service accounts.
//
// The check is on the PREFIX rather than on a list of names. The three
// namespaces every cluster ships — kube-system, kube-public, kube-node-lease —
// are not the whole set: the flannel CNI installs into kube-flannel, and the
// prefix is reserved precisely so distributions can add more. A list would
// silently stop covering whatever the next one is called.
//
// The consequence is more than co-tenancy. EnforcementNotes names another
// NetworkPolicy in the same namespace as the way this Job's default-deny is
// defeated: policies are ADDITIVE, so whatever the cluster's own components
// already have in their namespace, this one cannot subtract from it.
//
// The prefix bounds the guard to what Kubernetes itself reserves. It does NOT
// cover the system namespaces other projects and distributions choose for
// themselves — calico-system, tigera-operator, istio-system, openshift-*,
// metallb-system — which this renderer has no way to recognise. Note 3 of
// EnforcementNotes is the standing answer for those: run agents in a dedicated
// namespace, and audit the policy objects before applying.
const reservedNamespacePrefix = "kube-"

// reservedMountPaths are directories the container image or the kernel owns.
// The working directory is mounted as an EMPTY volume, so overlaying any of
// these hides what the image put there. tmpDir is included because the renderer
// already mounts its own scratch volume at that path.
var reservedMountPaths = []string{
	"/bin", "/boot", "/dev", "/etc", "/lib", "/lib32", "/lib64",
	"/proc", "/root", "/run", "/sbin", "/sys", "/usr", "/var", tmpDir,
}

// reservedMountPath reports whether dir is, or sits under, a reserved path.
func reservedMountPath(dir string) (string, bool) {
	for _, p := range reservedMountPaths {
		if dir == p || strings.HasPrefix(dir, p+"/") {
			return p, true
		}
	}
	return "", false
}

// pathsOverlap reports whether a and b name the same directory or one contains
// the other.
//
// "/" is handled first because the prefix form gets it wrong: b+"/" would be
// "//", so pathsOverlap("/", "/work") would say the root contains nothing.
// Nothing reaches here with "/" today — Validate rejects a "/" workspace source
// earlier — but a containment test that is wrong for the root is a trap for
// whoever removes that check as redundant.
//
// The prefix test is only meaningful for clean absolute paths. Callers pass
// values Validate constrains, but it collects all problems rather than stopping
// at the first, so an unclean WorkingDir can still reach this and hide a real
// overlap. That is safe only because the same Validate run rejects the spelling
// on its own.
func pathsOverlap(a, b string) bool {
	if a == "/" || b == "/" {
		return true
	}
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

// unsafeRune reports whether r must never reach a rendered manifest.
//
// Beyond the obvious control characters, two classes matter specifically here:
//
//   - U+2028 and U+2029 are YAML line breaks, and the emitter writes them RAW
//     inside a quoted scalar. Parsers disagree on whether they end a line, so
//     the same manifest can be read two ways — the exact ambiguity a security
//     contract must not contain.
//   - Bidirectional overrides and zero-width characters are invisible to
//     whoever reviews the manifest before applying it (Trojan Source). What a
//     human approves has to be what the cluster runs.
func unsafeRune(r rune) bool {
	switch {
	case r < 0x20 || r == 0x7f: // C0 controls and DEL
		return true
	case r >= 0x80 && r <= 0x9f: // C1 controls, including NEL (U+0085)
		return true
	case r == 0x2028 || r == 0x2029: // line and paragraph separators
		return true
	case r == 0x200b || r == 0x200c || r == 0x200d || r == 0xfeff: // zero-width, BOM
		return true
	case r == 0x200e || r == 0x200f: // bidi marks
		return true
	case r >= 0x202a && r <= 0x202e: // bidi embeddings and overrides
		return true
	case r >= 0x2066 && r <= 0x2069: // bidi isolates
		return true
	}
	return false
}

// hasUnsafeRunes reports whether v is malformed UTF-8 or contains any rune that
// must not reach a manifest, optionally allowing newline and tab.
func hasUnsafeRunes(v string, allowNewlineTab bool) bool {
	if !utf8.ValidString(v) {
		return true
	}
	for _, r := range v {
		if (r == '\n' || r == '\t') && allowNewlineTab {
			continue
		}
		if unsafeRune(r) {
			return true
		}
	}
	return false
}

// Validate reports every problem with the spec at once, so a caller fixing a
// manifest does not have to re-run to discover the next error.
func (s JobSpec) Validate() error {
	var problems []string
	add := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	// Identity.
	switch {
	case s.Name == "":
		add("name must not be empty; set it to a short identifier for this run (e.g. \"fix-tests\")")
	case !dns1123Label.MatchString(s.Name):
		add("name %q is not a DNS-1123 label; use lowercase letters, digits, and '-', starting and ending alphanumerically", s.Name)
	}
	if len(s.Name) > MaxNameLength {
		add("name is %d characters; Kubernetes appends a suffix when deriving pod names, so keep it to %d characters or fewer", len(s.Name), MaxNameLength)
	}

	switch {
	case s.Namespace == "":
		add("namespace must not be empty; name an existing namespace dedicated to agent runs (this renderer does not create it)")
	case !dns1123Label.MatchString(s.Namespace):
		add("namespace %q is not a DNS-1123 label; use lowercase letters, digits, and '-'", s.Namespace)
	case len(s.Namespace) > MaxNamespaceLength:
		add("namespace is %d characters; Kubernetes namespace names are limited to %d", len(s.Namespace), MaxNamespaceLength)
	case strings.HasPrefix(s.Namespace, reservedNamespacePrefix):
		add("namespace %q is reserved for cluster control-plane workloads: Kubernetes reserves the %q prefix for its own system namespaces, so a Job there shares a namespace with cluster components and their service accounts. NetworkPolicies are also additive, so whatever policy that namespace already carries, this Job's default-deny cannot subtract from it. Use a dedicated namespace for agent runs (e.g. \"andbo-runs\")", s.Namespace, reservedNamespacePrefix)
	}

	// Workload.
	switch {
	case s.Image == "":
		add("image must not be empty; set the agent image, pinned by digest where possible")
	case hasUnsafeRunes(s.Image, false) || strings.ContainsAny(s.Image, " \t"):
		add("image %q contains whitespace, control, bidirectional, or zero-width characters", s.Image)
	case len(s.Image) > 512:
		add("image reference is %d characters, which exceeds the 512-character limit", len(s.Image))
	}

	if len(s.Command) == 0 {
		add("command must not be empty; state the entrypoint explicitly so the manifest fully describes what runs")
	}
	for i, c := range s.Command {
		if c == "" {
			add("command[%d] is empty", i)
		} else if hasUnsafeRunes(c, false) {
			add("command[%d] contains control, bidirectional, or zero-width characters", i)
		}
	}
	for i, a := range s.Args {
		// Newline and tab are allowed here: an agent task description is
		// legitimately multi-line. Everything else (including a bare carriage
		// return, which YAML line-break normalisation would rewrite) is not.
		if hasUnsafeRunes(a, true) {
			add("args[%d] contains control, bidirectional, or zero-width characters (only newline and tab are allowed here)", i)
		}
	}

	for _, name := range sortedKeys(s.Env) {
		if !envName.MatchString(name) {
			add("env name %q is invalid; use letters, digits, and '_', not starting with a digit", name)
		}
		if hasUnsafeRunes(s.Env[name], false) {
			add("env value for %q contains control, bidirectional, or zero-width characters", name)
		}
	}

	switch {
	case s.WorkingDir == "" || !strings.HasPrefix(s.WorkingDir, "/"):
		add("workingDir %q must be an absolute path; it is backed by a writable emptyDir volume", s.WorkingDir)
	case s.WorkingDir == "/":
		add("workingDir must not be \"/\"; the root filesystem is read-only by design")
	case hasUnsafeRunes(s.WorkingDir, false):
		add("workingDir contains control, bidirectional, or zero-width characters")
	case s.WorkingDir != path.Clean(s.WorkingDir):
		// The reserved-path check below and Kubernetes' own duplicate-mountPath
		// validation both compare mount paths as literal strings, while the
		// KERNEL resolves them. Without this, "/work/../etc" slips past both and
		// still mounts an empty volume over /etc. Reject rather than silently
		// canonicalise, so the rendered mountPath is always the string the
		// caller supplied.
		add("workingDir %q is not a clean absolute path; remove any \"..\", \".\", repeated \"/\", or trailing \"/\" segments (it resolves to %q, and mount paths are compared literally)", s.WorkingDir, path.Clean(s.WorkingDir))
	default:
		// The working directory becomes a mountPath for an EMPTY volume, so
		// pointing it at a system directory silently replaces the image's
		// contents there — /etc takes the CA trust store and /etc/passwd with
		// it, /usr and /bin take the binaries.
		if dir, reserved := reservedMountPath(s.WorkingDir); reserved {
			add("workingDir %q is inside %q, which the pod needs; mounting an empty volume there would hide the image's contents (CA certificates, /etc/passwd, binaries). Choose a dedicated path such as %q", s.WorkingDir, dir, DefaultWorkingDir)
		}
	}

	problems = append(problems, s.checkWorkspaceTransport()...)

	// Identity of the process. This is the one numeric field that can undo the
	// non-root guarantee, so it is checked explicitly rather than defaulted.
	if err := checkUID(s.RunAsUser); err != nil {
		add("runAsUser is invalid: %v", err)
	}

	// Network. Unsupported modes fail closed instead of silently downgrading.
	switch s.NetworkMode {
	case NetworkDeny:
	case NetworkAllowlist, NetworkOpen:
		add("network mode %q is not implemented for Kubernetes and will not be silently downgraded; NetworkPolicy selects by IP, namespace, and pod, not by domain — use the container runtime for allowlisted egress, or keep %q here", s.NetworkMode, NetworkDeny)
	case "":
		add("network mode is not set; set it explicitly to %q (start from DefaultJobSpec)", NetworkDeny)
	default:
		add("network mode %q is invalid (expected: %q)", s.NetworkMode, NetworkDeny)
	}

	// Bounds.
	if s.ActiveDeadlineSeconds <= 0 || s.ActiveDeadlineSeconds > MaxActiveDeadlineSeconds {
		add("activeDeadlineSeconds is %d; it must be within 1..%d so a run cannot occupy the cluster indefinitely", s.ActiveDeadlineSeconds, MaxActiveDeadlineSeconds)
	}
	if s.TTLSecondsAfterFinished <= 0 || s.TTLSecondsAfterFinished > MaxTTLSecondsAfterFinished {
		add("ttlSecondsAfterFinished is %d; it must be within 1..%d so finished Jobs and their logs are cleaned up", s.TTLSecondsAfterFinished, MaxTTLSecondsAfterFinished)
	}

	problems = append(problems, checkResourcePair("cpu", s.CPURequest, s.CPULimit)...)
	problems = append(problems, checkResourcePair("memory", s.MemoryRequest, s.MemoryLimit)...)

	if _, err := parseQuantity(s.WorkspaceSizeLimit); err != nil {
		add("workspaceSizeLimit is invalid: %v; the workspace emptyDir must be bounded or it can fill the node disk", err)
	}
	if _, err := parseQuantity(s.TmpSizeLimit); err != nil {
		add("tmpSizeLimit is invalid: %v; the %s emptyDir must be bounded or it can fill the node disk", err, tmpDir)
	}

	// Optional hardening fields: validated only when explicitly requested.
	if s.ServiceAccountName != "" {
		if !dns1123Subdomain.MatchString(s.ServiceAccountName) || len(s.ServiceAccountName) > 253 {
			add("serviceAccountName %q is not a DNS-1123 subdomain", s.ServiceAccountName)
		}
	}
	if s.RuntimeClassName != "" {
		if !dns1123Subdomain.MatchString(s.RuntimeClassName) || len(s.RuntimeClassName) > 253 {
			add("runtimeClassName %q is not a DNS-1123 subdomain", s.RuntimeClassName)
		}
	}

	if len(problems) == 0 {
		return nil
	}
	// The trailing line names no Go symbol on purpose: this error reaches CLI
	// users through `andbo k8s render`, and telling them to call DefaultJobSpec()
	// is not something they can act on from a terminal.
	return fmt.Errorf("kubernetes job spec is invalid:\n  - %s\n\nFix the fields above, then render again. Every field not named above already holds this renderer's secure default.",
		strings.Join(problems, "\n  - "))
}

// checkWorkspaceTransport validates the declared workspace transport.
//
// The transport is required because the workspace volume is an emptyDir: with
// nothing declared, a spec that lost its workspace and a spec that never had
// one render identically. For the image transport the real hazard is masking —
// the writable emptyDirs are mounted over WorkingDir and tmpDir, so a source
// path that overlaps either is invisible to the init container. The copy then
// succeeds, delivers nothing, and the agent works on an empty tree.
func (s JobSpec) checkWorkspaceTransport() []string {
	var problems []string
	add := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	switch s.WorkspaceTransport {
	case WorkspaceEmpty:
		if s.ImageWorkspacePath != "" {
			add("imageWorkspacePath %q is set but workspaceTransport is %q, so nothing would copy it; set workspaceTransport to %q or clear the path", s.ImageWorkspacePath, WorkspaceEmpty, WorkspaceFromImage)
		}
		return problems
	case WorkspaceFromImage:
	case "":
		add("workspaceTransport is not set; declare it explicitly as %q (the agent starts on an empty volume) or %q (the workspace is baked into the image and copied in) — a Job cannot reach the host, so there is no safe default", WorkspaceEmpty, WorkspaceFromImage)
		return problems
	default:
		add("workspaceTransport %q is invalid (expected %q or %q)", s.WorkspaceTransport, WorkspaceEmpty, WorkspaceFromImage)
		return problems
	}

	switch {
	case s.ImageWorkspacePath == "":
		add("imageWorkspacePath must be set for workspaceTransport %q; name the directory inside the image that holds the workspace (e.g. \"/andbo/workspace\")", WorkspaceFromImage)
	case !strings.HasPrefix(s.ImageWorkspacePath, "/"):
		add("imageWorkspacePath %q must be an absolute path inside the image", s.ImageWorkspacePath)
	case s.ImageWorkspacePath == "/":
		add("imageWorkspacePath must not be \"/\"; copying the whole image root into the workspace volume is never what is meant")
	case hasUnsafeRunes(s.ImageWorkspacePath, false):
		add("imageWorkspacePath contains control, bidirectional, or zero-width characters")
	case s.ImageWorkspacePath != path.Clean(s.ImageWorkspacePath):
		// Same reason WorkingDir must be canonical: the overlap checks below
		// compare strings while the kernel resolves them.
		add("imageWorkspacePath %q is not a clean absolute path; remove any \"..\", \".\", repeated \"/\", or trailing \"/\" segments (it resolves to %q, and the mount overlap checks compare paths literally)", s.ImageWorkspacePath, path.Clean(s.ImageWorkspacePath))
	default:
		// tmpDir is part of reservedMountPaths, so this also catches a source
		// hidden behind the scratch volume.
		if dir, reserved := reservedMountPath(s.ImageWorkspacePath); reserved {
			add("imageWorkspacePath %q is inside %q, which the image or the kernel owns; a workspace source must be a dedicated directory (e.g. \"/andbo/workspace\"), not a system path", s.ImageWorkspacePath, dir)
		} else if s.WorkingDir != "" && pathsOverlap(s.ImageWorkspacePath, s.WorkingDir) {
			add("imageWorkspacePath %q overlaps workingDir %q, which is mounted as an EMPTY volume: the source would be hidden and the copy would deliver nothing. Keep the workspace source outside the working directory", s.ImageWorkspacePath, s.WorkingDir)
		}
	}

	return problems
}

// checkResourcePair validates that a request/limit pair is present, parseable,
// and ordered. Both halves are required: a limit without a request schedules
// unpredictably, and a request without a limit is not a bound.
func checkResourcePair(kind, request, limit string) []string {
	var problems []string

	req, reqErr := parseQuantity(request)
	if reqErr != nil {
		problems = append(problems, fmt.Sprintf("%s request is invalid: %v", kind, reqErr))
	}
	lim, limErr := parseQuantity(limit)
	if limErr != nil {
		problems = append(problems, fmt.Sprintf("%s limit is invalid: %v; an unbounded container can starve every other workload on the node", kind, limErr))
	}
	if reqErr == nil && limErr == nil && req > lim {
		problems = append(problems, fmt.Sprintf("%s request %q exceeds its limit %q; the pod would never be schedulable", kind, request, limit))
	}
	return problems
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
