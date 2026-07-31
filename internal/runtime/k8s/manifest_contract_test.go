package k8s

import (
	"os"
	"path/filepath"
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

			jobSpec, ok := dig(t, docs(t, manifest)[1], "spec").(map[string]any)
			if !ok {
				t.Fatal("job spec is not a mapping")
			}
			for field := range jobSpec {
				if !allowed[field] {
					t.Errorf("spec.%s is rendered and is not part of the no-retry contract; a Job field outside this set can hand back the retries backoffLimit 0 refuses (podFailurePolicy action Ignore does not count a failure against the budget and replaces the pod anyway; managedBy moves reconciliation off the Job controller entirely). Decide what it does to the contract, then add it to this set", field)
				}
			}

			// Exactly the fields the container struct declares, closed for the
			// same reason Job.spec is: naming restartPolicy alone would catch
			// the field that exists today and miss the next one.
			allowedOnContainer := map[string]bool{
				"name": true, "image": true, "imagePullPolicy": true,
				"command": true, "args": true, "workingDir": true,
				"env": true, "resources": true, "securityContext": true,
				"volumeMounts": true,
			}

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
// this whole commit exists to remove, one level up. A clause that states the
// limit cannot survive being rewritten into a guarantee.
func TestSecurity_OnePodAndFreshImageBoundsAreStated(t *testing.T) {
	notes := strings.ToLower(strings.Join(validSpec().EnforcementNotes(), "\n"))

	for _, want := range []struct{ topic, substr string }{
		{"Always is freshness, not tamper detection", "freshness control and not tamper detection"},
		{"the cached layers are reused and not re-verified", "nothing re-verifies them"},
		{"only a digest reference makes it an identity guarantee", "identity guarantee only when the reference is a digest"},
		{"Andbo does not vouch for the image it names", "does not sign, verify, or admit"},
		{"one pod scheduled is not one execution", "not a count of how many times the agent runs"},
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
	const (
		anchorOpen  = "`budget.max_runtime_minutes` above the "
		anchorClose = "-minute cap"
	)
	src, err := os.ReadFile(filepath.Join("..", "..", "..", "README.md"))
	if err != nil {
		t.Fatalf("read README.md: %v", err)
	}
	// Flatten first so the check does not depend on where the prose wraps.
	flat := strings.Join(strings.Fields(string(src)), " ")

	start := strings.Index(flat, anchorOpen)
	if start < 0 {
		t.Fatalf("README no longer states the budget.max_runtime_minutes cap in the form %q...%q; that figure is what operators size a run against, so it must be re-tied to MaxActiveDeadlineSeconds rather than left unchecked", anchorOpen, anchorClose)
	}
	rest := flat[start+len(anchorOpen):]
	end := strings.Index(rest, anchorClose)
	if end < 0 {
		t.Fatalf("README states %q but not %q after it; cannot read the documented cap", anchorOpen, anchorClose)
	}

	documented, err := strconv.Atoi(rest[:end])
	if err != nil {
		t.Fatalf("README documents the cap as %q, which is not a number: %v", rest[:end], err)
	}
	if want := MaxActiveDeadlineSeconds / 60; documented != want {
		t.Errorf("README tells operators the cap is %d minutes, but MaxActiveDeadlineSeconds is %d seconds (%d minutes). One of them moved without the other, and the one operators read is the wrong one", documented, MaxActiveDeadlineSeconds, want)
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
		{"the grace period is added to every budget", "goes on running for the pod's terminationgraceperiodseconds"},
		{"a Job that hit its deadline is not a Job that did nothing", "is not evidence that the agent did nothing"},
		{"the budget is per active period, not per Job", "hands the run a fresh full budget each time"},
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
//     TestBoundsTheRunsWallClock for what each end means.
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

			jobSpec, ok := dig(t, job, "spec").(map[string]any)
			if !ok {
				t.Fatal("job spec is not a mapping")
			}
			for field := range jobSpec {
				if !allowedOnJobSpec[field] {
					t.Errorf("spec.%s is rendered and is not part of the wall-clock contract; a Job field outside this set can reset the clock activeDeadlineSeconds starts (suspend resets status.startTime on resume, and the deadline is measured from it) or take enforcement away from the Job controller entirely (managedBy). Decide what it does to the run's bound, then add it to this set", field)
				}
			}

			pod, ok := dig(t, job, "spec", "template", "spec").(map[string]any)
			if !ok {
				t.Fatal("job spec.template.spec is not a mapping")
			}
			for field := range pod {
				if !allowedOnPodSpec[field] {
					t.Errorf("spec.template.spec.%s is rendered and is not part of the wall-clock contract; a pod field outside this set is added to every budget this package renders (terminationGracePeriodSeconds is the whole time between the SIGTERM the deadline triggers and the SIGKILL that follows it) or bounds something other than what it appears to (a pod-level activeDeadlineSeconds is the kubelet's, not the Job's). Decide what it does to the run's bound, then add it to this set", field)
				}
			}
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
		// substr must appear in the error, so a maintainer can tell which end of
		// the range was hit. Empty means the Job is bounded and must pass.
		substr string
	}{
		{name: "the default budget", deadline: DefaultActiveDeadlineSeconds},
		{name: "one second", deadline: 1},
		{name: "the cap itself", deadline: MaxActiveDeadlineSeconds},
		{name: "zero is not the absence of a deadline", deadline: 0, substr: "activeDeadlineSeconds=0"},
		{name: "negative", deadline: -1, substr: "activeDeadlineSeconds=-1"},
		{name: "one past the cap", deadline: MaxActiveDeadlineSeconds + 1, substr: "exceeds the"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			j := job{
				Metadata: objectMeta{Name: "fix-tests"},
				Spec:     jobSpec{ActiveDeadlineSeconds: tt.deadline},
			}

			err := boundsTheRunsWallClock(j)
			if tt.substr == "" {
				if err != nil {
					t.Fatalf("boundsTheRunsWallClock() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("boundsTheRunsWallClock() = nil, want a refusal")
			}
			if !strings.Contains(err.Error(), tt.substr) {
				t.Errorf("error does not say which end of the range was hit (want %q):\n%v", tt.substr, err)
			}
		})
	}
}
