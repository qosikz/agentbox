package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// doctorProject writes pol as the project's andbo.yaml and chdirs into it.
func doctorProject(t *testing.T, pol string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "andbo.yaml"), []byte(pol), 0o644); err != nil {
		t.Fatal(err)
	}
	chdir(t, dir)
	return dir
}

// doctorConfigCheck runs `andbo doctor --json` and returns its `config` check.
// The JSON form is used because it carries the verdict as a field rather than
// as a glyph, and it is what a pipeline reads.
func doctorConfigCheck(t *testing.T) doctorCheck {
	t.Helper()
	out, err := captureStdout(t, func() error {
		return NewRoot("test", "none", "now").cmdDoctor([]string{"--json"})
	})
	if err != nil {
		t.Fatalf("doctor --json failed: %v\n%s", err, out)
	}
	var checks []doctorCheck
	if jerr := json.Unmarshal([]byte(out), &checks); jerr != nil {
		t.Fatalf("doctor --json is not valid JSON: %v\n%s", jerr, out)
	}
	for _, c := range checks {
		if c.Name == "config" {
			return c
		}
	}
	t.Fatalf("doctor reported no `config` check:\n%s", out)
	return doctorCheck{}
}

// doctorPolicyCases are policies that `andbo policy check`, `run`, `exec`, and
// `k8s render` all refuse, plus the control that they all accept. Each `field`
// is what the user has to change, so a message that omits it leaves them with a
// verdict and nowhere to go.
var doctorPolicyCases = []struct {
	name  string
	pol   string
	valid bool
	field string
}{
	{"unknown network mode", "network:\n  mode: bogus\n", false, "network.mode"},
	{"unknown secrets mode", "secrets:\n  mode: env\n", false, "secrets.mode"},
	{"unknown runtime engine", "runtime:\n  engine: containerd\n", false, "runtime.engine"},
	{"empty runtime image", "runtime:\n  image: \"\"\n", false, "runtime.image"},
	{"negative budget", "budget:\n  max_runtime_minutes: -30\n", false, "budget.max_runtime_minutes"},
	// Above what a run deadline can hold. Refused by policy check, run, and exec
	// through checkBudgetMinutes rather than by config.Check, so it is the case
	// that catches a doctor wired to only half the validation.
	{"budget beyond an enforceable deadline", "budget:\n  max_runtime_minutes: " + strconv.Itoa(maxBudgetMinutes+1) + "\n", false, "budget.max_runtime_minutes"},
	{"secure defaults", "budget:\n  max_runtime_minutes: 30\n", true, ""},
}

// `andbo doctor` is the first thing a user runs when something looks wrong, and
// it reported `config: ✓ andbo.yaml valid` for policies every other surface
// refuses — it only asked whether the YAML parsed. That verdict sends someone
// looking at Docker, at their agent, at anything but the file that is actually
// broken.
//
// The assertion is agreement with `andbo policy check`, not a fixed list of
// invalid values: a validation rule added there later must not be able to drift
// away from doctor again without turning this red.
func TestDoctorAgreesWithPolicyCheckOnPolicyValidity(t *testing.T) {
	for _, tc := range doctorPolicyCases {
		t.Run(tc.name, func(t *testing.T) {
			doctorProject(t, tc.pol)

			// The other surfaces are consulted first: a case they happen to
			// accept would make the doctor assertion below vacuous, and this is
			// exactly the pairing the milestone is about.
			checkOut, checkErr := captureStdout(t, func() error {
				return NewRoot("test", "none", "now").cmdPolicy([]string{"check"})
			})
			if (CodeFor(checkErr) == ExitOK) != tc.valid {
				t.Fatalf("policy check disagrees with the fixture: exit=%d, want valid=%v\n%s",
					CodeFor(checkErr), tc.valid, checkOut)
			}

			got := doctorConfigCheck(t)
			if got.OK != tc.valid {
				t.Fatalf("doctor config check OK = %v, want %v (policy check exit %d)\ndetail: %s",
					got.OK, tc.valid, CodeFor(checkErr), got.Detail)
			}
			if !tc.valid {
				if !strings.Contains(got.Detail, tc.field) {
					t.Errorf("doctor does not name the field to fix (%q):\n%s", tc.field, got.Detail)
				}
				// Doctor prints one aligned line per check, so an embedded
				// newline lands as an unlabelled line under the table. The
				// budget refusal carries its fix on a second line and is the
				// case that would do it.
				if strings.Contains(got.Detail, "\n") {
					t.Errorf("doctor detail spans multiple lines, which breaks the check table:\n%q", got.Detail)
				}
			}
		})
	}
}

// run and exec refuse these policies too, and doctor is what a user reaches for
// when a run fails. Asserted separately from policy check because run applies
// flag overrides before validating, so it could diverge on its own.
func TestDoctorAgreesWithRunAndExecOnPolicyValidity(t *testing.T) {
	for _, tc := range doctorPolicyCases {
		if tc.valid {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			doctorProject(t, tc.pol)

			runOut, runErr := captureStderr(t, func() error {
				return NewRoot("test", "none", "now").cmdRun(context.Background(), []string{"fix failing tests", "--dry-run"})
			})
			if CodeFor(runErr) == ExitOK {
				t.Fatalf("run accepted the policy, so this case proves nothing:\n%s", runOut)
			}
			execOut, execErr := captureStderr(t, func() error {
				return NewRoot("test", "none", "now").cmdExec(context.Background(), []string{"echo hi", "--dry-run"})
			})
			if CodeFor(execErr) == ExitOK {
				t.Fatalf("exec accepted the policy, so this case proves nothing:\n%s", execOut)
			}

			if got := doctorConfigCheck(t); got.OK {
				t.Errorf("doctor calls the policy valid after run and exec refused it:\n%s", got.Detail)
			}
		})
	}
}

// The human report is what a user actually reads. It derives its glyph from the
// same field the JSON carries, but the detail is only visible here.
func TestDoctorHumanReportMarksAnInvalidPolicy(t *testing.T) {
	doctorProject(t, "network:\n  mode: bogus\n")
	out, err := captureStdout(t, func() error {
		return NewRoot("test", "none", "now").cmdDoctor(nil)
	})
	if err != nil {
		t.Fatalf("doctor failed: %v\n%s", err, out)
	}
	if strings.Contains(out, "✓ config") {
		t.Errorf("doctor marks the config check as passing:\n%s", out)
	}
	if !strings.Contains(out, "network.mode") {
		t.Errorf("doctor does not name the broken field:\n%s", out)
	}
}

// doctor is a diagnostic, not a gate: it reports every check it can and exits
// 0, including when it found nothing at all (no docker, no agent CLI, no
// andbo.yaml). An invalid policy is reported the same way, so a setup script
// that runs doctor before init does not start failing.
func TestDoctorStillExitsZeroOnAnInvalidPolicy(t *testing.T) {
	doctorProject(t, "network:\n  mode: bogus\n")
	if _, err := captureStdout(t, func() error {
		return NewRoot("test", "none", "now").cmdDoctor(nil)
	}); err != nil {
		t.Errorf("doctor returned an error: %v", err)
	}
}
