package k8s

import "fmt"

// retriesNothingAfterFailure reports whether the rendered Job would run the
// agent again after it fails.
//
// Two controllers do that in the manifest as it stands, they are configured by
// two different fields at two different levels, and pinning either one alone
// leaves the other free. This is a statement about the fields that EXIST here,
// not a claim that batch/v1 has only two ways to re-run a pod — see the reach
// bound below:
//
//   - The JOB controller creates a replacement POD, up to backoffLimit times.
//     Each replacement re-runs the agent from the start, repeating every side
//     effect the previous attempt had already committed outside the pod — the
//     commit, the push, the pull request, the tool call. Absence is not neutral:
//     the API server defaults an omitted backoffLimit to 6.
//   - The KUBELET restarts the failed CONTAINER in place under restartPolicy
//     OnFailure — same pod, same volumes, same half-written workspace. Be
//     careful about WHY this one matters, because the obvious reason is wrong:
//     backoffLimit is not bypassed by an in-place restart. The Job controller
//     counts those restarts explicitly (pastBackoffLimitOnFailure sums
//     RestartCount across the containers of pending and running pods, and with
//     backoffLimit 0 returns true on the first one), then fails the Job with
//     BackoffLimitExceeded. What backoffLimit cannot do is PREVENT the restart:
//     the kubelet acts at once, the controller reacts afterwards off its watch,
//     so the agent has already begun a second time on the half-written
//     workspace of the first when the Job is failed and its pod terminated —
//     which also destroys that pod's logs. OnFailure therefore buys the agent
//     one more start, not an unbounded number, and Never buys it none. That
//     bounded difference is still the whole reason an agent run must not use
//     OnFailure: one extra start is enough to commit or push twice. Always is
//     not a laxer setting but an invalid one — the API server rejects it for a
//     Job template — which is also what an omitted restartPolicy becomes, since
//     Always is the POD-level default; a drift there turns into a run that never
//     starts rather than one that runs twice. (An INIT container may carry its
//     own restartPolicy, where Always means a native sidecar. That is a
//     different field, one level down, which this guard does not reach — see
//     below.)
//
// It reads the CONSTRUCTED Job, not the values that fed it, for the same reason
// as the other guards: a future edit that makes either field caller-supplied
// fails the render rather than quietly emitting a manifest that re-runs an agent
// which has already pushed.
//
// Three bounds, all real:
//
//   - It reaches Spec.BackoffLimit and Spec.Template.Spec.RestartPolicy, and
//     nothing else. Other batch/v1 fields defeat the same property and are out
//     of reach here by ABSENCE FROM THE STRUCT rather than by any check: a
//     spec.podFailurePolicy rule with `action: Ignore` tells the Job controller
//     not to count that failure against the backoff budget AND to create a
//     replacement pod, which makes backoffLimit 0 inert while it still renders
//     as 0 — and it is valid only alongside restartPolicy Never, so it composes
//     exactly with this manifest. spec.managedBy moves reconciliation off the
//     Job controller entirely, after which every value pinned here is advisory.
//     A container's OWN restartPolicy is the same story one level down. This
//     guard cannot be fixed to see any of them: it reads named Go struct
//     fields, so a field that is not in the struct cannot be read, and the
//     moment one is added the guard goes on passing. Only a walk over the
//     encoded manifest can say "and nothing else", which is what
//     TestSecurity_NoOtherFieldCanReinstateRetries does. Review found this
//     exactly, on both axes: adding podFailurePolicy failed only the three
//     golden diffs, and regenerating the goldens took the whole repository
//     green on a manifest that re-runs the agent without bound.
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
		return fmt.Errorf("internal error: Job %q renders restartPolicy %q, want \"Never\": under OnFailure the kubelet restarts the agent container in place, in the same pod and on the same half-written workspace, and it does so before the Job controller can act — backoffLimit 0 does count that restart and fails the Job on it, but only after the agent has already started a second time and made whatever commit or push it makes early (an empty policy is the same case, since the pod default is Always); refusing to render a run the kubelet could start twice", j.Metadata.Name, p)
	}
	return nil
}
