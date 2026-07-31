package k8s

import "fmt"

// deniesEveryDirection reports whether the rendered NetworkPolicy would actually
// deny traffic, in both directions, to the pod it selects.
//
// bindsPolicyToPod is the other half and deliberately not this one: it checks
// WHERE the policy lands and WHAT it selects. A policy can pass it with a
// perfect namespace and a perfect selector and still deny nothing, which is the
// same end state — an agent with unrestricted egress under a document named
// `-deny-all` — reached by the one axis that guard does not read.
//
// policyTypes is that axis, and it is the whole of it, because this package
// renders no rule fields (see the networkPolicy type). Two ways it goes wrong,
// both silent:
//
//   - A LIST THAT NAMES ONE DIRECTION. A NetworkPolicy restricts only the
//     directions it names. `policyTypes: [Ingress]` on a policy with no rules is
//     a valid, applyable object that isolates the pod from inbound traffic and
//     leaves its egress exactly as it was.
//
//   - AN EMPTY LIST, WHICH IS NOT NEUTRAL. It is not a policy that denies
//     nothing by omission, and it is not rejected either: the API server
//     DEFAULTS it. SetDefaults_NetworkPolicy fills `len(obj.Spec.PolicyTypes) ==
//     0` with ["Ingress"], and appends "Egress" only `if len(obj.Spec.Egress) !=
//     0` — so on a rule-less deny-all, the field this package renders precisely
//     to close egress is completed by the cluster into the one value that opens
//     it. This is the same shape as the imagePullPolicy defaulting
//     runsOnePodWithFreshImages refuses, and it fails the same way: nothing in
//     the manifest looks wrong, because the wrong value is not in the manifest.
//
// It reads the CONSTRUCTED policy rather than the constants that fed it, so an
// edit anywhere between building the document and encoding it still has to leave
// it denying both ways.
//
// That is also what makes it more than a second opinion on what the tests
// already check, and the difference was measured rather than assumed. The tests
// below sample SHAPES; this runs for every CALLER. A downgrade gated on a value
// no fixture uses — `if s.RuntimeClassName == "kata" { return
// []string{"Ingress"} }`, where the fixtures reach only "gvisor" and "" — passes
// the entire repository green, goldens included, because nothing renders that
// caller. The guard refuses it, so the manifest is never emitted. No fixture set
// closes that in general, which is the same sampling bound containerLists states
// for the one-container contract.
//
// Bounds, all real:
//
//   - It checks that the policy DENIES, not that the cluster enforces the
//     denial. A CNI that does not implement NetworkPolicy, and any other policy
//     object in the namespace or above it that grants egress, are both outside a
//     renderer's reach and both stated in EnforcementNotes.
//
//   - It does not check the selector, on either axis bindsPolicyToPod already
//     owns or the one neither owns: an EMPTY matchLabels would select every pod
//     in the namespace, which is drift but drift in the safe direction — a wider
//     deny, not a narrower one — and refusing it here would be this guard
//     answering a question about binding.
//
//   - It is a manifest-time check on a mutable object, and this is the bound
//     worth reading before trusting it an hour into a run. A NetworkPolicy has
//     NO IMMUTABLE SPEC FIELDS: ValidateNetworkPolicyUpdate re-runs
//     ValidateNetworkPolicySpec and pins nothing in it, so every value checked
//     here can be edited on the live policy.
//
//     The word "spec" is load-bearing and an earlier version of this comment
//     dropped it, saying a NetworkPolicy had no immutable fields at all. That is
//     false, and review caught it: the update path also calls
//     ValidateObjectMetaUpdate, whose ValidateObjectMetaAccessorUpdate runs
//     ValidateImmutableField on name, namespace, uid, creationTimestamp,
//     deletionTimestamp and deletionGracePeriodSeconds. Those six are what make
//     an edited policy keep the identity it was reviewed under — so the wider
//     claim was not just wrong, it contradicted the argument it was supporting.
//     EnforcementNotes states the corrected bound and
//     TestSecurity_DenyAllBoundsAreStated pins the wording.
//
//   - Same as the other guards in this package: no test can prove this was
//     CALLED, since deleting the call site changes no output while render is
//     correct. The property itself is pinned on the rendered manifest by
//     TestSecurity_TheNetworkPolicyDeniesEveryDirection, and the document's
//     field set is closed by TestSecurity_NoOtherFieldCanPunchAHoleInTheDenyAll,
//     guard or no guard.
func deniesEveryDirection(np networkPolicy) error {
	if len(np.Spec.PolicyTypes) == 0 {
		return fmt.Errorf("internal error: NetworkPolicy %q names no policyTypes, and an empty list is not neutral: the API server defaults it to [Ingress] alone for a policy carrying no egress rules, so this would apply cleanly, still read as a default-deny, and leave the agent with unrestricted egress; refusing to render it", np.Metadata.Name)
	}

	// Length is checked BEFORE the counts, and the order is what keeps the
	// messages below honest. Upstream refuses `len(spec.PolicyTypes) >
	// len(allowed)` where allowed is {Ingress, Egress} — so THREE entries are
	// rejected and TWO are not, and there is no uniqueness check anywhere in
	// that validation. An earlier version of this guard attached "the API server
	// refuses more than two policyTypes and would reject the manifest outright"
	// to every repeat, which is false for exactly the two-entry repeats that are
	// accepted, and review caught it. Getting the length out of the way first
	// means the count checks are reasoning about a list of at most two.
	if len(np.Spec.PolicyTypes) > 2 {
		return fmt.Errorf("internal error: NetworkPolicy %q names %d policyTypes %v, and a NetworkPolicy has only two directions: the API server refuses a list longer than two outright, so this manifest would not apply and the run it describes could not start; refusing to render it", np.Metadata.Name, len(np.Spec.PolicyTypes), np.Spec.PolicyTypes)
	}

	var ingress, egress int
	for _, pt := range np.Spec.PolicyTypes {
		switch pt {
		case "Ingress":
			ingress++
		case "Egress":
			egress++
		default:
			return fmt.Errorf("internal error: NetworkPolicy %q names policyType %q, and the only directions a NetworkPolicy has are Ingress and Egress; the API server would reject the manifest, so the run this spec describes could not start; refusing to render it", np.Metadata.Name, pt)
		}
	}
	// At most two entries by now, so failing this means one direction is missing
	// — either omitted, or crowded out by a repeat of the other. Both reach the
	// cluster: the API server accepts a two-entry list without deduplicating it.
	if egress != 1 {
		return fmt.Errorf("internal error: NetworkPolicy %q names %q %d times in policyTypes %v, want exactly once: a NetworkPolicy restricts only the directions it names, and a list of at most two that does not name Egress once either omits it — leaving the agent's egress unrestricted — or repeats it and so leaves no room for Ingress, which the API server accepts without deduplicating and which leaves inbound traffic to the agent unrestricted instead; refusing to render it", np.Metadata.Name, "Egress", egress, np.Spec.PolicyTypes)
	}
	// Only reachable as ingress == 0: Egress is already known to appear exactly
	// once and the list holds at most two, so a repeated Ingress would have made
	// the egress count 0 and returned above. No repeat clause belongs here.
	if ingress != 1 {
		return fmt.Errorf("internal error: NetworkPolicy %q does not name %q in policyTypes %v: a NetworkPolicy restricts only the directions it names, so anything that can route to the pod reaches the agent; refusing to render it", np.Metadata.Name, "Ingress", np.Spec.PolicyTypes)
	}
	return nil
}
