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
				// Zero is how the policy spells "no deadline", and every caller
				// gates on `> 0`. A negative is refused by config.Check before
				// it can reach a command, but the conversion still has to answer
				// with that same 0 — not a window invented out of a sign flip —
				// for any caller added later.
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

// A budget of exactly zero is the documented way to say "no deadline", and the
// negative refusal must not quietly swallow it. Pinned so the boundary between
// "no deadline" and "not a duration" is a decision, not an accident.
func TestZeroBudgetStillMeansNoDeadline(t *testing.T) {
	budgetProject(t, "0")
	r := NewRoot("test", "none", "now")
	if err := r.cmdRun(context.Background(), []string{"fix failing tests", "--dry-run"}); err != nil {
		t.Fatalf("max_runtime_minutes=0 means no deadline and must be accepted: %v", err)
	}
}

// A negative budget is not a duration, and every surface used to read it
// differently: run and exec gate the deadline on `> 0` and so ran with NO
// deadline at all, k8s render gated the same way and fell through to the
// renderer's 1800s activeDeadlineSeconds default, and policy check called both
// of those policies valid. One value, three meanings, no error anywhere.
//
// -30 is the case that matters most — a sign typo on the default budget, which
// reads as "30 minutes" to everyone but the code. -9007199254740991 is the
// sharpest: naively converted it wraps POSITIVE into a plausible one-minute
// window, the direction that passes the runners' `Timeout > 0` gate and so
// reads as enforced.
func TestEverySurfaceRefusesANegativeBudget(t *testing.T) {
	// Each surface yields only its DIAGNOSTIC text, because they put it in
	// different places: policy check reports on stdout, run and exec warn on
	// stderr and return only a pointer to 'andbo policy check', and k8s render
	// writes to its own stderr stream.
	surfaces := map[string]func(*testing.T) (string, error){
		"run": func(t *testing.T) (string, error) {
			return captureStderr(t, func() error {
				return NewRoot("test", "none", "now").cmdRun(context.Background(), []string{"fix failing tests", "--dry-run"})
			})
		},
		"exec": func(t *testing.T) (string, error) {
			return captureStderr(t, func() error {
				return NewRoot("test", "none", "now").cmdExec(context.Background(), []string{"echo hi", "--dry-run"})
			})
		},
		"policy check": func(t *testing.T) (string, error) {
			out, err := captureStdout(t, func() error {
				return NewRoot("test", "none", "now").cmdPolicy([]string{"check"})
			})
			// Narrowed to the error list on purpose. This command prints the
			// effective policy first, and that block already echoes
			// `minutes=-30` — so asserting the value against the whole report
			// would stay green on a message that dropped it.
			if i := strings.Index(out, "Errors:"); i >= 0 {
				return out[i:], err
			}
			return out, err
		},
		"k8s render": func(t *testing.T) (string, error) {
			out, errOut, err := runK8s(t, okArgs()...)
			// A refused render must write no manifest. This is the concrete bug:
			// a negative left cs.Timeout at zero, the bridge kept its own 1800s
			// default, and the Job carried an activeDeadlineSeconds the policy
			// never asked for.
			if out != "" {
				t.Errorf("a refused render must write nothing to stdout, got:\n%s", out)
			}
			return errOut, err
		},
	}

	for _, minutes := range []string{"-1", "-30", "-9007199254740991"} {
		for name, invoke := range surfaces {
			t.Run(minutes+"/"+name, func(t *testing.T) {
				// Below int range the YAML decode fails before Check() ever sees
				// the value, and LoadPolicy's error is RETURNED rather than
				// warned — so the diagnostic stream would be empty for a reason
				// that has nothing to do with this guard.
				v, perr := strconv.ParseInt(minutes, 10, 64)
				if perr != nil {
					t.Fatalf("case %q is not an integer: %v", minutes, perr)
				}
				if (v > math.MaxInt32 || v < math.MinInt32) && strconv.IntSize < 64 {
					t.Skip("this budget does not fit in an int on this platform")
				}

				budgetProject(t, minutes)
				shown, err := invoke(t)
				// A malformed value is an invalid policy, which is the exit code
				// every surface already returns for one — so a CI gate that
				// watches for it catches this without learning a new code.
				if CodeFor(err) != ExitInvalidConfig {
					t.Fatalf("exit code = %d, want %d (err=%v)\n%s", CodeFor(err), ExitInvalidConfig, err, shown)
				}
				// The exit code alone would be satisfied by any unrelated
				// validation failure, so what the user reads has to name this one.
				for _, needle := range []string{"budget.max_runtime_minutes", minutes} {
					if !strings.Contains(shown, needle) {
						t.Errorf("what the user is shown does not name %q:\n%s", needle, shown)
					}
				}
			})
		}
	}
}

// run, exec, and k8s render print policy errors with warn(), which prefixes
// "! " and renders the message flat. A newline inside one therefore lands as a
// line carrying no prefix and no indent — it reads as an unattributed sentence
// between the errors, not as the fix for the error above it. Asserted on the
// stream the user actually reads, because the returned error says only "policy
// andbo.yaml is invalid".
func TestTheNegativeBudgetRefusalRendersAsOneLine(t *testing.T) {
	budgetProject(t, "-30")
	shown, _ := captureStderr(t, func() error {
		return NewRoot("test", "none", "now").cmdRun(context.Background(), []string{"fix failing tests", "--dry-run"})
	})
	for _, line := range strings.Split(strings.TrimRight(shown, "\n"), "\n") {
		if line != "" && !strings.HasPrefix(line, "! ") {
			t.Errorf("stderr line is not marked as a warning, so it reads as stray prose:\n%q\nfull stderr:\n%s", line, shown)
		}
	}
}

// policy check is the gate a pipeline runs before anything executes, so its
// verdict has to be the refusal and not a clean bill of health — on both output
// paths, which are separate returns that could disagree.
func TestPolicyCheckReportsTheNegativeBudgetRefusal(t *testing.T) {
	const minutes = "-30"

	t.Run("human", func(t *testing.T) {
		budgetProject(t, minutes)
		r := NewRoot("test", "none", "now")
		out, err := captureStdout(t, func() error { return r.cmdPolicy([]string{"check"}) })
		if CodeFor(err) != ExitInvalidConfig {
			t.Fatalf("exit code = %d, want %d (err=%v)\n%s", CodeFor(err), ExitInvalidConfig, err, out)
		}
		if strings.Contains(out, "✓ Policy valid") {
			t.Errorf("report calls the policy valid:\n%s", out)
		}
	})

	t.Run("json", func(t *testing.T) {
		budgetProject(t, minutes)
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
			t.Errorf("valid = true for a budget every surface refuses:\n%s", out)
		}
		if !slicesContainsSubstring(got.Errors, "budget.max_runtime_minutes") {
			t.Errorf("errors do not name the budget field: %q", got.Errors)
		}
	})
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
	return captureStream(t, &os.Stdout, fn)
}

// captureStderr is the same for stderr. run and exec print each policy error
// with warn(), straight to os.Stderr — the error they RETURN only names the
// file and points at 'andbo policy check' — so this is the only way to assert
// the user is told WHICH field is wrong.
func captureStderr(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	return captureStream(t, &os.Stderr, fn)
}

func captureStream(t *testing.T, stream **os.File, fn func() error) (string, error) {
	t.Helper()
	f, ferr := os.CreateTemp(t.TempDir(), "stream")
	if ferr != nil {
		t.Fatal(ferr)
	}
	defer f.Close()
	prev := *stream
	*stream = f
	// Deferred, not restored inline: a t.Fatal inside fn unwinds via
	// runtime.Goexit, which would otherwise leave the real stream pointing at a
	// TempDir file that is about to be deleted, silently swallowing the rest of
	// the package's diagnostics.
	defer func() { *stream = prev }()
	err := fn()
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
