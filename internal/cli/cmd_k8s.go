package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/qosikz/andbo/internal/adapters"
	"github.com/qosikz/andbo/internal/config"
	"github.com/qosikz/andbo/internal/policy"
	"github.com/qosikz/andbo/internal/runtime"
	"github.com/qosikz/andbo/internal/runtime/k8s"
)

// cmdK8s is the CLI surface for the Kubernetes renderer.
//
// It RENDERS ONLY. There is no apply, no kubeconfig, no cluster client, and no
// network call anywhere on this path: the output is a YAML stream the operator
// inspects and applies themselves. That is why the manifest goes to stdout on
// its own — `andbo k8s render ... | kubectl apply -f -` is the operator's
// decision to make, not Andbo's — while every diagnostic and every enforcement
// caveat goes to stderr.
func (r *Root) cmdK8s(args []string) error {
	return r.k8s(args, os.Stdout, os.Stderr)
}

// k8s is cmdK8s with injectable streams so tests can assert exactly what lands
// on stdout (the manifest, and nothing else).
func (r *Root) k8s(args []string, out, errOut io.Writer) error {
	if len(args) == 0 {
		return codedf(ExitGeneral, "usage: andbo k8s render \"<task>\" --name <job> --namespace <ns> --workspace <empty|image:PATH>")
	}
	switch args[0] {
	case "render":
		return k8sRender(args[1:], out, errOut)
	default:
		return codedf(ExitGeneral, "unknown k8s command: %s\n\nOnly 'render' exists: Andbo renders Kubernetes manifests and never applies them.", args[0])
	}
}

// k8sRenderOptions holds parsed flags for `andbo k8s render`.
type k8sRenderOptions struct {
	task           string
	name           string
	namespace      string
	workspace      string
	policy         string
	agent          string
	runtimeClass   string
	serviceAccount string
	json           bool
}

var k8sRenderValueFlags = map[string]bool{
	"name": true, "namespace": true, "workspace": true, "policy": true,
	"agent": true, "runtime-class": true, "service-account": true,
}

// parseK8sRenderFlags parses `k8s render` arguments. policy is left EMPTY when
// the flag is absent, so the caller can tell "use the implicit andbo.yaml, and
// fall back to built-in defaults if it is missing" apart from "the operator
// named a file", where a missing file has to be an error rather than a silent
// substitution.
func parseK8sRenderFlags(args []string) (k8sRenderOptions, error) {
	var o k8sRenderOptions

	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "--") {
			if o.task != "" {
				return o, fmt.Errorf("unexpected argument: %q (the task is a single quoted string)", a)
			}
			o.task = a
			continue
		}
		name := strings.TrimPrefix(a, "--")
		val, inline := "", false
		if eq := strings.IndexByte(name, '='); eq >= 0 {
			val, name, inline = name[eq+1:], name[:eq], true
		}
		// Reject the unknown flag first, so a typo is never reported as a
		// value/switch mismatch.
		if !k8sRenderValueFlags[name] && name != "json" {
			return o, fmt.Errorf("unknown flag: --%s", name)
		}
		if k8sRenderValueFlags[name] {
			if !inline {
				if i+1 >= len(args) {
					return o, fmt.Errorf("flag --%s requires a value", name)
				}
				i++
				val = args[i]
			}
			if val == "" {
				return o, fmt.Errorf("flag --%s requires a value", name)
			}
		} else if inline {
			// Boolean flags take no value. Accepting one silently would make
			// --json=false turn JSON ON, which is the opposite of what it reads.
			return o, fmt.Errorf("flag --%s takes no value (it is a switch; write --%s or leave it out)", name, name)
		}
		switch name {
		case "name":
			o.name = val
		case "namespace":
			o.namespace = val
		case "workspace":
			o.workspace = val
		case "policy":
			o.policy = val
		case "agent":
			o.agent = val
		case "runtime-class":
			o.runtimeClass = val
		case "service-account":
			o.serviceAccount = val
		case "json":
			o.json = true
		default:
			return o, fmt.Errorf("unknown flag: --%s", name)
		}
	}
	return o, nil
}

func k8sRender(args []string, out, errOut io.Writer) error {
	o, err := parseK8sRenderFlags(args)
	if err != nil {
		return coded(ExitGeneral, err)
	}
	switch {
	case o.task == "":
		return codedf(ExitGeneral, "missing task.\nExample: andbo k8s render \"fix failing tests\" --name fix-tests --namespace andbo-runs --workspace empty")
	case o.name == "":
		return codedf(ExitGeneral, "missing --name.\nIt becomes the Job name and the label the NetworkPolicy selects on, so it must be a DNS-1123 label (e.g. --name fix-tests).")
	case o.namespace == "":
		return codedf(ExitGeneral, "missing --namespace.\nName an EXISTING namespace dedicated to agent runs; this command does not create one, and a shared namespace can carry other NetworkPolicies that grant egress this one cannot remove.")
	}

	transport, imagePath, err := parseWorkspaceTransport(o.workspace)
	if err != nil {
		return coded(ExitGeneral, err)
	}

	// 1. Load and validate the policy exactly as `andbo run` does. The
	//    Kubernetes path gets no relaxed policy handling of its own.
	//
	// LoadPolicy falls back to built-in defaults when the file is missing. That
	// is right for the implicit andbo.yaml and wrong for a path the operator
	// typed: a mistyped --policy would otherwise render a DIFFERENT manifest —
	// the floating-tag default image in place of whatever digest was pinned —
	// under a summary asserting their policy had been applied.
	policyPath, usingDefaults := o.policy, false
	if policyPath == "" {
		policyPath = "andbo.yaml"
		if _, statErr := os.Stat(policyPath); statErr != nil {
			usingDefaults = true
		}
	} else if _, statErr := os.Stat(policyPath); statErr != nil {
		return codedf(ExitInvalidConfig,
			"policy file %s could not be read: %v.\nCheck the path, or drop --policy to use %s (and the built-in secure defaults if that is absent too).",
			policyPath, statErr, "andbo.yaml")
	}

	cfg, err := config.LoadPolicy(policyPath)
	if err != nil {
		return coded(ExitInvalidConfig, err)
	}
	if chk := cfg.Check(); !chk.OK() {
		for _, e := range chk.Errors {
			warnTo(errOut, e)
		}
		return codedf(ExitInvalidConfig, "policy %s is invalid; run 'andbo policy check'", policyPath)
	}
	ep := policy.BuildEffectivePolicy(cfg, policyPath, policy.Overrides{Agent: o.agent})

	// Nothing runs locally, so there is nothing to gate — but an unsafe option
	// must never pass unmentioned, or a future addition to the unsafe set goes
	// unreported on this path alone.
	if ep.RequiresUnsafeConfirmation() {
		warnTo(errOut, "policy sets unsafe options; nothing is executed here, but they shape the manifest:")
		for _, reason := range ep.UnsafeReasons() {
			warnTo(errOut, "  - "+reason)
		}
	}
	if o.namespace == "default" {
		warnTo(errOut, "rendering into the \"default\" namespace: it is the most likely place for a namespace-wide allow-dns-egress policy, which is additive and would hand the agent DNS despite this default-deny. Use a dedicated namespace for agent runs.")
	}

	// Fail safe: only "container" is a shape this renderer can express. "local"
	// means run on the host, which a Job cannot do, and an unknown value must
	// not be guessed at.
	//
	// This is a policy violation, not a config typo: it is the same category as
	// an unenforceable network mode — the policy asks for a mode this renderer
	// will not emit — so it must carry the same exit code, or a CI gate that
	// watches for "policy blocked" misses it.
	if ep.Runtime.Isolation != "container" {
		return codedf(ExitPolicyViolation,
			"policy runtime.isolation is %q, which has no Kubernetes equivalent: a Job always runs in a container on a cluster node, never on this host.\nSet runtime.isolation: container to render manifests, or run this workload on the container runtime with 'andbo run'.",
			ep.Runtime.Isolation)
	}

	// activeDeadlineSeconds is checked here rather than at the bridge so the
	// error speaks the user's vocabulary. The bridge sees a "command timeout"
	// the user never wrote, and names neither the field nor the file.
	//
	// The bound is compared in MINUTES, before any conversion to a Duration, so
	// it never depends on how that conversion behaves at the extremes. It once
	// had to: budgetWindow wrapped, 153722867281 minutes became 5.224192s,
	// passed a cap checked on the duration, and rendered a clean manifest whose
	// Job Kubernetes kills six seconds in (deadlineSeconds rounds up) — a silent
	// downgrade of the exact bound this check exists to enforce. The conversion
	// is total now (see maxBudgetMinutes); the comparison stays in minutes
	// because the error has to speak in the units the operator wrote.
	if mins, cap := ep.Budget.MaxRuntimeMinutes, k8s.MaxActiveDeadlineSeconds/60; mins > cap {
		return codedf(ExitPolicyViolation,
			"budget.max_runtime_minutes is %d, which exceeds the %d-minute cap on the Job's activeDeadlineSeconds.\nLower it in %s to at most %d; nothing local supervises a pod, so a run this renderer cannot bound would occupy the cluster until someone notices.",
			mins, cap, policyPath, cap)
	}

	// 2. Build the agent command. The workspace path handed to the adapter is
	//    the host directory whose contents the operator must bake into the
	//    image; the bridge remaps it to the pod path and refuses to let it
	//    survive anywhere a cluster would see it.
	hostWorkspace := ""
	if transport == k8s.WorkspaceFromImage {
		hostWorkspace, err = os.Getwd()
		if err != nil {
			return coded(ExitGeneral, err)
		}
	}

	adapter, err := adapters.Get(ep.Agent.Default, cfg.Agent.Custom)
	if err != nil {
		return coded(ExitAgentFailed, err)
	}
	// buildAgentEnv resolves policy secrets.allow out of the host environment.
	// That is deliberate: the bridge refuses to inline them, so an allowlisted
	// secret that is actually set stops the render instead of producing a
	// manifest that silently lost the agent's credentials.
	agentEnv := buildAgentEnv(ep)
	cmd, err := adapter.BuildCommand(context.Background(), adapters.Input{
		Task: o.task, WorkspacePath: hostWorkspace, Env: agentEnv,
	})
	if err != nil {
		return coded(ExitAgentFailed, err)
	}

	// An adapter may add environment of its own (goose sets GOOSE_MODE). No
	// variable except HOME crosses into a Job, so those agents cannot run here.
	// Caught before the bridge because the bridge's message blames host secrets,
	// which an adapter-supplied literal is not — and it names no variable, so
	// the user cannot tell which one to remove. Names only, never values.
	if extra := adapterAddedEnv(agentEnv, cmd.Env); len(extra) > 0 {
		return codedf(ExitPolicyViolation,
			"agent %q needs environment variable(s) %s that this renderer does not deliver: a manifest is plain text in etcd, this slice has no Kubernetes Secret transport, and nothing but HOME crosses into a Job.\nSet them with ENV in the Dockerfile for runtime.image, choose an agent that needs none, or run this workload on the container runtime with 'andbo run'.",
			ep.Agent.Default, strings.Join(extra, ", "))
	}

	// 3. Cross the security boundary. FromRuntimeSpec is where anything the
	//    renderer cannot enforce becomes an error rather than a downgrade, so
	//    its failures are policy violations, not configuration typos.
	base := k8s.DefaultJobSpec()
	base.Name = o.name
	base.Namespace = o.namespace
	base.WorkspaceTransport = transport
	base.ImageWorkspacePath = imagePath
	base.RuntimeClassName = o.runtimeClass
	base.ServiceAccountName = o.serviceAccount
	// The pod's root filesystem is read-only, so HOME has to point at the
	// writable volume or git and every package manager fails. Set here rather
	// than bridged from the runtime spec, so BOTH transports get it: with
	// workspaceTransport=empty there is no host workspace for the bridge to
	// rewrite, and the agent would otherwise get no HOME at all.
	base.Env = map[string]string{"HOME": base.WorkingDir}

	cs := toCommandSpec(cmd)
	// The policy's wall-clock budget becomes the Job's: nothing local supervises
	// a pod, so an unbounded run would occupy the cluster until someone notices.
	if ep.Budget.MaxRuntimeMinutes > 0 {
		cs.Timeout = budgetWindow(ep.Budget.MaxRuntimeMinutes)
	}

	spec, err := k8s.FromRuntimeSpec(base, k8sRuntimeSpec(ep, hostWorkspace, agentEnv), cs)
	if err != nil {
		return coded(ExitPolicyViolation, err)
	}

	// Validate explicitly, ahead of Render, so only VALIDATION failures get the
	// field-to-flag mapping appended. Render's other failure — the internal
	// guard that the NetworkPolicy still binds to the pod — is a bug in Andbo,
	// not something the operator can fix by changing a flag, and must not be
	// dressed up as one.
	if err := spec.Validate(); err != nil {
		// Validate reports MANIFEST field names, which a CLI user never typed.
		// Map them back to the inputs that produced them, or the error is not
		// actionable from a terminal.
		return codedf(ExitInvalidConfig, "%v\n\nThose are manifest fields. From this command they come from:\n%s",
			err, k8sFieldOrigins(policyPath))
	}
	manifest, err := spec.Render()
	if err != nil {
		return coded(ExitGeneral, err)
	}

	cliNotes, manifestNotes := k8sRenderNotes(), spec.EnforcementNotes()
	if o.json {
		return printJSON(out, map[string]any{
			"manifest":            manifest,
			"notes":               append(append([]string{}, cliNotes...), manifestNotes...),
			"name":                spec.Name,
			"namespace":           spec.Namespace,
			"image":               spec.Image,
			"network":             string(spec.NetworkMode),
			"workspace_transport": string(spec.WorkspaceTransport),
			"policy":              policyPath,
		})
	}

	// stdout carries the manifest and nothing else, so the command composes.
	if _, err := io.WriteString(out, manifest); err != nil {
		return coded(ExitGeneral, err)
	}
	printK8sSummary(errOut, policyPath, usingDefaults, spec, cliNotes, manifestNotes)
	return nil
}

// parseWorkspaceTransport decodes --workspace. There is no default on purpose:
// the workspace volume is an emptyDir, so a spec that lost its workspace and a
// spec that never had one render identically. Making the operator say which
// one they mean is the only way the difference stays visible.
func parseWorkspaceTransport(v string) (k8s.WorkspaceTransport, string, error) {
	const usage = "--workspace must be \"empty\" (the agent starts on an empty volume, deliberately) or \"image:/path\" (the workspace is baked into the agent image at /path and copied in by an init container).\nA Kubernetes Job cannot reach this host, so there is no safe default to guess"
	switch {
	case v == "":
		return "", "", fmt.Errorf("missing --workspace.\n%s.", usage)
	case v == "empty":
		return k8s.WorkspaceEmpty, "", nil
	case strings.HasPrefix(v, "image:"):
		p := strings.TrimPrefix(v, "image:")
		if p == "" {
			return "", "", fmt.Errorf("--workspace image: has no path.\nName the directory INSIDE the image that holds the workspace, e.g. --workspace image:/andbo/workspace.")
		}
		return k8s.WorkspaceFromImage, p, nil
	default:
		return "", "", fmt.Errorf("--workspace %q is not a transport.\n%s.", v, usage)
	}
}

// k8sFieldOrigins maps each manifest field a validation error can name back to
// the input that produced it, one pair per line. One line per field matters:
// a user reading "imageWorkspacePath is not absolute" needs the flag on the
// same line, not somewhere in a block that mentions every flag.
func k8sFieldOrigins(policyPath string) string {
	pairs := [][2]string{
		{"name", "--name"},
		{"namespace", "--namespace"},
		{"imageWorkspacePath", "--workspace image:PATH"},
		{"runtimeClassName", "--runtime-class"},
		{"serviceAccountName", "--service-account"},
		{"command", "the agent in " + policyPath},
		{"args", "the task text you typed"},
		{"image", "runtime.image in " + policyPath},
		{"network", "network.mode in " + policyPath},
		{"activeDeadlineSeconds", "budget.max_runtime_minutes in " + policyPath},
	}
	var b strings.Builder
	for _, p := range pairs {
		fmt.Fprintf(&b, "  %-21s <- %s\n", p[0], p[1])
	}
	b.WriteString("Anything not listed is a secure default this renderer sets itself.")
	return b.String()
}

// adapterAddedEnv returns, sorted, the environment names the adapter introduced
// on its own — present in the command it built but not in the environment Andbo
// handed it. A name the caller supplied is deliberately NOT reported here: that
// one is host-derived and belongs to the bridge's secret refusal.
func adapterAddedEnv(given, built map[string]string) []string {
	var extra []string
	for name := range built {
		if _, fromCaller := given[name]; !fromCaller {
			extra = append(extra, name)
		}
	}
	sort.Strings(extra)
	return extra
}

// k8sRuntimeSpec builds the RuntimeSpec the bridge validates.
//
// It is deliberately NOT buildRuntimeSpec: that one describes a container run
// on this host (docker socket flag, dry-run mount list, a sanitized workspace
// copy Andbo materializes). None of those exist here. What is shared is the
// shape the bridge expects — the workspace as the single write path, HOME
// pointed at it — so the same refusals apply.
func k8sRuntimeSpec(ep policy.EffectivePolicy, hostWorkspace string, env map[string]string) runtime.RuntimeSpec {
	var writePaths []string
	if hostWorkspace != "" {
		writePaths = []string{hostWorkspace}
	}

	// network.allow is only an allowlist when the mode selects it; carrying it
	// under mode=deny would report a contradiction the policy does not contain
	// (the default policy ships allow entries alongside mode: deny).
	var allowedDomains []string
	var allowedPorts []int
	if ep.EnforcedNetwork() == "allowlist" {
		allowedDomains = ep.Network.Allow
		allowedPorts = ep.Network.Ports
	}

	return runtime.RuntimeSpec{
		Engine: "kubernetes",
		Image:  ep.Runtime.Image,
		// The policy's own network mode crosses unmapped so the bridge's error
		// names what the operator actually wrote.
		NetworkMode:       ep.EnforcedNetwork(),
		AllowedDomains:    allowedDomains,
		AllowedPorts:      allowedPorts,
		Workdir:           hostWorkspace,
		Env:               env,
		User:              "10001:10001", // non-root
		Privileged:        false,         // never
		MountDockerSocket: false,         // never
		ReadOnlyPaths:     nil,
		WritePaths:        writePaths,
	}
}

// k8sRenderNotes are the caveats this CLI layer adds on top of the renderer's
// own EnforcementNotes. They exist because rendering a manifest is not running
// an agent: several controls `andbo run` applies have no counterpart here, and
// staying silent about that would be the overclaim.
func k8sRenderNotes() []string {
	return []string{
		"this command renders and does not run: no agent is executed, no workspace is copied, no tests run, no diff or PR is produced, no session is recorded, and no cluster is contacted. 'andbo session list' will not show this run",
		"policy secrets.allow names are never delivered to a Job: a manifest is plain text in etcd, readable by anyone who can get the Job, and this slice has no Kubernetes Secret transport. An allowlisted name that is set on this host stops the render rather than being silently dropped — EXCEPT PATH, LANG, LC_ALL, and TERM, which are always dropped without comment because the image supplies them, even when the policy allowlists one of those names",
		"policy filesystem.deny (.env, ~/.ssh, and the rest) is enforced when Andbo copies a workspace, and this command copies none: with --workspace image:PATH, whatever your image build put at PATH reaches the pod verbatim. Exclude denied paths in the image build yourself",
		"policy filesystem.read_only and filesystem.write do not shape the pod: the working directory is a single writable emptyDir, and nothing else is mounted",
		"policy commands.allow/deny, mcp.*, budget.max_usd, and budget.max_tokens have no counterpart in a manifest and are dropped: nothing inside the pod reads them, and this command is not present at runtime to enforce them. Only budget.max_runtime_minutes crosses, as the Job's activeDeadlineSeconds",
	}
}

// printK8sSummary writes the human-facing report to stderr. The two note groups
// stay separate because they answer different questions: what this COMMAND does
// not do, and what the applied MANIFESTS do not guarantee. Neither is elided —
// silence about an unimplemented control is the overclaim Andbo must not make.
func printK8sSummary(w io.Writer, policyPath string, usingDefaults bool, spec k8s.JobSpec, cliNotes, manifestNotes []string) {
	fmt.Fprintln(w)
	// Never claim a file was applied when none was read: this summary sits next
	// to a manifest the operator is about to review and apply.
	if usingDefaults {
		ok(w, "Policy: built-in defaults (no "+policyPath+" found)")
	} else {
		ok(w, "Policy applied ("+policyPath+")")
	}
	ok(w, fmt.Sprintf("Manifests rendered (NetworkPolicy + Job: %s/%s)", spec.Namespace, spec.Name))
	ok(w, "Network: "+string(spec.NetworkMode)+" (default-deny NetworkPolicy, no egress rules)")
	ok(w, "Workspace: "+k8sWorkspaceLabel(spec))
	ok(w, "No cluster contacted (rendered locally; nothing was applied)")
	fmt.Fprintf(w, "\nApply it yourself after review:\n  andbo k8s render ... | kubectl apply -f -\n")

	fmt.Fprintln(w, "\nNot enforced — what this command does not do:")
	for _, n := range cliNotes {
		fmt.Fprintf(w, "  - %s\n", n)
	}
	fmt.Fprintln(w, "\nNot enforced — what these manifests do not guarantee:")
	for _, n := range manifestNotes {
		fmt.Fprintf(w, "  - %s\n", n)
	}
}

func k8sWorkspaceLabel(spec k8s.JobSpec) string {
	if spec.WorkspaceTransport == k8s.WorkspaceFromImage {
		return "from image at " + spec.ImageWorkspacePath + " (copied into the writable volume before the agent starts)"
	}
	return "empty (the agent starts on an empty volume)"
}
