package k8s

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Every closure in this package stops at `metadata:`. The Job's four contracts
// and the deny-all all name "metadata" in their allowed key sets and none of
// them descends into it, so TWO rendered mappings were reached by no contract in
// the repository: objectMeta, which this file closes, and templateMeta, which it
// does not — see the bound at the end of carriesIdentityOnlyMetadata's doc, and
// read nothing here as a claim about the pod template's own metadata. The gap
// was measured, not argued.
//
// Adding `Annotations map[string]string` and `OwnerReferences []ownerReference`
// to objectMeta — both `omitempty`, both left unset — took the WHOLE repository
// green: no golden moved, no closure objected, `go vet` was clean. Wiring the
// second to a new JobSpec field then rendered
//
//	metadata:
//	  ownerReferences:
//	    - apiVersion: batch/v1
//	      kind: Job
//	      name: fix-tests
//	      uid: 0d4c1b2e-0000-4000-8000-000000000001
//	  name: fix-tests-deny-all
//	  namespace: andbo-runs
//
// on the deny-all NetworkPolicy, still green. That is the field worth naming,
// because it contradicts a bound EnforcementNotes already states: the policy
// "carries no ownerReference, so it is neither garbage-collected with the Job
// nor protected from removal". An owned policy is collected when the Job is,
// and the two deletions do not take the same time — a NetworkPolicy has no
// grace period while its pod has one, which this renderer never sets and the
// API server therefore defaults to 30 seconds. So deleting the Job can remove
// the policy while the agent is still inside that window, and the deny-all this
// package exists to render is gone for the last seconds of a run that is still
// committing and pushing. Nothing about the manifest looks wrong, because the
// field that did it is metadata rather than spec.
//
// The three contracts below are deliberately different questions. The property
// test asks what the manifest RENDERS, the closure asks what the struct COULD
// render, and the guard test asks what Render REFUSES — and only the last of
// those makes the boundary fail closed rather than merely observed.

// TestRenderedKeyAgreesWithTheEncoder pins renderedKey to what yaml.v3 actually
// emits, rather than to a plausible reading of struct tags.
//
// The guard's entire claim is about what SHIPS, so a parser that disagrees with
// the encoder is not a smaller guard — it is a hole shaped exactly like the one
// this file was written to close. Review found two, and both were invisible to
// every other test in the package:
//
//   - `yaml:"-,omitempty"`. yaml.v3 drops a field only when the tag is EXACTLY
//     "-"; with anything appended it falls through to the split and renders
//     under the literal key `-`. The guard split the tag FIRST and then tested
//     the name, so it skipped a field that ships. Measured: a
//     `Finalizers []string` with that tag took the whole repository green,
//     which is the pre-commit state reproduced on top of the new guard.
//   - an unexported field. yaml.v3 never renders one; the guard walked it
//     anyway and the label arm's `fv.Interface()` PANICKED out of Render
//     instead of returning an error.
//
// So this test compares key SETS against the encoder's own output. Anything
// that makes renderedKey and yaml.v3 disagree fails here, whichever direction
// the disagreement runs.
func TestRenderedKeyAgreesWithTheEncoder(t *testing.T) {
	type tagged struct {
		Name string `yaml:"name"`
	}
	type untagged struct {
		Namespace string
	}
	type optioned struct {
		Name string `yaml:"name,omitempty"`
	}
	type dropped struct {
		Name   string `yaml:"name"`
		Secret string `yaml:"-"`
	}
	type droppedWithOption struct {
		Name   string `yaml:"name"`
		Secret string `yaml:"-,omitempty"`
	}
	type withUnexported struct {
		Name   string `yaml:"name"`
		hidden string
	}

	for _, tt := range []struct {
		name string
		v    any
	}{
		{"a plain tag", tagged{Name: "n"}},
		{"no tag at all", untagged{Namespace: "ns"}},
		{"a tag carrying an option", optioned{Name: "n"}},
		{"a field the encoder drops", dropped{Name: "n", Secret: "s"}},
		{"a dropped tag with an option appended", droppedWithOption{Name: "n", Secret: "s"}},
		{"an unexported field", withUnexported{Name: "n", hidden: "h"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			out, err := yaml.Marshal(tt.v)
			if err != nil {
				t.Fatalf("yaml.Marshal(%T) = %v, want nil", tt.v, err)
			}
			emitted := map[string]any{}
			if err := yaml.Unmarshal(out, &emitted); err != nil {
				t.Fatalf("yaml.Unmarshal: %v", err)
			}
			var got []string
			for k := range emitted {
				got = append(got, k)
			}

			var want []string
			typ := reflect.TypeOf(tt.v)
			for i := range typ.NumField() {
				key, skipped, inline := renderedKey(typ.Field(i))
				if skipped || inline {
					continue
				}
				want = append(want, key)
			}

			slices.Sort(got)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Errorf("renderedKey predicts %v for %T but the encoder emits %v.\nThe guard checks the keys it predicts, so every key the encoder emits and renderedKey does not name is a metadata key that ships unexamined:\n%s", want, tt.v, got, out)
			}
		})
	}

	// Inline is reported rather than named, because the encoder emits the
	// inlined map's OWN keys and there is no single key to predict — which is
	// exactly why the guard refuses the shape instead of checking it.
	t.Run("an inlined map", func(t *testing.T) {
		type inlined struct {
			Name  string            `yaml:"name"`
			Extra map[string]string `yaml:",inline"`
		}
		typ := reflect.TypeOf(inlined{})
		if _, _, inline := renderedKey(typ.Field(1)); !inline {
			t.Error("renderedKey does not report the inline option; the guard would then check the field under its own name while the encoder spreads its keys across the mapping")
		}
		out, err := yaml.Marshal(inlined{Name: "n", Extra: map[string]string{"ownerReferences": "x"}})
		if err != nil {
			t.Fatalf("yaml.Marshal = %v, want nil", err)
		}
		if !strings.Contains(string(out), "ownerReferences") {
			t.Fatalf("the encoder does not inline the map, so this case no longer demonstrates the hazard:\n%s", out)
		}
	})
}

// metadataShapes returns the shapes to assert the metadata contract on.
//
// It borrows denyAllSpecs for its FIXTURE VALUES — that helper is already this
// package's record of "every optional field moved off its default", crossed with
// both transports and varied by namespace — and adds the axis metadata has that
// the deny-all does not: the object NAME, which is the only other JobSpec input
// objectMeta renders. A metadata field gated on a long name, or on a name that
// makes the policy's "-deny-all" suffix the longest thing in the document,
// renders in these shapes.
func metadataShapes(t *testing.T) map[string]JobSpec {
	t.Helper()

	out := denyAllSpecs(t)

	// The name axis is crossed with the RICHEST shape, not with validSpec.
	// Review caught the first version building these two from validSpec, which
	// leaves every optional field at its zero value — so a metadata field gated
	// on both a long name and an opt-in would have rendered in no shape here.
	// That is the fixture blindness this package has paid for before, arriving
	// through the one axis this contract added itself.
	rich, ok := out[string(WorkspaceFromImage)+"/all-options"]
	if !ok {
		t.Fatalf("denyAllSpecs no longer carries the %q shape, which is where this package keeps every optional field moved off its default; the name axis below would otherwise be crossed only with zero values", string(WorkspaceFromImage)+"/all-options")
	}

	// The longest name Validate admits, which is also the longest instance
	// label and the longest "-deny-all" policy name the renderer can emit.
	long := rich
	long.Name = strings.Repeat("a", MaxNameLength)
	out["longest admissible name"] = long

	short := rich
	short.Name = "a"
	out["shortest admissible name"] = short

	return out
}

// wantMetadataShapes is this contract's own floor, checked AT THE LOOP rather
// than inside metadataShapes.
//
// Most of what metadataShapes returns comes from another contract's fixture
// builder in another file, so narrowing it is reachable without touching this
// one. A floor that lives inside the helper being narrowed is not a floor: the
// deny-all contract paid for that lesson when review replaced a helper body
// wholesale and the check went with it.
const wantMetadataShapes = 8

func metadataFixtures(t *testing.T) map[string]JobSpec {
	t.Helper()
	specs := metadataShapes(t)
	if len(specs) < wantMetadataShapes {
		t.Fatalf("metadataShapes returned %d shapes, want at least %d: a contract that ranges over a narrowed fixture set asserts less than it claims, and over an empty one asserts nothing at all while still reporting a pass", len(specs), wantMetadataShapes)
	}
	return specs
}

// metadataKeys is the key set both rendered documents must carry, and it is
// asserted in BOTH directions.
//
// The absence half is what this contract is for. The presence half is defended
// elsewhere — bindsPolicyToPod refuses a missing or mismatched namespace with a
// message about where the run is confined — and is restated here only because
// "exactly these keys" is a cheaper thing to read than "these and not those".
var metadataKeys = []string{"name", "namespace", "labels"}

// TestSecurity_TheManifestsCarryIdentityOnlyMetadata asserts, on every shape
// Render emits, that both documents' metadata is identity and nothing else.
//
// Identity means: a name, a namespace, and the three labels this package
// decided on. Everything else Kubernetes admits under `metadata:` changes what
// the cluster DOES with the object rather than what the object says —
// ownerReferences decide what garbage-collects it, finalizers decide whether it
// can be deleted at all, and annotations are read by admission webhooks and
// controllers that can rewrite the very pod spec this package hardened. A
// render-only package cannot reason about any of that, which is the honest
// reason to refuse it rather than review it.
//
// The value shapes are checked too, not only the keys: a nested mapping or a
// sequence arriving under an allowed name is the same widening by another
// route.
func TestSecurity_TheManifestsCarryIdentityOnlyMetadata(t *testing.T) {
	wantLabels := []string{labelInstance, labelManagedBy, labelName}

	for name, spec := range metadataFixtures(t) {
		t.Run(name, func(t *testing.T) {
			manifest, err := spec.Render()
			if err != nil {
				t.Fatalf("Render() = %v, want nil", err)
			}
			for i, kind := range []string{"NetworkPolicy", "Job"} {
				meta, ok := dig(t, docs(t, manifest)[i], "metadata").(map[string]any)
				if !ok {
					t.Fatalf("%s metadata is not a mapping", kind)
				}

				var got []string
				for k, v := range meta {
					got = append(got, k)
					if k == "labels" {
						continue
					}
					if _, isScalar := v.(string); !isScalar {
						t.Errorf("%s metadata.%s renders %T rather than a scalar: nested structure under an allowed name widens this contract exactly as a new key would", kind, k, v)
					}
				}
				slices.Sort(got)
				if want := slices.Sorted(slices.Values(metadataKeys)); !slices.Equal(got, want) {
					t.Errorf("%s metadata keys = %v, want exactly %v: every other metadata field changes what the cluster does with the object rather than what it says — ownerReferences decide what garbage-collects it, finalizers whether it can be deleted, annotations what admission rewrites — and none of that is visible in the spec a reviewer reads", kind, got, want)
				}

				labels, ok := meta["labels"].(map[string]any)
				if !ok {
					t.Fatalf("%s metadata.labels is %T, not a mapping", kind, meta["labels"])
				}
				var gotLabels []string
				for k := range labels {
					gotLabels = append(gotLabels, k)
				}
				slices.Sort(gotLabels)
				if want := slices.Sorted(slices.Values(wantLabels)); !slices.Equal(gotLabels, want) {
					t.Errorf("%s metadata.labels = %v, want exactly %v: a label is a selector surface, and this package cannot know what else in the namespace selects on one it did not decide to carry", kind, gotLabels, want)
				}
			}
		})
	}
}

// TestSecurity_NoOtherFieldCanWidenTheMetadata closes objectMeta by field, which
// is the half the property test above cannot reach.
//
// The property test walks the RENDERED manifest, so a field added with
// `omitempty` and left unset is invisible to it — which is precisely the state
// the whole repository was measured in, green, with both annotations and
// ownerReferences declared. Reading the struct closes it at the source.
//
// objectMeta is shared by both documents, so one struct closure covers both;
// the loop is over shapes rather than types because assertClosed's second
// direction reads the rendered mapping as well.
func TestSecurity_NoOtherFieldCanWidenTheMetadata(t *testing.T) {
	allowed := map[string]bool{}
	for _, k := range metadataKeys {
		allowed[k] = true
	}

	const contract = "the identity-only metadata contract; a metadata field outside this set changes what the cluster DOES with the object rather than what it says, and none of it is visible in the spec a reviewer reads — ownerReferences make the object a garbage-collected dependent, finalizers decide whether it can be deleted, and annotations are read by admission controllers that can rewrite the pod spec this package hardened"

	for name, spec := range metadataFixtures(t) {
		t.Run(name, func(t *testing.T) {
			manifest, err := spec.Render()
			if err != nil {
				t.Fatalf("Render() = %v, want nil", err)
			}
			for i, kind := range []string{"NetworkPolicy", "Job"} {
				meta, ok := dig(t, docs(t, manifest)[i], "metadata").(map[string]any)
				if !ok {
					t.Fatalf("%s metadata is not a mapping", kind)
				}
				assertClosed(t, kind+".metadata", declaredKeys(t, reflect.TypeOf(objectMeta{})), meta, allowed, contract)
			}
		})
	}
}

// TestCarriesIdentityOnlyMetadata exercises the render-time guard directly, on
// metadata the public API cannot currently produce. That is what makes the
// boundary fail closed rather than merely asserted: an edit that renders a
// fourth label fails the render instead of emitting an object that still reads
// as Andbo's.
//
// Only one of the guard's five refusals is reachable from a VALUE, and saying so
// is part of the contract rather than an excuse. The label key set is data, so it
// is table-tested below. The other four — an inline field, an out-of-contract
// field name, a "labels" that is not the label map, and a field whose type is
// neither that map nor a bounded scalar — are properties of the objectMeta TYPE,
// which no test value can vary. They were proven by mutating the struct and
// observing every render in the package fail, and that is the point of putting
// them in the guard rather than only in the closure above: a declared field
// breaks the render for every caller, not one test.
//
// The tag-parsing those refusals rest on is NOT type-level and is tested
// directly, by TestRenderedKeyAgreesWithTheEncoder — which is where review found
// two holes that every test here was blind to.
func TestCarriesIdentityOnlyMetadata(t *testing.T) {
	for _, tt := range metadataGuardCases() {
		t.Run(tt.name, func(t *testing.T) {
			err := carriesIdentityOnlyMetadata(tt.kind, tt.meta)
			if tt.substr == "" {
				if err != nil {
					t.Fatalf("carriesIdentityOnlyMetadata(%q, %+v) = %v, want nil", tt.kind, tt.meta, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("carriesIdentityOnlyMetadata(%q, %+v) = nil, want an error", tt.kind, tt.meta)
			}
			if !strings.Contains(err.Error(), tt.substr) {
				t.Errorf("error %q does not contain %q", err, tt.substr)
			}
		})
	}
}

// metadataGuardCases pairs metadata with the phrase its refusal must carry.
//
// The phrases are branch-unique and are drawn from the REASONING, never from
// the input: every error here formats the offending key, so asserting the key
// would pass against any branch that happened to mention it and would test
// nothing.
func metadataGuardCases() []struct {
	name   string
	kind   string
	meta   objectMeta
	substr string
} {
	rendered := func() objectMeta {
		return objectMeta{
			Name:      "fix-tests",
			Namespace: "andbo-runs",
			Labels: map[string]string{
				labelName:      labelValueName,
				labelInstance:  "fix-tests",
				labelManagedBy: labelValueName,
			},
		}
	}
	with := func(k, v string) objectMeta {
		m := rendered()
		m.Labels[k] = v
		return m
	}

	const outside = "is not one of the labels this renderer decided to carry"

	return []struct {
		name   string
		kind   string
		meta   objectMeta
		substr string
	}{
		{"exactly what Render builds", "Job", rendered(), ""},
		// Absence is NOT what this guard refuses, and the asymmetry is
		// deliberate. An extra label is a selector surface this package cannot
		// bound; a missing one costs Andbo's own `kubectl get -l` view and
		// nothing a policy or controller acts on. Refusing both would be a
		// larger claim than the reasoning supports.
		{"no labels at all", "NetworkPolicy", objectMeta{Name: "fix-tests-deny-all", Namespace: "andbo-runs"}, ""},
		// The label that makes this concrete: sidecar injection is opt-in by
		// label or annotation in the common meshes, and a container injected
		// into the pod is the contract runsOnlyTheAgent refuses at the other
		// end of the same manifest.
		{"a label another controller acts on", "Job", with("sidecar.istio.io/inject", "true"), outside},
		// Substring collisions are refused by exact lookup, and the case is
		// kept because the deny-all contract shipped one: a check that matched
		// on prefix would let both of these through.
		{"a label that extends one of ours", "Job", with(labelName+"-2", labelValueName), outside},
		{"a label our own prefix would swallow", "Job", with("app.kubernetes.io/version", "v0"), outside},
		{"the empty label key", "Job", with("", "andbo"), outside},
	}
}
