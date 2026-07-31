package k8s

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

// The deny-all itself was the one contract in this package with neither a render
// guard nor a closure, and the gap was demonstrated rather than inferred.
//
// bindsPolicyToPod is the only NetworkPolicy guard Render calls, and it checks
// WHERE the policy lands and WHAT it selects — never what it DENIES. Called with
// a policy whose namespace and selector are perfect and whose policyTypes is
// nil, [], [Ingress], or [Egress], it returns nil in all four cases.
//
// The rendered VALUE was pinned, and that half must not be overstated: dropping
// "Egress" from the literal in Render fails three goldens and
// TestRender_NetworkPolicyDeniesBothDirections. What was not pinned is the
// SHAPE of the document. Adding an egress rule field to networkPolicySpec with
// `omitempty`, left unset, renders no byte and takes the entire repository green
// — the same latent authorisation that TestSecurity_NoOtherFieldCanAddAContainer
// and its siblings close on the Job side, with no equivalent here. The next
// commit to set that field punches a hole in the one control this package exists
// to render.

// denyAllSpecs returns the shapes to assert the deny-all on.
//
// It borrows containerLists's specs for their FIXTURE VALUES rather than for
// their container expectations: that helper is where this package keeps its
// hard-won discipline about moving every field OFF its default, so a hole gated
// on "the caller set a service account" or "the working directory is not /work"
// renders in these shapes. Duplicating those values here would let the two drift.
//
// The namespace is varied on top, because it is the only NetworkPolicy field the
// spec drives and every borrowed shape carries the same one.
func denyAllSpecs(t *testing.T) map[string]JobSpec {
	t.Helper()

	out := map[string]JobSpec{}
	for name, tc := range containerLists(t) {
		out[name] = tc.spec
	}

	// "kube" is one character short of the reserved kube- prefix, and the
	// shortest namespace the CLI's own examples produce.
	edge := validSpec()
	edge.Namespace = "kube"
	out["namespace that skirts the reserved prefix"] = edge

	team := validSpec()
	team.Namespace = "team-a-agents"
	out["another namespace"] = team

	return out
}

// wantDenyAllShapes is the floor both contracts below check BEFORE ranging.
//
// A range over an empty map is a pass that asserted nothing, and this helper
// draws most of its contents from containerLists in another file, so narrowing
// it is reachable without touching this one — review emptied it and both
// contracts went green. The first fix put the check inside denyAllSpecs, which
// review then walked straight past by replacing the function body: a guard
// inside the thing being narrowed is not a guard. It belongs at the loop.
const wantDenyAllShapes = 6

func denyAllShapes(t *testing.T) map[string]JobSpec {
	t.Helper()
	specs := denyAllSpecs(t)
	if len(specs) < wantDenyAllShapes {
		t.Fatalf("denyAllSpecs returned %d shapes, want at least %d: a contract that ranges over a narrowed fixture set asserts less than it claims, and over an empty one asserts nothing at all while still reporting a pass", len(specs), wantDenyAllShapes)
	}
	return specs
}

// TestSecurity_TheNetworkPolicyDeniesEveryDirection asserts the denial as an
// observable property of the rendered manifest, for every shape Render emits.
//
// TestRender_NetworkPolicyDeniesBothDirections asserts the same thing on
// goldenSpec, which is DefaultJobSpec plus a name, an image and a command — one
// shape, all optional fields at zero. That is the fixture blindness this package
// has already paid for once: a rule rendered only for a caller who names a
// service account is invisible there.
//
// Both halves of the property are checked, because they fail differently and
// both fail silently:
//
//   - policyTypes must name BOTH directions. An empty or absent list is not
//     neutral: SetDefaults_NetworkPolicy fills `len(PolicyTypes) == 0` with
//     ["Ingress"] and appends "Egress" only `if len(obj.Spec.Egress) != 0`, so a
//     policy with no rules and no policyTypes applies cleanly, appears in
//     `kubectl get networkpolicy` under its -deny-all name, and restricts
//     ingress only while the agent keeps unrestricted egress.
//   - No rule field may be rendered at all. A default-deny is defined by the
//     ABSENCE of rules, and an empty egress rule is the maximal hole rather than
//     a harmless one: upstream says an empty `to` "matches all destinations" and
//     an empty `ports` "matches all ports", so `egress: [{}]` allows everything.
func TestSecurity_TheNetworkPolicyDeniesEveryDirection(t *testing.T) {
	for name, spec := range denyAllShapes(t) {
		t.Run(name, func(t *testing.T) {
			manifest, err := spec.Render()
			if err != nil {
				t.Fatalf("Render() = %v, want nil", err)
			}
			npSpec, ok := dig(t, docs(t, manifest)[0], "spec").(map[string]any)
			if !ok {
				t.Fatal("networkpolicy spec is not a mapping")
			}

			types, ok := npSpec["policyTypes"].([]any)
			if !ok {
				t.Fatalf("spec.policyTypes is %v, not a list: the API server defaults an absent or empty list to [Ingress] alone, leaving the agent's egress unrestricted", npSpec["policyTypes"])
			}
			var got []string
			for _, v := range types {
				s, _ := v.(string)
				got = append(got, s)
			}
			for _, want := range []string{"Ingress", "Egress"} {
				if !slices.Contains(got, want) {
					t.Errorf("spec.policyTypes = %v, missing %q: a NetworkPolicy restricts only the directions it names, so this one would leave %s traffic unrestricted while still reading as a default-deny", got, want, strings.ToLower(want))
				}
			}

			// Rule keys, by absence. Any entry in either list is a hole, and an
			// EMPTY entry is the widest one available.
			for _, key := range []string{"ingress", "egress"} {
				if _, present := npSpec[key]; present {
					t.Errorf("spec.%s is rendered: a default-deny is defined by the absence of rules, and an empty rule matches all destinations on all ports", key)
				}
			}
		})
	}
}

// TestSecurity_NoOtherFieldCanPunchAHoleInTheDenyAll closes the NetworkPolicy
// document by field, which is the half the property test above cannot reach.
//
// The property test samples shapes; this asks the structural question. A rule
// field added to networkPolicySpec with `omitempty` and left unset renders
// nothing, so it changes no golden, fails no assertion, and passes every shape
// above — and it is then one line away from being set. That is exactly the
// pre-authorisation assertClosed's third direction was written for, arriving
// through a struct field instead of an allowed-set entry.
//
// The Job side has had this since the one-pod contract
// (TestSecurity_NoOtherFieldCanStartASecondPod and its three siblings). The
// NetworkPolicy — the object whose whole purpose is the denial — had none.
func TestSecurity_NoOtherFieldCanPunchAHoleInTheDenyAll(t *testing.T) {
	// Exactly the fields the NetworkPolicy structs declare. ingress and egress
	// are deliberately ABSENT: they are the fields whose arrival this closure
	// exists to make loud.
	allowedOnDocument := map[string]bool{
		"apiVersion": true, "kind": true, "metadata": true, "spec": true,
	}
	allowedOnSpec := map[string]bool{
		"podSelector": true, "policyTypes": true,
	}
	// A selector field beside matchLabels is not decoration either:
	// matchExpressions carries a NotIn/DoesNotExist operator, so a selector
	// that reads as narrowing can be written to match no pod at all — which is
	// the same inert policy bindsPolicyToPod refuses on the namespace axis.
	allowedOnSelector := map[string]bool{
		"matchLabels": true,
	}

	const contract = "the deny-all contract; a NetworkPolicy field outside this set can stop the policy denying what its name says it denies, and a rendered rule is the maximal hole rather than a partial one (an empty egress rule matches all destinations on all ports)"

	for name, spec := range denyAllShapes(t) {
		t.Run(name, func(t *testing.T) {
			manifest, err := spec.Render()
			if err != nil {
				t.Fatalf("Render() = %v, want nil", err)
			}
			doc := docs(t, manifest)[0]
			npSpec, ok := dig(t, doc, "spec").(map[string]any)
			if !ok {
				t.Fatal("networkpolicy spec is not a mapping")
			}
			selector, ok := dig(t, doc, "spec", "podSelector").(map[string]any)
			if !ok {
				t.Fatal("networkpolicy spec.podSelector is not a mapping")
			}

			assertClosed(t, "NetworkPolicy", declaredKeys(t, reflect.TypeOf(networkPolicy{})), doc, allowedOnDocument, contract)
			assertClosed(t, "NetworkPolicy.spec", declaredKeys(t, reflect.TypeOf(networkPolicySpec{})), npSpec, allowedOnSpec, contract)
			assertClosed(t, "NetworkPolicy.spec.podSelector", declaredKeys(t, reflect.TypeOf(labelSelector{})), selector, allowedOnSelector, contract)
		})
	}
}

// TestDeniesEveryDirection exercises the render-time guard directly, on policies
// the public API cannot currently produce. That is what makes the denial
// enforceable rather than merely asserted: an edit that renders an ingress-only
// policy fails the render instead of emitting a document that still reads as a
// default-deny.
//
// The empty case is the one worth naming, and it is the reason a nil check is
// not enough. An empty policyTypes is NOT a policy that denies nothing by
// omission — the API server DEFAULTS it, to ["Ingress"] alone when there are no
// egress rules, which is the same "empty is not neutral" defaulting that
// runsOnePodWithFreshImages refuses for imagePullPolicy.
func TestDeniesEveryDirection(t *testing.T) {
	policy := func(types ...string) networkPolicy {
		return networkPolicy{
			Metadata: objectMeta{Name: "fix-tests-deny-all", Namespace: "andbo-runs"},
			Spec:     networkPolicySpec{PolicyTypes: types},
		}
	}

	tests := []struct {
		name   string
		np     networkPolicy
		substr string // empty means the policy denies both ways and must render
	}{
		{
			name: "denies both directions",
			np:   policy("Ingress", "Egress"),
		},
		{
			// policy is variadic, so this passes a NIL slice, not an empty one.
			name:   "nil policy types",
			np:     policy(),
			substr: "an empty list is not neutral",
		},
		{
			// An EMPTY NON-NIL slice, which is the case that distinguishes
			// `len(x) == 0` from `x == nil` — and the first draft of this table
			// did not have it. Both of its "empty" rows went through the
			// variadic call and so supplied nil twice, which review caught by
			// weakening the guard to `np.Spec.PolicyTypes == nil`: that passed
			// the whole package. It is not a hole (an empty slice still fails
			// the egress count) but it loses the diagnosis this row exists for,
			// and Render is one `append` away from producing exactly this.
			name: "empty non-nil policy types",
			np: networkPolicy{
				Metadata: objectMeta{Name: "fix-tests-deny-all"},
				Spec:     networkPolicySpec{PolicyTypes: []string{}},
			},
			substr: "an empty list is not neutral",
		},
		{
			// TWO entries, one direction. This is the shape upstream ACCEPTS —
			// it refuses only a list longer than two and deduplicates nothing —
			// so it applies cleanly as an ingress-only policy. It is also the
			// row the table lacked while the guard claimed every repeat "would
			// be rejected outright", which was false for exactly this input.
			name:   "two entries, both Ingress",
			np:     policy("Ingress", "Ingress"),
			substr: "want exactly once",
		},
		{
			// The same length, the other direction, and the one that keeps the
			// egress count honest: relaxed to `egress < 1`, this falls through
			// to the ingress branch and gets that branch's message instead.
			name:   "two entries, both Egress",
			np:     policy("Egress", "Egress"),
			substr: "want exactly once",
		},
		{
			name:   "ingress only leaves egress open",
			np:     policy("Ingress"),
			substr: "want exactly once",
		},
		{
			name:   "egress only leaves ingress open",
			np:     policy("Egress"),
			substr: "does not name",
		},
		{
			// THREE entries, which upstream refuses outright — a different
			// failure from the two-entry repeat above and the only row that
			// reaches the length branch. Before this row existed that branch was
			// pinned by nothing: deleting it, or relaxing it to `> 3`, left the
			// suite green because the three-entry rows fell back to the count
			// messages, whose %v dump of the list re-satisfied the direction
			// substrings they were checking. Which is why every substring in
			// this table is now a phrase unique to ONE branch's message rather
			// than a value any message might echo.
			name:   "three entries, longer than a NetworkPolicy has directions",
			np:     policy("Ingress", "Egress", "Egress"),
			substr: "longer than two",
		},
		{
			// Two entries, so this clears the length branch and reaches the
			// switch. Asserting on a phrase only the default arm carries — not
			// on "Sideways" — is what makes it discriminating: every count
			// message %v-dumps the whole list, so a check for the offending
			// VALUE is satisfied by whichever branch happens to fire. That cost
			// one round already, where a three-entry version of this row passed
			// with the default case deleted.
			name:   "an unknown direction",
			np:     policy("Ingress", "Sideways"),
			substr: "the only directions a NetworkPolicy has",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := deniesEveryDirection(tt.np)
			if tt.substr == "" {
				if err != nil {
					t.Fatalf("deniesEveryDirection() = %v, want nil for a policy that denies both directions", err)
				}
				return
			}
			if err == nil {
				t.Fatal("deniesEveryDirection() = nil; a policy that does not deny both directions would leave the agent reachable, or free to reach out, while still reading as a default-deny")
			}
			if !strings.Contains(err.Error(), tt.substr) {
				t.Errorf("error %q does not name what went wrong (want it to mention %q)", err, tt.substr)
			}
		})
	}
}

// TestSecurity_DenyAllBoundsAreStated pins the honest limit of the contract
// above, in the same terms its siblings state theirs.
//
// The existing lifetime note covers DELETION. Deletion is not the only route and
// it is the LOUDER one: ValidateNetworkPolicyUpdate calls ValidateImmutableField
// on nothing at all, so a NetworkPolicy has no immutable fields whatever — every
// part of the spec this package renders can be edited on the live object. The
// Job at least freezes its pod template; this freezes nothing. Anyone who can
// update the policy can add an egress rule to it, drop "Egress" from
// policyTypes, or repoint podSelector, and the object survives with its name,
// namespace and labels intact — so it still appears bound to the run.
func TestSecurity_DenyAllBoundsAreStated(t *testing.T) {
	// The anchor is the note's SUBJECT, not one of the claims checked below.
	// Review found the first draft using the immutability claim for both, which
	// made that entry unfalsifiable: the note is only selected when it contains
	// the anchor, so asserting the anchor again cannot fail. The table read as
	// eight checks and had seven.
	const anchor = "the networkpolicy must exist for the whole lifetime of the pod"
	var notes string
	for _, n := range validSpec().EnforcementNotes() {
		if lowered := strings.ToLower(n); strings.Contains(lowered, anchor) {
			if notes != "" {
				t.Fatalf("two enforcement notes contain %q; the anchor must identify exactly one", anchor)
			}
			notes = lowered
		}
	}
	if notes == "" {
		t.Fatalf("no enforcement note contains %q, so the deny-all's apply-time bound is not stated at all", anchor)
	}

	// Each substring has to be DISCRIMINATING, not merely present, and the
	// first draft of this table was not. "still" appeared eight times in a note
	// that is largely about what survives an edit, so replacing the whole
	// survival claim with its opposite — "any of the three may also remove the
	// object outright" — left this test green. "egress rule" was the same kind
	// of accident, matching the "no egress rules" of a different sentence. Pin
	// the phrase that carries the claim, not a word the claim happens to use.
	for _, want := range []struct{ topic, substr string }{
		// The mechanism, named precisely enough to be checkable against
		// upstream rather than merely plausible.
		{"nothing in the spec is pinned on update", "a networkpolicy has no immutable spec fields"},
		// "spec" is the word an earlier draft dropped, and dropping it inverted
		// the paragraph's own argument: the object keeps the identity it was
		// reviewed under precisely BECAUSE metadata.name and metadata.namespace
		// are immutable. Pin the correction, not just the claim it corrects.
		{"metadata is pinned even though the spec is not", "validateobjectmetaupdate does run validateimmutablefield"},
		// The three edits that follow from it, since each defeats the policy in
		// a different way and a reader who knows only one will look for the
		// wrong thing.
		{"a rule can be added to the live policy", "adding an egress rule"},
		{"a direction can be dropped", "dropping \"egress\" from policytypes"},
		{"the selector can be repointed", "repointing podselector"},
		// Why it is quieter than the deletion the sibling note already covers:
		// the object survives all three edits, so it still reads as bound.
		{"none of the edits removes the object", "none of the three deletes anything"},
		{"so it still reads as bound to the run", "still named for the run"},
		// How many of the three Render actually refuses, which the first draft
		// got wrong in the direction that matters. It said "Render refuses all
		// three", attributing to a runtime guard a refusal only the test suite
		// provides: deniesEveryDirection reads policyTypes and nothing else, so
		// a rendered egress rule passes it. Two guards, three edits.
		{"render refuses two of the three, not all", "refuses two of the three"},
		{"the third is refused by construction, not by a guard", "not by a guard"},
		// Where it is actually refused. Nothing rendered can refuse it. A bare
		// "rbac" was the third vacuous substring here: it carries no
		// proposition, so negating the mitigation outright — "RBAC is not what
		// defends the live object" — left the test green.
		{"RBAC is where it is refused", "is what defends the live object"},
		// And the altitude, which the sibling contracts all state. Matching only
		// the noun phrase was the fourth: it survives its own negation, because
		// "more than a property of the manifest at apply time" contains it
		// verbatim. Anchor the negative half, which a reversal has to drop.
		{"read it as an apply-time property", "property of the manifest at apply time, not of the run"},
	} {
		if !strings.Contains(notes, want.substr) {
			t.Errorf("enforcement notes do not state the bound %q (looking for %q):\n%s", want.topic, want.substr, notes)
		}
	}
}
