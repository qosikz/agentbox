package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/qosikz/andbo/internal/config"
	"github.com/qosikz/andbo/internal/policy"
	"github.com/qosikz/andbo/internal/runtime"
)

// An allowlisted secret must be injected into the CONTAINER environment so a
// baked-in agent can authenticate, and its value must be redacted from logs.
func TestAllowedSecretInjectedIntoContainerAndRedacted(t *testing.T) {
	t.Setenv("ANDBO_FAKE_API_KEY", "dummy-not-real-CAFE-value")
	cfg := config.DefaultPolicy()
	cfg.Secrets.Allow = []string{"ANDBO_FAKE_API_KEY"}
	ep := policy.BuildEffectivePolicy(cfg, "andbo.yaml", policy.Overrides{})

	env := buildAgentEnv(ep)
	if env["ANDBO_FAKE_API_KEY"] != "dummy-not-real-CAFE-value" {
		t.Fatalf("allowlisted secret not injected into container env: %v", env)
	}
	// Container hygiene: the standard Linux PATH, never the host's.
	if env["PATH"] != containerPATH {
		t.Errorf("container PATH = %q, want %q", env["PATH"], containerPATH)
	}

	red := buildRedactor(ep)
	out := red.Redact("config: token=dummy-not-real-CAFE-value done")
	if strings.Contains(out, "dummy-not-real-CAFE-value") {
		t.Errorf("allowed secret value must be redacted from logs, got %q", out)
	}
	if !strings.Contains(out, "[REDACTED:ANDBO_FAKE_API_KEY]") {
		t.Errorf("expected named redaction tag, got %q", out)
	}
}

// deny-overrides-allow: a secret that is both allowed and denied (the secure
// defaults deny OPENAI_API_KEY) must NOT reach the sandbox, yet must still be
// redacted from logs.
func TestDeniedSecretNotInjectedEvenIfAllowed(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "denied-dummy-value-123")
	cfg := config.DefaultPolicy() // deny includes OPENAI_API_KEY
	cfg.Secrets.Allow = []string{"OPENAI_API_KEY"}
	ep := policy.BuildEffectivePolicy(cfg, "andbo.yaml", policy.Overrides{})

	env := buildAgentEnv(ep)
	if _, ok := env["OPENAI_API_KEY"]; ok {
		t.Fatal("deny-overrides-allow must keep a denied secret OUT of the sandbox env")
	}
	red := buildRedactor(ep)
	if got := red.Redact("leak OPENAI_API_KEY=denied-dummy-value-123"); strings.Contains(got, "denied-dummy-value-123") {
		t.Errorf("denied secret value must still be redacted from logs, got %q", got)
	}
}

// fakeRunner is a Runner test double that needs no container engine.
type fakeRunner struct {
	probePresent bool
	probeErr     error
	ran          bool
	egressLog    []string
}

func (f *fakeRunner) Name() string                        { return "fake" }
func (f *fakeRunner) Available(ctx context.Context) error { return nil }
func (f *fakeRunner) ProbeBinary(ctx context.Context, image, bin string) (bool, error) {
	return f.probePresent, f.probeErr
}
func (f *fakeRunner) Run(ctx context.Context, spec runtime.RuntimeSpec, command runtime.CommandSpec) (runtime.RunResult, error) {
	f.ran = true
	return runtime.RunResult{ExitCode: 0, EgressLog: f.egressLog}, nil
}

// A container run whose agent is absent from the image must fail fast with an
// actionable error BEFORE the agent is executed.
func TestRunBlocksWhenAgentMissingFromImage(t *testing.T) {
	dir := t.TempDir()
	writeExecPolicy(t, dir, "") // defaults: container/docker/deny, agent custom "echo"
	chdir(t, dir)

	fr := &fakeRunner{probePresent: false}
	orig := selectRunnerFn
	selectRunnerFn = func(ep policy.EffectivePolicy, o runOptions) (runtime.Runner, error) { return fr, nil }
	t.Cleanup(func() { selectRunnerFn = orig })

	r := NewRoot("test", "none", "now")
	err := r.cmdRun(context.Background(), []string{"do a thing"})
	if CodeFor(err) != ExitAgentFailed {
		t.Fatalf("want ExitAgentFailed when agent missing from image, got code %d err %v", CodeFor(err), err)
	}
	if err == nil || !strings.Contains(err.Error(), "not installed in runtime image") {
		t.Errorf("error should be actionable about the image, got: %v", err)
	}
	if fr.ran {
		t.Error("agent must NOT run when preflight says it is missing from the image")
	}
}

// An inconclusive probe (e.g. daemon hiccup) must NOT block: the run proceeds
// and lets the real execution surface any genuine failure.
func TestRunProceedsWhenProbeInconclusive(t *testing.T) {
	dir := t.TempDir()
	writeExecPolicy(t, dir, "")
	chdir(t, dir)

	fr := &fakeRunner{probePresent: false, probeErr: context.DeadlineExceeded}
	orig := selectRunnerFn
	selectRunnerFn = func(ep policy.EffectivePolicy, o runOptions) (runtime.Runner, error) { return fr, nil }
	t.Cleanup(func() { selectRunnerFn = orig })

	r := NewRoot("test", "none", "now")
	if err := r.cmdRun(context.Background(), []string{"do a thing"}); err != nil {
		t.Fatalf("inconclusive probe should not block the run, got: %v (code %d)", err, CodeFor(err))
	}
	if !fr.ran {
		t.Error("agent should run when the preflight probe is inconclusive")
	}
}

// A container DRY-RUN must not require the agent to exist on the host PATH: in
// container mode the agent lives in the image. (Previously this emitted a
// spurious "not found on PATH" warning.)
func TestContainerDryRunSkipsHostAgentCheck(t *testing.T) {
	dir := t.TempDir()
	writeExecPolicy(t, dir, "agent:\n  default: custom\n  custom:\n    command: definitely-not-on-host-xyzzy\n    args:\n      - \"{{ task }}\"\n")
	chdir(t, dir)

	r := NewRoot("test", "none", "now")
	if err := r.cmdRun(context.Background(), []string{"do a thing", "--dry-run"}); err != nil {
		t.Fatalf("container dry-run should not fail on a host-missing baked-in agent: %v (code %d)", err, CodeFor(err))
	}
}

// Egress audit lines returned by the runner must land in the session: denials
// as policy events, allows as log lines.
func TestRunRecordsEgressDenialsAsPolicyEvents(t *testing.T) {
	dir := t.TempDir()
	writeExecPolicy(t, dir, "")
	chdir(t, dir)

	fr := &fakeRunner{probePresent: true, egressLog: []string{
		"ANDBO-EGRESS ALLOW connect api.openai.com:443",
		"ANDBO-EGRESS DENY connect evil.example:443: host is not in the network.allow list",
	}}
	orig := selectRunnerFn
	selectRunnerFn = func(ep policy.EffectivePolicy, o runOptions) (runtime.Runner, error) { return fr, nil }
	t.Cleanup(func() { selectRunnerFn = orig })

	r := NewRoot("test", "none", "now")
	if err := r.cmdRun(context.Background(), []string{"do a thing"}); err != nil {
		t.Fatalf("run: %v (code %d)", err, CodeFor(err))
	}
	s := latestSession(t, dir)
	foundDeny := false
	for _, e := range s.PolicyEvents {
		if strings.Contains(e.Detail, "evil.example:443") {
			foundDeny = true
		}
		if strings.Contains(e.Detail, "api.openai.com") {
			t.Errorf("ALLOW lines must not become policy events: %v", e)
		}
	}
	if !foundDeny {
		t.Errorf("egress denial should be a policy event, got: %v", s.PolicyEvents)
	}
}

// recordEgress must classify by the verb field, not a free substring: an
// ALLOW line whose method/host text contains "DENY" must NOT be recorded as a
// denial, and a real DENY must always be a policy event.
func TestRecordEgressFieldAnchoredClassification(t *testing.T) {
	dir := t.TempDir()
	writeExecPolicy(t, dir, "")
	chdir(t, dir)
	fr := &fakeRunner{probePresent: true, egressLog: []string{
		"ANDBO-EGRESS ALLOW http DENY github.com:80",                // method literally "DENY" — still ALLOW
		"ANDBO-EGRESS DENY connect ALLOW.example:443: not in allow", // host contains ALLOW — still DENY
	}}
	orig := selectRunnerFn
	selectRunnerFn = func(ep policy.EffectivePolicy, o runOptions) (runtime.Runner, error) { return fr, nil }
	t.Cleanup(func() { selectRunnerFn = orig })

	r := NewRoot("test", "none", "now")
	if err := r.cmdRun(context.Background(), []string{"do a thing"}); err != nil {
		t.Fatalf("run: %v", err)
	}
	s := latestSession(t, dir)
	for _, e := range s.PolicyEvents {
		if strings.Contains(e.Detail, "ALLOW http DENY github.com") {
			t.Errorf("an ALLOW line was misclassified as a denial: %v", e)
		}
	}
	foundRealDeny := false
	for _, e := range s.PolicyEvents {
		if strings.Contains(e.Detail, "ALLOW.example:443") {
			foundRealDeny = true
		}
	}
	if !foundRealDeny {
		t.Errorf("a real DENY (host containing ALLOW) must be a policy event: %v", s.PolicyEvents)
	}
}
