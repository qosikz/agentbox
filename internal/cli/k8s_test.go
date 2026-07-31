package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// renderK8s runs `andbo k8s render` with args and returns stdout, stderr, err.
// Every test goes through the real dispatcher so flag parsing, policy loading,
// and the security boundary are exercised together.
func runK8s(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	r := NewRoot("test", "none", "now")
	err := r.k8s(args, &out, &errOut)
	return out.String(), errOut.String(), err
}

// k8sDocs decodes a rendered multi-document manifest so tests assert on
// structure rather than formatting.
func k8sDocs(t *testing.T, manifest string) []map[string]any {
	t.Helper()
	var docs []map[string]any
	dec := yaml.NewDecoder(strings.NewReader(manifest))
	for {
		var d map[string]any
		if err := dec.Decode(&d); err != nil {
			break
		}
		docs = append(docs, d)
	}
	if len(docs) == 0 {
		t.Fatalf("manifest decoded to zero documents:\n%s", manifest)
	}
	return docs
}

// dig walks a decoded document by key path, failing the test if the path is
// absent, so a missing security field is a failure and never a passing nil.
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

// setupK8sProject is setupProject plus one thing these tests need: an empty
// GITHUB_TOKEN. The policy `andbo init` writes allowlists that name, so on a
// machine with `gh` configured buildAgentEnv resolves the real token, the bridge
// correctly refuses to inline it, and every happy-path test here fails for a
// reason that has nothing to do with what it asserts.
func setupK8sProject(t *testing.T) string {
	t.Helper()
	dir := setupProject(t)
	t.Setenv("GITHUB_TOKEN", "")
	return dir
}

// k8sArgs builds a valid render invocation. The workspace transport is a
// parameter rather than a default that callers override, so no test depends on
// a duplicate flag resolving last-wins.
func k8sArgs(workspace string, extra ...string) []string {
	return append([]string{
		"render", "fix failing tests",
		"--name", "fix-tests",
		"--namespace", "andbo-runs",
		"--workspace", workspace,
	}, extra...)
}

// okArgs is the common case: a valid invocation with an empty workspace.
func okArgs(extra ...string) []string { return k8sArgs("empty", extra...) }

func TestK8sRenderProducesHardenedManifest(t *testing.T) {
	setupK8sProject(t)

	out, errOut, err := runK8s(t, okArgs()...)
	if err != nil {
		t.Fatalf("render should succeed, got err=%v code=%d\nstderr:\n%s", err, CodeFor(err), errOut)
	}

	docs := k8sDocs(t, out)
	if len(docs) != 2 {
		t.Fatalf("want 2 documents (NetworkPolicy, Job), got %d:\n%s", len(docs), out)
	}
	// Order is load-bearing: `kubectl apply -f -` applies in document order, so
	// the policy must exist before the pod it isolates.
	if got := docs[0]["kind"]; got != "NetworkPolicy" {
		t.Errorf("first document kind = %v, want NetworkPolicy", got)
	}
	if got := docs[1]["kind"]; got != "Job" {
		t.Errorf("second document kind = %v, want Job", got)
	}

	// Spot-check the hardening the CLI must never be able to weaken. The
	// exhaustive assertions live in internal/runtime/k8s; this proves the CLI
	// path reaches the same hardened renderer. Asserted on the AGENT container
	// specifically, not by searching the whole stream: with an init container
	// present every needle appears twice, so a regression in one of them could
	// hide behind the other's copy.
	pod, _ := dig(t, docs[1], "spec", "template", "spec").(map[string]any)
	if pod == nil {
		t.Fatal("pod spec is not a mapping")
	}
	for field, want := range map[string]any{
		"automountServiceAccountToken": false,
		"enableServiceLinks":           false,
		"hostNetwork":                  false,
		"hostPID":                      false,
		"hostIPC":                      false,
	} {
		if got := pod[field]; got != want {
			t.Errorf("pod %s = %v, want %v", field, got, want)
		}
	}
	containers, _ := pod["containers"].([]any)
	if len(containers) != 1 {
		t.Fatalf("want exactly one container, got %d", len(containers))
	}
	agent, ok := containers[0].(map[string]any)
	if !ok {
		t.Fatalf("container is not a mapping: %#v", containers[0])
	}
	for field, want := range map[string]any{
		"privileged":               false,
		"allowPrivilegeEscalation": false,
		"readOnlyRootFilesystem":   true,
		"runAsNonRoot":             true,
	} {
		if got := dig(t, agent, "securityContext", field); got != want {
			t.Errorf("agent container securityContext.%s = %v, want %v", field, got, want)
		}
	}
	if drop, _ := dig(t, agent, "securityContext", "capabilities", "drop").([]any); len(drop) != 1 || drop[0] != "ALL" {
		t.Errorf("capabilities.drop = %v, want [ALL]", drop)
	}
	// hostPath is not a declarable volume source anywhere in the contract.
	if strings.Contains(out, "hostPath") {
		t.Errorf("rendered manifest names hostPath:\n%s", out)
	}
	// The manifest is the only thing on stdout; everything else is stderr.
	if !strings.HasPrefix(out, "apiVersion:") {
		t.Errorf("stdout must start with the manifest so it can be piped to kubectl, got:\n%s", out)
	}
}

func TestK8sRenderRejectsIncompleteInvocations(t *testing.T) {
	// wantMsg pins WHICH failure each case produces. Without it every case is
	// satisfied by any error at all, since CodeFor returns ExitGeneral for any
	// plain error — "missing name" would pass on a namespace complaint.
	cases := []struct {
		name    string
		args    []string
		want    int
		wantMsg string
	}{
		{"no subcommand", []string{}, ExitGeneral, "usage: andbo k8s render"},
		{"unknown subcommand", []string{"apply"}, ExitGeneral, "unknown k8s command: apply"},
		{"missing task", []string{"render", "--name", "n", "--namespace", "ns", "--workspace", "empty"}, ExitGeneral, "missing task"},
		{"missing name", []string{"render", "t", "--namespace", "ns", "--workspace", "empty"}, ExitGeneral, "missing --name"},
		{"missing namespace", []string{"render", "t", "--name", "n", "--workspace", "empty"}, ExitGeneral, "missing --namespace"},
		{"missing workspace transport", []string{"render", "t", "--name", "n", "--namespace", "ns"}, ExitGeneral, "missing --workspace"},
		{"invalid workspace transport", []string{"render", "t", "--name", "n", "--namespace", "ns", "--workspace", "host"}, ExitGeneral, `--workspace "host" is not a transport`},
		{"image transport with no path", []string{"render", "t", "--name", "n", "--namespace", "ns", "--workspace", "image:"}, ExitGeneral, "--workspace image: has no path"},
		{"unknown flag", okArgs("--privileged"), ExitGeneral, "unknown flag: --privileged"},
		{"flag without value", []string{"render", "t", "--name"}, ExitGeneral, "flag --name requires a value"},
		{"value on a boolean flag", okArgs("--json=false"), ExitGeneral, "flag --json takes no value"},
		{"second positional", []string{"render", "t", "extra", "--name", "n", "--namespace", "ns", "--workspace", "empty"}, ExitGeneral, `unexpected argument: "extra"`},
		{"reserved namespace", []string{"render", "t", "--name", "n", "--namespace", "kube-system", "--workspace", "empty"}, ExitInvalidConfig, "reserved for cluster control-plane"},
		// What Kubernetes reserves is the kube- PREFIX, not only the three
		// namespaces it creates under it, so this case pins a name that no list
		// of shipped namespaces would have caught. The reason the operator is
		// given travels in the error text itself and is pinned at the guard, in
		// internal/runtime/k8s; restating it here would only give it somewhere
		// to drift.
		{"kube-prefixed namespace", []string{"render", "t", "--name", "n", "--namespace", "kube-flannel", "--workspace", "empty"}, ExitInvalidConfig, "reserved for cluster control-plane"},
		// Not every namespace a privileged component owns sits under a reserved
		// prefix. These are refused by exact name, and reach the operator through
		// the same exit code as any other invalid manifest field.
		{"privileged add-on namespace", []string{"render", "t", "--name", "n", "--namespace", "cert-manager", "--workspace", "empty"}, ExitInvalidConfig, "cluster-wide privilege"},
		{"name is not a DNS label", []string{"render", "t", "--name", "Fix_Tests", "--namespace", "ns", "--workspace", "empty"}, ExitInvalidConfig, "is not a DNS-1123 label"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setupK8sProject(t)
			out, _, err := runK8s(t, tc.args...)
			if CodeFor(err) != tc.want {
				t.Errorf("exit code = %d, want %d (err=%v)", CodeFor(err), tc.want, err)
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("error does not report %q, so this case could be passing for the wrong reason:\n%v", tc.wantMsg, err)
			}
			if out != "" {
				t.Errorf("a rejected render must write nothing to stdout, got:\n%s", out)
			}
		})
	}
}

// A run that asks for something the renderer cannot enforce must fail closed
// with a policy-violation code, never render a downgraded manifest.
func TestK8sRenderFailsClosedOnUnenforceablePolicy(t *testing.T) {
	cases := []struct {
		name   string
		policy string
		want   int
	}{
		{"open network", "network:\n  mode: open\n", ExitPolicyViolation},
		{"allowlist network", "network:\n  mode: allowlist\n  allow: [\"api.anthropic.com\"]\n", ExitPolicyViolation},
		// Same category as the network modes — a policy asking for a mode this
		// renderer will not emit — so it must carry the same exit code, or a CI
		// gate on "policy blocked" silently misses it.
		{"local isolation", "runtime:\n  isolation: local\n", ExitPolicyViolation},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := setupK8sProject(t)
			if err := os.WriteFile(filepath.Join(dir, "andbo.yaml"), []byte(tc.policy), 0o644); err != nil {
				t.Fatal(err)
			}
			out, _, err := runK8s(t, okArgs()...)
			if CodeFor(err) != tc.want {
				t.Errorf("exit code = %d, want %d (err=%v)", CodeFor(err), tc.want, err)
			}
			if out != "" {
				t.Errorf("stdout must be empty when the render fails closed, got:\n%s", out)
			}
			if err == nil || !strings.Contains(err.Error(), "container runtime") {
				t.Errorf("error must say where the workload CAN run, got: %v", err)
			}
		})
	}
}

// The manifest is plain text in etcd. A host secret must never reach it, and an
// allowlisted secret that is actually present must stop the render outright
// rather than be silently dropped.
func TestK8sRenderNeverEmitsHostSecrets(t *testing.T) {
	dir := setupK8sProject(t)
	if err := os.WriteFile(filepath.Join(dir, "andbo.yaml"),
		[]byte("secrets:\n  mode: explicit\n  allow: [\"FAKE_AGENT_TOKEN\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_AGENT_TOKEN", "sk-must-never-appear")

	out, errOut, err := runK8s(t, okArgs()...)
	if err == nil {
		t.Fatalf("render must refuse to inline an allowlisted host secret, got manifest:\n%s", out)
	}
	for _, stream := range []string{out, errOut, err.Error()} {
		if strings.Contains(stream, "sk-must-never-appear") {
			t.Fatalf("secret value leaked into output:\n%s", stream)
		}
	}
	if out != "" {
		t.Errorf("stdout must be empty, got:\n%s", out)
	}
}

// Host paths identify the operator's machine and do not exist in a pod. None
// may survive into a manifest applied to a shared cluster.
func TestK8sRenderNeverLeaksHostPaths(t *testing.T) {
	dir := setupK8sProject(t)
	out, _, err := runK8s(t, k8sArgs("image:/andbo/workspace")...)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	// Sentinels must be paths that identify THIS machine. os.TempDir() is not
	// one: on Linux it is "/tmp", which the pod legitimately mounts as its
	// scratch volume, so asserting its absence fails on a correct manifest. The
	// project directory below already sits under it and is specific.
	for _, host := range []string{dir, os.Getenv("HOME")} {
		if host == "" || host == "/" || strings.HasPrefix("/work", host) || strings.HasPrefix("/tmp", host) {
			continue
		}
		if strings.Contains(out, host) {
			t.Errorf("host path %q leaked into the manifest:\n%s", host, out)
		}
	}
	// HOME must be redirected to the pod working directory, not dropped: the
	// root filesystem is read-only, so git and package managers need a home.
	if !strings.Contains(out, "value: /work") {
		t.Errorf("image transport should bridge HOME to the pod working directory:\n%s", out)
	}
	if !strings.Contains(out, "workspace-init") {
		t.Errorf("image transport should render the workspace init container:\n%s", out)
	}
}

// A workspace path that is inside the pod working directory is hidden by the
// writable emptyDir mounted over it: the copy succeeds and delivers nothing.
func TestK8sRenderRejectsMaskedWorkspaceSource(t *testing.T) {
	setupK8sProject(t)
	out, _, err := runK8s(t, k8sArgs("image:/work/repo")...)
	if CodeFor(err) != ExitInvalidConfig {
		t.Errorf("exit code = %d, want %d (err=%v)", CodeFor(err), ExitInvalidConfig, err)
	}
	if out != "" {
		t.Errorf("stdout must be empty, got:\n%s", out)
	}
}

// The renderer reports MANIFEST field names ("imageWorkspacePath"), which a CLI
// user never typed. An error that does not name the flag to change is not
// actionable, so the CLI must map them back.
func TestK8sRenderErrorsNameTheFlagToFix(t *testing.T) {
	cases := []struct {
		name string
		args []string
		// field is the manifest field the reported CAUSE must name, and absent
		// is a field it must NOT name. Asserting only that the flag appears
		// somewhere would pass on any error at all, because the flag mapping is
		// a constant that lists every flag.
		field, absent, flag string
	}{
		{"workspace source", k8sArgs("image:/"), "imageWorkspacePath", "runtimeClassName", "--workspace image:PATH"},
		{"job name", []string{"render", "t", "--name", "Fix_Tests", "--namespace", "ns", "--workspace", "empty"}, "name", "imageWorkspacePath", "--name"},
		{"namespace", []string{"render", "t", "--name", "n", "--namespace", "kube-system", "--workspace", "empty"}, "namespace", "imageWorkspacePath", "--namespace"},
		// "args" is the task the user typed, not anything in the policy file.
		{"task text", []string{"render", "fix\rtests", "--name", "n", "--namespace", "ns", "--workspace", "empty"}, "args", "imageWorkspacePath", "the task text you typed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setupK8sProject(t)
			_, _, err := runK8s(t, tc.args...)
			if err == nil {
				t.Fatal("expected a validation error")
			}
			cause, mapping, found := strings.Cut(err.Error(), "\n\nThose are manifest fields")
			if !found {
				t.Fatalf("error carries no field-to-flag mapping:\n%v", err)
			}
			if !strings.Contains(cause, tc.field) {
				t.Errorf("reported cause does not name %q:\n%s", tc.field, cause)
			}
			if strings.Contains(cause, tc.absent) {
				t.Errorf("reported cause names the unrelated field %q:\n%s", tc.absent, cause)
			}
			// Scoped to the LINE that names the field, not the whole block. The
			// block lists every flag, so a block-wide search passes even when
			// every field is paired with the wrong flag.
			var line string
			for _, l := range strings.Split(mapping, "\n") {
				if f, _, ok := strings.Cut(strings.TrimSpace(l), " "); ok && f == tc.field {
					line = l
				}
			}
			if line == "" {
				t.Fatalf("mapping has no line for field %q:\n%s", tc.field, mapping)
			}
			if !strings.Contains(line, tc.flag) {
				t.Errorf("mapping pairs %q with the wrong input: %q (want %q)", tc.field, strings.TrimSpace(line), tc.flag)
			}
		})
	}
}

// The task text is the one caller-controlled string that reaches argv. It must
// only ever become a YAML scalar, never manifest structure.
func TestK8sRenderTaskTextCannotInjectManifestStructure(t *testing.T) {
	setupK8sProject(t)
	hostile := "fix tests\"\nprivileged: true\n  hostNetwork: true"
	out, _, err := runK8s(t,
		"render", hostile,
		"--name", "fix-tests",
		"--namespace", "andbo-runs",
		"--workspace", "empty",
	)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	docs := k8sDocs(t, out)
	if len(docs) != 2 {
		t.Fatalf("hostile task broke the document stream (%d docs):\n%s", len(docs), out)
	}

	// The assertion has to be structural, not textual: the payload legitimately
	// appears in the output as the text of a YAML scalar. What matters is that
	// it round-trips as DATA — one argument, byte-identical — and that it
	// created no field.
	podSpec, _ := dig(t, docs[1], "spec", "template", "spec").(map[string]any)
	if podSpec == nil {
		t.Fatalf("pod spec is not a mapping:\n%s", out)
	}
	if got := podSpec["hostNetwork"]; got != false {
		t.Errorf("hostNetwork = %v, want false", got)
	}
	if _, found := podSpec["privileged"]; found {
		t.Errorf("task text created a pod-level privileged field:\n%s", out)
	}
	c, _ := dig(t, docs[1], "spec", "template", "spec", "containers").([]any)
	if len(c) != 1 {
		t.Fatalf("want exactly one container, got %d", len(c))
	}
	agent, ok := c[0].(map[string]any)
	if !ok {
		t.Fatalf("container is not a mapping: %#v", c[0])
	}
	if got := dig(t, agent, "securityContext", "privileged"); got != false {
		t.Errorf("privileged = %v, want false", got)
	}
	args, _ := dig(t, agent, "args").([]any)
	if len(args) != 1 || args[0] != hostile {
		t.Errorf("hostile task did not round-trip as a single scalar argument: %#v", args)
	}
}

// Rendering twice must be byte-identical: a manifest that changes between runs
// cannot be reviewed, diffed, or pinned in version control.
func TestK8sRenderIsDeterministic(t *testing.T) {
	setupK8sProject(t)
	first, _, err := runK8s(t, okArgs()...)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for i := 0; i < 5; i++ {
		next, _, err := runK8s(t, okArgs()...)
		if err != nil {
			t.Fatalf("render %d: %v", i, err)
		}
		if next != first {
			t.Fatalf("render %d differs from the first:\n--- first ---\n%s\n--- next ---\n%s", i, first, next)
		}
	}
}

// The command renders locally and must not need — or read — any cluster
// credentials. An unreadable KUBECONFIG proves nothing consulted it.
func TestK8sRenderNeverReadsKubeconfig(t *testing.T) {
	dir := setupK8sProject(t)
	kubeconfig := filepath.Join(dir, "unreadable-kubeconfig")
	if err := os.WriteFile(kubeconfig, []byte("clusters: []\n"), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", kubeconfig)

	if _, _, err := runK8s(t, okArgs()...); err != nil {
		t.Fatalf("render must not depend on cluster credentials, got: %v", err)
	}
}

// Rendering is not running: no session is recorded, nothing is executed, and no
// workspace copy is materialized.
func TestK8sRenderRecordsNoSession(t *testing.T) {
	dir := setupK8sProject(t)
	if _, _, err := runK8s(t, okArgs()...); err != nil {
		t.Fatalf("render: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".andbo")); !os.IsNotExist(err) {
		t.Errorf("render must not create .andbo/ (stat err = %v)", err)
	}
}

// Andbo must not claim controls it has not implemented. The notes stay on
// stderr so stdout remains pipeable.
func TestK8sRenderNotesDoNotOverclaim(t *testing.T) {
	setupK8sProject(t)
	out, errOut, err := runK8s(t, k8sArgs("image:/andbo/workspace")...)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if strings.Contains(out, "Not enforced") {
		t.Errorf("notes must go to stderr, not stdout:\n%s", out)
	}
	for _, needle := range k8sNoteNeedles {
		if !strings.Contains(errOut, needle) {
			t.Errorf("enforcement notes are missing %q:\n%s", needle, errOut)
		}
	}
}

// k8sNoteNeedles are the caveats that must survive in every output mode. The
// CLI-layer ones matter most: they are the notes a change to the note plumbing
// would silently drop, leaving only the renderer's 18 and a passing count.
var k8sNoteNeedles = []string{
	// CNI dependency — the k8s package's own note, surfaced by the CLI.
	"NetworkPolicy",
	// CLI-layer gaps this slice introduces.
	"filesystem.deny",
	"secrets.allow",
	"does not run",
	// Policy sections a Job silently drops.
	"commands.",
	"mcp.",
	"budget.max_usd",
}

func TestK8sRenderJSON(t *testing.T) {
	setupK8sProject(t)
	out, _, err := runK8s(t, okArgs("--json")...)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	var got struct {
		Manifest           string   `json:"manifest"`
		Notes              []string `json:"notes"`
		Name               string   `json:"name"`
		Namespace          string   `json:"namespace"`
		Image              string   `json:"image"`
		Policy             string   `json:"policy"`
		Network            string   `json:"network"`
		WorkspaceTransport string   `json:"workspace_transport"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("--json output is not valid JSON: %v\n%s", err, out)
	}
	if !strings.Contains(got.Manifest, "kind: NetworkPolicy") {
		t.Errorf("manifest field is missing the NetworkPolicy:\n%s", got.Manifest)
	}
	// Machine-readable output carries the SAME honesty contract as stderr. A
	// count check would pass while the four CLI-layer notes were dropped and
	// only the renderer's eighteen remained.
	joined := strings.Join(got.Notes, "\n")
	for _, needle := range k8sNoteNeedles {
		if !strings.Contains(joined, needle) {
			t.Errorf("JSON notes are missing %q; --json must not be quieter than stderr", needle)
		}
	}
	if got.Image == "" || got.Policy == "" {
		t.Errorf("image=%q policy=%q; both identify what the manifest was built from", got.Image, got.Policy)
	}
	if got.Name != "fix-tests" || got.Namespace != "andbo-runs" {
		t.Errorf("identity = %q/%q, want fix-tests/andbo-runs", got.Name, got.Namespace)
	}
	if got.Network != "deny" {
		t.Errorf("network = %q, want deny", got.Network)
	}
	if got.WorkspaceTransport != "empty" {
		t.Errorf("workspace_transport = %q, want empty", got.WorkspaceTransport)
	}
}

// Optional hardening fields are rendered only when explicitly asked for, and
// naming a service account must never turn token automounting back on.
func TestK8sRenderOptionalHardeningFlags(t *testing.T) {
	setupK8sProject(t)
	out, _, err := runK8s(t, okArgs("--runtime-class", "gvisor", "--service-account", "andbo-runner")...)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(out, "runtimeClassName: gvisor") {
		t.Errorf("runtimeClassName not rendered:\n%s", out)
	}
	if !strings.Contains(out, "serviceAccountName: andbo-runner") {
		t.Errorf("serviceAccountName not rendered:\n%s", out)
	}
	if !strings.Contains(out, "automountServiceAccountToken: false") {
		t.Errorf("naming a service account must not automount its token:\n%s", out)
	}

	// And they are absent unless requested.
	bare, _, err := runK8s(t, okArgs()...)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if strings.Contains(bare, "runtimeClassName") || strings.Contains(bare, "serviceAccountName") {
		t.Errorf("optional fields rendered without being requested:\n%s", bare)
	}
}

// The policy's runtime budget is the Job's wall-clock budget: an agent that
// outlives it would occupy the cluster with no local process to stop it.
func TestK8sRenderMapsBudgetToActiveDeadline(t *testing.T) {
	dir := setupK8sProject(t)
	if err := os.WriteFile(filepath.Join(dir, "andbo.yaml"),
		[]byte("budget:\n  max_runtime_minutes: 7\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, _, err := runK8s(t, okArgs()...)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(out, "activeDeadlineSeconds: 420") {
		t.Errorf("budget.max_runtime_minutes=7 should bound the Job at 420s:\n%s", out)
	}
}

// An adapter that needs its own environment variable cannot run here: no
// variable except HOME crosses into a Job. The refusal must name the agent and
// the variable, because the renderer's own message blames host secrets — which
// an adapter-supplied literal is not.
func TestK8sRenderRefusesAgentsNeedingEnvironment(t *testing.T) {
	setupK8sProject(t)
	_, _, err := runK8s(t, okArgs("--agent", "goose")...)
	if CodeFor(err) != ExitPolicyViolation {
		t.Fatalf("exit code = %d, want %d (err=%v)", CodeFor(err), ExitPolicyViolation, err)
	}
	for _, needle := range []string{"goose", "GOOSE_MODE", "runtime.image"} {
		if !strings.Contains(err.Error(), needle) {
			t.Errorf("refusal does not mention %q, so it is not actionable:\n%v", needle, err)
		}
	}
	// An agent that needs nothing still renders.
	if _, _, err := runK8s(t, okArgs("--agent", "claude")...); err != nil {
		t.Errorf("an agent that adds no environment must still render, got: %v", err)
	}
}

// A budget the renderer cannot express must be reported in the vocabulary the
// user wrote it in, not as a "command timeout" they never set — and the check
// must survive values that overflow the duration arithmetic behind it.
func TestK8sRenderBudgetOverCapNamesThePolicyField(t *testing.T) {
	cases := []struct {
		name    string
		minutes string
	}{
		{"over the cap", "1500"},
		// These two are the reason the cap is compared in MINUTES. minutes *
		// time.Minute used to wrap: this one became 5.224192s, passed a cap
		// checked on the DURATION, and rendered a clean manifest whose Job
		// Kubernetes kills six seconds in (deadlineSeconds rounds up) — a silent
		// downgrade of the exact bound the check exists to enforce. budgetWindow
		// no longer wraps (see maxBudgetMinutes), and this check must keep
		// holding without it.
		{"would have wrapped to a small positive duration", "153722867281"},
		// Would have wrapped past zero.
		{"would have wrapped negative", "200000000000"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := setupK8sProject(t)
			if err := os.WriteFile(filepath.Join(dir, "andbo.yaml"),
				[]byte("budget:\n  max_runtime_minutes: "+tc.minutes+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			out, _, err := runK8s(t, okArgs()...)
			if CodeFor(err) != ExitPolicyViolation {
				t.Fatalf("exit code = %d, want %d (err=%v)\nstdout:\n%s", CodeFor(err), ExitPolicyViolation, err, out)
			}
			if out != "" {
				t.Errorf("stdout must be empty, got:\n%s", out)
			}
			for _, needle := range []string{"budget.max_runtime_minutes", "andbo.yaml"} {
				if !strings.Contains(err.Error(), needle) {
					t.Errorf("error does not name %q:\n%v", needle, err)
				}
			}
		})
	}
}

// Whatever the policy says, a rendered Job always carries a deadline inside the
// renderer's cap. This is the invariant the overflow above violated.
func TestK8sRenderDeadlineAlwaysWithinCap(t *testing.T) {
	for _, minutes := range []string{"1", "30", "1440", "0", "-5"} {
		dir := setupK8sProject(t)
		if err := os.WriteFile(filepath.Join(dir, "andbo.yaml"),
			[]byte("budget:\n  max_runtime_minutes: "+minutes+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		out, _, err := runK8s(t, okArgs()...)
		if err != nil {
			continue // refused; the invariant is about manifests that DO render
		}
		got, ok := dig(t, k8sDocs(t, out)[1], "spec", "activeDeadlineSeconds").(int)
		if !ok || got < 1 || got > 86400 {
			t.Errorf("max_runtime_minutes=%s rendered activeDeadlineSeconds=%v, outside 1..86400", minutes, got)
		}
	}
}

// A policy file the user named must exist. LoadPolicy falls back to built-in
// defaults when it does not, which is right for the implicit andbo.yaml but
// turns a typo into a silently different manifest — a floating-tag image in
// place of whatever digest the operator pinned — under a "✓ Policy applied"
// line asserting otherwise.
func TestK8sRenderRequiresAnExplicitPolicyToExist(t *testing.T) {
	setupK8sProject(t)
	out, _, err := runK8s(t, okArgs("--policy", "andbo.strict.yml")...)
	if CodeFor(err) != ExitInvalidConfig {
		t.Fatalf("exit code = %d, want %d (err=%v)", CodeFor(err), ExitInvalidConfig, err)
	}
	if out != "" {
		t.Errorf("stdout must be empty, got:\n%s", out)
	}
	if !strings.Contains(err.Error(), "andbo.strict.yml") {
		t.Errorf("error does not name the missing file:\n%v", err)
	}
}

// Without any policy file the defaults are correct — but the summary must say
// so rather than claim a file was applied.
func TestK8sRenderLabelsBuiltInDefaults(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)
	t.Setenv("GITHUB_TOKEN", "")

	out, errOut, err := runK8s(t, okArgs()...)
	if err != nil {
		t.Fatalf("render with no policy file should succeed, got: %v", err)
	}
	if out == "" {
		t.Fatal("no manifest rendered")
	}
	if !strings.Contains(errOut, "built-in defaults") {
		t.Errorf("summary must say the defaults were used, not that a file was applied:\n%s", errOut)
	}
}

// HOME must land on a writable path for BOTH transports: the pod root
// filesystem is read-only, and git and package managers fail without one.
func TestK8sRenderAlwaysGivesTheAgentAWritableHome(t *testing.T) {
	for _, ws := range []string{"empty", "image:/andbo/workspace"} {
		t.Run(ws, func(t *testing.T) {
			setupK8sProject(t)
			out, _, err := runK8s(t, k8sArgs(ws)...)
			if err != nil {
				t.Fatalf("render: %v", err)
			}
			c, _ := dig(t, k8sDocs(t, out)[1], "spec", "template", "spec", "containers").([]any)
			if len(c) != 1 {
				t.Fatalf("want one container, got %d", len(c))
			}
			env, _ := dig(t, c[0].(map[string]any), "env").([]any)
			var home string
			for _, e := range env {
				if m, ok := e.(map[string]any); ok && m["name"] == "HOME" {
					home, _ = m["value"].(string)
				}
			}
			if home != "/work" {
				t.Errorf("HOME = %q, want /work (the writable volume)", home)
			}
		})
	}
}

// The image transport is the one that consults os.Getwd(). A manifest that
// changes with the directory you happened to run from cannot be diffed or
// pinned.
func TestK8sRenderIsDeterministicAcrossWorkingDirectories(t *testing.T) {
	var manifests []string
	for i := 0; i < 2; i++ {
		setupK8sProject(t)
		out, _, err := runK8s(t, k8sArgs("image:/andbo/workspace")...)
		if err != nil {
			t.Fatalf("render %d: %v", i, err)
		}
		manifests = append(manifests, out)
	}
	if manifests[0] != manifests[1] {
		t.Errorf("manifest depends on the working directory:\n--- a ---\n%s\n--- b ---\n%s", manifests[0], manifests[1])
	}
}

// The default namespace is where a namespace-wide allow-dns-egress baseline is
// most likely to exist — the exfiltration channel the enforcement notes warn
// about. Not rejected (it is a valid namespace), but never silent.
func TestK8sRenderWarnsOnTheDefaultNamespace(t *testing.T) {
	setupK8sProject(t)
	_, errOut, err := runK8s(t, "render", "t", "--name", "n", "--namespace", "default", "--workspace", "empty")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(errOut, "default") || !strings.Contains(errOut, "dedicated") {
		t.Errorf("rendering into the default namespace must warn:\n%s", errOut)
	}
}

// An unsafe policy option cannot make this command do anything unsafe — nothing
// executes — but it must not pass unmentioned either, or a future addition to
// the unsafe set goes unreported on this path alone.
func TestK8sRenderReportsUnsafePolicyOptions(t *testing.T) {
	dir := setupK8sProject(t)
	if err := os.WriteFile(filepath.Join(dir, "andbo.yaml"),
		[]byte("secrets:\n  mode: explicit\n  allow: [\"*\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, errOut, err := runK8s(t, okArgs()...)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(errOut, "unsafe") {
		t.Errorf("unsafe policy options must be reported on stderr:\n%s", errOut)
	}
}

// Policy errors are part of the command's output and must reach the caller's
// stream, not bypass it via the process-wide stderr.
func TestK8sRenderPolicyErrorsGoToTheGivenStream(t *testing.T) {
	dir := setupK8sProject(t)
	if err := os.WriteFile(filepath.Join(dir, "andbo.yaml"),
		[]byte("secrets:\n  mode: bogus\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, errOut, err := runK8s(t, okArgs()...)
	if CodeFor(err) != ExitInvalidConfig {
		t.Fatalf("exit code = %d, want %d", CodeFor(err), ExitInvalidConfig)
	}
	if !strings.Contains(errOut, "secrets.mode") {
		t.Errorf("the specific policy error must reach the caller's stream:\n%s", errOut)
	}
}

// A Job nobody supervises must always carry a deadline. `andbo run` treats a
// zero budget as "no limit"; here that would leave a pod running until someone
// notices, so the renderer's own bounded default applies instead.
func TestK8sRenderAlwaysBoundsTheJobWithoutABudget(t *testing.T) {
	dir := setupK8sProject(t)
	if err := os.WriteFile(filepath.Join(dir, "andbo.yaml"),
		[]byte("budget:\n  max_runtime_minutes: 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, _, err := runK8s(t, okArgs()...)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(out, "activeDeadlineSeconds: 1800") {
		t.Errorf("a zero budget must still leave the renderer's bounded default:\n%s", out)
	}
}

// "Andbo never contacts a cluster" is a property of the whole binary, not of
// one code path, so it is asserted structurally: a Kubernetes client library in
// go.mod is the thing that would make contact possible in the first place. This
// test is here to fail the moment someone adds one.
func TestK8sRenderHasNoClusterClientDependency(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	for _, mod := range []string{
		"k8s.io/client-go",
		"k8s.io/apimachinery",
		"k8s.io/api",
		"k8s.io/kubectl",
		"sigs.k8s.io/controller-runtime",
	} {
		if strings.Contains(string(data), mod) {
			t.Errorf("go.mod depends on %s; Andbo renders Kubernetes manifests and must never be able to apply them", mod)
		}
	}
}

func TestParseK8sRenderFlags(t *testing.T) {
	o, err := parseK8sRenderFlags([]string{
		"fix tests", "--name=fix-tests", "--namespace", "andbo-runs",
		"--workspace=image:/andbo/workspace", "--policy", "custom.yaml",
		"--agent", "claude", "--runtime-class=gvisor", "--service-account", "sa", "--json",
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := k8sRenderOptions{
		task: "fix tests", name: "fix-tests", namespace: "andbo-runs",
		workspace: "image:/andbo/workspace", policy: "custom.yaml", agent: "claude",
		runtimeClass: "gvisor", serviceAccount: "sa", json: true,
	}
	if o != want {
		t.Errorf("parsed = %+v, want %+v", o, want)
	}
}
