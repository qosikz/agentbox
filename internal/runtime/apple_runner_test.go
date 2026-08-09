package runtime

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// TestApplePlatformSupported pins the platform gate. Apple Container ships only
// for macOS on Apple silicon, so the two rejections must be distinguishable: a
// Linux user and an Intel-Mac user need different next steps.
func TestApplePlatformSupported(t *testing.T) {
	tests := []struct {
		name      string
		goos      string
		goarch    string
		wantErr   bool
		wantHints []string
	}{
		{"darwin arm64 is supported", "darwin", "arm64", false, nil},
		{
			name: "linux is refused for the OS", goos: "linux", goarch: "amd64", wantErr: true,
			wantHints: []string{"macOS", "linux/amd64", "--engine docker", "--dry-run"},
		},
		{
			name: "intel mac is refused for the arch", goos: "darwin", goarch: "amd64", wantErr: true,
			wantHints: []string{"Apple silicon", "arm64", "darwin/amd64", "--engine docker"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := applePlatformSupported(tt.goos, tt.goarch)
			if !tt.wantErr {
				if err != nil {
					t.Fatalf("applePlatformSupported(%q,%q) = %v, want nil", tt.goos, tt.goarch, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("applePlatformSupported(%q,%q) = nil, want an error", tt.goos, tt.goarch)
			}
			for _, want := range tt.wantHints {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q missing hint %q", err, want)
				}
			}
		})
	}
	// The OS and arch refusals must not collapse into one another.
	osErr := applePlatformSupported("linux", "amd64").Error()
	archErr := applePlatformSupported("darwin", "amd64").Error()
	if osErr == archErr {
		t.Error("OS and arch refusals produced identical messages; they need different fixes")
	}
}

// TestMacOSVersionSupported pins the OS-version gate. Apple Container requires
// macOS 26 or newer, and only the MAJOR version decides: upstream states the
// requirement that way, and anything finer would reject point releases Andbo has
// no evidence against.
//
// Every non-conforming input fails CLOSED. Andbo cannot verify the requirement
// from a version it cannot parse, and guessing "probably fine" is how a hard
// requirement turns into a cryptic failure inside the engine.
func TestMacOSVersionSupported(t *testing.T) {
	tests := []struct {
		name      string
		version   string
		wantErr   bool
		wantHints []string
	}{
		{name: "26 major only is accepted", version: "26"},
		{name: "26.0 is accepted", version: "26.0"},
		{name: "the verified 26.5.2 is accepted", version: "26.5.2"},
		{name: "newer majors are accepted", version: "27.1"},
		{name: "far newer majors are accepted", version: "100.0"},
		{name: "surrounding whitespace is tolerated", version: " 26.5.2\n"},
		{
			name: "25 is rejected", version: "25.6", wantErr: true,
			wantHints: []string{"macOS 26", "25.6", "--engine docker", "--dry-run"},
		},
		{
			name: "an old major is rejected", version: "15.5", wantErr: true,
			wantHints: []string{"macOS 26", "15.5"},
		},
		{
			name: "empty is rejected", version: "", wantErr: true,
			wantHints: []string{"empty", "--engine docker"},
		},
		{
			name: "whitespace only is rejected", version: "   \n", wantErr: true,
			wantHints: []string{"empty", "--engine docker"},
		},
		{
			name: "non-numeric is rejected", version: "sonoma", wantErr: true,
			wantHints: []string{"could not parse", "sonoma", "--engine docker"},
		},
		{
			name: "a trailing suffix on the major is rejected", version: "26beta.1", wantErr: true,
			wantHints: []string{"could not parse", "26beta.1"},
		},
		{
			name: "a non-numeric minor is rejected", version: "26.beta", wantErr: true,
			wantHints: []string{"could not parse", "26.beta"},
		},
		{
			name: "an empty interior component is rejected", version: "26..1", wantErr: true,
			wantHints: []string{"could not parse", "26..1"},
		},
		{
			name: "a trailing separator is rejected", version: "26.", wantErr: true,
			wantHints: []string{"could not parse", "26."},
		},
		{
			name: "an empty major component is rejected", version: ".26", wantErr: true,
			wantHints: []string{"could not parse"},
		},
		{
			name: "a negative major is rejected", version: "-26.1", wantErr: true,
			wantHints: []string{"could not parse"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := MacOSVersionSupported(tt.version)
			if !tt.wantErr {
				if err != nil {
					t.Fatalf("MacOSVersionSupported(%q) = %v, want nil", tt.version, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("MacOSVersionSupported(%q) = nil, want an error", tt.version)
			}
			for _, want := range tt.wantHints {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q missing hint %q", err, want)
				}
			}
		})
	}
	// "too old" and "cannot be parsed" are different situations with different
	// fixes (upgrade vs. report a bug), so they must not collapse into one text.
	if MacOSVersionSupported("25.6").Error() == MacOSVersionSupported("sonoma").Error() {
		t.Error("the too-old and unparseable refusals produced identical messages")
	}
}

// TestAppleRunner_Name pins the ENGINE name users write in policy and --engine,
// which is deliberately NOT the binary it invokes.
func TestAppleRunner_Name(t *testing.T) {
	got := NewAppleRunner().Name()
	if got != "apple" {
		t.Errorf("Name() = %q, want apple", got)
	}
	if got == appleEngineBin {
		t.Errorf("Name() returned the binary name %q; policy vocabulary is the engine name", got)
	}
}

// TestAppleRunner_Available_ShortCircuits proves the gates run in priority order
// and that no subprocess is spawned on a host that cannot run this engine.
func TestAppleRunner_Available_ShortCircuits(t *testing.T) {
	errLook := errors.New("executable file not found in $PATH")
	errStatus := errors.New("connection refused")

	errVersion := errors.New("exec: \"sw_vers\": executable file not found")

	tests := []struct {
		name           string
		goos, goarch   string
		version        string
		versionErr     error
		lookErr        error
		statusErr      error
		wantErr        bool
		wantHints      []string
		wantVerCalls   int
		wantLookCalls  int
		wantStatCalls  int
		unwantedInText []string
	}{
		{
			name: "unsupported platform never shells out",
			goos: "linux", goarch: "amd64", wantErr: true,
			wantHints:    []string{"macOS"},
			wantVerCalls: 0, wantLookCalls: 0, wantStatCalls: 0,
		},
		{
			name: "an intel mac is refused before the version lookup",
			goos: "darwin", goarch: "amd64", wantErr: true,
			wantHints:    []string{"Apple silicon"},
			wantVerCalls: 0, wantLookCalls: 0, wantStatCalls: 0,
		},
		{
			name: "macOS 25 never looks for the binary",
			goos: "darwin", goarch: "arm64", version: "25.6", wantErr: true,
			wantHints:    []string{"macOS 26", "25.6"},
			wantVerCalls: 1, wantLookCalls: 0, wantStatCalls: 0,
			unwantedInText: []string{"Install"},
		},
		{
			name: "an unreadable version fails closed with a fix",
			goos: "darwin", goarch: "arm64", versionErr: errVersion, wantErr: true,
			wantHints:    []string{"macOS 26", "sw_vers", "executable file not found", "--engine docker", "--dry-run"},
			wantVerCalls: 1, wantLookCalls: 0, wantStatCalls: 0,
		},
		{
			name: "an unparseable version fails closed",
			goos: "darwin", goarch: "arm64", version: "not-a-version", wantErr: true,
			wantHints:    []string{"could not parse", "not-a-version"},
			wantVerCalls: 1, wantLookCalls: 0, wantStatCalls: 0,
		},
		{
			name: "missing binary never probes the service",
			goos: "darwin", goarch: "arm64", lookErr: errLook, wantErr: true,
			wantHints:    []string{"container", "Install"},
			wantVerCalls: 1, wantLookCalls: 1, wantStatCalls: 0,
		},
		{
			name: "service down is reported with its fix",
			goos: "darwin", goarch: "arm64", statusErr: errStatus, wantErr: true,
			wantHints:      []string{"container system start", "connection refused"},
			wantVerCalls:   1,
			wantLookCalls:  1,
			wantStatCalls:  1,
			unwantedInText: []string{"Install"},
		},
		{
			name: "all gates pass on the minimum supported macOS",
			goos: "darwin", goarch: "arm64", version: "26.0", wantErr: false,
			wantVerCalls: 1, wantLookCalls: 1, wantStatCalls: 1,
		},
		{
			name: "all gates pass on a newer macOS",
			goos: "darwin", goarch: "arm64", version: "27.1", wantErr: false,
			wantVerCalls: 1, wantLookCalls: 1, wantStatCalls: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			verCalls, lookCalls, statCalls := 0, 0, 0
			version := tt.version
			if version == "" && tt.versionErr == nil {
				version = "26.5.2" // the manually verified host
			}
			r := appleRunner{
				goos:   tt.goos,
				goarch: tt.goarch,
				macOSVersion: func(context.Context) (string, error) {
					verCalls++
					return version, tt.versionErr
				},
				lookPath: func(string) (string, error) {
					lookCalls++
					return "/usr/local/bin/container", tt.lookErr
				},
				systemStatus: func(context.Context) error {
					statCalls++
					return tt.statusErr
				},
			}
			err := r.Available(context.Background())
			if tt.wantErr && err == nil {
				t.Fatal("Available() = nil, want an error")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("Available() = %v, want nil", err)
			}
			for _, want := range tt.wantHints {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q missing hint %q", err, want)
				}
			}
			for _, unwanted := range tt.unwantedInText {
				if strings.Contains(err.Error(), unwanted) {
					t.Errorf("error %q leaked an unrelated branch's text %q", err, unwanted)
				}
			}
			if verCalls != tt.wantVerCalls {
				t.Errorf("macOSVersion called %d times, want %d", verCalls, tt.wantVerCalls)
			}
			if lookCalls != tt.wantLookCalls {
				t.Errorf("lookPath called %d times, want %d", lookCalls, tt.wantLookCalls)
			}
			if statCalls != tt.wantStatCalls {
				t.Errorf("systemStatus called %d times, want %d", statCalls, tt.wantStatCalls)
			}
		})
	}
}

// TestNewAppleRunner_WiresEverySeam guards against a seam being added to the
// struct but left nil by the constructor, which would panic on the first real
// run while every injected unit test stayed green.
func TestNewAppleRunner_WiresEverySeam(t *testing.T) {
	r, ok := NewAppleRunner().(appleRunner)
	if !ok {
		t.Fatalf("NewAppleRunner() = %T, want appleRunner", NewAppleRunner())
	}
	if r.goos == "" || r.goarch == "" {
		t.Errorf("build platform not captured: goos=%q goarch=%q", r.goos, r.goarch)
	}
	if r.macOSVersion == nil {
		t.Error("macOSVersion seam is nil; Available() would panic on darwin/arm64")
	}
	if r.lookPath == nil {
		t.Error("lookPath seam is nil")
	}
	if r.systemStatus == nil {
		t.Error("systemStatus seam is nil")
	}
}

// appleOKRunner returns a runner whose availability gates all pass, without
// touching a real binary, so Run/ProbeBinary logic is reachable on any host.
func appleOKRunner() appleRunner {
	return appleRunner{
		goos:         "darwin",
		goarch:       "arm64",
		macOSVersion: func(context.Context) (string, error) { return "26.5.2", nil },
		lookPath:     func(string) (string, error) { return "/usr/local/bin/container", nil },
		systemStatus: func(context.Context) error { return nil },
	}
}

// TestAppleRunner_Run_RefusesUnsupportedSpec proves an unsupported spec is
// refused with the builder's own error and never reaches the engine.
func TestAppleRunner_Run_RefusesUnsupportedSpec(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*RuntimeSpec)
		want   string
	}{
		{"allowlist", func(s *RuntimeSpec) { s.NetworkMode = "allowlist" }, "allowlist"},
		{"privileged", func(s *RuntimeSpec) { s.Privileged = true }, "privileged"},
		{"docker socket", func(s *RuntimeSpec) { s.MountDockerSocket = true }, "socket"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := appleSpec()
			tt.mutate(&spec)
			res, err := appleOKRunner().Run(context.Background(), spec, CommandSpec{Executable: "agent-cli"})
			if err == nil {
				t.Fatal("Run() = nil error, want a refusal")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Run() error %q does not explain the refusal (%q)", err, tt.want)
			}
			if res.ExitCode != -1 {
				t.Errorf("ExitCode = %d, want -1 for a run that never started", res.ExitCode)
			}
		})
	}
}

// TestAppleRunner_Run_UnavailableIsActionable checks the availability gate is
// reported through Run, not swallowed.
func TestAppleRunner_Run_UnavailableIsActionable(t *testing.T) {
	r := appleOKRunner()
	r.goos = "linux"
	r.goarch = "amd64"
	res, err := r.Run(context.Background(), appleSpec(), CommandSpec{Executable: "agent-cli"})
	if err == nil {
		t.Fatal("Run() = nil error, want an availability failure")
	}
	if !strings.Contains(err.Error(), "macOS") {
		t.Errorf("Run() error %q does not name the platform requirement", err)
	}
	if res.ExitCode != -1 {
		t.Errorf("ExitCode = %d, want -1", res.ExitCode)
	}
}

// TestAppleRunner_ProbeBinary_UnavailableIsInconclusive: (false, nil) means
// "conclusively absent" to the caller and would wrongly block the run with an
// "install the agent in your image" error. An unavailable engine must be
// reported as inconclusive instead.
func TestAppleRunner_ProbeBinary_UnavailableIsInconclusive(t *testing.T) {
	r := appleOKRunner()
	r.lookPath = func(string) (string, error) { return "", errors.New("not found") }

	present, err := r.ProbeBinary(context.Background(), "img:tag", "agent-cli")
	if present {
		t.Error("ProbeBinary() reported present despite an unavailable engine")
	}
	if err == nil {
		t.Fatal("ProbeBinary() = (false, nil): the caller reads that as conclusively absent")
	}
}

// TestAppleStartFailure_Wording pins the two non-exit failure modes and keeps
// the underlying error unwrappable.
func TestAppleStartFailure_Wording(t *testing.T) {
	runErr := errors.New("fork/exec: no such file")

	timedOut := appleStartFailure(context.DeadlineExceeded, runErr)
	if !strings.Contains(timedOut.Error(), "interrupted") || !strings.Contains(timedOut.Error(), "timeout") {
		t.Errorf("cancellation wording = %q, want it to mention interruption and timeout", timedOut)
	}
	if !errors.Is(timedOut, runErr) {
		t.Error("cancellation branch dropped the wrapped error")
	}

	startFailed := appleStartFailure(nil, runErr)
	if !strings.Contains(startFailed.Error(), "container system start") {
		t.Errorf("start-failure wording = %q, want the service fix named", startFailed)
	}
	if !errors.Is(startFailed, runErr) {
		t.Error("start-failure branch dropped the wrapped error")
	}
	if timedOut.Error() == startFailed.Error() {
		t.Error("the two failure modes produced identical messages")
	}
}

// TestDryRunRunner_AppleAllowlistIsNotClaimedEnforced guards against an
// overclaim: the dry-run plan decided "would be enforced via egress proxy" from
// the network mode ALONE. With the apple engine that sentence is false — the
// run fails closed instead — so the plan must say so rather than describe
// enforcement the engine will never perform.
func TestDryRunRunner_AppleAllowlistIsNotClaimedEnforced(t *testing.T) {
	spec := appleSpec()
	spec.NetworkMode = "allowlist"
	spec.AllowedDomains = []string{"api.example.com"}

	res, err := NewDryRunRunner().Run(context.Background(), spec, CommandSpec{Executable: "agent-cli"})
	if err != nil {
		t.Fatalf("dry run returned unexpected error: %v", err)
	}
	plan := strings.Join(res.Description, "\n")

	if strings.Contains(plan, "would be enforced") {
		t.Errorf("dry-run plan claims allowlist enforcement on the apple engine:\n%s", plan)
	}
	for _, want := range []string{"NOT supported", "apple", "fails closed"} {
		if !strings.Contains(plan, want) {
			t.Errorf("dry-run plan missing %q; it must state the run fails closed:\n%s", want, plan)
		}
	}
}

// TestDryRunRunner_DockerAllowlistStillDescribesEnforcement ensures the fix is
// engine-scoped and did not disable the docker/podman description.
func TestDryRunRunner_DockerAllowlistStillDescribesEnforcement(t *testing.T) {
	spec := secureSpec() // Engine: docker
	spec.NetworkMode = "allowlist"
	spec.AllowedDomains = []string{"api.example.com"}

	res, err := NewDryRunRunner().Run(context.Background(), spec, CommandSpec{Executable: "agent-cli"})
	if err != nil {
		t.Fatalf("dry run returned unexpected error: %v", err)
	}
	plan := strings.Join(res.Description, "\n")
	if !strings.Contains(plan, "allowlist") || !strings.Contains(plan, "api.example.com") {
		t.Errorf("docker allowlist plan lost its detail:\n%s", plan)
	}
	if strings.Contains(plan, "NOT supported") {
		t.Errorf("docker allowlist was wrongly reported as unsupported:\n%s", plan)
	}
}

// TestAppleProbeBinaryArgs_Hardened pins the probe container's isolation. It
// carries the same intent as the docker probe minus what this engine lacks:
// there is NO --security-opt no-new-privileges equivalent on apple, so the
// probe cannot forbid setuid privilege regain the way the docker probe does.
func TestAppleProbeBinaryArgs_Hardened(t *testing.T) {
	args := AppleProbeBinaryArgs("img:tag", "agent-cli")

	if len(args) < 2 || args[0] != "run" || args[1] != "--rm" {
		t.Fatalf("expected args to start with [run --rm], got %v", args)
	}
	for _, pair := range [][2]string{
		{"--cap-drop", "ALL"},
		{"--network", "none"},
		{"--user", "10001:10001"},
		{"--entrypoint", "sh"},
	} {
		if !containsPair(args, pair[0], pair[1]) {
			t.Errorf("expected %s %s, got %v", pair[0], pair[1], args)
		}
	}
	// Flags that do not exist on this engine must not be emitted: the CLI
	// rejects unknown options, so a stray flag would break every probe.
	for _, forbidden := range []string{"--security-opt", "--privileged", "--mount", "-v"} {
		if contains(args, forbidden) {
			t.Errorf("apple probe must not emit %s: %v", forbidden, args)
		}
	}
	if got := args[len(args)-3]; got != "img:tag" {
		t.Errorf("image should precede the shell invocation, got %q in %v", got, args)
	}
	if got := args[len(args)-2]; got != "-c" {
		t.Errorf("expected -c before the probe script, got %q", got)
	}
	script := args[len(args)-1]
	if !strings.Contains(script, "command -v --") {
		t.Errorf("probe script missing `command -v --`: %q", script)
	}
	if !strings.Contains(script, "exit 42") {
		t.Errorf("probe script missing the shell-agnostic sentinel exit 42: %q", script)
	}
}

// TestAppleProbeBinaryArgs_QuotesBinary ensures a malformed binary name cannot
// break out of the probe command.
func TestAppleProbeBinaryArgs_QuotesBinary(t *testing.T) {
	args := AppleProbeBinaryArgs("img:tag", "foo'; rm -rf /")
	script := args[len(args)-1]
	if !strings.Contains(script, `'\''`) {
		t.Errorf("expected embedded single quote to be escaped, got %q", script)
	}
	if strings.Contains(script, "; rm -rf / ") {
		t.Errorf("payload escaped its quoting: %q", script)
	}
}

// TestInsertContainerName_AppleArgs guards the SHARED helper against the apple
// argv shape: it inserts after "run --rm", which must remain the prefix here.
func TestInsertContainerName_AppleArgs(t *testing.T) {
	args, err := BuildAppleArgs(appleSpec(), CommandSpec{Executable: "agent-cli"})
	if err != nil {
		t.Fatalf("BuildAppleArgs() returned unexpected error: %v", err)
	}
	got := insertContainerName(args, "andbo-abc123")
	wantHead := []string{"run", "--rm", "--name", "andbo-abc123"}
	if !reflect.DeepEqual(got[:4], wantHead) {
		t.Fatalf("head = %v, want %v", got[:4], wantHead)
	}
	if !reflect.DeepEqual(got[4:], args[2:]) {
		t.Errorf("tail was altered:\n got: %v\nwant: %v", got[4:], args[2:])
	}
}
