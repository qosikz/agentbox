package k8s

import "fmt"

// allContainers returns every container this pod would start. A container list
// added to podSpec must be added here too, or the guard below stops seeing it —
// which is a thing that can be forgotten, so it is not the only line of defence
// (see runsOnePodWithFreshImages).
func (p podSpec) allContainers() []container {
	return append(append([]container{}, p.InitContainers...), p.Containers...)
}

// runsOnePodWithFreshImages reports whether the rendered Job would start exactly
// one pod attempt, and whether every container it starts re-resolves its image.
//
// Two properties, one guard: both are constants render sets inline, both are
// invisible in a manifest that otherwise reads as correct, and neither is caught
// by Validate — JobSpec has no field for either.
//
//   - completions above 1 runs the agent that many times over, parallelism above
//     1 lets those repeats race, and every side effect outside the pod (commit,
//     push, PR, tool call) then happens once per pod on the same repository with
//     the same credentials. The two are not interchangeable: a fixed-completion
//     Job never runs more pods than it has completions left, so parallelism
//     above 1 with completions 1 adds no pod — still refused, because a manifest
//     has to mean what it says. Below 1 is the same drift inverted: parallelism
//     0 leaves the Job paused until someone raises it, completions 0 marks it
//     Complete at once, and either way the agent never runs.
//   - imagePullPolicy anything but Always lets the kubelet serve whatever its
//     node already holds for that reference. Empty is not neutral: the API
//     SERVER defaults an omitted policy at pod admission — Always for :latest or
//     an untagged reference, IfNotPresent for every other tag AND for a digest,
//     which is every digest-pinned spec this package tells callers to prefer.
//     (No kubelet default exists; an empty policy reaching one just does not
//     pull.)
//
// It reads the CONSTRUCTED Job, not the values that fed it, so a container
// appended to either list without a pull policy fails the render rather than
// quietly running an older image.
//
// Three bounds, all real:
//
//   - It reaches the container lists podSpec.allContainers names, and nothing
//     else. A container declared in a NEW list — ephemeralContainers, or
//     whatever comes next — is invisible here until that list is added there.
//     Review demonstrated this exactly: a container in a new list, carrying no
//     pull policy at all, passed the whole suite. The structural check in
//     assertHardened is the backstop, because it matches any mapping that names
//     an image rather than any list this file happens to know about.
//   - The same apply-time bound the no-retry guard carries, with the answer
//     coming out the other way, and it decides which half of this check is worth
//     anything an hour into a run. completions is immutable on a live Job — but
//     by a chain rather than by the field being special: the update path lets it
//     move only for an Indexed Job, this Job is non-Indexed because no
//     completionMode is emitted and the API server defaults an absent one to
//     NonIndexed, and completionMode is itself immutable, so it cannot be
//     switched afterwards. parallelism is freely mutable and merely INERT, since
//     the controller wants completions-minus-successes pods and caps that by
//     parallelism rather than the reverse. So the pod count is held by the
//     completions half alone, and imagePullPolicy is held because it rides in
//     the immutable pod template. A field that broke that chain would pass this
//     guard while rendering correctly, which is why the Job-level key set is
//     closed for THIS contract by TestSecurity_NoOtherFieldCanStartASecondPod
//     rather than left to the closures the other two contracts own —
//     completionMode passes both of those honestly. EnforcementNotes states the
//     bound and TestSecurity_OnePodAndFreshImageBoundsAreStated pins the wording.
//   - Same as bindsPolicyToPod: no test can prove this was CALLED, since
//     deleting the call site changes no output while render is correct. The
//     properties themselves are pinned on the rendered manifest by
//     TestSecurity_JobRunsOnePodPerRun and
//     TestSecurity_EveryContainerRePullsItsImage.
func runsOnePodWithFreshImages(j job) error {
	if j.Spec.Completions != 1 || j.Spec.Parallelism != 1 {
		return fmt.Errorf("internal error: Job %q renders completions=%d parallelism=%d, and an agent run must be exactly one pod: completions above 1 runs the agent that many times over — concurrently, up to parallelism — repeating the run's commits, pushes, and tool calls per pod, while 0 on either field means it never runs at all; refusing to render it", j.Metadata.Name, j.Spec.Completions, j.Spec.Parallelism)
	}
	for _, c := range j.Spec.Template.Spec.allContainers() {
		if c.ImagePullPolicy != "Always" {
			return fmt.Errorf("internal error: container %q in Job %q renders imagePullPolicy %q, want \"Always\": the kubelet would then serve whatever image its node has already cached for %q — and an empty policy is not neutral, since the API server defaults one at pod admission to IfNotPresent for every reference but the :latest tag and the untagged form; refusing to render a run that could start from an image the node resolved earlier", c.Name, j.Metadata.Name, c.ImagePullPolicy, c.Image)
		}
	}
	return nil
}
