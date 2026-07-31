package k8s

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

var update = flag.Bool("update", false, "rewrite testdata golden manifests")

// docs decodes the rendered multi-document manifest into generic maps so tests
// can assert on structure rather than on formatting.
func docs(t *testing.T, manifest string) []map[string]any {
	t.Helper()
	var out []map[string]any
	dec := yaml.NewDecoder(strings.NewReader(manifest))
	for {
		var d map[string]any
		err := dec.Decode(&d)
		if err != nil {
			break
		}
		out = append(out, d)
	}
	if len(out) == 0 {
		t.Fatalf("rendered manifest decoded to zero documents:\n%s", manifest)
	}
	return out
}

// dig walks a decoded document by key path, failing the test if the path is
// absent. It exists so a missing security field is a test failure, never a
// silently-passing nil.
func dig(t *testing.T, doc map[string]any, path ...string) any {
	t.Helper()
	var cur any = doc
	for i, key := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			t.Fatalf("path %v: element %q is not a mapping", path, strings.Join(path[:i], "."))
		}
		cur, ok = m[key]
		if !ok {
			t.Fatalf("path %v: key %q is missing", path, key)
		}
	}
	return cur
}

func goldenSpec(t *testing.T) JobSpec {
	t.Helper()
	s := DefaultJobSpec()
	s.Name = "fix-tests"
	s.Namespace = "andbo-runs"
	s.Image = "ghcr.io/qosikz/andbo/runtime:latest"
	s.Command = []string{"andbo-agent"}
	return s
}

func checkGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("writing golden %s: %v", path, err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading golden %s: %v (run: go test ./internal/runtime/k8s -update)", path, err)
	}
	if got != string(want) {
		t.Errorf("rendered manifest differs from %s\n--- got ---\n%s\n--- want ---\n%s", path, got, want)
	}
}

func TestRender_GoldenMinimal(t *testing.T) {
	got, err := goldenSpec(t).Render()
	if err != nil {
		t.Fatalf("Render() = %v, want nil", err)
	}
	checkGolden(t, "minimal.golden.yaml", got)
}

func TestRender_GoldenFull(t *testing.T) {
	s := goldenSpec(t)
	s.Args = []string{"--task", "fix failing tests"}
	s.Env = map[string]string{"ANDBO_TASK": "fix failing tests", "CI": "true", "ANDBO_RUN_ID": "01J0"}
	s.RuntimeClassName = "gvisor"
	s.ServiceAccountName = "andbo-agent"
	s.WorkingDir = "/workspace"
	s.CPURequest = "250m"
	s.CPULimit = "2"
	s.MemoryRequest = "256Mi"
	s.MemoryLimit = "2Gi"
	s.ActiveDeadlineSeconds = 900
	s.TTLSecondsAfterFinished = 300
	s.RunAsUser = 65532

	got, err := s.Render()
	if err != nil {
		t.Fatalf("Render() = %v, want nil", err)
	}
	checkGolden(t, "full.golden.yaml", got)
}

// TestRender_GoldenImageWorkspace pins the workspace transport byte for byte:
// the init container is the one place the renderer emits a command of its own,
// so a change to its argv or its hardening must be a visible diff.
func TestRender_GoldenImageWorkspace(t *testing.T) {
	s := goldenSpec(t)
	s.WorkspaceTransport = WorkspaceFromImage
	s.ImageWorkspacePath = "/andbo/workspace"

	got, err := s.Render()
	if err != nil {
		t.Fatalf("Render() = %v, want nil", err)
	}
	checkGolden(t, "image-workspace.golden.yaml", got)
}

func TestRender_IsDeterministic(t *testing.T) {
	s := goldenSpec(t)
	s.Env = map[string]string{"Z": "1", "A": "2", "M": "3", "B": "4", "Q": "5"}

	first, err := s.Render()
	if err != nil {
		t.Fatalf("Render() = %v, want nil", err)
	}
	// Map iteration order varies per run; 50 renders in one process is enough to
	// catch any map-ordered output.
	for i := 0; i < 50; i++ {
		got, err := s.Render()
		if err != nil {
			t.Fatalf("Render() iteration %d = %v, want nil", i, err)
		}
		if got != first {
			t.Fatalf("Render() is not deterministic at iteration %d\n--- first ---\n%s\n--- got ---\n%s", i, first, got)
		}
	}
}

func TestRender_DocumentOrderAppliesNetworkPolicyFirst(t *testing.T) {
	got, err := goldenSpec(t).Render()
	if err != nil {
		t.Fatalf("Render() = %v, want nil", err)
	}
	d := docs(t, got)
	if len(d) != 2 {
		t.Fatalf("expected exactly 2 documents (NetworkPolicy, Job), got %d", len(d))
	}
	// Order matters: `kubectl apply -f` applies documents in order, so the
	// default-deny policy must exist before the Job pod can start.
	if kind := d[0]["kind"]; kind != "NetworkPolicy" {
		t.Errorf("document 0 kind = %v, want NetworkPolicy (it must be applied first)", kind)
	}
	if kind := d[1]["kind"]; kind != "Job" {
		t.Errorf("document 1 kind = %v, want Job", kind)
	}
}

func TestRender_NetworkPolicyDeniesBothDirections(t *testing.T) {
	got, err := goldenSpec(t).Render()
	if err != nil {
		t.Fatalf("Render() = %v, want nil", err)
	}
	np := docs(t, got)[0]

	if v := dig(t, np, "apiVersion"); v != "networking.k8s.io/v1" {
		t.Errorf("apiVersion = %v, want networking.k8s.io/v1", v)
	}
	types, ok := dig(t, np, "spec", "policyTypes").([]any)
	if !ok {
		t.Fatalf("spec.policyTypes is not a list")
	}
	if len(types) != 2 || types[0] != "Ingress" || types[1] != "Egress" {
		t.Errorf("spec.policyTypes = %v, want [Ingress Egress]", types)
	}
	// A default-deny policy is defined by the ABSENCE of rules. An empty rule
	// list would still deny, but any entry here would punch a hole, so assert
	// the keys are not present at all.
	spec, _ := np["spec"].(map[string]any)
	for _, key := range []string{"ingress", "egress"} {
		if _, present := spec[key]; present {
			t.Errorf("spec.%s must not be rendered: any rule would weaken default-deny", key)
		}
	}
}

// TestRender_NetworkPolicySelectorMatchesPod is the load-bearing test for
// network isolation: if the selector and the pod labels ever drift apart, the
// policy silently stops applying and the pod gets unrestricted egress.
func TestRender_NetworkPolicySelectorMatchesPod(t *testing.T) {
	got, err := goldenSpec(t).Render()
	if err != nil {
		t.Fatalf("Render() = %v, want nil", err)
	}
	d := docs(t, got)

	selector, ok := dig(t, d[0], "spec", "podSelector", "matchLabels").(map[string]any)
	if !ok {
		t.Fatal("networkpolicy spec.podSelector.matchLabels is not a mapping")
	}
	if len(selector) == 0 {
		t.Fatal("podSelector.matchLabels is empty: the policy would select every pod in the namespace")
	}
	podLabels, ok := dig(t, d[1], "spec", "template", "metadata", "labels").(map[string]any)
	if !ok {
		t.Fatal("job spec.template.metadata.labels is not a mapping")
	}
	for k, v := range selector {
		pv, present := podLabels[k]
		if !present || pv != v {
			t.Errorf("podSelector label %q=%v is not on the Job pod template (labels=%v); the NetworkPolicy would not apply", k, v, podLabels)
		}
	}
}

// TestRender_PodIsNotHandedTheClusterResolver is the DNS half of the isolation
// the NetworkPolicy provides. Left at its default, dnsPolicy is ClusterFirst,
// and the kubelet writes the kube-dns ClusterIP plus the cluster search domains
// into the pod's /etc/resolv.conf — the discovery route that runs alongside the
// environment one enableServiceLinks: false narrows.
func TestRender_PodIsNotHandedTheClusterResolver(t *testing.T) {
	got, err := goldenSpec(t).Render()
	if err != nil {
		t.Fatalf("Render() = %v, want nil", err)
	}
	pod := dig(t, docs(t, got)[1], "spec", "template", "spec").(map[string]any)

	if v := pod["dnsPolicy"]; v != "None" {
		t.Errorf("dnsPolicy = %v, want None; the ClusterFirst default points the pod at kube-dns", v)
	}
	// dnsPolicy None without a dnsConfig is rejected by the API server, so the
	// two are one field as far as this contract is concerned.
	cfg, ok := pod["dnsConfig"].(map[string]any)
	if !ok {
		t.Fatalf("dnsConfig is not a mapping (got %v); dnsPolicy None is invalid without one", pod["dnsConfig"])
	}
	ns, ok := cfg["nameservers"].([]any)
	if !ok || len(ns) != 1 || ns[0] != "127.0.0.1" {
		t.Errorf("dnsConfig.nameservers = %v, want [127.0.0.1]: the pod's own loopback, which cannot route off the pod", cfg["nameservers"])
	}
	// Absent by construction. A search list is how svc.cluster.local comes back,
	// and neither key has a rendered form to set.
	for _, key := range []string{"searches", "options"} {
		if _, present := cfg[key]; present {
			t.Errorf("dnsConfig.%s must not be rendered, got %v", key, cfg[key])
		}
	}
	if strings.Contains(got, "cluster.local") {
		t.Errorf("manifest names a cluster search domain:\n%s", got)
	}
}

func TestRender_JobHardening(t *testing.T) {
	got, err := goldenSpec(t).Render()
	if err != nil {
		t.Fatalf("Render() = %v, want nil", err)
	}
	job := docs(t, got)[1]

	if v := dig(t, job, "apiVersion"); v != "batch/v1" {
		t.Errorf("apiVersion = %v, want batch/v1", v)
	}
	// backoffLimit 0: an agent run has side effects and must never be retried.
	if v := dig(t, job, "spec", "backoffLimit"); v != 0 {
		t.Errorf("spec.backoffLimit = %v, want 0 (no silent retries)", v)
	}
	if v := dig(t, job, "spec", "activeDeadlineSeconds"); v != DefaultActiveDeadlineSeconds {
		t.Errorf("spec.activeDeadlineSeconds = %v, want %d", v, DefaultActiveDeadlineSeconds)
	}
	if v := dig(t, job, "spec", "ttlSecondsAfterFinished"); v != DefaultTTLSecondsAfterFinished {
		t.Errorf("spec.ttlSecondsAfterFinished = %v, want %d", v, DefaultTTLSecondsAfterFinished)
	}

	pod := dig(t, job, "spec", "template", "spec").(map[string]any)
	if v := pod["restartPolicy"]; v != "Never" {
		t.Errorf("restartPolicy = %v, want Never", v)
	}
	if v := pod["automountServiceAccountToken"]; v != false {
		t.Errorf("automountServiceAccountToken = %v, want false", v)
	}
	if v := pod["enableServiceLinks"]; v != false {
		t.Errorf("enableServiceLinks = %v, want false (no cluster service discovery leaked into env)", v)
	}
	for _, key := range []string{"hostNetwork", "hostPID", "hostIPC"} {
		if v := pod[key]; v != false {
			t.Errorf("%s = %v, want false", key, v)
		}
	}
	// Optional fields must be absent, not empty, when unset.
	for _, key := range []string{"serviceAccountName", "runtimeClassName", "nodeName", "hostAliases"} {
		if _, present := pod[key]; present {
			t.Errorf("%s must not be rendered when unset, got %v", key, pod[key])
		}
	}

	if v := dig(t, job, "spec", "template", "spec", "securityContext", "runAsNonRoot"); v != true {
		t.Errorf("pod securityContext.runAsNonRoot = %v, want true", v)
	}
	if v := dig(t, job, "spec", "template", "spec", "securityContext", "seccompProfile", "type"); v != "RuntimeDefault" {
		t.Errorf("pod seccompProfile.type = %v, want RuntimeDefault", v)
	}

	containers, ok := pod["containers"].([]any)
	if !ok || len(containers) != 1 {
		t.Fatalf("expected exactly 1 container, got %v", pod["containers"])
	}
	c := containers[0].(map[string]any)

	sc := dig(t, c, "securityContext").(map[string]any)
	for key, want := range map[string]any{
		"allowPrivilegeEscalation": false,
		"privileged":               false,
		"readOnlyRootFilesystem":   true,
		"runAsNonRoot":             true,
	} {
		if sc[key] != want {
			t.Errorf("container securityContext.%s = %v, want %v", key, sc[key], want)
		}
	}
	if v := dig(t, c, "securityContext", "runAsUser"); v != DefaultRunAsUser {
		t.Errorf("container runAsUser = %v, want %d", v, DefaultRunAsUser)
	}
	drop, ok := dig(t, c, "securityContext", "capabilities", "drop").([]any)
	if !ok || len(drop) != 1 || drop[0] != "ALL" {
		t.Errorf("capabilities.drop = %v, want [ALL]", drop)
	}
	if _, present := sc["capabilities"].(map[string]any)["add"]; present {
		t.Error("capabilities.add must never be rendered")
	}
	if v := dig(t, c, "securityContext", "seccompProfile", "type"); v != "RuntimeDefault" {
		t.Errorf("container seccompProfile.type = %v, want RuntimeDefault", v)
	}

	// Bounded resources on both ends: requests schedule it, limits cap it.
	for _, path := range [][]string{{"requests", "cpu"}, {"requests", "memory"}, {"limits", "cpu"}, {"limits", "memory"}} {
		if v := dig(t, c, append([]string{"resources"}, path...)...); v == "" {
			t.Errorf("resources.%s is empty, want a bounded quantity", strings.Join(path, "."))
		}
	}

	// readOnlyRootFilesystem is only workable because writable scratch space is
	// provided as size-limited emptyDir volumes.
	mounts, ok := c["volumeMounts"].([]any)
	if !ok || len(mounts) == 0 {
		t.Fatal("expected writable volumeMounts alongside readOnlyRootFilesystem")
	}
	volumes, ok := pod["volumes"].([]any)
	if !ok || len(volumes) != len(mounts) {
		t.Fatalf("expected %d volumes to back %d mounts, got %v", len(mounts), len(mounts), pod["volumes"])
	}
	for _, v := range volumes {
		vol := v.(map[string]any)
		ed, present := vol["emptyDir"]
		if !present {
			t.Errorf("volume %v must be an emptyDir; no other volume source is supported", vol["name"])
			continue
		}
		if sz := ed.(map[string]any)["sizeLimit"]; sz == nil || sz == "" {
			t.Errorf("volume %v emptyDir has no sizeLimit; scratch space must be bounded", vol["name"])
		}
	}
}

func TestRender_OptionalFieldsAppearOnlyWhenRequested(t *testing.T) {
	s := goldenSpec(t)
	s.ServiceAccountName = "andbo-agent"
	s.RuntimeClassName = "gvisor"

	got, err := s.Render()
	if err != nil {
		t.Fatalf("Render() = %v, want nil", err)
	}
	pod := dig(t, docs(t, got)[1], "spec", "template", "spec").(map[string]any)

	if v := pod["serviceAccountName"]; v != "andbo-agent" {
		t.Errorf("serviceAccountName = %v, want andbo-agent", v)
	}
	if v := pod["runtimeClassName"]; v != "gvisor" {
		t.Errorf("runtimeClassName = %v, want gvisor", v)
	}
	// Naming a service account must NOT start mounting its token: an explicit
	// identity is for image pulls / admission, not for API access.
	if v := pod["automountServiceAccountToken"]; v != false {
		t.Errorf("automountServiceAccountToken = %v, want false even with a named service account", v)
	}
}

func TestRender_EnvIsSortedAndLiteral(t *testing.T) {
	s := goldenSpec(t)
	s.Env = map[string]string{"ZULU": "z", "ALPHA": "a", "MIKE": "m"}

	got, err := s.Render()
	if err != nil {
		t.Fatalf("Render() = %v, want nil", err)
	}
	c := dig(t, docs(t, got)[1], "spec", "template", "spec", "containers").([]any)[0].(map[string]any)
	env, ok := c["env"].([]any)
	if !ok || len(env) != 3 {
		t.Fatalf("expected 3 env entries, got %v", c["env"])
	}
	wantNames := []string{"ALPHA", "MIKE", "ZULU"}
	for i, e := range env {
		entry := e.(map[string]any)
		if entry["name"] != wantNames[i] {
			t.Errorf("env[%d].name = %v, want %v (env must be sorted)", i, entry["name"], wantNames[i])
		}
		// Only literal values: valueFrom would let a manifest pull Secrets or
		// the downward API into the agent, which this contract does not allow.
		if _, present := entry["valueFrom"]; present {
			t.Errorf("env[%d] must not carry valueFrom", i)
		}
	}
}
