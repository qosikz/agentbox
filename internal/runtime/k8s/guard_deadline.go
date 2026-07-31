package k8s

import "fmt"

// boundsTheRunsWallClock reports whether the rendered Job carries a wall-clock
// budget that can actually end the run.
//
// This is the one field that decides when an unattended agent stops. Andbo
// renders manifests and never contacts a cluster, so there is no local
// supervisor, no second timer, and no process anywhere in this codebase that
// watches a pod: whatever spec.activeDeadlineSeconds says is the entire bound on
// how long a run may hold cluster capacity. Validate already checks the value
// the CALLER supplied; this reads the CONSTRUCTED Job for the same reason the
// other guards do — an edit that stops copying the validated budget into the
// manifest, or a future caller path that reaches Render around Validate, fails
// the render rather than emitting a Job nothing will terminate.
//
// The range is 1..MaxActiveDeadlineSeconds, and the two ends are refused for
// opposite reasons:
//
//   - 0 is the subtle one, because it is INSIDE what Kubernetes accepts. Batch
//     validation asks only that a Job's activeDeadlineSeconds be NONNEGATIVE
//     (ValidateNonnegativeField), so 0 is admitted — while the POD-level field
//     of the same name must be positive, which means the identical value is
//     valid at one level and rejected one level down. It does not mean "no
//     deadline" either; that is what ABSENCE means. pastActiveDeadline compares
//     elapsed time against the budget with `>=`, so a zero budget is already
//     exceeded the first time the Job controller evaluates it: the Job is failed
//     on that sync rather than after any useful work. A budget that cannot be
//     spent is not a bound, and a manifest asking for one is drift rather than
//     an intent.
//   - Above the cap the manifest is perfectly valid Kubernetes. What it
//     describes is a run holding cluster capacity for longer than this package
//     is willing to be responsible for with nothing watching it, which is a
//     decision this repository makes rather than one the API server makes.
//
// Negative values are refused by the same branch as 0 and are also rejected by
// the API server, so they cost a run that never starts rather than one that
// never stops. Refusing them here is what makes the message name the field.
//
// Four bounds, all real:
//
//   - It reaches Spec.ActiveDeadlineSeconds and nothing else. Fields that extend
//     or reset the same clock are out of reach by ABSENCE FROM THE STRUCTS
//     rather than by any check here: a pod-level terminationGracePeriodSeconds
//     is ADDED to every budget, since the Job controller deletes the pods
//     gracefully when the deadline is hit; spec.suspend STOPS and RESETS the
//     clock, because pastActiveDeadline returns false outright while a Job is
//     suspended and resuming sets status.startTime to the resume moment, which
//     is where the deadline is measured from; spec.managedBy hands
//     reconciliation to a controller outside the cluster's own, and syncJob then
//     returns early for that Job, after which nothing enforces this field at
//     all. This guard cannot be fixed to see any of them — it reads named Go
//     struct fields, so a field that is not in the struct cannot be read, and
//     the moment one is added the guard goes on passing. Only a walk over the
//     encoded manifest can say "and nothing else", which is what
//     TestSecurity_NoOtherFieldCanExtendTheRunsWallClock does at both levels.
//   - It cannot see ABSENCE. ActiveDeadlineSeconds is a non-pointer int64, so a
//     field dropped from the YAML still reads as 0 here — refused, but for the
//     wrong reason, and a pointer field made omitempty would render nothing and
//     pass. Rendered presence and form are asserted on the encoded manifest by
//     TestSecurity_TheRunHasABoundedWallClock, which is where that distinction
//     can be made at all.
//   - A deadline is not a kill. It is the moment the Job controller BEGINS
//     terminating the run: deleteActivePods calls DeletePod, whose interface
//     takes no DeleteOptions at all, so the deletion cannot carry a grace-period
//     override and the agent keeps running for the pod's termination grace
//     period afterwards. Whatever it does in that window — a commit, a push — is
//     already done. EnforcementNotes states this, and the bounded-run claim must
//     not be read past it.
//   - Same as the other guards: no test can prove this was CALLED, since
//     deleting the call site changes no output while render is correct. The
//     value itself is pinned on the rendered manifest by
//     TestSecurity_TheRunHasABoundedWallClock.
func boundsTheRunsWallClock(j job) error {
	switch d := j.Spec.ActiveDeadlineSeconds; {
	case d <= 0:
		return fmt.Errorf("internal error: Job %q renders activeDeadlineSeconds=%d, want 1..%d: a zero budget is not the absence of a deadline but a deadline already spent — the Job controller compares elapsed time against it with >=, so the run is past its deadline the first time the deadline is evaluated and the Job is failed rather than performed — and a negative one is rejected outright by the API server, so the run never starts; refusing to render a manifest whose stated budget cannot be spent", j.Metadata.Name, d, MaxActiveDeadlineSeconds)
	case d > MaxActiveDeadlineSeconds:
		return fmt.Errorf("internal error: Job %q renders activeDeadlineSeconds=%d, which exceeds the %d-second cap this package renders: nothing in Andbo supervises a pod, so a budget past the cap is a run holding cluster capacity for a day or more with no local timer to end it; lower budget.max_runtime_minutes, or raise MaxActiveDeadlineSeconds deliberately", j.Metadata.Name, d, MaxActiveDeadlineSeconds)
	}
	return nil
}
