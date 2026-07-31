package k8s

import "fmt"

// retriesNothingAfterFailure reports whether the rendered Job would run the
// agent again after it fails.
//
// TWO controllers can do that, they are configured by two different fields at
// two different levels of the manifest, and pinning either one alone leaves the
// other free:
//
//   - The JOB controller creates a replacement POD, up to backoffLimit times.
//     Each replacement re-runs the agent from the start, repeating every side
//     effect the previous attempt had already committed outside the pod — the
//     commit, the push, the pull request, the tool call. Absence is not neutral:
//     the API server defaults an omitted backoffLimit to 6.
//   - The KUBELET restarts the failed CONTAINER in place under restartPolicy
//     OnFailure — same pod, same volumes, same half-written workspace. The two
//     interact the wrong way round: an in-place container restart is not a POD
//     failure, so the Job controller never counts it and backoffLimit is never
//     consumed. OnFailure alongside backoffLimit 0 therefore restarts the agent
//     indefinitely while the Job reads as no-retries. Never is the only value
//     that holds. Always is not a laxer setting but an invalid one — the API
//     server rejects it for a Job template — which is also what an omitted
//     restartPolicy becomes, since Always is the pod default; a drift there
//     turns into a run that never starts rather than one that runs twice.
//
// It reads the CONSTRUCTED Job, not the values that fed it, for the same reason
// as the other guards: a future edit that makes either field caller-supplied
// fails the render rather than quietly emitting a manifest that re-runs an agent
// which has already pushed.
//
// Two bounds, both real:
//
//   - This is not at-most-once EXECUTION, and no field in this manifest can be.
//     It refuses the retries Andbo would ask for; node failure, preemption, and
//     pod deletion still start the same run a second time, and nothing stops the
//     same manifest being applied twice. EnforcementNotes states this, and the
//     no-retry claim must not be read past it.
//   - Same as bindsPolicyToPod and runsOnePodWithFreshImages: no test can prove
//     this was CALLED, since deleting the call site changes no output while
//     render is correct. The values themselves are pinned on the rendered
//     manifest by TestSecurity_NeitherControllerNorKubeletRetriesTheAgent.
func retriesNothingAfterFailure(j job) error {
	if j.Spec.BackoffLimit != 0 {
		return fmt.Errorf("internal error: Job %q renders backoffLimit=%d, want 0: the Job controller would then replace a failed pod and start the agent over, repeating the commits, pushes, and tool calls the failed attempt had already made outside the pod; refusing to render it", j.Metadata.Name, j.Spec.BackoffLimit)
	}
	if p := j.Spec.Template.Spec.RestartPolicy; p != "Never" {
		return fmt.Errorf("internal error: Job %q renders restartPolicy %q, want \"Never\": the kubelet would then restart the agent container in place, in the same pod and on the same half-written workspace — and an in-place restart is not a pod failure, so backoffLimit 0 never counts it and cannot stop it (an empty policy is the same case, since the pod default is Always); refusing to render a run the kubelet could repeat", j.Metadata.Name, p)
	}
	return nil
}
