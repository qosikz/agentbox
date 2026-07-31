package k8s

import (
	"bytes"
	"fmt"

	"gopkg.in/yaml.v3"
)

// The manifest types below are a deliberately minimal subset of the Kubernetes
// API. Fields that would weaken isolation — hostPath and every other volume
// source, hostNetwork/hostPID/hostIPC as anything but false, privileged,
// capabilities.add, valueFrom/envFrom, nodeName, hostAliases — are simply not
// declared, so no caller input can produce them. Values that must be constant
// are set in render, not taken from JobSpec.

type objectMeta struct {
	Name      string            `yaml:"name"`
	Namespace string            `yaml:"namespace"`
	Labels    map[string]string `yaml:"labels"`
}

type templateMeta struct {
	Labels map[string]string `yaml:"labels"`
}

type labelSelector struct {
	MatchLabels map[string]string `yaml:"matchLabels"`
}

// networkPolicy carries no ingress or egress field: a NetworkPolicy that
// selects a pod and declares both policy types without rules denies everything
// in both directions. Adding rule fields here is the only way to punch a hole,
// so they are absent by construction.
type networkPolicy struct {
	APIVersion string            `yaml:"apiVersion"`
	Kind       string            `yaml:"kind"`
	Metadata   objectMeta        `yaml:"metadata"`
	Spec       networkPolicySpec `yaml:"spec"`
}

type networkPolicySpec struct {
	PodSelector labelSelector `yaml:"podSelector"`
	PolicyTypes []string      `yaml:"policyTypes"`
}

type job struct {
	APIVersion string     `yaml:"apiVersion"`
	Kind       string     `yaml:"kind"`
	Metadata   objectMeta `yaml:"metadata"`
	Spec       jobSpec    `yaml:"spec"`
}

type jobSpec struct {
	BackoffLimit            int64       `yaml:"backoffLimit"`
	Completions             int64       `yaml:"completions"`
	Parallelism             int64       `yaml:"parallelism"`
	ActiveDeadlineSeconds   int64       `yaml:"activeDeadlineSeconds"`
	TTLSecondsAfterFinished int64       `yaml:"ttlSecondsAfterFinished"`
	Template                podTemplate `yaml:"template"`
}

type podTemplate struct {
	Metadata templateMeta `yaml:"metadata"`
	Spec     podSpec      `yaml:"spec"`
}

type podSpec struct {
	RestartPolicy                string             `yaml:"restartPolicy"`
	AutomountServiceAccountToken bool               `yaml:"automountServiceAccountToken"`
	EnableServiceLinks           bool               `yaml:"enableServiceLinks"`
	DNSPolicy                    string             `yaml:"dnsPolicy"`
	DNSConfig                    podDNSConfig       `yaml:"dnsConfig"`
	HostNetwork                  bool               `yaml:"hostNetwork"`
	HostPID                      bool               `yaml:"hostPID"`
	HostIPC                      bool               `yaml:"hostIPC"`
	ServiceAccountName           string             `yaml:"serviceAccountName,omitempty"`
	RuntimeClassName             string             `yaml:"runtimeClassName,omitempty"`
	SecurityContext              podSecurityContext `yaml:"securityContext"`
	InitContainers               []container        `yaml:"initContainers,omitempty"`
	Containers                   []container        `yaml:"containers"`
	Volumes                      []volume           `yaml:"volumes"`
}

// podDNSConfig declares nameservers and nothing else. Searches and options are
// absent by construction: a search list is how svc.cluster.local would come
// back into a pod whose whole point is that it resolves nothing the cluster
// runs, and neither field has a rendered form to set.
type podDNSConfig struct {
	Nameservers []string `yaml:"nameservers"`
}

type podSecurityContext struct {
	RunAsNonRoot   bool           `yaml:"runAsNonRoot"`
	RunAsUser      int64          `yaml:"runAsUser"`
	RunAsGroup     int64          `yaml:"runAsGroup"`
	FSGroup        int64          `yaml:"fsGroup"`
	SeccompProfile seccompProfile `yaml:"seccompProfile"`
}

type seccompProfile struct {
	Type string `yaml:"type"`
}

type container struct {
	Name            string                   `yaml:"name"`
	Image           string                   `yaml:"image"`
	ImagePullPolicy string                   `yaml:"imagePullPolicy"`
	Command         []string                 `yaml:"command"`
	Args            []string                 `yaml:"args,omitempty"`
	WorkingDir      string                   `yaml:"workingDir"`
	Env             []envVar                 `yaml:"env,omitempty"`
	Resources       resourceRequirements     `yaml:"resources"`
	SecurityContext containerSecurityContext `yaml:"securityContext"`
	VolumeMounts    []volumeMount            `yaml:"volumeMounts"`
}

// envVar has no ValueFrom: environment values are literals only, so a manifest
// cannot pull a Secret, ConfigMap, or downward-API value into the agent.
type envVar struct {
	Name  string `yaml:"name"`
	Value string `yaml:"value"`
}

type resourceRequirements struct {
	Requests resourceList `yaml:"requests"`
	Limits   resourceList `yaml:"limits"`
}

type resourceList struct {
	CPU    string `yaml:"cpu"`
	Memory string `yaml:"memory"`
}

// containerSecurityContext has no Privileged-true path and no Capabilities.Add:
// the drop list is the only capability field that exists.
type containerSecurityContext struct {
	AllowPrivilegeEscalation bool           `yaml:"allowPrivilegeEscalation"`
	Privileged               bool           `yaml:"privileged"`
	ReadOnlyRootFilesystem   bool           `yaml:"readOnlyRootFilesystem"`
	RunAsNonRoot             bool           `yaml:"runAsNonRoot"`
	RunAsUser                int64          `yaml:"runAsUser"`
	RunAsGroup               int64          `yaml:"runAsGroup"`
	Capabilities             capabilities   `yaml:"capabilities"`
	SeccompProfile           seccompProfile `yaml:"seccompProfile"`
}

type capabilities struct {
	Drop []string `yaml:"drop"`
}

type volumeMount struct {
	Name      string `yaml:"name"`
	MountPath string `yaml:"mountPath"`
}

// volume declares emptyDir as its only source. hostPath, secret, configMap,
// projected, csi, and persistentVolumeClaim are absent by construction.
type volume struct {
	Name     string   `yaml:"name"`
	EmptyDir emptyDir `yaml:"emptyDir"`
}

type emptyDir struct {
	SizeLimit string `yaml:"sizeLimit"`
}

// Render validates the spec and returns the manifest as a two-document YAML
// string: the default-deny NetworkPolicy first, then the Job.
//
// The order is load-bearing. `kubectl apply -f` processes documents in order,
// so the policy exists before the pod it isolates. Validation runs first and
// unconditionally: there is no code path that renders an unvalidated spec.
func (s JobSpec) Render() (string, error) {
	if err := s.Validate(); err != nil {
		return "", err
	}

	podLabels := s.jobLabels()
	selector := s.selectorLabels()

	np := networkPolicy{
		APIVersion: "networking.k8s.io/v1",
		Kind:       "NetworkPolicy",
		Metadata: objectMeta{
			Name:      s.Name + "-deny-all",
			Namespace: s.Namespace,
			Labels:    podLabels,
		},
		Spec: networkPolicySpec{
			PodSelector: labelSelector{MatchLabels: selector},
			PolicyTypes: []string{"Ingress", "Egress"},
		},
	}

	j := job{
		APIVersion: "batch/v1",
		Kind:       "Job",
		Metadata: objectMeta{
			Name:      s.Name,
			Namespace: s.Namespace,
			Labels:    podLabels,
		},
		Spec: jobSpec{
			// No retries: an agent run has side effects (commits, PRs, tool
			// calls), so a retry would repeat them. This bounds the JOB
			// controller, which is not the only thing that can start the agent
			// twice — see RestartPolicy below, which the kubelet acts on first.
			// Neither field gives at-most-once execution: node failure or pod
			// deletion can still start the run twice (see EnforcementNotes and
			// retriesNothingAfterFailure).
			BackoffLimit:            0,
			Completions:             1,
			Parallelism:             1,
			ActiveDeadlineSeconds:   s.ActiveDeadlineSeconds,
			TTLSecondsAfterFinished: s.TTLSecondsAfterFinished,
			Template: podTemplate{
				Metadata: templateMeta{Labels: podLabels},
				Spec: podSpec{
					// The kubelet's half of the no-retry contract: OnFailure
					// would restart the agent container in place, on the same
					// workspace, and the kubelet does that before the Job
					// controller can fail the Job on it. BackoffLimit above
					// counts that restart but cannot prevent it, so OnFailure
					// costs one extra start of the agent.
					RestartPolicy:                "Never",
					AutomountServiceAccountToken: false,
					// Drops the per-namespace Service variables. It does NOT
					// empty the agent's view of the cluster: the kubelet
					// injects KUBERNETES_SERVICE_HOST/_PORT for the
					// default-namespace kubernetes Service whatever this says
					// (see EnforcementNotes). Narrowing is still worth it.
					EnableServiceLinks: false,
					// The resolver is the other discovery route. The
					// ClusterFirst default hands the pod the kube-dns ClusterIP
					// as its nameserver and <ns>.svc.cluster.local,
					// svc.cluster.local, and cluster.local as its search list.
					// dnsPolicy None replaces both with what dnsConfig says,
					// and dnsConfig says one loopback address — the pod's OWN
					// loopback (it has its own netns), which reaches nothing.
					// That reasoning depends on HostNetwork staying false: with
					// host networking 127.0.0.1 is the NODE's loopback, where a
					// node-local DNS cache commonly listens.
					//
					// The kubelet writes this from the pod spec, so it does not
					// depend on the CNI. That is the whole of the claim, and it
					// stops ACCIDENTAL resolution only: it does nothing against
					// a process that picks its own resolver socket, which is
					// what the allow-DNS-egress baseline in EnforcementNotes is
					// about. The NetworkPolicy stays the enforcement.
					DNSPolicy:          "None",
					DNSConfig:          podDNSConfig{Nameservers: []string{"127.0.0.1"}},
					HostNetwork:        false,
					HostPID:            false,
					HostIPC:            false,
					ServiceAccountName: s.ServiceAccountName,
					RuntimeClassName:   s.RuntimeClassName,
					SecurityContext: podSecurityContext{
						RunAsNonRoot:   true,
						RunAsUser:      s.RunAsUser,
						RunAsGroup:     s.RunAsUser,
						FSGroup:        s.RunAsUser,
						SeccompProfile: seccompProfile{Type: "RuntimeDefault"},
					},
					InitContainers: s.initContainers(),
					Containers: []container{{
						Name:  containerName,
						Image: s.Image,
						// Always re-resolve the image so a poisoned or stale
						// node-local cache cannot silently supply a different one.
						ImagePullPolicy: "Always",
						Command:         s.Command,
						Args:            s.Args,
						WorkingDir:      s.WorkingDir,
						Env:             sortedEnv(s.Env),
						Resources:       s.resources(),
						SecurityContext: s.containerSecurityContext(),
						// The two writable mounts are what make
						// readOnlyRootFilesystem workable for a real agent.
						VolumeMounts: []volumeMount{
							{Name: workspaceVolume, MountPath: s.WorkingDir},
							{Name: tmpVolume, MountPath: tmpDir},
						},
					}},
					Volumes: []volume{
						{Name: workspaceVolume, EmptyDir: emptyDir{SizeLimit: s.WorkspaceSizeLimit}},
						{Name: tmpVolume, EmptyDir: emptyDir{SizeLimit: s.TmpSizeLimit}},
					},
				},
			},
		},
	}

	// Defence in depth, and the last things checked before encoding.
	if err := bindsPolicyToPod(np, j); err != nil {
		return "", err
	}
	if err := runsOnePodWithFreshImages(j); err != nil {
		return "", err
	}
	if err := retriesNothingAfterFailure(j); err != nil {
		return "", err
	}
	if err := boundsTheRunsWallClock(j); err != nil {
		return "", err
	}

	return encodeDocs(np, j)
}

// bindsPolicyToPod reports whether the rendered NetworkPolicy would actually
// restrict the rendered Job's pod.
//
// Kubernetes applies a NetworkPolicy to a pod on TWO conditions, and both fail
// silently:
//
//   - The policy is in the POD'S NAMESPACE. NetworkPolicy is a namespaced
//     object with no cross-namespace form — podSelector selects inside the
//     policy's own namespace only — so a policy that lands anywhere else
//     restricts nothing here. A missing namespace is the same failure by
//     another route: piped into `kubectl apply -f -`, an object without one
//     goes to whatever namespace the operator's context names.
//   - podSelector matches the POD TEMPLATE's labels, which are not the Job's
//     own metadata labels.
//
// Either way the manifest is valid, still reads as a default-deny, and applies
// without complaint — `kubectl apply -f -` with no -n takes each document's own
// namespace and never compares them — while the agent has unrestricted egress.
// That is the one outcome this package exists to prevent, so drift on either
// axis fails the render rather than emitting a policy that would not apply.
//
// It reads the CONSTRUCTED objects rather than the values that fed them, so an
// edit anywhere between building them and encoding them still has to leave the
// two bound. The bound on that: no test can prove this function was CALLED —
// deleting the call site changes no output, so nothing fails. What is testable
// is the property itself, which
// TestSecurity_NetworkPolicyLandsInThePodsNamespace pins on the rendered
// manifest, guard or no guard.
func bindsPolicyToPod(np networkPolicy, j job) error {
	switch {
	case np.Metadata.Namespace == "" && j.Metadata.Namespace == "":
		// Both would land in the operator's context namespace, so these two do
		// still bind and egress is still denied. The loss is different and must
		// be reported as itself: the manifest no longer says WHERE the run is
		// confined, so the namespace Validate ran its reserved-namespace checks
		// against is not the one this would be applied to, and `kubectl apply
		// -n` could put the pair beside the control-plane workloads those
		// checks exist to refuse.
		return fmt.Errorf("internal error: neither the NetworkPolicy %q nor its Job names a namespace, so the manifest does not state where the run is confined and would be applied to whichever namespace the operator's kubectl context or -n flag names — not the one this spec was validated against; refusing to render it", np.Metadata.Name)
	case np.Metadata.Namespace == "":
		return fmt.Errorf("internal error: NetworkPolicy %q names no namespace while its Job is in %q, so applying the manifest would place the policy wherever the operator's kubectl context points and the two would bind only by coincidence; refusing to render a policy that would leave the agent with unrestricted egress", np.Metadata.Name, j.Metadata.Namespace)
	case np.Metadata.Namespace != j.Metadata.Namespace:
		return fmt.Errorf("internal error: NetworkPolicy %q is in namespace %q but its Job is in %q, and a NetworkPolicy restricts only pods in its own namespace; refusing to render a policy that would leave the agent with unrestricted egress", np.Metadata.Name, np.Metadata.Namespace, j.Metadata.Namespace)
	}

	podLabels := j.Spec.Template.Metadata.Labels
	for k, v := range np.Spec.PodSelector.MatchLabels {
		if podLabels[k] != v {
			return fmt.Errorf("internal error: NetworkPolicy selector label %q=%q is not on the Job pod template; refusing to render a policy that would not apply", k, v)
		}
	}
	return nil
}

// resources and containerSecurityContext are shared by the agent container and
// the workspace init container. They are methods rather than duplicated
// literals so the init container's hardening cannot drift from the agent's —
// an init container runs in the same pod with the same volumes, so a weaker
// context there would be a weaker context for the whole run.
func (s JobSpec) resources() resourceRequirements {
	return resourceRequirements{
		Requests: resourceList{CPU: s.CPURequest, Memory: s.MemoryRequest},
		Limits:   resourceList{CPU: s.CPULimit, Memory: s.MemoryLimit},
	}
}

func (s JobSpec) containerSecurityContext() containerSecurityContext {
	return containerSecurityContext{
		AllowPrivilegeEscalation: false,
		Privileged:               false,
		ReadOnlyRootFilesystem:   true,
		RunAsNonRoot:             true,
		RunAsUser:                s.RunAsUser,
		RunAsGroup:               s.RunAsUser,
		Capabilities:             capabilities{Drop: []string{"ALL"}},
		SeccompProfile:           seccompProfile{Type: "RuntimeDefault"},
	}
}

// initContainers renders the workspace transport. For WorkspaceEmpty there is
// nothing to do and the field is omitted entirely.
//
// For WorkspaceFromImage a single init container copies the image-carried
// workspace into the writable emptyDir before the agent starts. Notes on the
// shape, all of them load-bearing:
//
//   - The command is EXEC FORM with no shell. A shell would turn both paths
//     into script text, so a directory name containing ';' or '$(...)' would
//     become a command. Validation already constrains the path, but not
//     running a shell is what makes that class of bug structurally impossible.
//   - "--" ends option parsing for the same reason: validation requires both
//     paths to be absolute, but the separator means a path can never be read as
//     a cp flag even if that check is ever loosened.
//   - It uses the SAME image as the agent, so a digest-pinned spec has exactly
//     one image to audit and the workspace cannot come from a second source.
//   - It carries no Env: the init container never needs one, and Env is where
//     this codebase's secrets would otherwise appear.
//   - It mounts only the workspace volume — it has no reason to touch the
//     agent's scratch space.
//   - "-R", NOT "-a", and no other preserve flag. kubelet creates the emptyDir
//     owned by root and fsGroup only changes its GROUP, so the volume root
//     keeps uid 0 no matter what runAsUser and fsGroup are set to. Any preserve
//     flag makes coreutils treat a failure to copy the DESTINATION directory's
//     metadata as fatal, and setting a directory's timestamps needs ownership
//     (utimensat), not write permission — so `cp -a` exits 1 on a real cluster
//     even though every file copied, and the failed init container takes the
//     whole Job down. `-R` still copies modes, dotfiles such as .git, and
//     symlinks as symlinks; it drops mtimes, which costs one slower `git
//     status` while git rebuilds its stat cache.
//   - The trailing "/." copies the CONTENTS of the source, including dotfiles,
//     into an existing destination directory.
func (s JobSpec) initContainers() []container {
	if s.WorkspaceTransport != WorkspaceFromImage {
		return nil
	}
	return []container{{
		Name:            initContainerName,
		Image:           s.Image,
		ImagePullPolicy: "Always",
		Command:         []string{"cp"},
		Args:            []string{"-R", "--", s.ImageWorkspacePath + "/.", s.WorkingDir},
		WorkingDir:      s.WorkingDir,
		Resources:       s.resources(),
		SecurityContext: s.containerSecurityContext(),
		VolumeMounts:    []volumeMount{{Name: workspaceVolume, MountPath: s.WorkingDir}},
	}}
}

// encodeDocs marshals documents into one YAML stream. Marshalling typed structs
// (rather than templating strings) is what makes manifest injection impossible:
// every caller-supplied string can only become a quoted scalar.
func encodeDocs(docs ...any) (string, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	for _, d := range docs {
		if err := enc.Encode(d); err != nil {
			return "", fmt.Errorf("rendering kubernetes manifest: %w", err)
		}
	}
	if err := enc.Close(); err != nil {
		return "", fmt.Errorf("rendering kubernetes manifest: %w", err)
	}
	return buf.String(), nil
}

// sortedEnv converts the env map to a stable, sorted list so the rendered
// manifest is byte-identical across runs (Go map iteration order is random).
func sortedEnv(env map[string]string) []envVar {
	if len(env) == 0 {
		return nil
	}
	out := make([]envVar, 0, len(env))
	for _, k := range sortedKeys(env) {
		out = append(out, envVar{Name: k, Value: env[k]})
	}
	return out
}
