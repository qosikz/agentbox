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

// carriesIdentityOnlyMetadata reports whether a rendered object's `metadata:` is
// identity and nothing else.
//
// Every other closure in this package stops at `metadata:` — the Job's four
// contracts and the deny-all all name "metadata" in their allowed key sets and
// none descends into it — so objectMeta was the one rendered mapping nothing in
// the repository reached. That was measured: adding `annotations` and
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
//     same time. A NetworkPolicy has no grace period; a pod does, and this
//     renderer sets none so the API server defaults it to 30 seconds. Deleting
//     the Job therefore removes the policy while the agent may still be inside
//     that window, and cascading deletion gives no ordering guarantee among an
//     owner's dependents. The deny-all is gone for the last seconds of a run
//     that is still committing and pushing.
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
// Four refusals, and only ONE of them is reachable from a value. The label key
// set is data, so TestCarriesIdentityOnlyMetadata drives it directly. The other
// three are properties of the objectMeta TYPE — an inline map, a field name
// outside the contract, a field whose type is neither a bounded scalar nor the
// label map — which no test value can vary, and each was proven by mutating the
// struct and watching every render in the package fail. Putting them here rather
// than only in TestSecurity_NoOtherFieldCanWidenTheMetadata is the difference
// between a declared field failing one test and a declared field failing every
// render, which is what fail-closed means for a boundary this quiet.
//
// The bound: it reads objectMeta. templateMeta — the pod template's own
// metadata, whose labels are what the NetworkPolicy binds to — is a different
// type and this function does not reach it.
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
		key, opts, _ := strings.Cut(f.Tag.Get("yaml"), ",")

		// An inline map renders its own keys at the metadata level, where this
		// contract has no name to check them under — so the key check below
		// would pass a field that widens the mapping arbitrarily.
		if slices.Contains(strings.Split(opts, ","), "inline") {
			return fmt.Errorf("internal error: %s metadata field %s is marked yaml inline, so whatever it holds is rendered as metadata keys this contract never names; refusing to render it", kind, f.Name)
		}
		switch key {
		case "-":
			// Explicitly not rendered, so not part of the boundary.
			continue
		case "":
			// yaml.v3 lowercases the field name when no tag is given.
			key = strings.ToLower(f.Name)
		}

		if !metadataKeysInContract[key] {
			return fmt.Errorf("internal error: %s metadata declares %q, and this renderer puts only a name, a namespace and labels there: every other metadata field changes what the cluster DOES with the object rather than what it says — ownerReferences make it a garbage-collected dependent, finalizers decide whether it can be deleted, annotations are an input to admission — and none of that is visible in the spec a reviewer reads; refusing to render it", kind, key)
		}

		fv := v.Field(i)
		switch {
		case fv.Kind() == reflect.String:
			// A bounded scalar: it can only ever say who the object is.
		case key == "labels" && fv.Type() == reflect.TypeOf(map[string]string(nil)):
			for _, k := range sortedKeys(fv.Interface().(map[string]string)) {
				if !labelKeysInContract[k] {
					return fmt.Errorf("internal error: %s metadata carries label %q=%q, which is not one of the labels this renderer decided to carry (%s, %s, %s): a label is a selector surface, and another NetworkPolicy, controller, or injector in the namespace can act on one this package did not choose; refusing to render it", kind, k, fv.Interface().(map[string]string)[k], labelName, labelInstance, labelManagedBy)
				}
			}
		default:
			return fmt.Errorf("internal error: %s metadata renders %q as a %s rather than a bounded scalar or the label map, so this contract cannot state what it carries — nested structure under an allowed name widens the mapping exactly as a new key would; refusing to render it", kind, key, fv.Kind())
		}
	}
	return nil
}
