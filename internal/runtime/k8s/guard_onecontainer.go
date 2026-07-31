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
// v1.PodRunning` is reached before any of the terminal cases in that switch, so
// a container that does not exit holds the pod Running after the agent has
// exited 0 — the Job never completes, spends its entire activeDeadlineSeconds,
// and ends Failed/DeadlineExceeded with the pod and its logs deleted. (There is
// one terminal return AHEAD of the switch, for failed init containers; it cannot
// apply here, since an agent that exited 0 had its init containers succeed.) The
// other direction is the terminal branch: the pod is PodSucceeded only `if
// stopped == succeeded`, and otherwise under restartPolicy Never it is PodFailed
// — so a second container exiting non-zero fails a run the agent completed, and
// against backoffLimit 0 that fails the Job. Neither outcome names the extra
// container in the Job's status, so both read as the agent having failed. It
// also shares the pod's network namespace, which puts it inside — and equally,
// in reach of — whatever egress the NetworkPolicy leaves open, and any volume it
// mounts is a volume it can rewrite under the agent.
//
// An extra INIT container is not concurrent — with one caveat this package can
// state because it controls the field: an init container carrying restartPolicy
// Always is a NATIVE SIDECAR, which the kubelet counts among the running
// containers for the life of the pod, so it would be concurrent after all. The
// container struct declares no restartPolicy, and two closed key sets have to be
// widened before it could (TestSecurity_NoOtherFieldCanAddAContainer for this
// contract, TestSecurity_NoOtherFieldCanReinstateRetries for the no-retry one).
// Absent that, an init container runs BEFORE the agent on the same workspace
// volume, so it can seed or rewrite the tree the agent then commits and pushes,
// and it spends the same activeDeadlineSeconds.
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
//   - Both containers are checked by name AND by argv, and the second half is
//     what stops a check on the slot from being a check on the nameplate. Review
//     caught this file authorising the init slot and then reading only its name,
//     which let a shell-form init container through — the same nominal check the
//     paragraph above rules out for the agent. For the agent, argv means Command
//     AND Args: the rendered argv is the two concatenated, and comparing only
//     Command left an appended flag invisible. Neither check verifies the IMAGE
//     contains what argv names; nothing in a render-only package can.
//
//   - Unlike the no-retry and one-pod contracts, the apply-time half of this one
//     holds — but not because the pod template is frozen, and the flat version of
//     that claim was an overclaim review caught. validatePodTemplateUpdate has
//     three branches and the third exempts container RESOURCES while the Job is
//     suspended; a separate exemption lifts the scheduling directives, INCLUDING
//     the template's labels, which is what this manifest's NetworkPolicy binds
//     to. What holds container-list MEMBERSHIP is narrower and stronger:
//     validatePodResourceUpdatesOnly copies the new resources across only `if
//     len(newPod.Containers) == len(oldPodCopy.Containers)` and only where the
//     names line up, then compares the WHOLE pod spec for equality — so a list
//     that grew, shrank, or was renamed fails in all three branches. On a live
//     pod, core validation admits only `spec.containers[*].image` and
//     `spec.initContainers[*].image`.
//
//     What no manifest can hold is the POD. An ephemeral container is added
//     through the pod's own `ephemeralcontainers` subresource, not through the
//     template, so it never meets that immutability at all — and it cannot be
//     removed once added. EnforcementNotes states that bound and
//     TestSecurity_OneContainerBoundsAreStated pins the wording.
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
	// Command AND Args: the rendered argv is the two concatenated, so comparing
	// only the first leaves everything after argv[0] caller-invisible — an
	// appended flag cost exactly one golden regeneration before review caught it.
	if agent := pod.Containers[0]; agent.Name != containerName || !slices.Equal(agent.Command, s.Command) || !slices.Equal(agent.Args, s.Args) {
		return fmt.Errorf("internal error: the only container in Job %q is %q running %v %v, want %q running the spec's %v %v: the manifest would start something other than the agent the caller asked for; refusing to render it", j.Metadata.Name, agent.Name, agent.Command, agent.Args, containerName, s.Command, s.Args)
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
	if wantInit == 0 {
		return nil
	}

	// The slot the transport authorises is checked for WHAT SITS IN IT, not just
	// for its nameplate. Review found this half doing exactly the nominal check
	// the doc above calls out for the agent: an init container keeping the right
	// name while running a shell passed the entire suite, and needed no golden
	// regeneration to do it, because no golden fixture pairs the image transport
	// with an opt-in field.
	init := pod.InitContainers[0]
	if init.Name != initContainerName {
		return fmt.Errorf("internal error: Job %q renders init container %q, want %q: workspaceTransport %q accounts for the workspace copy and nothing else; refusing to render it", j.Metadata.Name, init.Name, initContainerName, s.WorkspaceTransport)
	}
	if !slices.Equal(init.Command, []string{"cp"}) {
		return fmt.Errorf("internal error: the workspace init container in Job %q runs %v, want the exec form [cp]: a shell would make the workspace path executable rather than an operand, so a directory name carrying ';' or '$(...)' would become a command in the pod holding the agent's volumes; refusing to render it", j.Metadata.Name, init.Command)
	}
	// "--" is part of the contract, not decoration: it is what keeps a path from
	// being read as a cp flag if the validator is ever loosened. No preserve flag
	// is equally load-bearing (see initContainers).
	if wantArgs := []string{"-R", "--", s.ImageWorkspacePath + "/.", s.WorkingDir}; !slices.Equal(init.Args, wantArgs) {
		return fmt.Errorf("internal error: the workspace init container in Job %q copies %v, want %v: the copy has to land in the agent's working directory, or the agent starts on an empty tree while the manifest still reads as a workspace transport; refusing to render it", j.Metadata.Name, init.Args, wantArgs)
	}
	return nil
}
