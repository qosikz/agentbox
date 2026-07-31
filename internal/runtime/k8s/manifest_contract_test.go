package k8s

import (
	"strings"
	"testing"
)

// The three fields covered here — completions, parallelism, and
// imagePullPolicy — were pinned ONLY by the byte-for-byte golden manifests.
// That is a weaker guard than it looks: a golden diff reports that the output
// CHANGED, not that a security property broke, and `go test -update` rewrites
// the goldens, after which the whole suite goes green on a manifest that starts
// two agents from a cached image. These tests state the properties by name so a
// regression fails as itself and no regeneration can absorb it.

// allContainers returns every container the rendered pod would start, init
// containers included, keyed by name.
//
// Init containers are not a footnote here. One runs in the same pod, on the
// same volumes, from the same image reference as the agent, so a pull policy
// that drifted there is a pull policy that drifted for the run.
func allContainers(t *testing.T, manifest string) map[string]map[string]any {
	t.Helper()
	pod, ok := dig(t, docs(t, manifest)[1], "spec", "template", "spec").(map[string]any)
	if !ok {
		t.Fatal("job spec.template.spec is not a mapping")
	}
	out := map[string]map[string]any{}
	for _, key := range []string{"initContainers", "containers"} {
		list, present := pod[key]
		if !present {
			continue // initContainers is legitimately absent for the empty transport
		}
		items, ok := list.([]any)
		if !ok {
			t.Fatalf("%s is not a list, got %v", key, list)
		}
		for i, item := range items {
			c, ok := item.(map[string]any)
			if !ok {
				t.Fatalf("%s[%d] is not a mapping", key, i)
			}
			name, ok := c["name"].(string)
			if !ok {
				t.Fatalf("%s[%d] has no name", key, i)
			}
			out[name] = c
		}
	}
	if len(out) == 0 {
		t.Fatal("rendered pod declares no containers")
	}
	return out
}

// transportSpecs returns one valid spec per workspace transport, so a property
// asserted here is asserted on every shape this renderer emits rather than on
// the single spec the goldens happen to use.
func transportSpecs(t *testing.T) map[string]JobSpec {
	t.Helper()
	image := validSpec()
	image.WorkspaceTransport = WorkspaceFromImage
	image.ImageWorkspacePath = "/andbo/workspace"
	return map[string]JobSpec{
		string(WorkspaceEmpty):     validSpec(),
		string(WorkspaceFromImage): image,
	}
}

// TestSecurity_JobRunsOnePodPerRun pins the Job to a single pod attempt.
//
// An agent run has side effects that are not confined to the pod — commits,
// pushes, pull requests, and whatever tools the image carries. Kubernetes runs
// `parallelism` pods at once until `completions` of them have succeeded, so
// either field above 1 turns one requested run into several concurrent agents
// racing on the same repository with the same credentials, and the manifest
// still reads as a normal Job. This is the same argument backoffLimit 0 already
// makes about retries, on the axis backoffLimit does not cover.
//
// Both fields are asserted PRESENT as well as equal to 1. Kubernetes defaults
// them to 1, so absence is not itself a weakening — but this package's contract
// is that the manifest fully describes the run (Command is required for the same
// reason), and a reviewer should not have to know an API default to know how
// many agents a manifest starts.
func TestSecurity_JobRunsOnePodPerRun(t *testing.T) {
	for name, spec := range transportSpecs(t) {
		t.Run(name, func(t *testing.T) {
			manifest, err := spec.Render()
			if err != nil {
				t.Fatalf("Render() = %v, want nil", err)
			}
			jobSpec, ok := dig(t, docs(t, manifest)[1], "spec").(map[string]any)
			if !ok {
				t.Fatal("job spec is not a mapping")
			}
			for _, field := range []string{"completions", "parallelism"} {
				v, present := jobSpec[field]
				if !present {
					t.Errorf("spec.%s is not rendered; the manifest must state how many pods the run starts rather than lean on the API default", field)
					continue
				}
				if v != 1 {
					t.Errorf("spec.%s = %v, want 1; above 1 the Job starts concurrent agents that repeat every side effect of the run", field, v)
				}
			}
		})
	}
}

// TestSecurity_EveryContainerRePullsItsImage pins imagePullPolicy Always on
// every container the pod starts.
//
// The kubelet resolves an image reference once and then reuses whatever the
// node already has. Always makes it re-resolve on every start, so a node whose
// image cache holds a stale or tampered layer for that reference cannot supply
// it silently to a run that asked for the current one.
//
// Absence is a real weakening here, unlike the two Job fields above: the kubelet
// defaults an omitted policy to Always ONLY for the `:latest` tag, and to
// IfNotPresent for everything else — including the digest pin this package tells
// callers to use in production. Omitting the field would therefore turn the
// hardest-pinned specs into the cached-image case.
//
// The bound, which EnforcementNotes states and this test does not claim past:
// Always re-resolves the REFERENCE, so it is only an identity guarantee when
// that reference is a digest. A mutable tag re-resolved every start can return
// different bytes every start, and the pull itself is the kubelet's, outside
// anything this manifest controls.
func TestSecurity_EveryContainerRePullsItsImage(t *testing.T) {
	for name, spec := range transportSpecs(t) {
		t.Run(name, func(t *testing.T) {
			manifest, err := spec.Render()
			if err != nil {
				t.Fatalf("Render() = %v, want nil", err)
			}
			for cname, c := range allContainers(t, manifest) {
				v, present := c["imagePullPolicy"]
				if !present {
					t.Errorf("container %q renders no imagePullPolicy; the kubelet then defaults it to IfNotPresent for every reference except the :latest tag, so a digest-pinned run would accept the node's cached image", cname)
					continue
				}
				if v != "Always" {
					t.Errorf("container %q imagePullPolicy = %v, want Always; anything else lets a stale or tampered node-local image serve the run", cname, v)
				}
			}
		})
	}
}

// TestSecurity_ImageTransportStartsBothContainersFromOneReference covers the
// container the pull policy is easiest to forget on. The init container carries
// the workspace, runs in the same pod, and uses the same image reference — so if
// it and the agent could resolve to different bytes, a digest-pinned spec would
// have two images to audit instead of one.
func TestSecurity_ImageTransportStartsBothContainersFromOneReference(t *testing.T) {
	spec := transportSpecs(t)[string(WorkspaceFromImage)]
	manifest, err := spec.Render()
	if err != nil {
		t.Fatalf("Render() = %v, want nil", err)
	}
	containers := allContainers(t, manifest)
	if len(containers) != 2 {
		t.Fatalf("expected the agent and the workspace init container, got %d: %v", len(containers), containers)
	}
	for _, want := range []string{containerName, initContainerName} {
		c, present := containers[want]
		if !present {
			t.Fatalf("container %q is missing from the rendered pod", want)
		}
		if c["image"] != spec.Image {
			t.Errorf("container %q image = %v, want %q; the workspace must not come from a second image", want, c["image"], spec.Image)
		}
	}
}

// TestRunsOnePodWithFreshImages exercises the render-time guard directly, on
// constructed Jobs the public API cannot produce. It is what makes the property
// enforceable rather than merely asserted: a future edit that adds a third
// container, or that makes either Job field caller-supplied, fails the render
// instead of emitting a manifest that starts more agents or an older image.
func TestRunsOnePodWithFreshImages(t *testing.T) {
	ok := func() job {
		return job{Spec: jobSpec{
			Completions: 1,
			Parallelism: 1,
			Template: podTemplate{Spec: podSpec{
				InitContainers: []container{{Name: initContainerName, ImagePullPolicy: "Always"}},
				Containers:     []container{{Name: containerName, ImagePullPolicy: "Always"}},
			}},
		}}
	}

	tests := []struct {
		name    string
		mutate  func(*job)
		wantErr bool
	}{
		{name: "hardened", mutate: func(*job) {}},
		{name: "completions above one", mutate: func(j *job) { j.Spec.Completions = 2 }, wantErr: true},
		{name: "completions zero", mutate: func(j *job) { j.Spec.Completions = 0 }, wantErr: true},
		{name: "parallelism above one", mutate: func(j *job) { j.Spec.Parallelism = 5 }, wantErr: true},
		{name: "parallelism zero", mutate: func(j *job) { j.Spec.Parallelism = 0 }, wantErr: true},
		{
			name:    "agent container caches its image",
			mutate:  func(j *job) { j.Spec.Template.Spec.Containers[0].ImagePullPolicy = "IfNotPresent" },
			wantErr: true,
		},
		{
			name:    "agent container names no policy",
			mutate:  func(j *job) { j.Spec.Template.Spec.Containers[0].ImagePullPolicy = "" },
			wantErr: true,
		},
		{
			name:    "init container caches its image",
			mutate:  func(j *job) { j.Spec.Template.Spec.InitContainers[0].ImagePullPolicy = "Never" },
			wantErr: true,
		},
		{
			name: "a container added later forgets the policy",
			mutate: func(j *job) {
				j.Spec.Template.Spec.Containers = append(j.Spec.Template.Spec.Containers,
					container{Name: "sidecar", ImagePullPolicy: "IfNotPresent"})
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			j := ok()
			tt.mutate(&j)

			err := runsOnePodWithFreshImages(j)
			if tt.wantErr && err == nil {
				t.Fatal("runsOnePodWithFreshImages() = nil, want a refusal")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("runsOnePodWithFreshImages() = %v, want nil", err)
			}
		})
	}
}

// TestSecurity_OnePodAndFreshImageBoundsAreStated keeps the two claims this
// milestone adds from being read wider than they are. Both have a bound that an
// operator has to know, and an untrue reading of either is worse than no claim:
// "one pod" is not at-most-once execution, and re-resolving a mutable tag is not
// image identity.
func TestSecurity_OnePodAndFreshImageBoundsAreStated(t *testing.T) {
	notes := strings.ToLower(strings.Join(validSpec().EnforcementNotes(), "\n"))

	for _, want := range []struct{ topic, substr string }{
		{"the pull policy re-resolves the reference, not the bytes", "imagepullpolicy always re-resolves"},
		{"digest pinning is what makes the reference an identity", "digest"},
		{"parallelism/completions bound the Job's own pods only", "parallelism 1"},
	} {
		if !strings.Contains(notes, want.substr) {
			t.Errorf("enforcement notes do not state %s (looking for %q):\n%s", want.topic, want.substr, notes)
		}
	}
}
