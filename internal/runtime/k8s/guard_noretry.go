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
//     BackoffLimitExceeded. What backoffLimit cannot do is PREVENT the restart,
//     and the reason is causal rather than a race the controller might win:
//     RestartCount only increments once the restart has HAPPENED, and while the
//     kubelet is sitting out its backoff the pod is Running rather than Failed,
//     so neither counting path has anything to read. The controller gets its
//     signal from the second start. By the time it fails the Job and terminates
//     the pod — destroying that pod's logs — the agent has already begun again
//     on the half-written workspace of the first attempt. OnFailure therefore buys the agent
//     one more start, not an unbounded number, and Never buys it none. That
//     bounded difference is still the whole reason an agent run must not use
//     OnFailure: one extra start is enough to commit or push twice. Always is
//     not a laxer setting but an invalid one — the API server rejects it for a
//     Job template — which is also what an omitted restartPolicy becomes, since
//     Always is the POD-level default; a drift there turns into a run that never
//     starts rather than one that runs twice. (A CONTAINER may carry its own
//     restartPolicy — on an init container, where Always is the native-sidecar
//     form, and on a regular container under the newer container-restart rules.
//     That is a different field one level down which this guard does not reach,
//     and it is the worse case rather than a lesser one: the counting above is
//     gated on the POD-level policy, so a container-level restart under Never is
//     never counted at all. See below.)
//
// It reads the CONSTRUCTED Job, not the values that fed it, for the same reason
// as the other guards: a future edit that makes either field caller-supplied
// fails the render rather than quietly emitting a manifest that re-runs an agent
// which has already pushed.
//
// Four bounds, all real:
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
//     A container's own restartPolicy is the same story one level down, and is
//     the only axis here that is genuinely unbounded: pastBackoffLimitOnFailure
//     opens by returning false unless the POD-level policy is OnFailure, so a
//     container the kubelet restarts under pod-level Never is counted by
//     nothing. This guard cannot be fixed to see any of them: it reads named Go
//     struct fields, so a field that is not in the struct cannot be read, and
//     the moment one is added the guard goes on passing. Only a walk over the
//     encoded manifest can say "and nothing else", which is what
//     TestSecurity_NoOtherFieldCanReinstateRetries does. Review demonstrated the
//     hole on the podFailurePolicy axis: adding one failed only the three golden
//     diffs, and regenerating the goldens took the whole repository green on a
//     manifest that re-runs the agent without bound. The container axis was NOT
//     in that state — mustEqual already rejected a container-level Always at any
//     depth, and no regeneration could absorb it. The container check earns its
//     place by asserting the key is ABSENT rather than that its value is Never,
//     which is what covers restartPolicyRules and whatever else lands on a
//     container next.
//   - Both values are pinned in the MANIFEST and only one of them stays pinned
//     in the CLUSTER, which is the bound a reader of this guard is most likely
//     to assume away. restartPolicy rides in spec.template, and EVERY branch of
//     the update path ends in an immutability check on the template — while the
//     Job is suspended the only exemptions are container resources and the
//     scheduling directives, neither of which reaches restartPolicy — so the
//     kubelet's in-place restarts cannot be turned back on at all. backoffLimit
//     appears in none of that path's ValidateImmutableField calls, so an update
//     raises it and the Job controller compares the raised value on its next
//     sync. The one thing that ends the exposure is the run ending: a Job
//     carrying a Complete or Failed condition is skipped before the controller
//     reads the retry budget, so a Job already failed on BackoffLimitExceeded
//     cannot be re-armed. Do not read the reverse into that. A Job the
//     controller has not finished can be made to run the agent again WITHOUT
//     touching backoffLimit, because the controller's own pod deletions are not
//     counted as failures at all — see the third bound on
//     runsOnePodWithFreshImages, which is where that mechanism is written down.
//     This guard runs at render time and can say none of it; EnforcementNotes
//     does, and TestSecurity_NoRetryBoundsAreStated pins the wording.
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
