package k8s

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
)

// metadataKeysInContract is the whole of what this renderer puts under
// `metadata:`: who the object is and where it lives. Nothing here changes what
// the cluster DOES with the object.
var metadataKeysInContract = map[string]bool{
	"name":      true,
	"namespace": true,
	"labels":    true,
}

// labelKeysInContract is the set jobLabels builds, named here as the contract
// rather than derived from that function: deriving it would make this check
// assert that jobLabels equals itself.
var labelKeysInContract = map[string]bool{
	labelName:      true,
	labelInstance:  true,
	labelManagedBy: true,
}

// renderedKey reports the YAML key a struct field renders under, whether the
// encoder drops it entirely, and whether it is inlined.
//
// It exists because this guard's whole claim is about what SHIPS, so it has to
// agree with the encoder rather than with a plausible reading of struct tags.
// TestRenderedKeyAgreesWithTheEncoder pins it against yaml.v3 itself instead of
// against that reading, which is the only way to keep the two from drifting.
func renderedKey(f reflect.StructField) (key string, skipped, inline bool) {
	// yaml.v3 never renders an unexported field. Walking one anyway is not
	// merely wasted work: reflect refuses to hand over its value, so the label
	// arm's Interface() PANICS out of Render instead of returning an error.
	if f.PkgPath != "" && !f.Anonymous {
		return "", true, false
	}

	tag := f.Tag.Get("yaml")
	// The encoder drops a field only when the tag is EXACTLY "-". With an option
	// appended it falls through to the split and renders under the literal key
	// "-", so this test has to come BEFORE the split, not after it. Splitting
	// first is what let `yaml:"-,omitempty"` ship unexamined.
	if tag == "-" {
		return "", true, false
	}

	name, opts, _ := strings.Cut(tag, ",")
	inline = slices.Contains(strings.Split(opts, ","), "inline")
	if name == "" {
		// yaml.v3 lowercases the field name when the tag names nothing.
		name = strings.ToLower(f.Name)
	}
	return name, false, inline
}

// carriesIdentityOnlyMetadata reports whether a rendered object's `metadata:` is
// identity and nothing else.
//
// Every other closure in this package stops at `metadata:` — the Job's four
// contracts and the deny-all all name "metadata" in their allowed key sets and
// none descends into it — so objectMeta was one of TWO rendered mappings nothing
// in the repository reached. This closes that one; the other, templateMeta, is
// named at the end of this comment and is still open. That was measured: adding
// `annotations` and
// `ownerReferences` to objectMeta with `omitempty`, left unset, took the whole
// repository green, and wiring the second to a JobSpec field then rendered an
// `ownerReferences:` block on the deny-all NetworkPolicy, still green.
//
// Why metadata is worth a guard and not just a closure. A spec field that
// weakens isolation is visible to anyone reading the manifest; a metadata field
// is not, because it changes what the CLUSTER does with the object rather than
// what the object says:
//
//   - ownerReferences makes the object a garbage-collected dependent. On the
//     deny-all policy that contradicts a bound EnforcementNotes already states —
//     it "carries no ownerReference, so it is neither garbage-collected with the
//     Job nor protected from removal" — and the two deletions do not take the
//     same time, and that asymmetry alone is the whole argument: a NetworkPolicy
//     is not a pod and has no terminationGracePeriodSeconds, so its deletion
//     completes at once, while the Job's pod keeps running for up to the 30
//     seconds this renderer never sets and the API server therefore defaults.
//     So deleting the Job can leave the deny-all gone for the last seconds of a
//     run that is still committing and pushing — and an agent that ignores
//     SIGTERM burns that whole window. (An earlier version of this note also
//     claimed cascading deletion gives no ordering guarantee among an owner's
//     dependents. That may well be true, but it was not checked against the
//     garbage collector's source, and the grace-period asymmetry carries the
//     point without it.)
//   - finalizers decides whether the object can be deleted at all.
//   - annotations are an input to admission webhooks and operators. What any
//     particular cluster does with one is exactly the kind of thing a
//     render-only package cannot reason about, which is the honest reason to
//     refuse the field rather than to review its contents.
//   - labels are a SELECTOR SURFACE. This package renders three and knows what
//     selects on them; a fourth can be matched by another NetworkPolicy in the
//     namespace (which is additive and can only ADD egress), by a controller, or
//     by a mesh injector whose opt-in is a label — and an injected container is
//     what runsOnlyTheAgent refuses at the other end of the same manifest.
//
// Absence is deliberately NOT refused. A missing label costs Andbo's own
// `kubectl get -l` view and nothing that a policy or controller acts on, so
// refusing it would be a larger claim than the reasoning supports. The
// namespace's presence is defended by bindsPolicyToPod, which can say something
// this function cannot about what a missing one means.
//
// Five refusals, and only ONE of them is reachable from a value. The label key
// set is data, so TestCarriesIdentityOnlyMetadata drives it directly. The other
// four are properties of the objectMeta TYPE — an inline field, a field name
// outside the contract, a "labels" that is not the label map, and a field whose
// type is neither that map nor a bounded scalar — which no test value can vary,
// and each was proven by mutating the struct and watching every render in the
// package fail. Putting them here rather
// than only in TestSecurity_NoOtherFieldCanWidenTheMetadata is the difference
// between a declared field failing one test and a declared field failing every
// render, which is what fail-closed means for a boundary this quiet.
//
// Two honest notes on those three, both from review:
//
//   - The inline refusal is MASKED for the ordinary `yaml:",inline"` form, since
//     renderedKey then falls back to the lowercased field name and the key check
//     refuses it first. It is uniquely load-bearing only when the inline field's
//     tag names a key already in the contract — `yaml:"labels,inline"` — where
//     deleting it takes the suite green.
//   - The two call sites in Render are equivalent to each other, not merely
//     untested. Both pass an objectMeta, so the three type-driven arms behave
//     identically, and Render assigns the SAME label map to both, so the one
//     value-driven arm cannot fail on one and pass on the other. Only the kind
//     string in the message differs.
//
// The bound, and it is the larger one: this reads objectMeta. templateMeta — the
// POD's own metadata, whose labels the NetworkPolicy binds to and whose labels
// and annotations are what a mesh injector actually reads — is a different type,
// is reached by no guard and by no closure in this package, and review
// demonstrated it: an `annotations` field on templateMeta wired to a new JobSpec
// field renders `sidecar.istio.io/inject: "true"` into the pod template with the
// whole repository green. That is the next gate, not a solved problem.
//
// Same as every other guard here: no test can prove this was CALLED, since
// deleting the call site changes no output while Render is correct. The property
// itself is pinned on the rendered manifest by
// TestSecurity_TheManifestsCarryIdentityOnlyMetadata, guard or no guard.
func carriesIdentityOnlyMetadata(kind string, meta objectMeta) error {
	v := reflect.ValueOf(meta)
	typ := v.Type()

	for i := range typ.NumField() {
		f := typ.Field(i)
		key, skipped, inline := renderedKey(f)
		if skipped {
			continue
		}

		// An inline map renders its own keys at the metadata level, where this
		// contract has no name to check them under — so the key check below
		// would pass a field that widens the mapping arbitrarily.
		if inline {
			return fmt.Errorf("internal error: %s metadata field %s is marked yaml inline, so whatever it holds is rendered as metadata keys this contract never names; refusing to render it", kind, f.Name)
		}

		if !metadataKeysInContract[key] {
			return fmt.Errorf("internal error: %s metadata declares %q, and this renderer puts only a name, a namespace and labels there: every other metadata field changes what the cluster DOES with the object rather than what it says — ownerReferences make it a garbage-collected dependent, finalizers decide whether it can be deleted, annotations are an input to admission — and none of that is visible in the spec a reviewer reads; refusing to render it", kind, key)
		}

		fv := v.Field(i)
		switch {
		// "labels" is matched FIRST and its type demanded here rather than in
		// the case guard. With the scalar arm first, retyping Labels to a
		// string would have been accepted as a bounded scalar and skipped the
		// key check below — an arm that silently un-covers another arm is the
		// masking this package has already paid for once.
		case key == "labels":
			if fv.Type() != reflect.TypeOf(map[string]string(nil)) {
				return fmt.Errorf("internal error: %s metadata renders %q as a %s rather than the label map, so the label keys below are not checked at all; refusing to render it", kind, key, fv.Kind())
			}
			for _, k := range sortedKeys(fv.Interface().(map[string]string)) {
				if !labelKeysInContract[k] {
					return fmt.Errorf("internal error: %s metadata carries label %q=%q, which is not one of the labels this renderer decided to carry (%s, %s, %s): a label is a selector surface, and another NetworkPolicy, controller, or injector in the namespace can act on one this package did not choose; refusing to render it", kind, k, fv.Interface().(map[string]string)[k], labelName, labelInstance, labelManagedBy)
				}
			}
		case fv.Kind() == reflect.String:
			// A bounded scalar: it can only ever say who the object is.
		default:
			return fmt.Errorf("internal error: %s metadata renders %q as a %s rather than a bounded scalar or the label map, so this contract cannot state what it carries — nested structure under an allowed name widens the mapping exactly as a new key would; refusing to render it", kind, key, fv.Kind())
		}
	}
	return nil
}
