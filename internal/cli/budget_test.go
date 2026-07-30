package cli

import (
	"context"
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
// Each wraps a different way, because each failure mode is different: a small
// positive window kills the run early under a message naming a bound it never
// had, and a zero or negative one removes the deadline entirely.
var budgetOverflowCases = []struct{ name, minutes string }{
	{"one past the largest representable budget", "153722868"},
	{"wraps to a few seconds", "153722867281"},
	{"wraps to exactly zero", "9007199254740992"},
	{"wraps negative", "200000000000"},
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
// in an int64: a minute costs 6e10 of that range, so the product wraps above
// ~153.7 million minutes. A wrapped window is not merely wrong, it can be
// unenforced — both runners gate on `if command.Timeout > 0`
// (internal/runtime/local.go, internal/runtime/docker.go), so a budget wrapping
// to zero or negative silently removes the deadline they exist to apply.
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
		// and zero is how every runner spells "no deadline".
		{"wraps to exactly zero", 1 << 53},
		{"wraps to a few seconds", 153722867281},
		{"wraps negative", 200000000000},
		{"the largest int64", math.MaxInt64},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.minutes > math.MaxInt32 && strconv.IntSize < 64 {
				t.Skip("this budget does not fit in an int on this platform")
			}
			got := budgetWindow(int(tc.minutes))
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

// exec is the same runtime under a different front door, so it must refuse the
// same policy identically. Its budget kill reports the same claim run's does.
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

// The refusal must land before the unsafe confirmation, not after: an
// unenforceable budget is not something the user should be asked to accept
// risk for first, and the prompt blocks on stdin in an interactive terminal.
func TestUnenforceableBudgetIsRefusedBeforeTheUnsafeGate(t *testing.T) {
	budgetProject(t, "153722867281")
	// Point stdin at /dev/null so a regression fails fast on EOF instead of
	// blocking at the prompt when the suite runs in a terminal.
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	prev := os.Stdin
	os.Stdin = devNull
	t.Cleanup(func() { os.Stdin = prev; devNull.Close() })

	r := NewRoot("test", "none", "now")
	// --runtime local is unsafe and deliberately given WITHOUT --yes-unsafe: if
	// the budget check ran after the gate, this would fail as unconfirmed-unsafe
	// rather than naming the budget.
	err = r.cmdRun(context.Background(), []string{"fix failing tests", "--runtime", "local"})
	assertBudgetRefused(t, err, "153722867281")
}

// The boundary itself must still run: the guard rejects what cannot be
// represented, not what is merely large.
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
