package cli

import (
	"context"
	"encoding/json"
	"errors"
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

// doctorLine returns the human-report line for the named check, matched on the
// name column rather than on a rendered prefix. Asserting the ABSENCE of a
// "✓ config" substring instead would go quietly green the day the printf's
// spacing changed — while the check it names was still printed as passing.
func doctorLine(t *testing.T, out, name string) []string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if f := strings.Fields(line); len(f) >= 2 && f[1] == name {
			return f
		}
	}
	t.Fatalf("doctor printed no %q line at all:\n%s", name, out)
	return nil
}

// doctorPolicyCases are policies that `andbo policy check`, `run`, `exec`, and
// `k8s render` all refuse, plus the control that they all accept. Each `field`
// is what the user has to change, so a message that omits it leaves them with a
// verdict and nowhere to go.
//
// Doctor's contract is `andbo policy check`'s verdict, no wider — it is
// host-local and target-agnostic. `k8s render` refuses more (a budget over the
// 1440-minute activeDeadlineSeconds cap, `runtime.isolation: local`,
// `network.mode` allowlist/open, an agent needing environment variables), all of
// which `andbo run` accepts, so doctor reporting them would be a false alarm on
// the commoner path. Kept out of this table deliberately, not by oversight.
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

// run, exec, and k8s render refuse these policies too, and doctor is what a
// user reaches for when a run has just failed. Asserted separately from policy
// check because run and exec apply flag overrides before validating and k8s
// render validates on its own path, so any of them could diverge alone.
func TestDoctorReportsWhatRunExecAndK8sRenderRefuse(t *testing.T) {
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
			k8sOut, k8sErrOut, k8sErr := runK8s(t, okArgs()...)
			if CodeFor(k8sErr) == ExitOK {
				t.Fatalf("k8s render accepted the policy, so this case proves nothing:\n%s", k8sErrOut)
			}
			if k8sOut != "" {
				t.Errorf("a refused render must write nothing to stdout, got:\n%s", k8sOut)
			}

			got := doctorConfigCheck(t)
			if got.OK {
				t.Fatalf("doctor calls the policy valid after run, exec, and k8s render refused it:\n%s", got.Detail)
			}
			// Without this, a `config` line red for an unrelated reason — "no
			// andbo.yaml", say — satisfies the test, which would then prove
			// only that doctor is unhappy, not that it is unhappy about the
			// same field those three named.
			if !strings.Contains(got.Detail, tc.field) {
				t.Errorf("doctor does not name the field the other surfaces refused (%q):\n%s", tc.field, got.Detail)
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
	fields := doctorLine(t, out, "config")
	if fields[0] != "✗" {
		t.Errorf("the config line is marked %q, not failing:\n%s", fields[0], out)
	}
	if !strings.Contains(strings.Join(fields, " "), "network.mode") {
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

// TestAppleDoctorCheck covers the platform-scoped `container` row.
//
// Apple Container ships only for macOS on Apple silicon, so a missing binary on
// Linux is not a finding — it is the only possible answer, and a ✗ there would
// be noise for users who can never use this engine.
func TestAppleDoctorCheck(t *testing.T) {
	found := func(string) (string, error) { return "/usr/local/bin/container", nil }
	missing := func(string) (string, error) { return "", errors.New("not found") }
	version := func(v string) func() (string, error) {
		return func() (string, error) { return v, nil }
	}
	versionFails := func() (string, error) { return "", errors.New("sw_vers: no such file") }

	tests := []struct {
		name       string
		goos       string
		goarch     string
		macOSVer   func() (string, error)
		lookPath   func(string) (string, error)
		wantEmit   bool
		wantOK     bool
		wantDetail []string
		unwantedIn []string
	}{
		{
			name: "linux emits nothing", goos: "linux", goarch: "amd64",
			macOSVer: versionFails, lookPath: missing, wantEmit: false,
		},
		{
			name: "darwin arm64 with the binary", goos: "darwin", goarch: "arm64",
			macOSVer: version("26.5.2"), lookPath: found, wantEmit: true, wantOK: true,
			wantDetail: []string{"/usr/local/bin/container"},
		},

		{
			name: "a newer macOS is still usable", goos: "darwin", goarch: "arm64",
			macOSVer: version("27.0"), lookPath: found, wantEmit: true, wantOK: true,
			wantDetail: []string{"/usr/local/bin/container"},
		},
		{
			name: "missing binary is a finding", goos: "darwin", goarch: "arm64",
			macOSVer: version("26.5.2"), lookPath: missing, wantEmit: true, wantOK: false,
			wantDetail: []string{"not found on PATH", "--engine apple"},
		},
		{
			name: "intel mac cannot use it", goos: "darwin", goarch: "amd64",
			macOSVer: version("26.5.2"), lookPath: found, wantEmit: true, wantOK: false,
			wantDetail: []string{"Apple silicon", "darwin/amd64"},
		},
		// The point of the version row: an installed `container` binary on
		// macOS 25 must NOT be reported as usable. Doctor is what a user runs
		// after a failed run, so a ✓ here would send them hunting elsewhere.
		{
			name: "macOS 25 is reported incompatible despite the binary", goos: "darwin", goarch: "arm64",
			macOSVer: version("25.6"), lookPath: found, wantEmit: true, wantOK: false,
			wantDetail: []string{"macOS 26", "25.6"},
			unwantedIn: []string{"/usr/local/bin/container"},
		},
		{
			name: "an unparseable version is reported incompatible", goos: "darwin", goarch: "arm64",
			macOSVer: version(""), lookPath: found, wantEmit: true, wantOK: false,
			wantDetail: []string{"macOS 26"},
			unwantedIn: []string{"/usr/local/bin/container"},
		},
		{
			name: "a failed version lookup is reported, not assumed away", goos: "darwin", goarch: "arm64",
			macOSVer: versionFails, lookPath: found, wantEmit: true, wantOK: false,
			wantDetail: []string{"macOS 26", "sw_vers"},
			unwantedIn: []string{"/usr/local/bin/container"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			check, emitted := appleDoctorCheck(tt.goos, tt.goarch, tt.macOSVer, tt.lookPath)
			if emitted != tt.wantEmit {
				t.Fatalf("emitted = %v, want %v", emitted, tt.wantEmit)
			}
			if !tt.wantEmit {
				return
			}
			if check.Name != "container" {
				t.Errorf("check name = %q, want container (the binary name, matching the other rows)", check.Name)
			}
			// The row must fit doctor's %-14s column or the table misaligns.
			if len(check.Name) > 14 {
				t.Errorf("check name %q exceeds the 14-char column", check.Name)
			}
			if check.OK != tt.wantOK {
				t.Errorf("OK = %v, want %v", check.OK, tt.wantOK)
			}
			for _, want := range tt.wantDetail {
				if !strings.Contains(check.Detail, want) {
					t.Errorf("detail %q missing %q", check.Detail, want)
				}
			}
			for _, unwanted := range tt.unwantedIn {
				if strings.Contains(check.Detail, unwanted) {
					t.Errorf("detail %q reports %q as usable", check.Detail, unwanted)
				}
			}
			// Doctor prints one aligned row per check; a newline in the detail
			// lands under the table with no check name against it.
			if strings.Contains(check.Detail, "\n") {
				t.Errorf("detail %q spans multiple lines", check.Detail)
			}
		})
	}
}
