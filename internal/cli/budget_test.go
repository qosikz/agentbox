package cli

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// budgetProject creates a temp project whose only policy statement is the
// runtime budget — everything else stays on the secure built-in defaults — and
// chdirs into it.
func budgetProject(t *testing.T, minutes string) string {
	t.Helper()
	dir := t.TempDir()
	pol := "budget:\n  max_runtime_minutes: " + minutes + "\ntests:\n  commands: []\n"
	if err := os.WriteFile(filepath.Join(dir, "andbo.yaml"), []byte(pol), 0o644); err != nil {
		t.Fatal(err)
	}
	chdir(t, dir)
	return dir
}

// budgetOverflowCases are the budgets whose naive minutes*time.Minute wraps.
// The wrap is cyclic, so these are sampled outcomes rather than an ordered
// progression: 153722868 is the FIRST value over the bound and already lands
// far negative, while 2^53 lands on exactly zero. Each is kept because the
// resulting window is a different lie — a few seconds, none at the runner
// layer, or a negative — and all three were reported as the policy's own bound.
var budgetOverflowCases = []struct{ name, minutes string }{
	{"one past the largest representable budget", strconv.Itoa(maxBudgetMinutes + 1)},
	{"wraps to a few seconds", "153722867281"},
	{"wraps to exactly zero", "9007199254740992"},
	{"wraps negative", "200000000000"},
	{"the largest int64", strconv.FormatInt(math.MaxInt64, 10)},
}

// assertBudgetRefused checks the refusal is the one a user can act on: the
// policy-violation exit code (the same one `andbo k8s render` uses for a budget
// it cannot bound), and a message naming the field, the value written, the
// maximum, and the file to change.
func assertBudgetRefused(t *testing.T, err error, minutes string) {
	t.Helper()
	if CodeFor(err) != ExitPolicyViolation {
		t.Fatalf("exit code = %d, want %d (err=%v)", CodeFor(err), ExitPolicyViolation, err)
	}
	for _, needle := range []string{"budget.max_runtime_minutes", minutes, strconv.Itoa(maxBudgetMinutes), "andbo.yaml"} {
		if !strings.Contains(err.Error(), needle) {
			t.Errorf("error does not name %q:\n%v", needle, err)
		}
	}
}

// budgetWindow converts minutes into a time.Duration, which counts NANOSECONDS
// in an int64: a minute costs 6e10 of that range, so the product wraps outside
// ±153.7 million minutes. Both directions are wrong in a way the container and
// local runners cannot see, because they gate on `if command.Timeout > 0`
// (internal/runtime/local.go, internal/runtime/docker.go): wrapping to zero or
// negative drops the deadline at that layer, and wrapping the other way hands
// them a short window that reads as enforced. Total means every int maps to the
// bound the policy asked for, or to 0 where it asked for none.
func TestBudgetWindowIsTotal(t *testing.T) {
	cases := []struct {
		name    string
		minutes int64
	}{
		{"unset", 0},
		{"one minute", 1},
		{"the default policy", 30},
		{"a day", 1440},
		{"the largest representable budget", int64(maxBudgetMinutes)},
		{"one past the largest representable budget", int64(maxBudgetMinutes) + 1},
		// 2^53 minutes * 6e10 ns is exactly 2^64, so the product wraps to ZERO —
		// which is how the local and container runners spell "no deadline".
		// (The k8s bridge gates on `!= 0` and substitutes its own default, so
		// zero means something different again over there.)
		{"wraps to exactly zero", 1 << 53},
		{"wraps to a few seconds", 153722867281},
		{"wraps negative", 200000000000},
		{"the largest int64", math.MaxInt64},
		// The negative half wraps too, and wraps POSITIVE — the direction that
		// survives the runners' `Timeout > 0` gate and so reads as enforced.
		// -2^53+1 is the sharpest: it produces a plausible one-minute window.
		{"a negative budget", -1},
		{"a negative budget wrapping positive", -153722868},
		{"a negative budget wrapping to a plausible minute", -9007199254740991},
		// MinInt64 already produced 0 before the clamp — -2^63 * 6e10 is a
		// multiple of 2^64 — so it pins a coincidence, not the fix. MinInt64+1
		// is the one that used to come back as a plausible one-minute window.
		{"the smallest int64", math.MinInt64},
		{"one above the smallest int64", math.MinInt64 + 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if (tc.minutes > math.MaxInt32 || tc.minutes < math.MinInt32) && strconv.IntSize < 64 {
				t.Skip("this budget does not fit in an int on this platform")
			}
			got := budgetWindow(int(tc.minutes))
			if tc.minutes <= 0 {
				// At or below zero is how the policy spells "no deadline", and
				// every caller gates on `> 0`. The conversion must say exactly
				// that, not a window arithmetic invented out of a sign flip.
				if got != 0 {
					t.Fatalf("budgetWindow(%d) = %v, want 0; a non-positive budget means no deadline", tc.minutes, got)
				}
				return
			}
			if tc.minutes <= int64(maxBudgetMinutes) {
				if want := time.Duration(tc.minutes) * time.Minute; got != want {
					t.Fatalf("budgetWindow(%d) = %v, want %v", tc.minutes, got, want)
				}
				return
			}
			// Unrepresentable. Callers reject these before they get here; the
			// clamp is what keeps a caller that forgets from getting a wrapped
			// window instead of the longest one that can actually be held.
			if got <= 0 {
				t.Fatalf("budgetWindow(%d) = %v; a non-positive window disables the runner deadline", tc.minutes, got)
			}
			if want := time.Duration(maxBudgetMinutes) * time.Minute; got != want {
				t.Fatalf("budgetWindow(%d) = %v, want it clamped to %v", tc.minutes, got, want)
			}
		})
	}
}

// A budget Andbo cannot convert into a faithful deadline must stop the run
// before anything executes. Without the guard the run started under a wrapped
// window and, when that window expired seconds later, reported "the run hit
// budget.max_runtime_minutes (153722867281)" — a bound it was never given.
func TestRunRefusesABudgetItCannotEnforce(t *testing.T) {
	for _, tc := range budgetOverflowCases {
		t.Run(tc.name, func(t *testing.T) {
			budgetProject(t, tc.minutes)
			r := NewRoot("test", "none", "now")
			err := r.cmdRun(context.Background(), []string{"fix failing tests", "--dry-run"})
			assertBudgetRefused(t, err, tc.minutes)
		})
	}
}

// exec is the same runtime under a different front door — it derives the same
// deadline from the same field — so it must refuse the same policy identically.
func TestExecRefusesABudgetItCannotEnforce(t *testing.T) {
	for _, tc := range budgetOverflowCases {
		t.Run(tc.name, func(t *testing.T) {
			budgetProject(t, tc.minutes)
			r := NewRoot("test", "none", "now")
			err := r.cmdExec(context.Background(), []string{"echo hi", "--dry-run"})
			assertBudgetRefused(t, err, tc.minutes)
		})
	}
}

// silenceStdin points os.Stdin at /dev/null so an unsafe-confirmation prompt
// reaches EOF immediately. Without it, a regression that let the run past the
// budget check would BLOCK at the prompt when the suite runs in a terminal
// instead of failing.
func silenceStdin(t *testing.T) {
	t.Helper()
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	prev := os.Stdin
	os.Stdin = devNull
	t.Cleanup(func() { os.Stdin = prev; devNull.Close() })
}

// The refusal must land before the unsafe confirmation, not after: an
// unenforceable budget is not something the user should be asked to accept risk
// for first. Both commands are covered, because --dry-run turns the unsafe gate
// into a warning and so cannot detect the ordering on its own.
func TestUnenforceableBudgetIsRefusedBeforeTheUnsafeGate(t *testing.T) {
	const minutes = "153722867281"
	// --runtime local is unsafe and deliberately given WITHOUT --yes-unsafe: if
	// the budget check ran after the gate, these would fail as
	// unconfirmed-unsafe (exit 8) rather than naming the budget.
	cases := map[string]func(*Root) error{
		"run": func(r *Root) error {
			return r.cmdRun(context.Background(), []string{"fix failing tests", "--runtime", "local"})
		},
		"exec": func(r *Root) error { return r.cmdExec(context.Background(), []string{"echo hi", "--runtime", "local"}) },
	}
	for name, invoke := range cases {
		t.Run(name, func(t *testing.T) {
			budgetProject(t, minutes)
			silenceStdin(t)
			assertBudgetRefused(t, invoke(NewRoot("test", "none", "now")), minutes)
		})
	}
}

// The boundary itself must still be accepted: the guard rejects what cannot be
// represented, not what is merely large. (Dry-run, so this proves the CHECK
// lets it through — nothing executes under a 292-year window.)
func TestRunAcceptsTheLargestRepresentableBudget(t *testing.T) {
	budgetProject(t, strconv.Itoa(maxBudgetMinutes))
	r := NewRoot("test", "none", "now")
	if err := r.cmdRun(context.Background(), []string{"fix failing tests", "--dry-run"}); err != nil {
		t.Fatalf("max_runtime_minutes=%d is representable and must be accepted: %v (code %d)",
			maxBudgetMinutes, err, CodeFor(err))
	}
}

// A budget of zero or below is the documented way to say "no deadline", and
// this guard must not quietly change that. Pinned so the meaning of the values
// on the other boundary is a decision, not an accident.
func TestNonPositiveBudgetStillMeansNoDeadline(t *testing.T) {
	for _, minutes := range []string{"0", "-5"} {
		t.Run("minutes="+minutes, func(t *testing.T) {
			budgetProject(t, minutes)
			r := NewRoot("test", "none", "now")
			if err := r.cmdRun(context.Background(), []string{"fix failing tests", "--dry-run"}); err != nil {
				t.Fatalf("max_runtime_minutes=%s means no deadline and must be accepted: %v", minutes, err)
			}
		})
	}
}

// `andbo policy check` is the gate a pipeline runs BEFORE anything executes, so
// a budget run and exec will refuse has to be an error there too. Reporting the
// policy valid and then dying on the first real run is the failure this whole
// slice is about, one step earlier.
func TestPolicyCheckRefusesABudgetRunCannotEnforce(t *testing.T) {
	for _, tc := range budgetOverflowCases {
		t.Run(tc.name, func(t *testing.T) {
			budgetProject(t, tc.minutes)
			r := NewRoot("test", "none", "now")
			out, err := captureStdout(t, func() error { return r.cmdPolicy([]string{"check"}) })
			// policy check keeps its own contract for an invalid policy (exit 7);
			// what must not happen is a clean bill of health.
			if CodeFor(err) != ExitInvalidConfig {
				t.Fatalf("exit code = %d, want %d (err=%v)\n%s", CodeFor(err), ExitInvalidConfig, err, out)
			}
			// Exit code alone would be satisfied by any unrelated validation
			// error, so the report has to name this one.
			for _, needle := range []string{"budget.max_runtime_minutes", tc.minutes, strconv.Itoa(maxBudgetMinutes)} {
				if !strings.Contains(out, needle) {
					t.Errorf("report does not name %q:\n%s", needle, out)
				}
			}
			if strings.Contains(out, "✓ Policy valid") {
				t.Errorf("report calls the policy valid:\n%s", out)
			}
			// The fix line is a continuation of its bullet, not a stray
			// unindented line in the middle of the error list.
			if !strings.Contains(out, "\n    Lower it in andbo.yaml") {
				t.Errorf("multi-line error lost its continuation indent:\n%s", out)
			}
		})
	}
}

// --json is the machine-readable contract a pipeline gates on, and it is a
// SEPARATE return path from the human one, so an untested budget error there
// could report exit 0 with valid:false or vice versa.
func TestPolicyCheckJSONReportsTheBudgetRefusal(t *testing.T) {
	budgetProject(t, "153722867281")
	r := NewRoot("test", "none", "now")
	out, err := captureStdout(t, func() error { return r.cmdPolicy([]string{"check", "--json"}) })
	if CodeFor(err) != ExitInvalidConfig {
		t.Fatalf("exit code = %d, want %d (err=%v)\n%s", CodeFor(err), ExitInvalidConfig, err, out)
	}
	var got struct {
		Valid  bool     `json:"valid"`
		Errors []string `json:"errors"`
	}
	if jerr := json.Unmarshal([]byte(out), &got); jerr != nil {
		t.Fatalf("stdout is not valid JSON (%v):\n%s", jerr, out)
	}
	if got.Valid {
		t.Errorf("valid = true for a budget run and exec refuse:\n%s", out)
	}
	if !slicesContainsSubstring(got.Errors, "budget.max_runtime_minutes") {
		t.Errorf("errors do not name the budget field: %q", got.Errors)
	}
}

func slicesContainsSubstring(items []string, needle string) bool {
	for _, s := range items {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}

// captureStdout runs fn with os.Stdout redirected to a temp file and returns
// what it wrote. cmdPolicy prints straight to os.Stdout and Root has no
// injectable writer, so this is the only way to assert its report.
func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	f, ferr := os.CreateTemp(t.TempDir(), "stdout")
	if ferr != nil {
		t.Fatal(ferr)
	}
	defer f.Close()
	prev := os.Stdout
	os.Stdout = f
	err := fn()
	os.Stdout = prev
	data, rerr := os.ReadFile(f.Name())
	if rerr != nil {
		t.Fatal(rerr)
	}
	return string(data), err
}

// run, exec, and k8s render must refuse the same policy the same way. A budget
// one surface silently downgrades is a bound the other two claim to enforce.
func TestEverySurfaceRefusesTheSameUnenforceableBudget(t *testing.T) {
	const minutes = "153722867281"

	budgetProject(t, minutes)
	r := NewRoot("test", "none", "now")

	runErr := r.cmdRun(context.Background(), []string{"fix failing tests", "--dry-run"})
	execErr := r.cmdExec(context.Background(), []string{"echo hi", "--dry-run"})
	// k8s render refuses under its own, much tighter cap (activeDeadlineSeconds);
	// what must match is the verdict and the exit code, not the maximum.
	out, _, k8sErr := runK8s(t, okArgs()...)
	if out != "" {
		t.Errorf("a refused render must write nothing to stdout, got:\n%s", out)
	}

	for name, err := range map[string]error{"run": runErr, "exec": execErr, "k8s render": k8sErr} {
		if CodeFor(err) != ExitPolicyViolation {
			t.Errorf("%s: exit code = %d, want %d (err=%v)", name, CodeFor(err), ExitPolicyViolation, err)
		}
		if err == nil || !strings.Contains(err.Error(), "budget.max_runtime_minutes") {
			t.Errorf("%s: error does not name the policy field: %v", name, err)
		}
	}
}
