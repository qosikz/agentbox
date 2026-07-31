package k8s

import (
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/qosikz/andbo/internal/runtime"
)

// unsafeRunes are characters that must never reach a rendered manifest.
//
// Two distinct problems, both found by review of the first implementation:
//
//   - U+2028 and U+2029 are YAML line breaks that the emitter writes RAW into a
//     single-quoted scalar. A parser that treats them as line breaks and one
//     that does not read the same manifest differently, which is exactly the
//     ambiguity a security contract must not contain.
//   - Bidi overrides and zero-width characters are invisible to a human
//     reviewing the manifest before applying it (Trojan Source). A reviewer
//     approving a manifest must see what the cluster will run.
var unsafeRunes = map[string]string{
	"C0 control":          "\x01",
	"DEL":                 "\x7f",
	"newline":             "\n",
	"carriage return":     "\r",
	"C1 control NEL":      "\u0085",
	"C1 control":          "\u009c",
	"line separator":      "\u2028",
	"paragraph separator": "\u2029",
	"zero width space":    "\u200b",
	"zero width joiner":   "\u200d",
	"bidi LRM":            "\u200e",
	"bidi RLO":            "\u202e",
	"bidi isolate":        "\u2066",
	"BOM":                 "\ufeff",
}

// TestSecurity_UnsafeRunesAreRejected covers every caller-supplied string that
// is not already constrained to ASCII by a regex.
func TestSecurity_UnsafeRunesAreRejected(t *testing.T) {
	fields := map[string]func(*JobSpec, string){
		"Image":      func(s *JobSpec, v string) { s.Image = "repo/img" + v },
		"Command":    func(s *JobSpec, v string) { s.Command = []string{"agent" + v} },
		"Args":       func(s *JobSpec, v string) { s.Args = []string{"--task" + v} },
		"EnvValue":   func(s *JobSpec, v string) { s.Env = map[string]string{"TASK": "do it" + v} },
		"WorkingDir": func(s *JobSpec, v string) { s.WorkingDir = "/work" + v },
	}

	for field, set := range fields {
		for name, r := range unsafeRunes {
			// Args deliberately allow newline and tab: an agent task description
			// is legitimately multi-line, and both round-trip unambiguously.
			if field == "Args" && (r == "\n" || r == "\t") {
				continue
			}
			t.Run(field+"/"+name, func(t *testing.T) {
				s := validSpec()
				set(&s, r)

				if _, err := s.Render(); err == nil {
					t.Fatalf("Render() accepted %s (%q) in %s, want a rejection", name, r, field)
				}
			})
		}
	}
}

// TestSecurity_AllowedRunesStillRender guards against the rejection above being
// so broad that ordinary international task text stops working.
func TestSecurity_AllowedRunesStillRender(t *testing.T) {
	for _, v := range []string{"caf\u00e9", "\u65e5\u672c\u8a9e\u306e\u30bf\u30b9\u30af", "naive - em dash \u2014 ok", "emoji \U0001F680"} {
		t.Run(v, func(t *testing.T) {
			s := validSpec()
			s.Env = map[string]string{"TASK": v}

			manifest, err := s.Render()
			if err != nil {
				t.Fatalf("Render() = %v, want nil for ordinary text %q", err, v)
			}
			assertHardened(t, manifest)

			c := dig(t, docs(t, manifest)[1], "spec", "template", "spec", "containers").([]any)[0].(map[string]any)
			got := c["env"].([]any)[0].(map[string]any)["value"]
			if got != v {
				t.Errorf("env value round-trip mismatch: got %q, want %q", got, v)
			}
		})
	}
}

// TestSecurity_NamespaceLengthIsBounded covers a namespace that satisfies the
// DNS-1123 character rules but exceeds the 63-character limit.
func TestSecurity_NamespaceLengthIsBounded(t *testing.T) {
	s := validSpec()
	s.Namespace = strings.Repeat("a", 64)

	err := s.Validate()
	if err == nil {
		t.Fatal("Validate() accepted a 64-character namespace, want a rejection")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "namespace") {
		t.Errorf("error = %q, want it to mention the namespace", err)
	}

	s.Namespace = strings.Repeat("a", 63)
	if err := s.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil for a 63-character namespace", err)
	}
}

// TestSecurity_ReservedNamespacePrefixIsRefused covers the namespaces an agent
// must never land in.
//
// This is not hygiene. EnforcementNotes names another NetworkPolicy in the same
// namespace as the way this Job's default-deny is silently defeated — policies
// are additive and cannot subtract from one another — so a namespace owned by
// the cluster's own components is the one place the deny is least able to hold.
// Every cluster ships more of those than the three Kubernetes creates under the
// prefix it reserves — the prefix is the reservation, not those three names.
func TestSecurity_ReservedNamespacePrefixIsRefused(t *testing.T) {
	refused := []string{
		// The three every cluster ships.
		"kube-system", "kube-public", "kube-node-lease",
		// The flannel CNI installs into kube-flannel.
		"kube-flannel",
		// The PREFIX is what is reserved, so the tail does not matter: these
		// are arbitrary names under it, not namespaces known to exist.
		"kube-apiserver", "kube-ovn", "kube-a",
		// OpenShift reserves its own prefix the same way, and enforces it:
		// the cluster refuses to create a project under openshift-. The tail
		// does not matter here either.
		"openshift-monitoring", "openshift-apiserver", "openshift-ingress", "openshift-a",
	}
	for _, ns := range refused {
		t.Run("refused/"+ns, func(t *testing.T) {
			s := validSpec()
			s.Namespace = ns

			// Assert on Render, not Validate: Render is the only surface that
			// produces bytes an operator can apply, so it is the one that has to
			// refuse.
			_, err := s.Render()
			if err == nil {
				t.Fatalf("Render() accepted namespace %q, want a rejection", ns)
			}
			// Pin all three parts the project requires of an error — what
			// failed, why, and how to fix it. Without the "additive" clause the
			// whole reason can be deleted and only the bare assertion "this is
			// reserved" survives, which is not something an operator can weigh.
			for _, want := range []string{ns, "reserved", "additive", "dedicated namespace"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error does not contain %q, so it does not explain what failed, why, and how to fix it:\n%v", want, err)
				}
			}
		})
	}

	// The guard has to be a PREFIX test. A substring or fuzzy one would refuse
	// ordinary namespaces an operator may legitimately dedicate to agent runs,
	// and a renderer that refuses the safe case teaches operators to work around
	// it. "default" belongs here too: `andbo k8s render` WARNS about it rather
	// than refusing, and its warning is printed before validation runs — so
	// refusing it here would print the advice and then reject the render anyway.
	//
	// A namespace that renders WITHOUT being one to dedicate to agent runs goes
	// in residualSystemNamespaces instead. What the guard does not refuse is not
	// the same as what it vetted, and this list is read as the second.
	allowed := []string{"kube", "andbo-kube-runs", "default", "andbo-runs"}
	for _, ns := range allowed {
		t.Run("allowed/"+ns, func(t *testing.T) {
			s := validSpec()
			s.Namespace = ns

			manifest, err := s.Render()
			if err != nil {
				t.Fatalf("Render() = %v, want nil for namespace %q", err, ns)
			}
			assertHardened(t, manifest)
			if got := dig(t, docs(t, manifest)[1], "metadata", "namespace"); got != ns {
				t.Errorf("Job metadata.namespace = %v, want %q", got, ns)
			}
		})
	}
}

// systemAddOnNamespaces are the exact names a cluster-wide privileged add-on
// conventionally installs into. None of them sits under a prefix any platform
// reserves, so each has to be named to be refused.
//
// kubeflow and kubernetes-dashboard are the two that make the case for naming
// them. Each is "kube" followed by something that is not the hyphen, so each
// misses the reserved prefix by a single character — a guard that only tested
// the prefix rendered them looking like names it had weighed and cleared.
//
// What any given cluster actually put in these is not something this renderer
// can know. The convention is the whole of the claim, and it is enough to
// refuse on: the cost of a wrong refusal is an operator renaming a namespace,
// and the cost of a wrong acceptance is an agent sharing a namespace with a
// component that holds cluster-wide privilege, under a default-deny that
// NetworkPolicy additivity will not let this Job enforce.
var systemAddOnNamespaces = []string{
	"kubeflow",
	"kubernetes-dashboard",
	"calico-system",
	"tigera-operator",
	"istio-system",
	"metallb-system",
	"openshift",
	"cattle-system",
	"gatekeeper-system",
	"kyverno",
	"cert-manager",
	"ingress-nginx",
	"velero",
}

// TestSecurity_SystemAddOnNamespacesAreRefused covers the names outside every
// reserved prefix that a privileged add-on still owns.
//
// The reason is the one on the prefix guard: EnforcementNotes names another
// NetworkPolicy in the same namespace as the way this Job's default-deny is
// silently defeated, because policies are additive and cannot subtract from one
// another. A namespace belonging to a component that holds cluster-wide
// privilege is exactly where that bites, and no prefix test reaches these.
func TestSecurity_SystemAddOnNamespacesAreRefused(t *testing.T) {
	for _, ns := range systemAddOnNamespaces {
		t.Run("refused/"+ns, func(t *testing.T) {
			s := validSpec()
			s.Namespace = ns

			// Render, not Validate: Render is the only surface that produces
			// bytes an operator can apply, so it is the one that has to refuse.
			_, err := s.Render()
			if err == nil {
				t.Fatalf("Render() accepted namespace %q, want a rejection", ns)
			}
			// What failed, why, and how to fix it. "privileg" is the why these
			// names are refused at all — without it the error asserts only that
			// the name is taken, which is hygiene, not a security reason.
			for _, want := range []string{ns, "privileg", "additive", "dedicated namespace"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error does not contain %q, so it does not explain what failed, why, and how to fix it:\n%v", want, err)
				}
			}
		})
	}

	// The match has to be EXACT. Namespace names are flat — Kubernetes gives
	// "cert-manager-runs" no relationship to "cert-manager" — so treating one of
	// these names as a prefix would refuse namespaces an operator may
	// legitimately dedicate to agent runs, and a renderer that refuses the safe
	// case teaches operators to work around it. Only a platform that actually
	// reserves a family gets a prefix test, which is why these are not in it.
	for _, ns := range []string{"cert-manager-runs", "velero-agent-runs", "kyverno-agent", "andbo-istio-system"} {
		t.Run("allowed/"+ns, func(t *testing.T) {
			s := validSpec()
			s.Namespace = ns

			manifest, err := s.Render()
			if err != nil {
				t.Fatalf("Render() = %v, want nil for namespace %q: these names are matched exactly, not as prefixes", err, ns)
			}
			assertHardened(t, manifest)
			if got := dig(t, docs(t, manifest)[1], "metadata", "namespace"); got != ns {
				t.Errorf("Job metadata.namespace = %v, want %q", got, ns)
			}
		})
	}
}

// residualSystemNamespaces are names a privileged project conventionally
// installs into that this guard still does NOT refuse, because they are neither
// under a reserved prefix nor named in systemAddOnNamespaces.
//
// cattle-fleet-system is the one to watch. Rancher spreads itself across a
// cattle-* family, but Rancher does not RESERVE that prefix the way Kubernetes
// reserves kube- and OpenShift reserves openshift-, so only the name this guard
// states — cattle-system — is refused, and the rest of the family renders.
// "openshift" without the hyphen is the same shape of gap against a prefix that
// is reserved.
//
// The list is illustrative, not exhaustive — no list could be, which is the
// point of a bound. It holds the names most likely to be mistaken for cleared
// ones.
var residualSystemNamespaces = []string{
	"argocd",
	"flux-system",
	"linkerd",
	"vault",
	"crossplane-system",
	"rook-ceph",
	"cattle-fleet-system",
}

// TestSecurity_ReservedNamespaceBoundIsDocumentedAndTrue covers the guard's
// stated BOUND rather than what it refuses.
//
// The guard refuses two reserved prefixes and a list of names, so every system
// namespace outside both still renders. That residual is a claim this package
// makes about itself — it is written on reservedNamespaceNames — and both
// halves of it need checking: every namespace this package files as residual
// has to appear in that bound, and each one has to actually still render.
//
// The first half is what keeps the two lists from disagreeing. It does not make
// either exhaustive, and neither claims to be: a name absent from BOTH is
// invisible to this test, so adding one here is how it gets checked.
func TestSecurity_ReservedNamespaceBoundIsDocumentedAndTrue(t *testing.T) {
	// The bound is prose, so read it where it is written rather than restating
	// it here — a copy in the test would drift from the one operators read.
	//
	// Match the ENUMERATION, not the whole comment: the names have to be in the
	// list the bound gives, and a name that only turns up in a later sentence is
	// not in it. Both anchors and the enclosing comment are load-bearing, so
	// rewording either fails this test rather than silently skipping it.
	const (
		listOpen  = "still render — "
		listClose = " — because this renderer"
	)
	src, err := os.ReadFile("validate.go")
	if err != nil {
		t.Fatalf("read validate.go: %v", err)
	}
	// Flatten to one line first so the check does not depend on where the prose
	// happens to wrap.
	flat := strings.Join(strings.Fields(strings.ReplaceAll(string(src), "//", " ")), " ")
	start := strings.Index(flat, listOpen)
	end := strings.Index(flat, listClose)
	if start < 0 || end < start {
		t.Fatalf("cannot find the enumerated residual in the doc comment on reservedNamespaceNames in validate.go, between %q and %q; that enumeration is the guard's stated bound and is what this test checks", listOpen, listClose)
	}
	enumerated := flat[start+len(listOpen) : end]

	for _, ns := range residualSystemNamespaces {
		if !strings.Contains(enumerated, ns) {
			t.Errorf("the guard's stated bound does not name %q, so an operator reading it would take that namespace for one the guard considered and cleared. It is not: it is a name a privileged add-on conventionally installs into, and it renders.\nbound names: %s", ns, enumerated)
		}
	}

	// The other half: the bound says these render, and nothing pinned that. A
	// broader guard would refuse them, which is a real change to who can run
	// where, and it must not happen without the bound being rewritten.
	for _, ns := range residualSystemNamespaces {
		t.Run("renders/"+ns, func(t *testing.T) {
			s := validSpec()
			s.Namespace = ns

			manifest, err := s.Render()
			if err != nil {
				t.Fatalf("Render() = %v, want nil: the guard covers the reserved prefixes and the names it states, and the stated bound says this namespace renders", err)
			}
			assertHardened(t, manifest)
			if got := dig(t, docs(t, manifest)[1], "metadata", "namespace"); got != ns {
				t.Errorf("Job metadata.namespace = %v, want %q", got, ns)
			}
		})
	}
}

// TestSecurity_TimeoutOverflowFailsClosed covers durations large enough that
// rounding up to whole seconds overflows int64 and yields a negative deadline —
// an unbounded run if the caller trusts the mapping without re-validating.
func TestSecurity_TimeoutOverflowFailsClosed(t *testing.T) {
	for _, d := range []time.Duration{math.MaxInt64, math.MaxInt64 - 1, math.MaxInt64 / 2} {
		t.Run(d.String(), func(t *testing.T) {
			got, err := FromRuntimeSpec(baseSpec(), containerSpec(), runtime.CommandSpec{
				Executable: "andbo-agent",
				Timeout:    d,
			})
			if err == nil {
				t.Fatalf("FromRuntimeSpec() accepted timeout %s, producing activeDeadlineSeconds=%d; want a rejection", d, got.ActiveDeadlineSeconds)
			}
		})
	}
}

// TestSecurity_UnmappableRuntimeFieldsFailClosed covers RuntimeSpec and
// CommandSpec fields that carry security intent this renderer cannot honour.
// Dropping them silently would produce a manifest that looks correct while
// having lost a control (or the workspace itself).
func TestSecurity_UnmappableRuntimeFieldsFailClosed(t *testing.T) {
	t.Run("allowed domains cannot be silently dropped", func(t *testing.T) {
		rs := containerSpec()
		rs.AllowedDomains = []string{"github.com"}

		if _, err := FromRuntimeSpec(baseSpec(), rs, runtime.CommandSpec{Executable: "andbo-agent"}); err == nil {
			t.Fatal("FromRuntimeSpec() silently dropped AllowedDomains, want a rejection")
		}
	})

	t.Run("allowed ports cannot be silently dropped", func(t *testing.T) {
		rs := containerSpec()
		rs.AllowedPorts = []int{8080}

		if _, err := FromRuntimeSpec(baseSpec(), rs, runtime.CommandSpec{Executable: "andbo-agent"}); err == nil {
			t.Fatal("FromRuntimeSpec() silently dropped AllowedPorts, want a rejection")
		}
	})

	t.Run("host working directory cannot be silently dropped", func(t *testing.T) {
		cs := runtime.CommandSpec{Executable: "andbo-agent", WorkingDir: "/Users/dev/project"}

		_, err := FromRuntimeSpec(baseSpec(), containerSpec(), cs)
		if err == nil {
			t.Fatal("FromRuntimeSpec() silently dropped CommandSpec.WorkingDir, want a rejection")
		}
		if !strings.Contains(strings.ToLower(err.Error()), "workspace") {
			t.Errorf("error = %q, want it to explain the missing workspace transport", err)
		}
	})
}

// TestSecurity_HostEnvIsNeverBridged covers the most serious finding from
// review: RuntimeSpec.Env carries RESOLVED SECRET VALUES in this codebase
// (buildAgentEnv reads every name in policy.Secrets.Allow out of the host
// environment), and envVar has no valueFrom, so the renderer cannot deliver
// them safely. Inlining them would write a live token into a plain-text
// manifest destined for a shared cluster and etcd.
func TestSecurity_HostEnvIsNeverBridged(t *testing.T) {
	t.Run("runtime env is rejected", func(t *testing.T) {
		rs := containerSpec()
		rs.Env = map[string]string{"GITHUB_TOKEN": "ghp_realtokenvalue"}

		_, err := FromRuntimeSpec(baseSpec(), rs, runtime.CommandSpec{Executable: "andbo-agent"})
		if err == nil {
			t.Fatal("FromRuntimeSpec() inlined host environment into the manifest, want a rejection")
		}
		if strings.Contains(err.Error(), "ghp_realtokenvalue") {
			t.Errorf("error leaks the secret value: %q", err)
		}
	})

	t.Run("command env is rejected", func(t *testing.T) {
		cs := runtime.CommandSpec{
			Executable: "andbo-agent",
			Env:        map[string]string{"ANTHROPIC_API_KEY": "sk-realkeyvalue"},
		}

		_, err := FromRuntimeSpec(baseSpec(), containerSpec(), cs)
		if err == nil {
			t.Fatal("FromRuntimeSpec() inlined command environment into the manifest, want a rejection")
		}
		if strings.Contains(err.Error(), "sk-realkeyvalue") {
			t.Errorf("error leaks the secret value: %q", err)
		}
	})

	t.Run("caller-authored base env still renders", func(t *testing.T) {
		base := baseSpec()
		base.Env = map[string]string{"ANDBO_RUN_ID": "01J0"}

		got, err := FromRuntimeSpec(base, containerSpec(), runtime.CommandSpec{Executable: "andbo-agent"})
		if err != nil {
			t.Fatalf("FromRuntimeSpec() = %v, want nil for caller-authored env", err)
		}
		if got.Env["ANDBO_RUN_ID"] != "01J0" {
			t.Errorf("Env = %v, want the caller's own literals preserved", got.Env)
		}
	})
}

// TestSecurity_HostWorkdirIsNeverBridged covers the same class as the host
// mount rejection: RuntimeSpec.Workdir is a HOST path (buildRuntimeSpec sets it
// to the workspace copy, meaningful in Docker only because of the bind mount
// this renderer refuses). A workspace is accepted only when the JobSpec
// declares how it reaches the pod, and even then the host path is remapped
// rather than rendered — see TestFromRuntimeSpec_AcceptsTheRealWorkspaceShape.
// With no transport declared it must fail rather than render a workspace path
// backed by an empty volume.
func TestSecurity_HostWorkdirIsNeverBridged(t *testing.T) {
	rs := containerSpec()
	rs.Workdir = "/Users/alice/.andbo/ws/abc123"

	_, err := FromRuntimeSpec(baseSpec(), rs, runtime.CommandSpec{Executable: "andbo-agent"})
	if err == nil {
		t.Fatal("FromRuntimeSpec() accepted a host working directory, want a rejection")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "host") {
		t.Errorf("error = %q, want it to explain that the path is a host path", err)
	}
}

// TestSecurity_RunAsUserIsBoundedToUidRange covers UIDs that are positive in
// int64 but truncate to 0 (root) in a 32-bit uid_t.
func TestSecurity_RunAsUserIsBoundedToUidRange(t *testing.T) {
	t.Run("validate rejects above the 32-bit range", func(t *testing.T) {
		for _, uid := range []int64{math.MaxInt32 + 1, 4294967296, math.MaxInt64} {
			s := validSpec()
			s.RunAsUser = uid

			if _, err := s.Render(); err == nil {
				t.Errorf("Render() accepted runAsUser %d, which truncates inside a 32-bit uid_t", uid)
			}
		}
	})

	t.Run("bridge rejects above the 32-bit range", func(t *testing.T) {
		rs := containerSpec()
		rs.User = "4294967296:4294967296"

		if _, err := FromRuntimeSpec(baseSpec(), rs, runtime.CommandSpec{Executable: "andbo-agent"}); err == nil {
			t.Error("FromRuntimeSpec() accepted a UID that truncates to root")
		}
	})

	t.Run("the top of the valid range still works", func(t *testing.T) {
		s := validSpec()
		s.RunAsUser = math.MaxInt32

		if err := s.Validate(); err != nil {
			t.Errorf("Validate() = %v, want nil for the highest valid UID", err)
		}
	})
}

// TestSecurity_WorkingDirCannotShadowSystemDirectories covers a working
// directory that mounts an empty volume over a system path, silently removing
// the CA trust store, /etc/passwd, or the image's binaries.
func TestSecurity_WorkingDirCannotShadowSystemDirectories(t *testing.T) {
	for _, dir := range []string{"/etc", "/etc/ssl", "/usr", "/usr/local/bin", "/bin", "/sbin", "/lib", "/var", "/proc", "/sys", "/dev", "/run", "/tmp", "/tmp/work"} {
		t.Run(dir, func(t *testing.T) {
			s := validSpec()
			s.WorkingDir = dir

			if _, err := s.Render(); err == nil {
				t.Fatalf("Render() accepted workingDir %q, which would shadow a system directory with an empty volume", dir)
			}
		})
	}

	for _, dir := range []string{"/work", "/workspace", "/home/agent", "/srv/repo", "/opt/andbo"} {
		t.Run("allowed"+dir, func(t *testing.T) {
			s := validSpec()
			s.WorkingDir = dir

			if err := s.Validate(); err != nil {
				t.Errorf("Validate() = %v, want nil for %q", err, dir)
			}
		})
	}
}

// TestSecurity_FromRuntimeSpecReturnsNothingUsableOnError makes the error path
// safe for a caller that ignores the error: the returned spec must not
// validate.
func TestSecurity_FromRuntimeSpecReturnsNothingUsableOnError(t *testing.T) {
	rs := containerSpec()
	rs.Privileged = true

	got, err := FromRuntimeSpec(baseSpec(), rs, runtime.CommandSpec{Executable: "andbo-agent"})
	if err == nil {
		t.Fatal("FromRuntimeSpec() = nil error, want a rejection")
	}
	if verr := got.Validate(); verr == nil {
		t.Error("the spec returned alongside the error validates; a caller ignoring the error would render it")
	}
}

// TestSecurity_ArgsAlwaysReflectTheCommandSpec covers argv leaking from the base
// spec when a new executable is supplied without arguments.
func TestSecurity_ArgsAlwaysReflectTheCommandSpec(t *testing.T) {
	base := baseSpec()
	base.Command = []string{"old-agent"}
	base.Args = []string{"--dangerous-flag"}

	got, err := FromRuntimeSpec(base, containerSpec(), runtime.CommandSpec{Executable: "andbo-agent"})
	if err != nil {
		t.Fatalf("FromRuntimeSpec() = %v, want nil", err)
	}
	if len(got.Args) != 0 {
		t.Errorf("Args = %v, want empty: the CommandSpec supplied none, so stale argv must not survive", got.Args)
	}
}

// TestSecurity_EnforcementNotesDoNotOverclaim covers the review finding that
// the notes asserted absolute network isolation. NetworkPolicies are additive,
// so a deny-all cannot subtract from another policy's allow.
func TestSecurity_EnforcementNotesDoNotOverclaim(t *testing.T) {
	notes := strings.ToLower(strings.Join(validSpec().EnforcementNotes(), "\n"))

	for _, want := range []struct{ topic, substr string }{
		{"NetworkPolicy additivity", "additive"},
		{"other policies in the namespace granting egress", "adminnetworkpolicy"},
		{"policy lifetime vs the pod", "lifetime"},
		{"at-most-once execution is not guaranteed", "at-most-once"},
		{"HOME is not writable", "home"},
	} {
		if !strings.Contains(notes, want.substr) {
			t.Errorf("enforcement notes do not mention %s (looking for %q):\n%s", want.topic, want.substr, notes)
		}
	}

	// The absolute claim the review flagged must be gone.
	if strings.Contains(notes, "cannot resolve or reach anything") {
		t.Error("enforcement notes still claim absolute network isolation, which NetworkPolicy additivity makes false")
	}
}

// TestSecurity_WorkingDirMustBeCanonical covers a bypass of the reserved-path
// denylist found in review: reservedMountPath compares raw strings, so any
// non-canonical spelling walks straight through it while the KERNEL still
// resolves the mount to the reserved directory.
func TestSecurity_WorkingDirMustBeCanonical(t *testing.T) {
	bypasses := []string{
		"/work/../etc",           // resolves to /etc: hides the CA trust store
		"//etc",                  // doubled separator
		"/./etc",                 // single-dot segment
		"/work/../tmp",           // collides with the renderer's own scratch volume
		"//tmp",                  //
		"/work/../usr/local/bin", // hides the image's binaries
		"/work/..//proc",         //
		"/work/",                 // trailing separator: same path, second spelling
		"/work/./sub",            //
	}

	for _, dir := range bypasses {
		t.Run(dir, func(t *testing.T) {
			s := validSpec()
			s.WorkingDir = dir

			if _, err := s.Render(); err == nil {
				t.Fatalf("Render() accepted non-canonical workingDir %q; the mount path is compared literally but resolves elsewhere at runtime", dir)
			}
		})
	}

	// Canonical paths outside the reserved set must still work.
	for _, dir := range []string{"/work", "/workspace", "/home/agent", "/srv/repo/sub"} {
		t.Run("allowed"+dir, func(t *testing.T) {
			s := validSpec()
			s.WorkingDir = dir

			if err := s.Validate(); err != nil {
				t.Errorf("Validate() = %v, want nil for canonical path %q", err, dir)
			}
		})
	}
}

// TestSecurity_QuantityGrammarMatchesKubernetes covers validator fidelity:
// strconv.ParseFloat is a strict superset of the Kubernetes quantity grammar,
// so forms Kubernetes rejects were passing validation here and failing later at
// apply time instead of at the boundary.
func TestSecurity_QuantityGrammarMatchesKubernetes(t *testing.T) {
	rejected := []string{
		"0x1p10", // hex float
		"1_000",  // underscore separators
		"1_0Gi",  //
		"1e3m",   // exponent combined with a decimalSI suffix
		"1e3Ki",  // exponent combined with a binarySI suffix
		"1e3Mi",  //
	}
	for _, q := range rejected {
		t.Run("reject/"+q, func(t *testing.T) {
			if v, err := parseQuantity(q); err == nil {
				t.Errorf("parseQuantity(%q) = %v, want an error: Kubernetes rejects this form", q, v)
			}
		})
	}

	// Forms Kubernetes accepts must keep working, with the right value.
	accepted := map[string]float64{
		"2": 2, "1.5": 1.5, "500m": 0.5, "1k": 1000, "5M": 5e6,
		"1Ki": 1024, "512Mi": 512 * 1024 * 1024, "2Gi": 2 * 1024 * 1024 * 1024,
		"1e3": 1000, "1E3": 1000, "1E": 1e18, "100m": 0.1, ".5": 0.5,
	}
	for q, want := range accepted {
		t.Run("accept/"+q, func(t *testing.T) {
			got, err := parseQuantity(q)
			if err != nil {
				t.Fatalf("parseQuantity(%q) = %v, want %v", q, err, want)
			}
			if got != want {
				t.Errorf("parseQuantity(%q) = %v, want %v", q, got, want)
			}
		})
	}
}
