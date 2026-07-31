package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qosikz/andbo/internal/adapters"
)

// agentProject writes a project whose only policy statement is agent.default —
// everything else stays on the secure built-in defaults — and chdirs into it.
// The value is written as a raw YAML scalar so a test can express the empty
// string, which is a different failure from a misspelt one and is only
// reachable by writing it out.
func agentProject(t *testing.T, yamlValue string) string {
	t.Helper()
	dir := t.TempDir()
	pol := "agent:\n  default: " + yamlValue + "\n"
	if err := os.WriteFile(filepath.Join(dir, "andbo.yaml"), []byte(pol), 0o644); err != nil {
		t.Fatal(err)
	}
	chdir(t, dir)
	return dir
}

// agentDefaultCases are agent.default values no adapter answers to. `run` and
// `k8s render` already refuse all three at adapters.Get; the milestone is that
// the two commands a pipeline runs FIRST refuse them too.
var agentDefaultCases = []struct {
	name  string
	yaml  string // as written in andbo.yaml
	value string // as the policy decodes it
}{
	{"a misspelt adapter name", "bogus", "bogus"},
	// adapters.Get switches on an exact string, so this resolves to nothing
	// while looking, to the person who wrote it, like the supported name.
	{"a supported name in the wrong case", "Custom", "Custom"},
	// Reachable only by writing it: the built-in default is "custom", and a
	// policy that omits the key keeps it.
	{"explicitly empty", `""`, ""},
}

// assertNamesTheAgentFault checks a refusal a user can act on: the field to
// change, the value they wrote, and the set of names that would work. A verdict
// without those three is a dead end — doctor and policy check are exactly the
// commands someone runs when they do not yet know what is wrong.
func assertNamesTheAgentFault(t *testing.T, text, value string) {
	t.Helper()
	needles := []string{"agent.default", fmt.Sprintf("%q", value)}
	needles = append(needles, adapters.SupportedNames()...)
	for _, needle := range needles {
		if !strings.Contains(text, needle) {
			t.Errorf("refusal does not name %q:\n%s", needle, text)
		}
	}
}

// `andbo policy check` is the gate a pipeline runs BEFORE anything executes. It
// called a policy valid whose agent.default names no adapter, and `andbo run`
// and `andbo k8s render` then died at exit 4 — the same silent gap between
// surfaces that checkBudgetMinutes exists to close, one field over.
func TestPolicyCheckRefusesAnAgentDefaultNoAdapterAnswersTo(t *testing.T) {
	for _, tc := range agentDefaultCases {
		t.Run(tc.name, func(t *testing.T) {
			agentProject(t, tc.yaml)

			out, err := captureStdout(t, func() error {
				return NewRoot("test", "none", "now").cmdPolicy([]string{"check"})
			})
			// policy check keeps its own contract for an invalid policy: an
			// invalid-config exit, not the ExitAgentFailed run and k8s render
			// return when the adapter itself cannot be built.
			if CodeFor(err) != ExitInvalidConfig {
				t.Fatalf("exit code = %d, want %d (err=%v)\n%s", CodeFor(err), ExitInvalidConfig, err, out)
			}
			if strings.Contains(out, "✓ Policy valid") {
				t.Errorf("policy check still printed its valid line:\n%s", out)
			}
			assertNamesTheAgentFault(t, out, tc.value)
		})
	}
}

// The JSON form is what a CI gate reads: it carries the verdict as a field
// rather than as a glyph, and the reason has to travel with it.
func TestPolicyCheckJSONMarksAnAgentDefaultInvalid(t *testing.T) {
	for _, tc := range agentDefaultCases {
		t.Run(tc.name, func(t *testing.T) {
			agentProject(t, tc.yaml)

			out, err := captureStdout(t, func() error {
				return NewRoot("test", "none", "now").cmdPolicy([]string{"check", "--json"})
			})
			if CodeFor(err) != ExitInvalidConfig {
				t.Fatalf("exit code = %d, want %d (err=%v)\n%s", CodeFor(err), ExitInvalidConfig, err, out)
			}
			var got struct {
				Valid  bool     `json:"valid"`
				Errors []string `json:"errors"`
			}
			if jerr := json.Unmarshal([]byte(out), &got); jerr != nil {
				t.Fatalf("policy check --json is not valid JSON: %v\n%s", jerr, out)
			}
			if got.Valid {
				t.Fatalf("policy check --json reports valid=true:\n%s", out)
			}
			assertNamesTheAgentFault(t, strings.Join(got.Errors, "\n"), tc.value)
		})
	}
}

// `andbo doctor` is what a user reaches for after a run has failed, and it
// reported `config: ✓ andbo.yaml valid` for exactly the policy that made the
// run fail. Its verdict is `andbo policy check`'s, so it has to move with it.
func TestDoctorReportsAnAgentDefaultNoAdapterAnswersTo(t *testing.T) {
	for _, tc := range agentDefaultCases {
		t.Run(tc.name, func(t *testing.T) {
			agentProject(t, tc.yaml)

			// doctorConfigCheck fails the test if doctor returns an error, so
			// this also holds doctor to diagnosing rather than gating.
			got := doctorConfigCheck(t)
			if got.OK {
				t.Fatalf("doctor calls the policy valid: %s", got.Detail)
			}
			assertNamesTheAgentFault(t, got.Detail, tc.value)
			// Doctor prints one aligned line per check; a newline in the detail
			// lands under the table with no check name against it.
			if strings.Contains(got.Detail, "\n") {
				t.Errorf("doctor detail spans multiple lines, which breaks the check table:\n%q", got.Detail)
			}
		})
	}
}

// The point of the milestone: the surfaces that refuse and the surfaces that
// gate agree. Each case first asserts run and k8s render refuse it, so a
// fixture that stopped being invalid fails loudly instead of quietly proving
// nothing — and their exit code is asserted unchanged, because this milestone
// moves the gates forward, it does not renumber what was already refused.
func TestAgentDefaultIsRefusedBeforeRunAndK8sRender(t *testing.T) {
	for _, tc := range agentDefaultCases {
		t.Run(tc.name, func(t *testing.T) {
			agentProject(t, tc.yaml)

			runOut, runErr := captureStderr(t, func() error {
				return NewRoot("test", "none", "now").cmdRun(context.Background(), []string{"fix failing tests", "--dry-run"})
			})
			if CodeFor(runErr) != ExitAgentFailed {
				t.Fatalf("run exit code = %d, want %d (err=%v)\n%s", CodeFor(runErr), ExitAgentFailed, runErr, runOut)
			}
			k8sOut, k8sErrOut, k8sErr := runK8s(t, okArgs()...)
			if CodeFor(k8sErr) != ExitAgentFailed {
				t.Fatalf("k8s render exit code = %d, want %d (err=%v)\n%s", CodeFor(k8sErr), ExitAgentFailed, k8sErr, k8sErrOut)
			}
			if k8sOut != "" {
				t.Errorf("a refused render must write nothing to stdout, got:\n%s", k8sOut)
			}

			checkOut, checkErr := captureStdout(t, func() error {
				return NewRoot("test", "none", "now").cmdPolicy([]string{"check"})
			})
			if CodeFor(checkErr) == ExitOK {
				t.Errorf("policy check accepts a policy run and k8s render refuse:\n%s", checkOut)
			}
			if got := doctorConfigCheck(t); got.OK {
				t.Errorf("doctor accepts a policy run and k8s render refuse: %s", got.Detail)
			}
		})
	}
}

// The other half of the contract, and the one a false positive would break: the
// set policy check accepts is the adapter registry's, not a second list beside
// it. An adapter added to adapters.SupportedNames later is accepted here with
// no edit; one that stops resolving turns this red rather than quietly making
// every policy naming it unrunnable-but-valid.
func TestEverySupportedAgentPassesPolicyCheckAndDoctor(t *testing.T) {
	for _, name := range adapters.SupportedNames() {
		t.Run(name, func(t *testing.T) {
			agentProject(t, name)

			out, err := captureStdout(t, func() error {
				return NewRoot("test", "none", "now").cmdPolicy([]string{"check"})
			})
			if err != nil {
				t.Fatalf("policy check refuses supported agent %q: exit=%d err=%v\n%s", name, CodeFor(err), err, out)
			}
			if !strings.Contains(out, "✓ Policy valid") {
				t.Errorf("policy check did not print its valid line for %q:\n%s", name, out)
			}
			if got := doctorConfigCheck(t); !got.OK {
				t.Errorf("doctor refuses supported agent %q: %s", name, got.Detail)
			}
		})
	}
}

// --agent overrides agent.default for a single run, and does so BEFORE the
// adapter is resolved. Validating the raw file's agent.default inside
// config.Check would break this: run calls Check before it applies overrides,
// so a policy with a bad default plus a good --agent would stop working.
func TestAgentFlagStillOverridesABrokenPolicyDefault(t *testing.T) {
	agentProject(t, "bogus")

	out, err := captureStderr(t, func() error {
		return NewRoot("test", "none", "now").cmdRun(context.Background(),
			[]string{"fix failing tests", "--dry-run", "--agent", "custom"})
	})
	if err != nil {
		t.Fatalf("run --agent custom failed: exit=%d err=%v\n%s", CodeFor(err), err, out)
	}
}
