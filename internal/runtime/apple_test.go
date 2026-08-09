package runtime

import (
	"reflect"
	"strings"
	"testing"
)

// appleSpec returns Andbo's secure defaults targeted at the apple engine.
func appleSpec() RuntimeSpec {
	s := secureSpec()
	s.Engine = "apple"
	return s
}

// appleFullSpec exercises every optional field the builder can emit, so the
// anchor test below pins their relative order.
func appleFullSpec() RuntimeSpec {
	s := appleSpec()
	s.Env = map[string]string{"BETA": "2", "ALPHA": "1"}
	s.ReadOnlyPaths = []string{"/ro"}
	s.WritePaths = []string{"/rw"}
	return s
}

// TestBuildAppleArgs_ExactOrder is the anchor: it asserts the ENTIRE argv for a
// rich spec, so every other builder test can be a property on top of it. An
// assertion on the whole slice is what catches a flag emitted in the wrong
// position, which per-token containment checks cannot see.
func TestBuildAppleArgs_ExactOrder(t *testing.T) {
	spec := appleFullSpec()
	cmd := CommandSpec{Executable: "agent-cli", Args: []string{"--task", "fix tests"}}

	got, err := BuildAppleArgs(spec, cmd)
	if err != nil {
		t.Fatalf("BuildAppleArgs() returned unexpected error: %v", err)
	}
	want := []string{
		"run", "--rm",
		"--cap-drop", "ALL",
		"--network", "none",
		"--user", "10001:10001",
		"--read-only", "--tmpfs", "/tmp",
		"-w", "/work",
		"-e", "ALPHA=1",
		"-e", "BETA=2",
		"--mount", "type=bind,source=/ro,target=/ro,readonly",
		"--mount", "type=bind,source=/rw,target=/rw",
		"ghcr.io/qosikz/andbo:latest",
		"agent-cli", "--task", "fix tests",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("BuildAppleArgs() argv mismatch\n got: %v\nwant: %v", got, want)
	}
}

// TestBuildAppleArgs_SecureDefaults pins the hardening prefix by index, so a
// flag cannot drift to a position where it no longer applies.
func TestBuildAppleArgs_SecureDefaults(t *testing.T) {
	args, err := BuildAppleArgs(appleSpec(), CommandSpec{Executable: "agent-cli"})
	if err != nil {
		t.Fatalf("BuildAppleArgs() returned unexpected error: %v", err)
	}
	wantPrefix := []string{"run", "--rm", "--cap-drop", "ALL", "--network", "none"}
	if len(args) < len(wantPrefix) || !reflect.DeepEqual(args[:len(wantPrefix)], wantPrefix) {
		t.Errorf("argv prefix = %v, want %v", args, wantPrefix)
	}
	// The root filesystem is always read-only; writes arrive as explicit bind
	// mounts, never as an implicitly writable rootfs.
	if !contains(args, "--read-only") {
		t.Errorf("expected --read-only, got %v", args)
	}
	if !containsPair(args, "--user", "10001:10001") {
		t.Errorf("expected non-root --user, got %v", args)
	}
}

// TestAppleNetworkArg_Mapping pins the policy->network-name mapping for the
// apple engine. Upstream reserves "none" as "no network attachment" and refuses
// to create a user network by that name, so mapping unknown modes to "none" is
// a real fail-closed default, not a hopeful one.
func TestAppleNetworkArg_Mapping(t *testing.T) {
	tests := []struct {
		name    string
		mode    string
		want    string
		wantErr bool
	}{
		{"open maps to default network", "open", appleDefaultNetwork, false},
		{"bridge maps to default network", "bridge", appleDefaultNetwork, false},
		{"deny maps to no network", "deny", appleNoNetwork, false},
		{"none maps to no network", "none", appleNoNetwork, false},
		{"empty maps to no network", "", appleNoNetwork, false},
		{"unknown mode maps to no network", "bogus", appleNoNetwork, false},
		{"allowlist is refused", "allowlist", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := appleNetworkArg(tt.mode)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("appleNetworkArg(%q) = %q, nil; want an error", tt.mode, got)
				}
				if got != "" {
					t.Errorf("appleNetworkArg(%q) returned network %q alongside an error; want \"\"", tt.mode, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("appleNetworkArg(%q) returned unexpected error: %v", tt.mode, err)
			}
			if got != tt.want {
				t.Errorf("appleNetworkArg(%q) = %q, want %q", tt.mode, got, tt.want)
			}
		})
	}
}

// appleModes are the network modes that build successfully.
var appleModes = []string{"", "none", "deny", "open", "bridge", "weird", "NONE", "Open"}

// TestBuildAppleArgs_AlwaysEmitsNetwork is SECURITY-critical. Omitting
// --network does NOT mean "no network" on this engine: `container system start`
// creates a vmnet network named "default" that containers attach to unless told
// otherwise, so an omitted flag falls OPEN.
func TestBuildAppleArgs_AlwaysEmitsNetwork(t *testing.T) {
	for _, mode := range appleModes {
		t.Run("mode="+mode, func(t *testing.T) {
			spec := appleSpec()
			spec.NetworkMode = mode
			args, err := BuildAppleArgs(spec, CommandSpec{Executable: "agent-cli"})
			if err != nil {
				t.Fatalf("BuildAppleArgs() returned unexpected error: %v", err)
			}
			n := 0
			idx := -1
			for i, a := range args {
				if a == "--network" {
					n++
					idx = i
				}
			}
			if n != 1 {
				t.Fatalf("expected exactly one --network token, got %d in %v", n, args)
			}
			if idx == len(args)-1 {
				t.Fatalf("--network is the last element, so it has no value: %v", args)
			}
			if v := args[idx+1]; v != "none" && v != "default" {
				t.Errorf("--network value = %q, want none or default", v)
			}
		})
	}
}

// TestBuildAppleArgs_NeverDefaultNetworkUnlessOpen is SECURITY-critical: the
// connected "default" network must be reachable ONLY by an explicit open/bridge
// request, never by an empty or unrecognized mode.
func TestBuildAppleArgs_NeverDefaultNetworkUnlessOpen(t *testing.T) {
	for _, mode := range appleModes {
		if mode == "open" || mode == "bridge" {
			continue
		}
		t.Run("mode="+mode, func(t *testing.T) {
			spec := appleSpec()
			spec.NetworkMode = mode
			args, err := BuildAppleArgs(spec, CommandSpec{Executable: "agent-cli"})
			if err != nil {
				t.Fatalf("BuildAppleArgs() returned unexpected error: %v", err)
			}
			if containsPair(args, "--network", "default") {
				t.Errorf("mode %q reached the connected default network: %v", mode, args)
			}
			if !containsPair(args, "--network", "none") {
				t.Errorf("mode %q did not fail closed to --network none: %v", mode, args)
			}
		})
	}
}

// TestBuildAppleArgs_EveryEnvOperandIsKeyValue is SECURITY-critical. Apple's
// `-e KEY` (with no "=") means "inherit KEY from the HOST environment", which is
// precisely the host-env passthrough Andbo forbids. An empty value must still
// emit "KEY=", never a bare "KEY".
func TestBuildAppleArgs_EveryEnvOperandIsKeyValue(t *testing.T) {
	spec := appleSpec()
	spec.Env = map[string]string{"EMPTY": "", "TOKEN": "v1", "ZED": "z"}
	args, err := BuildAppleArgs(spec, CommandSpec{
		Executable: "agent-cli",
		Env:        map[string]string{"ALSO_EMPTY": ""},
	})
	if err != nil {
		t.Fatalf("BuildAppleArgs() returned unexpected error: %v", err)
	}
	seen := 0
	for i, a := range args {
		if a != "-e" {
			continue
		}
		if i == len(args)-1 {
			t.Fatalf("-e is the last element, so it has no operand: %v", args)
		}
		operand := args[i+1]
		seen++
		if !strings.Contains(operand, "=") {
			t.Errorf("-e operand %q has no '='; a bare key inherits from the HOST environment", operand)
		}
	}
	if seen != 4 {
		t.Errorf("expected 4 -e operands, got %d in %v", seen, args)
	}
	if !containsPair(args, "-e", "EMPTY=") {
		t.Errorf("empty value must still emit EMPTY=, got %v", args)
	}
}

// TestBuildAppleArgs_EnvDeterministicAndCommandOverrides pins sorted order and
// the command-wins merge rule.
func TestBuildAppleArgs_EnvDeterministicAndCommandOverrides(t *testing.T) {
	spec := appleSpec()
	spec.Env = map[string]string{"ZULU": "z", "ALPHA": "a", "MIKE": "spec"}
	cmd := CommandSpec{Executable: "agent-cli", Env: map[string]string{"MIKE": "command"}}

	first, err := BuildAppleArgs(spec, cmd)
	if err != nil {
		t.Fatalf("BuildAppleArgs() returned unexpected error: %v", err)
	}
	second, err := BuildAppleArgs(spec, cmd)
	if err != nil {
		t.Fatalf("BuildAppleArgs() returned unexpected error: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Errorf("argv is not deterministic across builds:\n%v\n%v", first, second)
	}

	var envOperands []string
	for i, a := range first {
		if a == "-e" {
			envOperands = append(envOperands, first[i+1])
		}
	}
	want := []string{"ALPHA=a", "MIKE=command", "ZULU=z"}
	if !reflect.DeepEqual(envOperands, want) {
		t.Errorf("env operands = %v, want %v (sorted, command overriding spec)", envOperands, want)
	}
}

// TestBuildAppleArgs_Mounts pins the exact --mount syntax. Apple has no
// docker-style ":ro" suffix, so a naive port of the docker form would mount a
// read-only path WRITABLE.
func TestBuildAppleArgs_Mounts(t *testing.T) {
	spec := appleSpec()
	spec.ReadOnlyPaths = []string{"/ro"}
	spec.WritePaths = []string{"/rw"}
	args, err := BuildAppleArgs(spec, CommandSpec{Executable: "agent-cli"})
	if err != nil {
		t.Fatalf("BuildAppleArgs() returned unexpected error: %v", err)
	}
	if !containsPair(args, "--mount", "type=bind,source=/ro,target=/ro,readonly") {
		t.Errorf("read-only mount missing or malformed: %v", args)
	}
	if !containsPair(args, "--mount", "type=bind,source=/rw,target=/rw") {
		t.Errorf("read-write mount missing or malformed: %v", args)
	}
	// The writable mount must NOT carry the readonly marker, and the read-only
	// one must: swapping them silently inverts the policy.
	for i, a := range args {
		if a != "--mount" {
			continue
		}
		operand := args[i+1]
		if strings.Contains(operand, "source=/rw") && strings.HasSuffix(operand, ",readonly") {
			t.Errorf("write path was mounted read-only: %q", operand)
		}
		if strings.Contains(operand, "source=/ro") && !strings.HasSuffix(operand, ",readonly") {
			t.Errorf("read-only path was mounted writable: %q", operand)
		}
	}
	// The docker form must not leak in.
	if contains(args, "-v") {
		t.Errorf("apple argv must not use -v bind mounts: %v", args)
	}
}

// TestBuildAppleArgs_NoShellInterpolation ensures the builder never composes a
// shell string: a hostile-looking command must survive as one argv element.
func TestBuildAppleArgs_NoShellInterpolation(t *testing.T) {
	spec := appleSpec()
	cmd := CommandSpec{Executable: "sh", Args: []string{"-lc", "echo hi; rm -rf /"}}
	args, err := BuildAppleArgs(spec, cmd)
	if err != nil {
		t.Fatalf("BuildAppleArgs() returned unexpected error: %v", err)
	}
	tail := args[len(args)-3:]
	want := []string{"sh", "-lc", "echo hi; rm -rf /"}
	if !reflect.DeepEqual(tail, want) {
		t.Errorf("command tail = %v, want %v (each element passed through verbatim)", tail, want)
	}
	// Paths with spaces and metacharacters must also pass through untouched.
	spec2 := appleSpec()
	spec2.WritePaths = []string{"/tmp/a b$(whoami)"}
	args2, err := BuildAppleArgs(spec2, CommandSpec{Executable: "agent-cli"})
	if err != nil {
		t.Fatalf("BuildAppleArgs() returned unexpected error: %v", err)
	}
	if !containsPair(args2, "--mount", "type=bind,source=/tmp/a b$(whoami),target=/tmp/a b$(whoami)") {
		t.Errorf("path with metacharacters was altered: %v", args2)
	}
}

// TestBuildAppleArgs_OmitsOptionalFlagsWhenEmpty keeps the argv minimal, while
// the two unconditional guards stay put.
func TestBuildAppleArgs_OmitsOptionalFlagsWhenEmpty(t *testing.T) {
	spec := appleSpec()
	spec.Workdir = ""
	spec.User = ""
	spec.Env = nil
	args, err := BuildAppleArgs(spec, CommandSpec{Executable: "agent-cli"})
	if err != nil {
		t.Fatalf("BuildAppleArgs() returned unexpected error: %v", err)
	}
	for _, flag := range []string{"-w", "--user", "-e", "--mount"} {
		if contains(args, flag) {
			t.Errorf("expected %s to be omitted for an empty spec, got %v", flag, args)
		}
	}
	if !contains(args, "--read-only") || !contains(args, "--network") {
		t.Errorf("unconditional guards must survive an empty spec: %v", args)
	}
}

// TestBuildAppleArgs_ReadOnlyPairsWithTmpfs pins D4: --read-only is emitted
// only together with a writable /tmp. Andbo does NOT set --read-only on
// docker/podman, so --read-only alone would make the same policy+image succeed
// on docker and fail on apple with an opaque write error on /tmp.
func TestBuildAppleArgs_ReadOnlyPairsWithTmpfs(t *testing.T) {
	args, err := BuildAppleArgs(appleSpec(), CommandSpec{Executable: "agent-cli"})
	if err != nil {
		t.Fatalf("BuildAppleArgs() returned unexpected error: %v", err)
	}
	if !contains(args, "--read-only") {
		t.Fatalf("expected --read-only, got %v", args)
	}
	if !containsPair(args, "--tmpfs", "/tmp") {
		t.Fatalf("--read-only must be paired with --tmpfs /tmp, got %v", args)
	}
	// Adjacent and in order, so the pairing is visible at a glance in the argv
	// and cannot drift apart unnoticed.
	for i, a := range args {
		if a == "--read-only" {
			if i+2 >= len(args) || args[i+1] != "--tmpfs" || args[i+2] != "/tmp" {
				t.Errorf("expected `--read-only --tmpfs /tmp` adjacent, got %v", args[i:])
			}
		}
	}
}

// TestBuildAppleArgs_RefusesMountPathWithSeparators pins D5. Apple parses a
// --mount value by splitting on "," then "="; a path containing either yields a
// cryptic engine error. Andbo refuses it up front with the offending path named.
//
// The fail direction is CLOSED, so this is robustness/UX rather than a
// vulnerability: the parser has no "rw" or negating directive (readonly/ro are
// append-only), so a crafted path can only ADD read-only, never remove it.
func TestBuildAppleArgs_RefusesMountPathWithSeparators(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*RuntimeSpec)
		path   string
		char   string
	}{
		{"comma in write path", func(s *RuntimeSpec) { s.WritePaths = []string{"/work/a,b"} }, "/work/a,b", ","},
		{"equals in write path", func(s *RuntimeSpec) { s.WritePaths = []string{"/work/a=b"} }, "/work/a=b", "="},
		{"comma in readonly path", func(s *RuntimeSpec) { s.ReadOnlyPaths = []string{"/ro/x,y"} }, "/ro/x,y", ","},
		{"equals in readonly path", func(s *RuntimeSpec) { s.ReadOnlyPaths = []string{"/ro/x=y"} }, "/ro/x=y", "="},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := appleSpec()
			tt.mutate(&spec)
			args, err := BuildAppleArgs(spec, CommandSpec{Executable: "agent-cli"})
			if err == nil {
				t.Fatalf("BuildAppleArgs() = %v, nil; want a refusal for %q", args, tt.path)
			}
			if args != nil {
				t.Errorf("BuildAppleArgs() leaked partial argv %v alongside an error; want nil", args)
			}
			// The error must name the offending path and the character, or the
			// user cannot tell which of many mounts is at fault.
			if !strings.Contains(err.Error(), tt.path) {
				t.Errorf("error %q does not name the offending path %q", err, tt.path)
			}
			if !strings.Contains(err.Error(), tt.char) {
				t.Errorf("error %q does not name the offending character %q", err, tt.char)
			}
		})
	}
}

// TestBuildAppleArgs_RefusesUnsupportedSpec covers the fail-closed gates. Each
// refused field must produce an actionable error AND nil args: a partial argv
// escaping alongside an error is how an unsupported spec turns into a run that
// silently drops the protection the user asked for.
func TestBuildAppleArgs_RefusesUnsupportedSpec(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func(*RuntimeSpec)
		wantHint []string
	}{
		{
			name:     "privileged has no apple equivalent",
			mutate:   func(s *RuntimeSpec) { s.Privileged = true },
			wantHint: []string{"privileged", "apple", "--engine docker"},
		},
		{
			name:     "docker socket mount has no apple equivalent",
			mutate:   func(s *RuntimeSpec) { s.MountDockerSocket = true },
			wantHint: []string{"docker socket", "apple", "--engine docker"},
		},
		{
			name:     "allowlist egress is unenforceable",
			mutate:   func(s *RuntimeSpec) { s.NetworkMode = "allowlist" },
			wantHint: []string{"allowlist", "network.mode=deny"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := secureSpec()
			tt.mutate(&spec)
			args, err := BuildAppleArgs(spec, CommandSpec{Executable: "agent-cli"})
			if err == nil {
				t.Fatalf("BuildAppleArgs() = %v, nil; want a refusal", args)
			}
			if args != nil {
				t.Errorf("BuildAppleArgs() leaked partial argv %v alongside an error; want nil", args)
			}
			for _, want := range tt.wantHint {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q missing actionable hint %q", err, want)
				}
			}
		})
	}
}

// TestAppleNetworkArg_AllowlistErrorIsActionable requires the fail-closed
// refusal to explain what failed, why, and how to fix it.
func TestAppleNetworkArg_AllowlistErrorIsActionable(t *testing.T) {
	_, err := appleNetworkArg("allowlist")
	if err == nil {
		t.Fatal("expected an error for network.mode=allowlist on the apple engine")
	}
	for _, want := range []string{"allowlist", "apple", "--engine docker", "podman", "network.mode=deny"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("allowlist error %q missing actionable hint %q", err, want)
		}
	}
}
