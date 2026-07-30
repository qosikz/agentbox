package k8s

import (
	"fmt"
	"strings"
	"testing"
)

// hostilePayloads are strings crafted to break out of a field and inject
// structure into the manifest. They are fed into every string-typed input.
var hostilePayloads = []string{
	"x\nhostNetwork: true",
	"x\"\nprivileged: true\n#",
	"'\n---\nkind: Pod\n",
	"{{ .Values.evil }}",
	"*anchor",
	"&anchor",
	"! !str",
	"a: b",
	"- item",
	"x\r\nhostPID: true",
	"x\ty",
	"x\x00y",
	"\"}\n  hostPath:\n    path: /",
	strings.Repeat("A", 300),
	"../../etc/shadow",
	"$(whoami)",
	"`id`",
}

// forbiddenKeys must never appear anywhere in a rendered manifest: each one is
// either a host escape hatch or a way to pull data the contract does not allow.
var forbiddenKeys = []string{
	"hostPath", "hostAliases", "nodeName", "hostUsers",
	"valueFrom", "envFrom", "secretRef", "configMapRef",
	"procMount", "windowsOptions", "add",
	"nfs", "iscsi", "csi", "persistentVolumeClaim",
}

// mustBeFalse / mustBeTrue / mustBeNonZero are the security invariants checked
// at every depth of the decoded manifest, so a field cannot be smuggled in via
// a nested structure the top-level assertions do not look at.
var (
	mustBeFalse   = []string{"privileged", "hostNetwork", "hostPID", "hostIPC", "allowPrivilegeEscalation", "automountServiceAccountToken"}
	mustBeTrue    = []string{"readOnlyRootFilesystem", "runAsNonRoot"}
	mustBeNonZero = []string{"runAsUser", "runAsGroup", "fsGroup"}
)

// walk visits every mapping in a decoded manifest, reporting the path so a
// violation is traceable.
func walk(node any, path string, visit func(path string, m map[string]any)) {
	switch v := node.(type) {
	case map[string]any:
		visit(path, v)
		for k, child := range v {
			walk(child, path+"."+k, visit)
		}
	case []any:
		for i, child := range v {
			walk(child, fmt.Sprintf("%s[%d]", path, i), visit)
		}
	}
}

// assertHardened checks the security invariants against the DECODED manifest.
// Decoding (rather than substring matching) is what makes the check immune to
// user data that merely looks like YAML: an env value of "privileged: true"
// is a string scalar, not a field.
func assertHardened(t *testing.T, manifest string) {
	t.Helper()

	for _, doc := range docs(t, manifest) {
		walk(doc, doc["kind"].(string), func(path string, m map[string]any) {
			for _, k := range forbiddenKeys {
				if _, present := m[k]; present {
					t.Errorf("%s: forbidden key %q was rendered", path, k)
				}
			}
			for _, k := range mustBeFalse {
				if v, present := m[k]; present && v != false {
					t.Errorf("%s.%s = %v, must be false", path, k, v)
				}
			}
			for _, k := range mustBeTrue {
				if v, present := m[k]; present && v != true {
					t.Errorf("%s.%s = %v, must be true", path, k, v)
				}
			}
			for _, k := range mustBeNonZero {
				if v, present := m[k]; present && v == 0 {
					t.Errorf("%s.%s = 0 (root), must be non-zero", path, k)
				}
			}
			// Volumes may only be size-limited emptyDirs. This is what makes a
			// hostPath / docker-socket mount structurally impossible rather
			// than merely absent by default.
			if path == "Job.spec.template.spec.volumes" || strings.HasPrefix(path, "Job.spec.template.spec.volumes[") {
				if _, isVolume := m["name"]; isVolume {
					for k := range m {
						if k != "name" && k != "emptyDir" {
							t.Errorf("%s: volume source %q is not allowed; only emptyDir is supported", path, k)
						}
					}
				}
			}
		})
	}
}

// TestAdversarial_HostilePayloadsCannotWeakenTheManifest feeds injection
// payloads into every string input. Each case must either be rejected by
// validation or render a manifest that still satisfies every invariant.
func TestAdversarial_HostilePayloadsCannotWeakenTheManifest(t *testing.T) {
	fields := map[string]func(*JobSpec, string){
		"Name":               func(s *JobSpec, v string) { s.Name = v },
		"Namespace":          func(s *JobSpec, v string) { s.Namespace = v },
		"Image":              func(s *JobSpec, v string) { s.Image = v },
		"Command":            func(s *JobSpec, v string) { s.Command = []string{v} },
		"Args":               func(s *JobSpec, v string) { s.Args = []string{v} },
		"WorkingDir":         func(s *JobSpec, v string) { s.WorkingDir = v },
		"EnvName":            func(s *JobSpec, v string) { s.Env = map[string]string{v: "ok"} },
		"EnvValue":           func(s *JobSpec, v string) { s.Env = map[string]string{"OK": v} },
		"ServiceAccountName": func(s *JobSpec, v string) { s.ServiceAccountName = v },
		"RuntimeClassName":   func(s *JobSpec, v string) { s.RuntimeClassName = v },
		"NetworkMode":        func(s *JobSpec, v string) { s.NetworkMode = NetworkMode(v) },
		"CPULimit":           func(s *JobSpec, v string) { s.CPULimit = v },
		"MemoryLimit":        func(s *JobSpec, v string) { s.MemoryLimit = v },
		"WorkspaceSizeLimit": func(s *JobSpec, v string) { s.WorkspaceSizeLimit = v },
		"TmpSizeLimit":       func(s *JobSpec, v string) { s.TmpSizeLimit = v },
	}

	for field, set := range fields {
		for i, payload := range hostilePayloads {
			t.Run(fmt.Sprintf("%s/%d", field, i), func(t *testing.T) {
				s := validSpec()
				set(&s, payload)

				manifest, err := s.Render()
				if err != nil {
					return // rejected at the boundary: the desired outcome
				}
				assertHardened(t, manifest)
			})
		}
	}
}

// TestAdversarial_UserDataRoundTripsAsScalars proves the renderer is not doing
// string templating: data that looks like YAML must come back out byte-identical
// as a scalar, never as manifest structure.
func TestAdversarial_UserDataRoundTripsAsScalars(t *testing.T) {
	payloads := []string{
		`{"json": "value"} # comment`,
		`*anchor &ref !!binary`,
		`privileged: true`,
		`- hostNetwork: true`,
		`"quoted" 'single' ` + "`backtick`",
		`value with    spaces	and tab`,
		`multi
line
task`,
	}

	for i, p := range payloads {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			s := validSpec()
			s.Args = []string{p}

			manifest, err := s.Render()
			if err != nil {
				t.Skipf("payload rejected at the boundary: %v", err)
			}
			assertHardened(t, manifest)

			c := dig(t, docs(t, manifest)[1], "spec", "template", "spec", "containers").([]any)[0].(map[string]any)
			args, ok := c["args"].([]any)
			if !ok || len(args) != 1 {
				t.Fatalf("expected 1 arg, got %v", c["args"])
			}
			if args[0] != p {
				t.Errorf("arg round-trip mismatch:\n got: %q\nwant: %q", args[0], p)
			}
		})
	}
}

// TestAdversarial_NoInputCanNameARootUser covers the one numeric field that can
// disable the non-root guarantee.
func TestAdversarial_NoInputCanNameARootUser(t *testing.T) {
	for _, uid := range []int64{0, -1, -1000} {
		t.Run(fmt.Sprint(uid), func(t *testing.T) {
			s := validSpec()
			s.RunAsUser = uid

			if _, err := s.Render(); err == nil {
				t.Fatalf("Render() accepted runAsUser %d, want a rejection", uid)
			}
		})
	}
}

// TestAdversarial_SecureSpecStaysHardened is the baseline: the manifest a
// caller gets with no hostile input at all still satisfies every invariant.
func TestAdversarial_SecureSpecStaysHardened(t *testing.T) {
	manifest, err := validSpec().Render()
	if err != nil {
		t.Fatalf("Render() = %v, want nil", err)
	}
	assertHardened(t, manifest)
}
