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
			// Asserting the absence of the exact success string would weld this
			// to that one phrase: a reworded "✓ Policy OK" printed beside the
			// errors would pass. No line of a refusal may open with the success
			// glyph at all — errors, unsafe options, and enforcement notes each
			// carry their own.
			for _, line := range strings.Split(out, "\n") {
				if strings.HasPrefix(line, "✓") {
					t.Errorf("a refusal printed a success line %q:\n%s", line, out)
				}
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

			// exec is deliberately outside the agreement: it resolves no adapter
			// at all, because the caller supplies the command and is therefore
			// the agent. So the gates now refuse a policy exec runs to
			// completion. Pinned in both directions — as a decision rather than
			// an accident — since every other invalid-policy case in this package
			// asserts that exec refuses too.
			execOut, execErr := captureStderr(t, func() error {
				return NewRoot("test", "none", "now").cmdExec(context.Background(), []string{"echo hi", "--dry-run"})
			})
			if execErr != nil {
				t.Errorf("exec refuses a policy whose agent it never consults: exit=%d err=%v\n%s",
					CodeFor(execErr), execErr, execOut)
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

// The refusal echoes a value the POLICY FILE controls into doctor's aligned
// check table, a surface this milestone put it on for the first time. %q is the
// only thing standing between an untrusted andbo.yaml and live control bytes
// there: a carriage return plus an erase-line escape rewrites the row it is
// printed on, so a policy could erase its own ✗ and leave behind a row that
// reads as passing. Dropping the ESCAPING while keeping the quotes — %q for
// \"%s\" — leaves every other assertion in this file, and in the rest of the
// repo, green: the tests above match on fmt.Sprintf("%q", value), so they pin
// the quotes and not the escaping. Plain %s reddens those three as well, but
// only because it also drops the quotes they match on. This pins the escaping
// on its own.
//
// Asserted on the two surfaces this commit created: doctor's detail and policy
// check's error strings. `policy check`'s human report separately echoes the
// agent name raw in its "Agent:" line (internal/policy/effective.go), which
// predates this change and is not what this test covers.
func TestAgentDefaultCannotSmuggleControlBytesIntoTheReport(t *testing.T) {
	// A double-quoted YAML scalar: ESC, erase-line, CR, then a forged row, and
	// a NUL for good measure.
	agentProject(t, `"x\e[2K\r\0  ✓ config         andbo.yaml valid"`)

	// Newlines are not in this set: the error carries its fix on a second line,
	// from the format string rather than from the policy. Doctor flattens those,
	// and that is asserted separately below.
	const raw = "\x1b\r\x00"
	got := doctorConfigCheck(t)
	if got.OK {
		t.Fatalf("doctor calls the hostile policy valid: %s", got.Detail)
	}
	if strings.ContainsAny(got.Detail, raw+"\n") {
		t.Errorf("doctor's check table carries raw control bytes from the policy:\n%q", got.Detail)
	}

	out, err := captureStdout(t, func() error {
		return NewRoot("test", "none", "now").cmdPolicy([]string{"check", "--json"})
	})
	if CodeFor(err) != ExitInvalidConfig {
		t.Fatalf("exit code = %d, want %d\n%s", CodeFor(err), ExitInvalidConfig, out)
	}
	var decoded struct {
		Errors []string `json:"errors"`
	}
	if jerr := json.Unmarshal([]byte(out), &decoded); jerr != nil {
		t.Fatalf("policy check --json is not valid JSON: %v\n%s", jerr, out)
	}
	if len(decoded.Errors) == 0 {
		t.Fatalf("policy check --json reported no errors for a policy it refused:\n%s", out)
	}
	for _, e := range decoded.Errors {
		if strings.ContainsAny(e, raw) {
			t.Errorf("policy check's error carries raw control bytes from the policy:\n%q", e)
		}
	}
}

// The other half of the contract, and the one a false positive would break: the
// set policy check accepts is the adapter registry's, not a second list beside
// it. An adapter added to adapters.SupportedNames later is accepted here with
// no edit; one that stops resolving turns this red rather than quietly making
// every policy naming it unrunnable-but-valid.
func TestEverySupportedAgentPassesPolicyCheckAndDoctor(t *testing.T) {
	// The whole table is that one call, so an empty return would run zero
	// subtests and report PASS — and would silently drop the same names from
	// assertNamesTheAgentFault's needles.
	if len(adapters.SupportedNames()) == 0 {
		t.Fatal("adapters.SupportedNames() is empty; this test would assert nothing")
	}
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
//
// "The run did not fail" is too weak an assertion to carry that: a run honouring
// the flag but resolving some OTHER adapter also exits 0, and would have Andbo's
// own session record name one agent while a different one executed. The plan is
// asserted instead, on stdout — cmdRun RETURNS its error rather than printing
// it, so stderr is empty on both paths and proves nothing either way.
func TestAgentFlagStillOverridesABrokenPolicyDefault(t *testing.T) {
	agentProject(t, "bogus")

	out, err := captureStdout(t, func() error {
		return NewRoot("test", "none", "now").cmdRun(context.Background(),
			[]string{"fix failing tests", "--dry-run", "--agent", "custom"})
	})
	if err != nil {
		t.Fatalf("run --agent custom failed: exit=%d err=%v\n%s", CodeFor(err), err, out)
	}
	// The command line the custom adapter builds from the policy's echo stub.
	// This is what would actually have run, not what the run says it selected.
	if !strings.Contains(out, "exec: echo fix failing tests") {
		t.Errorf("run reports no plan built by the agent --agent named:\n%s", out)
	}
	if !strings.Contains(out, "Agent: custom") {
		t.Errorf("run does not record the agent --agent named:\n%s", out)
	}
}
