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
	HostNetwork                  bool               `yaml:"hostNetwork"`
	HostPID                      bool               `yaml:"hostPID"`
	HostIPC                      bool               `yaml:"hostIPC"`
	ServiceAccountName           string             `yaml:"serviceAccountName,omitempty"`
	RuntimeClassName             string             `yaml:"runtimeClassName,omitempty"`
	SecurityContext              podSecurityContext `yaml:"securityContext"`
	Containers                   []container        `yaml:"containers"`
	Volumes                      []volume           `yaml:"volumes"`
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
	// Defence in depth: if the selector and the pod labels ever drift apart the
	// NetworkPolicy silently stops applying and the pod gets unrestricted
	// egress. Fail rendering rather than emit a policy that matches nothing.
	for k, v := range selector {
		if podLabels[k] != v {
			return "", fmt.Errorf("internal error: NetworkPolicy selector label %q=%q is not on the Job pod template; refusing to render a policy that would not apply", k, v)
		}
	}

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
			// calls), so a retry would repeat them. This is not at-most-once
			// execution — node failure or pod deletion can still start the run
			// twice (see EnforcementNotes).
			BackoffLimit:            0,
			Completions:             1,
			Parallelism:             1,
			ActiveDeadlineSeconds:   s.ActiveDeadlineSeconds,
			TTLSecondsAfterFinished: s.TTLSecondsAfterFinished,
			Template: podTemplate{
				Metadata: templateMeta{Labels: podLabels},
				Spec: podSpec{
					RestartPolicy:                "Never",
					AutomountServiceAccountToken: false,
					// No cluster service discovery injected into the agent's env.
					EnableServiceLinks: false,
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
						Resources: resourceRequirements{
							Requests: resourceList{CPU: s.CPURequest, Memory: s.MemoryRequest},
							Limits:   resourceList{CPU: s.CPULimit, Memory: s.MemoryLimit},
						},
						SecurityContext: containerSecurityContext{
							AllowPrivilegeEscalation: false,
							Privileged:               false,
							ReadOnlyRootFilesystem:   true,
							RunAsNonRoot:             true,
							RunAsUser:                s.RunAsUser,
							RunAsGroup:               s.RunAsUser,
							Capabilities:             capabilities{Drop: []string{"ALL"}},
							SeccompProfile:           seccompProfile{Type: "RuntimeDefault"},
						},
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

	return encodeDocs(np, j)
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
