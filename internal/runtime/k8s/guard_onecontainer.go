package k8s

import (
	"fmt"
	"slices"
)

// runsOnlyTheAgent reports whether the rendered Job starts exactly one agent
// container, and whether the only other container it declares is the one the
// caller's own inputs account for.
//
// The two lists carry different risks and are refused separately.
//
// A second entry in `containers` runs CONCURRENTLY with the agent for the pod's
// whole life, and it takes over the outcome of the run in both directions. In
// the kubelet's getPhase, `case running > 0 && unknown == 0: return
// v1.PodRunning` is evaluated before every terminal case, so a container that
// does not exit holds the pod Running after the agent has exited 0 — the Job
// never completes, spends its entire activeDeadlineSeconds, and ends
// Failed/DeadlineExceeded with the pod and its logs deleted. The other direction
// is the terminal branch: the pod is PodSucceeded only `if stopped ==
// succeeded`, and otherwise under restartPolicy Never it is PodFailed — so a
// second container exiting non-zero fails a run the agent completed, and against
// backoffLimit 0 that fails the Job. Neither outcome names the extra container
// in the Job's status, so both read as the agent having failed. It also shares
// the pod's network namespace, which puts it inside — and equally, in reach of —
// whatever egress the NetworkPolicy leaves open, and any volume it mounts is a
// volume it can rewrite under the agent.
//
// An extra INIT container is not concurrent, but it runs BEFORE the agent on the
// same workspace volume, so it can seed or rewrite the tree the agent then
// commits and pushes, and it spends the same activeDeadlineSeconds.
//
// It takes the SPEC as well as the constructed Job, which no other guard here
// does, and the reason is the point of the check: the number of containers a
// manifest may declare is not a constant, it is whatever the caller's opt-in
// fields account for. Recovering that from the Job alone would be circular —
// counting the init containers and then asserting the count matches itself. The
// transport is the authority, so the transport is what it reads.
//
// Three bounds, all real:
//
//   - It reaches the two container lists podSpec declares, and nothing else. A
//     container in a NEW list — ephemeralContainers, or whatever comes next — is
//     invisible here until that list exists on the struct, and no guard can
//     escape that, because a guard reads fields. This is the same bound
//     runsOnePodWithFreshImages carries and it is load-bearing in the same way.
//     Two things close it: TestSecurity_ThePodStartsOnlyTheAgent counts
//     containers by SHAPE (any mapping that names an image), so it reaches a list
//     this file does not know about as soon as a fixture renders one, and
//     TestSecurity_NoOtherFieldCanAddAContainer closes podSpec's key set so
//     adding the field is a deliberate act rather than a quiet one.
//
//   - The check on the agent is by name AND by argv. The name alone would be
//     nominal — it would pass any container that kept the name and ran something
//     else — and argv is what ties the one container the pod starts to the
//     command the caller actually asked to run. It does NOT verify the image
//     contains that command; nothing in a render-only package can.
//
//   - Unlike the no-retry and one-pod contracts, the apply-time half of this one
//     mostly holds: both container lists ride in spec.template, which every
//     branch of the Job update path refuses to change, so nobody edits a
//     container into a live Job the way they can raise backoffLimit or drop
//     parallelism to 0. What no manifest can hold is the POD. An ephemeral
//     container is added through the pod's own `ephemeralcontainers`
//     subresource, not through the template, so it never meets that immutability
//     at all — and it cannot be removed once added. EnforcementNotes states that
//     bound and TestSecurity_OneContainerBoundsAreStated pins the wording.
//
// Same as bindsPolicyToPod and runsOnePodWithFreshImages: no test can prove this
// was CALLED, since deleting the call site changes no output while render is
// correct. The property itself is pinned on the rendered manifest by
// TestSecurity_ThePodStartsOnlyTheAgent, guard or no guard.
func runsOnlyTheAgent(s JobSpec, j job) error {
	pod := j.Spec.Template.Spec

	if len(pod.Containers) != 1 {
		return fmt.Errorf("internal error: Job %q renders %d containers, and an agent run must be exactly one: every container beside the agent runs for the life of the pod, so one that does not exit holds the run open until activeDeadlineSeconds after the agent has already finished, and one that exits non-zero fails the pod under restartPolicy Never even though the agent succeeded — in both cases the Job reports a failure that says nothing about the extra container; refusing to render it", j.Metadata.Name, len(pod.Containers))
	}
	if agent := pod.Containers[0]; agent.Name != containerName || !slices.Equal(agent.Command, s.Command) {
		return fmt.Errorf("internal error: the only container in Job %q is %q running %v, want %q running the spec's command %v: the manifest would start something other than the agent the caller asked for; refusing to render it", j.Metadata.Name, agent.Name, agent.Command, containerName, s.Command)
	}

	// The workspace transport is the ONLY input that legitimately adds a
	// container, so it is the only thing that can authorise one.
	wantInit := 0
	if s.WorkspaceTransport == WorkspaceFromImage {
		wantInit = 1
	}
	if len(pod.InitContainers) != wantInit {
		return fmt.Errorf("internal error: Job %q renders %d init containers but workspaceTransport %q accounts for %d: an init container runs before the agent on the same workspace volume, so an unaccounted one can rewrite the tree the agent then commits and pushes, and it spends the same activeDeadlineSeconds; refusing to render it", j.Metadata.Name, len(pod.InitContainers), s.WorkspaceTransport, wantInit)
	}
	if wantInit == 1 && pod.InitContainers[0].Name != initContainerName {
		return fmt.Errorf("internal error: Job %q renders init container %q, want %q: workspaceTransport %q accounts for the workspace copy and nothing else; refusing to render it", j.Metadata.Name, pod.InitContainers[0].Name, initContainerName, s.WorkspaceTransport)
	}
	return nil
}
