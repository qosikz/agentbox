package k8s

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
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

// allContainers returns every container the rendered pod would start, keyed by
// name — init containers included, because one runs in the same pod, on the
// same volumes, from the same image reference as the agent, so a pull policy
// that drifted there is a pull policy that drifted for the run.
//
// It finds containers by SHAPE — any mapping that names an image — rather than
// by walking `initContainers` and `containers` by name. Review showed why: the
// by-name version was blind to a container declared in a list that did not
// exist when it was written, and a manifest carrying such a container with no
// pull policy at all passed the whole suite once the goldens were regenerated.
// Nothing else in this manifest carries an `image` key, so the shape is exact.
func allContainers(t *testing.T, manifest string) map[string]map[string]any {
	t.Helper()
	job := docs(t, manifest)[1]
	pod, ok := dig(t, job, "spec", "template", "spec").(map[string]any)
	if !ok {
		t.Fatal("job spec.template.spec is not a mapping")
	}
	// The agent's own list is not optional, and the shape walk below cannot say
	// so: with initContainers rendered and containers absent, it would return a
	// non-empty set that starts no agent.
	if _, present := pod["containers"]; !present {
		t.Fatal("pod renders no containers list; the agent container is not optional")
	}

	out := map[string]map[string]any{}
	walk(job, "Job", func(path string, m map[string]any) {
		if _, isContainer := m["image"]; !isContainer {
			return
		}
		name, ok := m["name"].(string)
		if !ok || name == "" {
			t.Fatalf("%s names an image but has no name", path)
		}
		// Keying by name means a duplicate would silently replace its twin, and
		// walk iterates a Go map, so WHICH one survived would vary run to run —
		// an assertion below could then pass on one CI run and fail on the next
		// against identical input. Review found exactly that: a colliding
		// container carrying a bad restart policy was caught on 6 runs of 10.
		// Fail loudly instead; a pod with two containers of one name is a
		// manifest this package must never emit in any case.
		if _, dup := out[name]; dup {
			t.Fatalf("%s: a second container is named %q; container names must be unique across every list, and this check is keyed by name", path, name)
		}
		out[name] = m
	})
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
// up to `parallelism` pods at once, capped by the number of remaining
// `completions`, until that many have succeeded. So completions above 1 repeats
// the whole run once per completion, parallelism above 1 lets those repeats race
// each other on the same repository with the same credentials, and the manifest
// still reads as a normal Job. This is the argument backoffLimit 0 already makes
// about retries, on the axis backoffLimit does not cover.
//
// Both fields are asserted PRESENT as well as equal to 1, and for completions
// that is not a formality. parallelism defaults to 1 unconditionally, but
// completions defaults to 1 only when parallelism is ALSO unset — and this
// renderer always emits parallelism, so dropping completions alone leaves it
// nil, which makes the Job a work-queue Job that finishes when any single pod
// succeeds rather than a fixed-completion one. For parallelism, absence would
// still break this package's contract that the manifest fully describes the run
// (Command is required for the same reason): a reviewer should not have to know
// an API default to know how many agents a manifest starts.
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
					t.Errorf("spec.%s = %v, want 1; above 1 the Job repeats every side effect of the run once per pod, and 0 means it never runs", field, v)
				}
			}
		})
	}
}

// TestSecurity_EveryContainerRePullsItsImage pins imagePullPolicy Always on
// every container the pod starts.
//
// Under IfNotPresent the kubelet does not contact the registry at all when it
// already holds something for that reference. Always makes it re-resolve at the
// registry on every start, so a node cannot go on serving what it resolved for
// that reference earlier.
//
// Absence is a weakening here in a way it is not for the two Job fields above:
// the API server defaults an omitted policy at pod admission to Always for the
// `:latest` tag or an untagged reference, and to IfNotPresent for every other
// tag AND for a digest — which is exactly the digest pin this package tells
// callers to use in production. Omitting the field would therefore turn the
// hardest-pinned specs into the cached-image case.
//
// The bound, which EnforcementNotes states and this test does not claim past:
// this is a FRESHNESS control, not an integrity one. Once the reference resolves
// to a digest the node already stores, the runtime reuses those layers and
// nothing re-verifies them, so it is no defence against a compromised image
// store. It is an identity guarantee only when the reference is a digest, and
// the pull itself is the kubelet's, outside anything this manifest controls.
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
					t.Errorf("container %q renders no imagePullPolicy; the API server then defaults it at pod admission to IfNotPresent for every reference but the :latest tag and the untagged form, so a digest-pinned run would accept the node's cached image", cname)
					continue
				}
				if v != "Always" {
					t.Errorf("container %q imagePullPolicy = %v, want Always; anything else lets an image the node resolved earlier serve the run", cname, v)
				}
			}
		})
	}
}

// TestSecurity_NoOtherFieldCanStartASecondPod closes the Job spec for the
// one-pod contract, which had no closure of its own at all.
//
// Two closures already walk this same struct, and neither is a substitute,
// because a closure is not a fence — it is a QUESTION put to whoever adds a
// field, and the answer only defends the contract the question names. The
// no-retry closure asks "does this hand back the retries backoffLimit 0
// refuses?"; the wall-clock closure asks "does this end or restart the clock?".
// Nothing asked "does this let a second agent run?".
//
// Measured on the parent commit, and the measurement is the point. Adding
// `CompletionMode string \`yaml:"completionMode,omitempty"\“ to jobSpec failed
// exactly those two closures and no others. Both questions have the same
// correct answer for that field — completionMode reinstates no retry on its own
// (backoffLimitPerIndex and maxFailedIndexes are separate fields, still outside
// both sets) and moves no deadline — so a maintainer who reads both messages and
// answers both HONESTLY adds it to both allowed sets. Doing exactly that took
// the WHOLE REPOSITORY green, with no golden regeneration.
//
// What it would have shipped: completionMode Indexed is the single switch that
// unpins completions on a LIVE Job. validateCompletions returns
// ValidateImmutableField unless the Job is Indexed, so for this manifest
// completions is fixed at apply time — that is the whole reason the one-pod
// property survives, since parallelism is freely mutable and is capped by
// completions rather than the other way round. Under Indexed, completions
// becomes mutable "only in tandem with parallelism", which is not a loophole
// that leaks one pod but the one that delivers CONCURRENT agents on one
// repository with one set of credentials. The rendered manifest still reads
// `completions: 1` and `parallelism: 1` throughout, and
// runsOnePodWithFreshImages still passes: the guard reads the constructed Job,
// and the Job it reads is correct.
//
// Only the Job spec is closed here, and the two levels below it are deliberately
// not re-closed:
//
//   - The pod spec decides nothing about how many pod ATTEMPTS a Job makes; that
//     is Job-level in batch/v1 without exception.
//   - Containers are already closed once, by the no-retry test, and that closure
//     rejects ANY field outside its set rather than any field bearing on
//     restarts — so a container field is stopped whatever it does. The
//     attribution gap that justifies this test at Job level has no counterpart
//     there today: the container half of this contract is imagePullPolicy, and
//     batch/v1 has no second container field that decides what the kubelet
//     resolves. Re-closing it would be a map kept in step with another map to
//     catch a field that does not exist.
func TestSecurity_NoOtherFieldCanStartASecondPod(t *testing.T) {
	// Exactly the fields jobSpec declares, re-decided for the one-pod contract.
	allowedOnJobSpec := map[string]bool{
		"backoffLimit":            true,
		"completions":             true,
		"parallelism":             true,
		"activeDeadlineSeconds":   true,
		"ttlSecondsAfterFinished": true,
		"template":                true,
	}

	for name, spec := range transportSpecs(t) {
		t.Run(name, func(t *testing.T) {
			manifest, err := spec.Render()
			if err != nil {
				t.Fatalf("Render() = %v, want nil", err)
			}
			renderedJobSpec, ok := dig(t, docs(t, manifest)[1], "spec").(map[string]any)
			if !ok {
				t.Fatal("job spec is not a mapping")
			}
			assertClosed(t, "spec", declaredKeys(t, reflect.TypeOf(jobSpec{})), renderedJobSpec, allowedOnJobSpec,
				"the one-pod contract; a Job field outside this set can add pod attempts the rendered completions and parallelism do not describe, or unpin the field that is holding the count down. completionMode is the measured case and the reason this closure exists separately: it reinstates no retry and moves no deadline, so it passes both of the other two closures honestly, and it makes completions MUTABLE ON A LIVE JOB — only in tandem with parallelism, which is concurrent agents on one repository. (spec.scheduling is the same shape in current batch/v1: its gang minCount is bounded by parallelism and is updatable after creation.)")
		})
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
		name   string
		mutate func(*job)
		// substr must appear in the error, so the message names the axis that
		// drifted. This guard fuses two unrelated properties, so an
		// unattributable "internal error" would leave a maintainer unable to
		// tell a pull-policy drift from a pod-count one. Empty means the Job is
		// hardened and must pass.
		substr string
	}{
		{name: "hardened", mutate: func(*job) {}},
		{name: "completions above one", mutate: func(j *job) { j.Spec.Completions = 2 }, substr: "completions=2"},
		{name: "completions zero", mutate: func(j *job) { j.Spec.Completions = 0 }, substr: "completions=0"},
		{name: "parallelism above one", mutate: func(j *job) { j.Spec.Parallelism = 5 }, substr: "parallelism=5"},
		{name: "parallelism zero", mutate: func(j *job) { j.Spec.Parallelism = 0 }, substr: "parallelism=0"},
		{
			name:   "agent container caches its image",
			mutate: func(j *job) { j.Spec.Template.Spec.Containers[0].ImagePullPolicy = "IfNotPresent" },
			substr: `container "agent"`,
		},
		{
			name:   "agent container names no policy",
			mutate: func(j *job) { j.Spec.Template.Spec.Containers[0].ImagePullPolicy = "" },
			substr: "imagePullPolicy",
		},
		{
			name:   "init container caches its image",
			mutate: func(j *job) { j.Spec.Template.Spec.InitContainers[0].ImagePullPolicy = "Never" },
			substr: `container "workspace-init"`,
		},
		{
			name: "a container added later forgets the policy",
			mutate: func(j *job) {
				j.Spec.Template.Spec.Containers = append(j.Spec.Template.Spec.Containers,
					container{Name: "sidecar", ImagePullPolicy: "IfNotPresent"})
			},
			substr: `container "sidecar"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			j := ok()
			tt.mutate(&j)

			err := runsOnePodWithFreshImages(j)
			if tt.substr == "" {
				if err != nil {
					t.Fatalf("runsOnePodWithFreshImages() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("runsOnePodWithFreshImages() = nil, want a refusal")
			}
			if !strings.Contains(err.Error(), tt.substr) {
				t.Errorf("error does not name what drifted (want %q):\n%v", tt.substr, err)
			}
		})
	}
}

// TestSecurity_NeitherControllerNorKubeletRetriesTheAgent pins the two fields
// that together decide whether a failed agent run is repeated.
//
// They are one control on two axes, and each is useless alone:
//
//   - spec.backoffLimit bounds how many replacement PODS the Job controller
//     creates after a pod fails. Absence is a weakening, not a neutral: the API
//     server defaults it to 6, so a dropped field is six more runs.
//   - spec.template.spec.restartPolicy decides whether the KUBELET restarts the
//     failed CONTAINER in place — same pod, same volumes, same workspace. This
//     is NOT a way round backoffLimit: the Job controller counts in-place
//     restarts too, and at backoffLimit 0 the first one fails the Job. It is a
//     matter of ordering, and the ordering is causal: RestartCount increments
//     only once the restart has happened, and during the kubelet's backoff the
//     pod is Running rather than Failed, so the controller has nothing to count
//     until the agent has already started again. Under OnFailure it therefore
//     gets one further start on the half-written workspace of the failed one
//     before the Job is failed and the pod (and its logs) destroyed. One extra start, not an unbounded
//     number — and one is enough for a second commit or push. Absence is worse
//     than a wrong value, because the pod default is Always, which the API
//     server rejects outright for a Job template — the run then never starts.
//
// Both are asserted PRESENT as well as equal, for those two reasons and for the
// same contract reason as completions and parallelism above: a reviewer should
// not have to know an API default to know whether a manifest re-runs an agent
// that has already pushed a commit.
func TestSecurity_NeitherControllerNorKubeletRetriesTheAgent(t *testing.T) {
	for name, spec := range transportSpecs(t) {
		t.Run(name, func(t *testing.T) {
			manifest, err := spec.Render()
			if err != nil {
				t.Fatalf("Render() = %v, want nil", err)
			}
			job := docs(t, manifest)[1]

			jobSpec, ok := dig(t, job, "spec").(map[string]any)
			if !ok {
				t.Fatal("job spec is not a mapping")
			}
			switch v, present := jobSpec["backoffLimit"]; {
			case !present:
				t.Error("spec.backoffLimit is not rendered; the API server then defaults it to 6, so a failed run is retried six more times with every side effect it already committed")
			case v != 0:
				t.Errorf("spec.backoffLimit = %v, want 0; above 0 the Job controller replaces a failed pod and repeats the run's commits, pushes, and tool calls", v)
			}

			pod, ok := dig(t, job, "spec", "template", "spec").(map[string]any)
			if !ok {
				t.Fatal("job spec.template.spec is not a mapping")
			}
			switch v, present := pod["restartPolicy"]; {
			case !present:
				t.Error("restartPolicy is not rendered; the pod default is Always, which the API server rejects for a Job template, so the run would never start")
			case v != "Never":
				t.Errorf("restartPolicy = %v, want Never; under OnFailure the kubelet restarts the agent container in place on the same half-written workspace, and it does so before the Job controller can fail the Job on that restart, so the agent gets one further start", v)
			}
		})
	}
}

// TestSecurity_NoOtherFieldCanReinstateRetries closes the set, because pinning
// two fields to the right value does not make a manifest no-retry: a THIRD
// field can hand the retries back while both pinned values still read correctly.
//
// This is the same defect shape review already found in this package once, one
// level up. The pull-policy guard enumerated the container lists it was written
// against, so a container in a list that did not exist when it was written
// passed the whole suite. retriesNothingAfterFailure enumerates the FIELDS it
// was written against, and the guard cannot do better: it reads named Go struct
// fields, and a field that is not in the struct cannot be read. Only a walk over
// the ENCODED manifest can say "and nothing else", so that check lives here
// rather than in the guard.
//
// The two axes, both real batch/v1 surface and both absent from the Go structs
// today, so this test is about the next edit and not about today's output:
//
//   - Job.spec. podFailurePolicy is the sharp one: `action: Ignore` tells the
//     Job controller NOT to count that failure against the backoff budget and
//     to create a replacement pod anyway, which makes backoffLimit 0 inert
//     while it still renders as 0. It is valid ONLY with restartPolicy Never,
//     so it composes precisely with this manifest — the no-retry contract's own
//     precondition is its precondition. The realistic way in is not malice: the
//     upstream recipe for "do not let a preemption burn the retry budget" is
//     verbatim `action: Ignore` on a DisruptionTarget condition, so a
//     maintainer being KINDER to preempted runs would silently convert "never
//     re-run" into "re-run without bound". managedBy is the other: it hands
//     reconciliation to a controller outside the cluster's Job controller,
//     after which every field asserted here is advisory. backoffLimitPerIndex
//     and maxFailedIndexes need completionMode Indexed, which is equally
//     outside the set.
//
//   - Containers, which are the WORSE half. Pod-level restartPolicy Never is
//     not the whole restart story: a container may carry its OWN restartPolicy
//     — on an init container, where Always is the native-sidecar form, and on a
//     regular container under the newer container-restart rules — and the
//     kubelet then restarts it in place whatever the pod says. Unlike pod-level
//     OnFailure, that restart is not counted anywhere:
//     pastBackoffLimitOnFailure returns early unless the POD-level policy is
//     OnFailure, so under Never it never sums a RestartCount at all, and the
//     pod stays Running rather than Failed so the pod-counting path has nothing
//     either. This is the one axis on which the agent really can be re-run
//     without bound, and it is exactly the axis retriesNothingAfterFailure
//     cannot see.
//
//     Both the restartPolicy key and the whole container key set are closed,
//     for the reason the by-name version of this file keeps re-learning: naming
//     one key catches the field that exists today and nothing else.
//     restartPolicyRules is the near case — it is invalid without a
//     restartPolicy beside it, so every VALID form is already caught by the
//     named check, but a future container field carrying no such dependency
//     would not be.
//
// A legitimate future sidecar would fail this test. That is the intended cost:
// it is a run the kubelet may restart, and it should not become renderable
// without someone re-deciding the contract in this file.
func TestSecurity_NoOtherFieldCanReinstateRetries(t *testing.T) {
	// Exactly the fields jobSpec declares. Adding one here is the deliberate
	// act this test exists to force.
	allowed := map[string]bool{
		"backoffLimit":            true,
		"completions":             true,
		"parallelism":             true,
		"activeDeadlineSeconds":   true,
		"ttlSecondsAfterFinished": true,
		"template":                true,
	}

	for name, spec := range transportSpecs(t) {
		t.Run(name, func(t *testing.T) {
			manifest, err := spec.Render()
			if err != nil {
				t.Fatalf("Render() = %v, want nil", err)
			}

			renderedJobSpec, ok := dig(t, docs(t, manifest)[1], "spec").(map[string]any)
			if !ok {
				t.Fatal("job spec is not a mapping")
			}
			// Struct AND rendered keys. Walking only what rendered left this
			// closure blind to any field added with omitempty that the fixtures
			// leave at zero — proven with managedBy, the very field named below,
			// which took the whole repository green while rendering normally for
			// a caller who set it. See declaredKeys.
			assertClosed(t, "spec", declaredKeys(t, reflect.TypeOf(jobSpec{})), renderedJobSpec, allowed,
				"the no-retry contract; a Job field outside this set can hand back the retries backoffLimit 0 refuses (podFailurePolicy action Ignore does not count a failure against the budget and replaces the pod anyway; managedBy moves reconciliation off the Job controller entirely)")

			// Exactly the fields the container struct declares, closed for the
			// same reason Job.spec is: naming restartPolicy alone would catch
			// the field that exists today and miss the next one.
			allowedOnContainer := map[string]bool{
				"name": true, "image": true, "imagePullPolicy": true,
				"command": true, "args": true, "workingDir": true,
				"env": true, "resources": true, "securityContext": true,
				"volumeMounts": true,
			}

			// The container STRUCT, checked once: a field declared there is
			// reachable by any caller, and an omitempty one renders in no
			// fixture. A container-level lifecycle hook was the demonstrated
			// case.
			assertClosed(t, "container", declaredKeys(t, reflect.TypeOf(container{})), nil, allowedOnContainer,
				"the contract this package states about containers; a container field bears on whether the kubelet may restart it (restartPolicyRules and whatever succeeds it) or on how long it takes to stop (a lifecycle preStop hook runs inside the termination grace period)")

			for cname, c := range allContainers(t, manifest) {
				if v, present := c["restartPolicy"]; present {
					t.Errorf("container %q declares restartPolicy %v; the kubelet honours a container's own restart policy whatever the pod's Never says, and the Job controller does not count such a restart against backoffLimit at all — pastBackoffLimitOnFailure returns early unless the POD-level policy is OnFailure — so this container could be restarted without bound", cname, v)
				}
				for key := range c {
					if key == "restartPolicy" || allowedOnContainer[key] {
						continue
					}
					t.Errorf("container %q renders %q, which is not part of the contract this package states about containers; if it bears on whether the kubelet may restart this container (restartPolicyRules and whatever succeeds it), decide what it does to the no-retry contract, then add it to this set", cname, key)
				}
			}
		})
	}
}

// TestRetriesNothingAfterFailure exercises the render-time guard directly, on
// constructed Jobs the public API cannot produce. It is what makes the property
// enforceable rather than merely asserted: a future edit that makes either field
// caller-supplied fails the render instead of emitting a manifest that runs the
// agent again.
//
// "Dropped" is deliberately not claimed for both fields. BackoffLimit is a
// non-pointer int64, so a field that vanishes from the YAML (an added omitempty)
// still reads as 0 here and passes — this guard cannot tell 0 from absent. Two
// tests do catch it, and both are needed for different reasons: the presence
// branch of TestSecurity_NeitherControllerNorKubeletRetriesTheAgent says why the
// absence is a weakening and covers both transports, while TestRender_JobHardening
// catches it as a side effect of dig fataling on a missing key. It IS claimed for
// RestartPolicy, whose zero value is the empty string refused below.
func TestRetriesNothingAfterFailure(t *testing.T) {
	ok := func() job {
		return job{Spec: jobSpec{
			BackoffLimit: 0,
			Template:     podTemplate{Spec: podSpec{RestartPolicy: "Never"}},
		}}
	}

	tests := []struct {
		name   string
		mutate func(*job)
		// substr must appear in the error. The guard fuses two fields owned by
		// two different controllers, so an unattributable "internal error" would
		// leave a maintainer unable to tell a Job-controller retry from a
		// kubelet one. Empty means the Job is hardened and must pass.
		substr string
	}{
		{name: "hardened", mutate: func(*job) {}},
		{name: "one retry", mutate: func(j *job) { j.Spec.BackoffLimit = 1 }, substr: "backoffLimit=1"},
		{name: "the api default", mutate: func(j *job) { j.Spec.BackoffLimit = 6 }, substr: "backoffLimit=6"},
		{name: "negative retries", mutate: func(j *job) { j.Spec.BackoffLimit = -1 }, substr: "backoffLimit=-1"},
		{
			name:   "kubelet restarts the container in place",
			mutate: func(j *job) { j.Spec.Template.Spec.RestartPolicy = "OnFailure" },
			substr: `restartPolicy "OnFailure"`,
		},
		{
			name:   "kubelet restarts it unconditionally",
			mutate: func(j *job) { j.Spec.Template.Spec.RestartPolicy = "Always" },
			substr: `restartPolicy "Always"`,
		},
		{
			// The empty value must appear in the message, not just the field
			// name: an operator reading "renders restartPolicy" with nothing
			// after it cannot tell a dropped field from a mis-set one.
			name:   "the field was dropped",
			mutate: func(j *job) { j.Spec.Template.Spec.RestartPolicy = "" },
			substr: `restartPolicy ""`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			j := ok()
			tt.mutate(&j)

			err := retriesNothingAfterFailure(j)
			if tt.substr == "" {
				if err != nil {
					t.Fatalf("retriesNothingAfterFailure() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("retriesNothingAfterFailure() = nil, want a refusal")
			}
			if !strings.Contains(err.Error(), tt.substr) {
				t.Errorf("error does not name what drifted (want %q):\n%v", tt.substr, err)
			}
		})
	}
}

// TestSecurity_NoRetryBoundsAreStated keeps the no-retry claim from being read
// as at-most-once execution, and keeps the two fields from being documented as
// if either one carried the property on its own.
//
// It asserts the LIMITING clauses, not the topic words, for the reason spelled
// out on TestSecurity_OnePodAndFreshImageBoundsAreStated: matching on
// "backoffLimit" would pass against a note that says backoffLimit 0 guarantees
// the agent runs at most once, which is the exact overclaim this test exists to
// prevent.
func TestSecurity_NoRetryBoundsAreStated(t *testing.T) {
	notes := strings.ToLower(strings.Join(validSpec().EnforcementNotes(), "\n"))

	for _, want := range []struct{ topic, substr string }{
		{"the two fields are one control", "only correct together"},
		// The bound that keeps the note from overstating its own case. An
		// earlier draft of this note said an in-place restart never consumes
		// backoffLimit and so ran the agent without bound; the Job controller
		// in fact sums container restarts and fails the Job on the first one at
		// backoffLimit 0, and review caught it against the upstream source. The
		// real cost of OnFailure is one extra start, and a note that inflates
		// it is as wrong as one that omits it — an operator who is told
		// "unbounded" and later finds out otherwise stops trusting the rest of
		// this list.
		{"OnFailure costs one extra start, not unbounded retries", "one more start and not an unbounded number"},
		// Understating a bound fails the same way overstating it does. "One
		// extra start" reads as momentary; the pod is deleted GRACEFULLY, so
		// that start gets the full termination grace period — 30s here, since
		// this renderer sets none — which is time to commit and push. And the
		// Job lands in Failed either way with its logs gone, so the operator
		// this note is written for cannot reconstruct what happened from the
		// Job at all.
		{"the extra start gets the full grace period, not an instant", "defaults to 30 seconds"},
		{"a failed Job does not mean the agent did nothing", "cannot tell from the job whether the agent committed or pushed"},
		{"no-retry is not at-most-once execution", "neither field is at-most-once execution"},
		// What the manifest is still worth once it has been applied, which
		// nothing in this note used to say. The two fields are one control on two
		// axes — and they are ALSO one control whose two halves the API server
		// treats oppositely on update, which is the fact an operator reviewing a
		// manifest most needs and the one the symmetry of the note most obscures.
		// Verified verbatim against upstream master: every branch of
		// validatePodTemplateUpdate ends in ValidateImmutableField on the template,
		// while ValidateJobSpecUpdate's ValidateImmutableField calls name selector,
		// completionMode, podFailurePolicy, backoffLimitPerIndex, managedBy and
		// successPolicy — and not backoffLimit.
		//
		// Every clause below FUSES POLARITY TO SUBJECT, and that is a correction.
		// The first draft asserted "only one of the two survives the apply",
		// "refuses to change the template at all" and "raises it on a running job",
		// and review took the whole repository GREEN on a note rewritten to say
		// backoffLimit is the pinned half and restartPolicy the loose one — the
		// exact inversion of upstream. Each of those matched a rewrite that
		// negated it ("nobody RAISES IT ON A RUNNING JOB"), because they pinned the
		// rule without pinning WHICH FIELD it lands on. An anchor that names a
		// field and its direction in one breath cannot survive being swapped.
		{"restartPolicy is the half the API server holds", "restartpolicy cannot be changed on this job at all"},
		{"backoffLimit is the half nothing holds", "backofflimit is the half nothing pins"},
		{"the retry the manifest refuses is one update away", "is one edit away for as long as the agent is alive"},
		// The bound on the exposure, stated as precisely as the exposure itself.
		// Raising backoffLimit is not a way to resurrect a run that already
		// finished: syncJob returns for a Job carrying a Complete or Failed
		// condition before it reads the retry budget. An operator told only the
		// alarming half goes looking for a mitigation that already exists — but
		// the clause is phrased to assert the WINDOW exists, since a reassuring
		// bound on its own is something an overclaiming rewrite keeps gladly.
		{"the window closes only when the run does", "only the job's own end closes that window"},
	} {
		if !strings.Contains(notes, want.substr) {
			t.Errorf("enforcement notes do not state the bound %q (looking for %q):\n%s", want.topic, want.substr, notes)
		}
	}
}

// TestSecurity_OnePodAndFreshImageBoundsAreStated keeps the two claims this
// milestone adds from being read wider than they are. Both have a bound an
// operator has to know, and an untrue reading of either is worse than no claim:
// "one pod" is not at-most-once execution, and Always is a freshness control,
// not an integrity one.
//
// It asserts the LIMITING clauses, not the topic words. Matching on the topic
// ("digest", "parallelism 1") only proves a note about the subject exists —
// review demonstrated that such a test passes when both notes are replaced by
// pure overclaims that happen to use the same vocabulary, which is the failure
// this whole commit exists to remove, one level up.
//
// The rule these clauses follow, and it took two rounds of review to get right:
// FUSE POLARITY TO SUBJECT. An anchor that pins a rule without naming the field
// it lands on ("refuses to change the template at all") survives having the
// fields swapped underneath it, and an anchor phrased as a verb phrase ("raises
// it on a running job") survives being negated in front of ("nobody raises it on
// a running job"). Both were measured: an inverted set of notes claiming
// backoffLimit is the pinned half kept every anchor of the first draft and took
// the whole repository green. Each anchor is now a complete claim that has to be
// DELETED to state the opposite.
//
// The honest bound on all of this: these are substring matches over prose, so
// they resist a careless rewrite, not a deliberate one. A fluent note asserting
// the opposite now fails — verified — but text that quotes an anchor in order to
// contradict it ("X is a myth") still passes, and no substring check can help
// with that. What these assertions defend against is a maintainer softening a
// note without noticing what it was for; they are not a proof of truthfulness.
func TestSecurity_OnePodAndFreshImageBoundsAreStated(t *testing.T) {
	notes := strings.ToLower(strings.Join(validSpec().EnforcementNotes(), "\n"))

	for _, want := range []struct{ topic, substr string }{
		{"Always is freshness, not tamper detection", "freshness control and not tamper detection"},
		{"the cached layers are reused and not re-verified", "nothing re-verifies them"},
		{"only a digest reference makes it an identity guarantee", "identity guarantee only when the reference is a digest"},
		{"Andbo does not vouch for the image it names", "does not sign, verify, or admit"},
		{"one pod scheduled is not one execution", "not a count of how many times the agent runs"},
		// The apply-time half. Only completions is held, and the reason is a
		// three-step chain a reader cannot be expected to reconstruct:
		// validateCompletions makes completions immutable only for a non-Indexed
		// Job, this Job is non-Indexed because the renderer emits no completionMode
		// and SetDefaults_Job stores NonIndexed, and completionMode is itself in
		// the immutable set so the Job cannot be switched later. A note that
		// claimed "completions is immutable" flat would be an overclaim about
		// batch/v1 and would stop being true the moment this renderer emitted a
		// completionMode.
		{"completions is the one field the cluster holds", "completions is the only one of the two the cluster holds"},
		{"and it is held only because the mode cannot change", "it cannot become indexed later"},
		// parallelism, and this is a CORRECTION rather than an addition. The first
		// draft called it "freely mutable and merely INERT" on the strength of
		// manageJob capping wanted pods at completions, and review falsified it
		// against the controller: the reasoning covered only the RAISING
		// direction. Lowering parallelism to 0 is a legal update
		// (validateJobSpec asks only for nonnegative), and manageJob then deletes
		// the running pod through deleteJobPods — which strips the job-tracking
		// finalizer BEFORE issuing the delete, so getValidPodsWithFilter and
		// trackJobStatusAndRemoveFinalizers both skip that pod and no failure is
		// ever counted against backoffLimit 0. The Job stays alive with Active 0,
		// and raising parallelism back starts the agent over. Do it quickly and
		// the replacement is created while the original is still inside its
		// termination grace period, because manageJob subtracts `terminating`
		// from the pod-creation diff only under podReplacementPolicy Failed,
		// which this renderer never emits. So parallelism is the concurrency
		// vector the note used to rule out.
		{"parallelism is not inert", "parallelism is a restart switch and not a no-op"},
		{"lowering it deletes the pod uncounted", "without counting a failure against backofflimit"},
		{"raising it again re-runs the agent", "starts the agent over"},
		{"immutability is of this Job object and no wider", "delete-and-recreate is not an update"},
		// The pull policy's own apply-time status, with the limit fused on. The
		// first draft anchored "same template immutability", which review showed
		// was a pure topic match: the clause it was meant to bound survived being
		// deleted outright. Anchor the LIMIT instead.
		{"the pull policy is held but what it resolves to is not", "what no immutability can hold is what the registry serves"},
	} {
		if !strings.Contains(notes, want.substr) {
			t.Errorf("enforcement notes do not state the bound %q (looking for %q):\n%s", want.topic, want.substr, notes)
		}
	}
}

// TestSecurity_WallClockCapIsDocumentedAndTrue ties MaxActiveDeadlineSeconds to
// the figure operators are given for it.
//
// Every bounds assertion in this package is keyed to the constant, which is
// right — a test that restated 86400 would just be a second copy to keep in
// step. The cost is that the constant is then unfalsifiable from inside the
// package: raising it to 30 days leaves the whole k8s suite green, because every
// check moves with it. Measured on the parent commit, exactly one test in the
// repository objects, and it lives in the CLI package.
//
// The README is where that is not true. It tells operators the cap in MINUTES,
// as a flat number, and nothing connects that number to the constant — so the
// figure someone plans a run against can silently stop being the figure the
// renderer enforces. This is the shape
// TestSecurity_ReservedNamespaceBoundIsDocumentedAndTrue already establishes:
// read the claim where it is written rather than restating it.
//
// Failing when the sentence is REWORDED is deliberate, not brittleness. The
// number is the claim; a rewrite that moves it is a rewrite that has to be read
// against the constant, and a test that quietly skipped when its anchor moved
// would defend nothing.
func TestSecurity_WallClockCapIsDocumentedAndTrue(t *testing.T) {
	// Both figures the README puts in front of an operator, and the constant
	// each one is a rendering of. The DEFAULT belongs here at least as much as
	// the cap, and review is what established which way round: the cap is the
	// number a run is REFUSED at, while the default is the number a run actually
	// GETS whenever budget.max_runtime_minutes is 0. Measured, changing
	// DefaultActiveDeadlineSeconds objected only in the three goldens and in one
	// CLI test asserting the literal string "activeDeadlineSeconds: 1800" — a
	// second hardcoded copy rather than a tie to the constant — so nothing
	// connected the documented figure to the emitted one.
	tests := []struct {
		what        string
		open, close string
		// unit converts the constant into the unit the README quotes it in.
		unit  int64
		value int64
	}{
		{
			what:  "the cap a run is refused at",
			open:  "`budget.max_runtime_minutes` above the ",
			close: "-minute cap",
			unit:  60,
			value: MaxActiveDeadlineSeconds,
		},
		{
			what:  "the default a run gets when no budget is set",
			open:  "`budget.max_runtime_minutes`, or ",
			close: "s when that is `0`",
			unit:  1,
			value: DefaultActiveDeadlineSeconds,
		},
	}

	src, err := os.ReadFile(filepath.Join("..", "..", "..", "README.md"))
	if err != nil {
		t.Fatalf("read README.md: %v", err)
	}
	// Flatten first so the check does not depend on where the prose wraps.
	flat := strings.Join(strings.Fields(string(src)), " ")

	for _, tt := range tests {
		t.Run(tt.what, func(t *testing.T) {
			start := strings.Index(flat, tt.open)
			if start < 0 {
				t.Fatalf("README no longer states %s in the form %q...%q; that figure is what operators size a run against, so it must be re-tied to the constant rather than left unchecked", tt.what, tt.open, tt.close)
			}
			rest := flat[start+len(tt.open):]
			end := strings.Index(rest, tt.close)
			if end < 0 {
				t.Fatalf("README states %q but not %q after it; cannot read %s", tt.open, tt.close, tt.what)
			}

			documented, err := strconv.Atoi(rest[:end])
			if err != nil {
				t.Fatalf("README documents %s as %q, which is not a number: %v", tt.what, rest[:end], err)
			}
			if want := tt.value / tt.unit; int64(documented) != want {
				t.Errorf("README tells operators %s is %d, but the constant is %d seconds (%d in the README's unit). One of them moved without the other, and the one operators read is the wrong one", tt.what, documented, tt.value, want)
			}
		})
	}
}

// TestSecurity_ReadmeSaysTheImmutableListIsPartial keeps a partial list from
// being read as the list.
//
// Both documents name the same six fields, and only one of them carried the
// qualifier. EnforcementNotes says the six are "the immutable SPEC FIELDS, not
// everything the update path checks" — pinned below by
// TestSecurity_WallClockBoundsAreStated — while the README's parallel sentence
// said "the update validation's immutability checks name" them and stopped. That
// is not a smaller version of the same claim, it is a different one: a
// README-only reader concludes the pod template is editable on a live Job, which
// is the opposite of what the paragraph three above it tells them about
// `restartPolicy`. Verified against upstream: `ValidateJobSpecUpdate` calls
// `ValidateImmutableField` on exactly those six, and separately calls
// `validatePodTemplateUpdate`, `validateCompletions` and
// `validateJobSchedulingUpdate`.
//
// The named HELPERS are what this asserts, not the shape of the sentence. A
// rewrite is free to say it differently; what it may not do is drop the fact
// that the six are one of several checks, and a helper name is the smallest
// thing that cannot survive that drop. The six-name list is the anchor rather
// than an assertion, so this fails loudly if the passage moves instead of
// quietly passing on a README that no longer contains it.
func TestSecurity_ReadmeSaysTheImmutableListIsPartial(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "..", "README.md"))
	if err != nil {
		t.Fatalf("read README.md: %v", err)
	}
	flat := strings.Join(strings.Fields(string(src)), " ")

	const anchor = "`selector`, `completionMode`, `podFailurePolicy`, `backoffLimitPerIndex`, `managedBy` and `successPolicy`"
	start := strings.Index(flat, anchor)
	if start < 0 {
		t.Fatalf("README no longer lists the six immutable Job spec fields; that list is what an operator reads to decide which parts of a reviewed manifest survive being applied, so it must be re-tied to its qualifier rather than left unchecked")
	}

	// The passage, not the whole file: a qualifier somewhere else in the README
	// does not correct this sentence for the person reading this sentence.
	rest := flat[start:]
	if len(rest) > 700 {
		rest = rest[:700]
	}
	for _, want := range []struct{ topic, substr string }{
		{"the list is the immutable spec fields and not the whole update path", "not* everything the update path holds"},
		{"the pod template is pinned by a separate call", "validatePodTemplateUpdate"},
		{"completions is pinned by a separate call", "validateCompletions"},
	} {
		if !strings.Contains(rest, want.substr) {
			t.Errorf("the README passage listing the six immutable spec fields does not state %s (looking for %q). Without it the six read as the whole of what the update path holds, and the pod template — which holds restartPolicy and imagePullPolicy — reads as editable on a live Job:\n%s", want.topic, want.substr, rest)
		}
	}
}

// TestSecurity_WallClockBoundsAreStated keeps the bounded-run claim from being
// read as "the agent stops at the deadline", which is the reading an operator
// will take from the field name alone and the one that decides whether they
// trust a Job's outcome.
//
// It asserts the LIMITING clauses, not the topic words, for the reason spelled
// out on TestSecurity_OnePodAndFreshImageBoundsAreStated: a note that merely
// mentions activeDeadlineSeconds passes a keyword match while promising a hard
// stop. Each clause below is one an overclaiming rewrite could not keep.
//
// The phrasing is deliberately not shared with the no-retry note. Both end at
// the same place — a Failed Job whose pod and logs are gone — but asserting the
// no-retry note's wording here would pass even if this note dropped the point
// entirely, since the notes are matched as one joined string.
func TestSecurity_WallClockBoundsAreStated(t *testing.T) {
	notes := strings.ToLower(strings.Join(validSpec().EnforcementNotes(), "\n"))

	for _, want := range []struct{ topic, substr string }{
		{"the deadline starts termination rather than ending the run", "not when the agent stops"},
		// The size of the overrun, which is what makes it actionable: an
		// operator who reads "terminated at 1800s" plans differently from one
		// who reads "SIGTERM at 1800s, SIGKILL 30 seconds later".
		{"the grace period is added to every budget", "up to the pod's terminationgraceperiodseconds"},
		// Both halves, because the note previously stated only the reassuring
		// one. A grace period is a CEILING — an agent that handles SIGTERM exits
		// at once — and the push that does not fit in what is left is not
		// "unfinished" but killed part-way, which is the outcome an operator
		// most needs to be told about.
		{"the grace period is a ceiling, not a duration", "ceiling and not a duration"},
		{"a push that overruns is killed part-way", "sigkilled mid-flight"},
		{"a Job that hit its deadline is not a Job that did nothing", "is not evidence that the agent did nothing"},
		// This clause has now been wrong in BOTH directions, and the second time
		// is more instructive than the first. It began as "a suspend/resume cycle
		// hands the run a fresh full budget each time"; review corrected that to
		// "a suspend does not pause an Andbo run, it kills it", reasoning that
		// the suspend deletes the pod, isPodFailed counts a deleted pod as
		// failed, and 1 > 0 finishes the Job against backoffLimit 0. That
		// reasoning named a real exemption (podReplacementPolicy Failed) but the
		// WRONG one, and re-review caught it against the controller: the operative
		// exemption is that deleteJobPods strips the job-tracking finalizer BEFORE
		// issuing the delete, and both counting paths skip a finalizer-less pod.
		// Upstream's real distinction is not which policy is set — it is WHO
		// deleted the pod. A controller-initiated deletion (suspend, or scaling
		// parallelism down) is uncounted; an externally-initiated one keeps its
		// finalizer and IS counted.
		//
		// So the ORIGINAL claim was closer to true than its correction. A suspend
		// neither pauses nor kills: the Job takes a JobSuspended condition, stays
		// unfinished, and resume unconditionally resets status.startTime to now —
		// which hands the run a fresh full budget AND starts the agent over. The
		// lesson pinned here is that a confident mechanism is not evidence: two
		// successive notes described the controller accurately and drew opposite
		// wrong conclusions from it.
		{"suspend and resume is a budget reset, not a pause and not a kill", "hands the run a fresh full budget and starts the agent over"},
		{"the deletion a suspend causes is not counted as a failure", "the controller strips that finalizer before it deletes"},
		{"the immutable list is spec fields, not the whole update path", "not everything the update path checks"},
		// The suspend/resume route was documented first and is the more
		// interesting one, which is exactly why it must not stand alone: it
		// reads as though defeating the budget takes a trick. It does not.
		// activeDeadlineSeconds is absent from the immutable list in
		// ValidateJobSpecUpdate, so the same permission simply raises the
		// number, and a note that describes only the elaborate route
		// understates who can extend a run to anyone reading it as a threat
		// model.
		{"the rendered budget is not fixed once applied", "mutable on a live job"},
		{"nothing local enforces it", "nothing in andbo supervises a pod"},
	} {
		if !strings.Contains(notes, want.substr) {
			t.Errorf("enforcement notes do not state the bound %q (looking for %q):\n%s", want.topic, want.substr, notes)
		}
	}
}

// declaredKeys returns the YAML key of every field a manifest struct declares,
// whether or not any fixture causes it to be rendered.
//
// This exists because the key-set closures in this file walk the RENDERED
// manifest, and review demonstrated what that misses. A field added with
// `omitempty` and left at its zero value by the fixtures renders nothing, so no
// walk over the output can see it — while it renders perfectly well for a caller
// who sets it. Measured on the merged contract: adding
// `ManagedBy string \`yaml:"managedBy,omitempty"\“ to jobSpec, wired from a new
// JobSpec field, took the WHOLE REPOSITORY green with no golden regeneration,
// and a caller setting it rendered `managedBy:` beside a correct
// `activeDeadlineSeconds:` — which switches the Job to an external controller and
// so disables the deadline AND backoffLimit together. That is the exact field
// both closures name in their own failure messages as the thing they exist to
// catch.
//
// The crack was already written down and not recognised: the pod-spec closure
// notes that three of its keys "render only when set" and that the check is
// therefore one-directional. That tolerance for known optional fields is also a
// hole for unknown ones — validSpec sets neither serviceAccountName nor
// runtimeClassName, so the closure had in fact never seen two of the keys in its
// own allow-list.
//
// Reading the STRUCT closes it at the source: a field that exists can be seen
// here whatever any fixture does with it. The rendered-key walks stay, because
// the two catch different things — this one cannot see a key that appears
// without a struct field behind it (a nested map, or a renamed tag), and that
// walk cannot see a field that did not render.
func declaredKeys(t *testing.T, typ reflect.Type) []string {
	t.Helper()
	var out []string
	for i := range typ.NumField() {
		f := typ.Field(i)
		tag, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		switch tag {
		case "-":
			continue
		case "":
			// yaml.v3 lowercases the field name when no tag is given.
			tag = strings.ToLower(f.Name)
		}
		out = append(out, tag)
	}
	return out
}

// assertClosed checks three directions: every key the struct DECLARES and every
// key the manifest RENDERED must be in the allowed set, and every key the
// allowed set names must actually be declared. contract names the property
// being defended, so a maintainer who adds a field is told which decision they
// are being asked to make.
//
// The third direction is the one that is easy to leave out, and review proved
// the cost. With only the first two, a key can be PRE-AUTHORISED: a test-only
// diff that adds "completionMode" to the allowed sets changes no manifest byte,
// renders nothing, touches no production code, and takes the whole repository
// green — while silently disarming every closure for a field that lands in a
// later commit. The same asymmetry lets an entry go stale when a field is
// removed, permanently disarming the check for whatever reclaims that name. An
// allowed set is a record of decisions ABOUT FIELDS THAT EXIST, so a name in it
// with no field behind it is either a decision made too early or one that
// outlived its subject.
func assertClosed(t *testing.T, path string, declared []string, rendered map[string]any, allowed map[string]bool, contract string) {
	t.Helper()
	seen := map[string]bool{}
	for _, k := range declared {
		seen[k] = true
		if !allowed[k] {
			t.Errorf("%s.%s is declared by the manifest struct and is not part of %s. It renders for any caller that sets it, whether or not the fixtures here do — which is precisely how this check was blind before. %s", path, k, contract, closureAdvice)
		}
	}
	for k := range rendered {
		if !allowed[k] && !seen[k] {
			t.Errorf("%s.%s is rendered without a struct field of that name behind it and is not part of %s. %s", path, k, contract, closureAdvice)
		}
	}
	for k := range allowed {
		if _, isRendered := rendered[k]; !seen[k] && !isRendered {
			t.Errorf("%s.%s is allowed by %s but no manifest struct field and no rendered key carries that name. Either it was authorised before it existed — which disarms this closure for whatever lands under that name next — or it outlived the field it was decided about. Remove it, or add the field the decision was made for", path, k, contract)
		}
	}
}

const closureAdvice = "Decide what it does to the contract, then add it to this set."

// deadlineSpecs returns transportSpecs with a distinctive budget, so an
// assertion about the rendered deadline cannot be satisfied by a constant. The
// value is inside the caps and is not the default, not the max, and not a round
// number any other field in this package uses.
func deadlineSpecs(t *testing.T) map[string]JobSpec {
	t.Helper()
	out := transportSpecs(t)
	for name, s := range out {
		s.ActiveDeadlineSeconds = 4321
		out[name] = s
	}
	return out
}

// TestSecurity_TheRunHasABoundedWallClock pins spec.activeDeadlineSeconds as a
// value with a FORM, a RANGE, and a source — not merely as a key that is
// present.
//
// Nothing local supervises a rendered run. This package never contacts a
// cluster, so there is no second timer anywhere in Andbo: the field asserted
// here is the whole of the wall-clock bound, and whatever it fails to say, no
// other part of this codebase says instead.
//
//   - PRESENCE. Absence is the sharpest failure and the one a golden diff
//     reports most quietly. activeDeadlineSeconds is a *int64 that the API
//     server does not default, so a field that vanishes is not a longer run but
//     an UNBOUNDED one: the Job controller has nothing to compare a start time
//     against and never terminates the agent. An omitempty added to a pointer,
//     or a spec field that stops being copied, both land here.
//   - FORM. A wall clock has exactly one rendered shape, and the two ways out
//     of it fail in opposite directions. A quoted `"4321"` is not an integer,
//     and the API server rejects the whole Job — the run never starts. A `null`
//     is accepted and means no deadline at all — the run never stops. Neither
//     is visible in a manifest that still lists the key, which is why the type
//     is asserted rather than the key.
//   - RANGE. Below 1 and above the cap are both refused; see
//     TestBoundsTheRunsWallClock for what each end means. Unlike the other
//     three, this one is DOMINATED here and cannot fail on its own — review
//     established it and the honest thing is to say so rather than present four
//     independent properties. In range, SOURCE below is stricter and fails
//     first; out of range, Render never reaches the encoding at all, because it
//     validates before it builds. It is kept as the assertion that survives
//     SOURCE being relaxed, not as one that earns its keep today, and the range
//     itself is enforced twice over — by Validate on the input and by
//     boundsTheRunsWallClock on the constructed Job.
//   - SOURCE. The rendered value must be the value that was VALIDATED. A
//     renderer that emitted a constant would pass a presence-and-range check
//     while describing a run nobody asked for — the manifest would state a
//     budget the operator did not choose, and the operator has no other place
//     to read it from. deadlineSpecs supplies a value that is not the default
//     precisely so a constant cannot pass.
func TestSecurity_TheRunHasABoundedWallClock(t *testing.T) {
	for name, spec := range deadlineSpecs(t) {
		t.Run(name, func(t *testing.T) {
			manifest, err := spec.Render()
			if err != nil {
				t.Fatalf("Render() = %v, want nil", err)
			}

			jobSpec, ok := dig(t, docs(t, manifest)[1], "spec").(map[string]any)
			if !ok {
				t.Fatal("job spec is not a mapping")
			}

			raw, present := jobSpec["activeDeadlineSeconds"]
			if !present {
				t.Fatal("spec.activeDeadlineSeconds is not rendered; the API server defaults no deadline, so the Job controller never terminates the run and the agent occupies the cluster until someone notices — nothing in Andbo supervises a pod")
			}
			got, ok := raw.(int)
			if !ok {
				t.Fatalf("spec.activeDeadlineSeconds = %#v (%T), want an integer scalar; a quoted string is rejected by the API server so the run never starts, and a null means no deadline at all so it never stops", raw, raw)
			}
			if got < 1 || got > MaxActiveDeadlineSeconds {
				t.Errorf("spec.activeDeadlineSeconds = %d, want within 1..%d; outside that range the manifest either fails the run at once or lets it hold the cluster longer than this package is willing to render", got, MaxActiveDeadlineSeconds)
			}
			if int64(got) != spec.ActiveDeadlineSeconds {
				t.Errorf("spec.activeDeadlineSeconds = %d, want %d (the validated budget); a rendered constant would state a budget the caller never chose, and the manifest is the only place the run's bound is written down", got, spec.ActiveDeadlineSeconds)
			}
		})
	}
}

// TestSecurity_NoOtherFieldCanExtendTheRunsWallClock closes the set, because a
// correct activeDeadlineSeconds does not make a manifest wall-clock bounded: a
// second field can extend, reset, or unhook the clock while the deadline itself
// still renders exactly as asserted above.
//
// This is the third time this package has met the same defect shape, and the
// level moves down each time: the pull-policy guard enumerated container LISTS
// and missed a new one; retriesNothingAfterFailure enumerates Job FIELDS and
// cannot see one that is not in the struct. Both closures live at Job.spec and
// on containers. The POD SPEC between them — Job.spec.template.spec — is closed
// by nothing today, and it is exactly where the wall clock leaks.
//
// The two levels, and what each does to the clock:
//
//   - POD SPEC. terminationGracePeriodSeconds is the one that ADDS time. When
//     the deadline is exceeded the Job controller deletes the pods, and it
//     passes no grace-period override, so the kubelet allows the pod's own
//     grace period between SIGTERM and SIGKILL. This renderer sets no such
//     field, which is why the agent gets the 30-second default rather than an
//     arbitrary one — a value here is added to every budget this package
//     renders, and the field looks like ordinary politeness towards a
//     shutting-down process. A pod-level activeDeadlineSeconds is the near
//     miss: it is a real PodSpec field the kubelet enforces on its own, so a
//     reviewer reading one at this level would reasonably assume it is the
//     Job's — it is not, and a larger value there does not raise the Job's
//     bound while a smaller one silently lowers it.
//
//   - JOB SPEC. The set is the same six the no-retry closure names, and it is
//     restated rather than shared on purpose: adding a Job field must force a
//     decision about EACH contract it can break, and the two contracts fail on
//     different fields. suspend STOPS and RESETS this one — pastActiveDeadline
//     returns false outright while a Job is suspended, and resuming sets
//     status.startTime to the resume moment, which is where the deadline is
//     measured from, so a suspend/resume cycle hands the agent a fresh full
//     budget as many times as it is repeated. managedBy makes it ADVISORY:
//     syncJob returns early for a Job the built-in controller does not own, and
//     nothing else here enforces a deadline. Neither is anything the no-retry
//     closure would fail on for the reason that matters here.
//
// A legitimate future grace period, or a deliberate pod-level deadline, fails
// this test. That is the intended cost: both change what "bounded" means for
// every rendered run, and neither should become renderable without someone
// re-deciding the contract in this file.
func TestSecurity_NoOtherFieldCanExtendTheRunsWallClock(t *testing.T) {
	// Exactly the fields jobSpec declares, re-decided for the wall clock.
	allowedOnJobSpec := map[string]bool{
		"backoffLimit":            true,
		"completions":             true,
		"parallelism":             true,
		"activeDeadlineSeconds":   true,
		"ttlSecondsAfterFinished": true,
		"template":                true,
	}

	// Exactly the fields podSpec declares. Three of them render only when set
	// (serviceAccountName, runtimeClassName, initContainers); this check is
	// one-directional, so their absence is not a failure here.
	allowedOnPodSpec := map[string]bool{
		"restartPolicy": true, "automountServiceAccountToken": true,
		"enableServiceLinks": true, "dnsPolicy": true, "dnsConfig": true,
		"hostNetwork": true, "hostPID": true, "hostIPC": true,
		"serviceAccountName": true, "runtimeClassName": true,
		"securityContext": true, "initContainers": true,
		"containers": true, "volumes": true,
	}

	for name, spec := range deadlineSpecs(t) {
		t.Run(name, func(t *testing.T) {
			manifest, err := spec.Render()
			if err != nil {
				t.Fatalf("Render() = %v, want nil", err)
			}
			job := docs(t, manifest)[1]

			renderedJobSpec, ok := dig(t, job, "spec").(map[string]any)
			if !ok {
				t.Fatal("job spec is not a mapping")
			}
			assertClosed(t, "spec", declaredKeys(t, reflect.TypeOf(jobSpec{})), renderedJobSpec, allowedOnJobSpec,
				"the wall-clock contract; a Job field outside this set can end or restart the clock activeDeadlineSeconds starts (suspending a running Job deletes its pod, which against backoffLimit 0 finishes the Job outright) or take enforcement away from the Job controller entirely (managedBy)")

			pod, ok := dig(t, job, "spec", "template", "spec").(map[string]any)
			if !ok {
				t.Fatal("job spec.template.spec is not a mapping")
			}
			assertClosed(t, "spec.template.spec", declaredKeys(t, reflect.TypeOf(podSpec{})), pod, allowedOnPodSpec,
				"the wall-clock contract; a pod field outside this set is added to every budget this package renders (terminationGracePeriodSeconds is the ceiling on the time between the SIGTERM the deadline triggers and the SIGKILL that follows it) or bounds something other than what it appears to (a pod-level activeDeadlineSeconds is the kubelet's, not the Job's)")
		})
	}
}

// TestBoundsTheRunsWallClock exercises the render-time guard directly, on
// constructed Jobs the public API cannot produce. It is what makes the property
// enforceable rather than merely asserted: a future edit that stops copying the
// validated budget, or that introduces a caller path around Validate, fails the
// render instead of emitting a manifest with no usable bound.
//
// The two ends of the range fail differently and the messages must not be
// interchangeable:
//
//   - 0 is INSIDE what the API server accepts for a Job (batch validation asks
//     only that the value be nonnegative, unlike the pod-level field of the same
//     name, which must be positive) and outside what this contract allows. It
//     does not mean "no deadline" — that is what absence means — it means the
//     Job is past its deadline the first time the controller evaluates it, so
//     the run is failed rather than performed. A budget that cannot be spent is
//     not a bound.
//   - Above the cap the manifest is still valid Kubernetes; what it describes is
//     a run holding a cluster for longer than this package is willing to render
//     with nothing local watching it.
//
// "Dropped" is not claimed. ActiveDeadlineSeconds is a non-pointer int64, so a
// field that vanishes from the YAML still reads as 0 here and is refused for the
// wrong reason. Rendered ABSENCE is caught by
// TestSecurity_TheRunHasABoundedWallClock, which is the test that can tell the
// two apart.
func TestBoundsTheRunsWallClock(t *testing.T) {
	tests := []struct {
		name     string
		deadline int64
		// Every substr must appear in the error, and TWO things have to be
		// pinned, not one. Naming the value alone does not discriminate the
		// branches: both messages interpolate it with %d, so
		// "activeDeadlineSeconds=0" appears in whichever message the guard
		// returns. Review demonstrated the cost — replacing the low branch's
		// return with the over-cap message verbatim took the whole repository
		// green, leaving the guard to tell a maintainer that a budget of 0
		// "exceeds the 86400-second cap" and to advise lowering it, which is the
		// opposite of the fix for the case this guard's doc comment exists to
		// explain as the subtle one. Each case therefore carries a clause unique
		// to its branch as well. Empty means the Job is bounded and must pass.
		substrs []string
	}{
		{name: "the default budget", deadline: DefaultActiveDeadlineSeconds},
		{name: "one second", deadline: 1},
		{name: "the cap itself", deadline: MaxActiveDeadlineSeconds},
		{
			name:     "zero is not the absence of a deadline",
			deadline: 0,
			substrs:  []string{"activeDeadlineSeconds=0", "a deadline already spent"},
		},
		{
			name:     "negative",
			deadline: -1,
			substrs:  []string{"activeDeadlineSeconds=-1", "a deadline already spent"},
		},
		{
			name:     "one past the cap",
			deadline: MaxActiveDeadlineSeconds + 1,
			substrs:  []string{"activeDeadlineSeconds=86401", "exceeds the"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			j := job{
				Metadata: objectMeta{Name: "fix-tests"},
				Spec:     jobSpec{ActiveDeadlineSeconds: tt.deadline},
			}

			err := boundsTheRunsWallClock(j)
			if len(tt.substrs) == 0 {
				if err != nil {
					t.Fatalf("boundsTheRunsWallClock() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("boundsTheRunsWallClock() = nil, want a refusal")
			}
			for _, want := range tt.substrs {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error does not say which end of the range was hit (want %q):\n%v", want, err)
				}
			}
		})
	}
}
