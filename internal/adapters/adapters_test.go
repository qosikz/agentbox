package adapters

import (
	"context"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/qosi/agentbox/internal/config"
)

func TestCustomAdapterName(t *testing.T) {
	if got := (CustomAdapter{}).Name(); got != "custom" {
		t.Fatalf("Name() = %q, want %q", got, "custom")
	}
}

func TestAiderAdapterName(t *testing.T) {
	if got := (AiderAdapter{}).Name(); got != "aider" {
		t.Fatalf("Name() = %q, want %q", got, "aider")
	}
}

func TestNewAdapterNames(t *testing.T) {
	tests := []struct {
		adapter Adapter
		want    string
	}{
		{CodexAdapter{}, "codex"},
		{GeminiAdapter{}, "gemini"},
		{OpenCodeAdapter{}, "opencode"},
		{GooseAdapter{}, "goose"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := tt.adapter.Name(); got != tt.want {
				t.Errorf("Name() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCustomAdapterBuildCommand(t *testing.T) {
	tests := []struct {
		name     string
		adapter  CustomAdapter
		input    Input
		wantArgs []string
	}{
		{
			name:    "spaced placeholder templated and other args preserved",
			adapter: CustomAdapter{Command: "mytool", Args: []string{"run", "--prompt", "{{ task }}", "--verbose"}},
			input: Input{
				Task:          "fix the bug",
				WorkspacePath: "/work",
				Env:           map[string]string{"FOO": "bar"},
			},
			wantArgs: []string{"run", "--prompt", "fix the bug", "--verbose"},
		},
		{
			name:     "tight placeholder templated",
			adapter:  CustomAdapter{Command: "mytool", Args: []string{"{{task}}"}},
			input:    Input{Task: "do it"},
			wantArgs: []string{"do it"},
		},
		{
			name:     "extra args appended after templated args",
			adapter:  CustomAdapter{Command: "mytool", Args: []string{"--prompt", "{{ task }}"}},
			input:    Input{Task: "ship", ExtraArgs: []string{"--flag", "x"}},
			wantArgs: []string{"--prompt", "ship", "--flag", "x"},
		},
		{
			name:     "no placeholder leaves args unchanged",
			adapter:  CustomAdapter{Command: "mytool", Args: []string{"--no-task"}},
			input:    Input{Task: "ignored"},
			wantArgs: []string{"--no-task"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd, err := tt.adapter.BuildCommand(context.Background(), tt.input)
			if err != nil {
				t.Fatalf("BuildCommand() error = %v", err)
			}
			if cmd.Executable != tt.adapter.Command {
				t.Errorf("Executable = %q, want %q", cmd.Executable, tt.adapter.Command)
			}
			if !reflect.DeepEqual(cmd.Args, tt.wantArgs) {
				t.Errorf("Args = %#v, want %#v", cmd.Args, tt.wantArgs)
			}
			if cmd.WorkingDir != tt.input.WorkspacePath {
				t.Errorf("WorkingDir = %q, want %q", cmd.WorkingDir, tt.input.WorkspacePath)
			}
			if !reflect.DeepEqual(cmd.Env, tt.input.Env) {
				t.Errorf("Env = %#v, want %#v", cmd.Env, tt.input.Env)
			}
		})
	}
}

func TestCustomAdapterBuildCommandDoesNotMutateInputArgs(t *testing.T) {
	original := []string{"--prompt", "{{ task }}"}
	a := CustomAdapter{Command: "mytool", Args: original}
	if _, err := a.BuildCommand(context.Background(), Input{Task: "abc"}); err != nil {
		t.Fatalf("BuildCommand() error = %v", err)
	}
	if original[1] != "{{ task }}" {
		t.Errorf("input Args mutated: got %q", original[1])
	}
}

func TestAiderAdapterBuildCommand(t *testing.T) {
	a := AiderAdapter{}
	input := Input{
		Task:          "refactor",
		WorkspacePath: "/repo",
		Env:           map[string]string{"K": "V"},
		ExtraArgs:     []string{"--yes"},
	}
	cmd, err := a.BuildCommand(context.Background(), input)
	if err != nil {
		t.Fatalf("BuildCommand() error = %v", err)
	}
	if cmd.Executable != "aider" {
		t.Errorf("Executable = %q, want %q", cmd.Executable, "aider")
	}
	wantArgs := []string{"--message", "refactor", "--yes"}
	if !reflect.DeepEqual(cmd.Args, wantArgs) {
		t.Errorf("Args = %#v, want %#v", cmd.Args, wantArgs)
	}
	if cmd.WorkingDir != input.WorkspacePath {
		t.Errorf("WorkingDir = %q, want %q", cmd.WorkingDir, input.WorkspacePath)
	}
	if !reflect.DeepEqual(cmd.Env, input.Env) {
		t.Errorf("Env = %#v, want %#v", cmd.Env, input.Env)
	}
}

func TestNewAdapterBuildCommands(t *testing.T) {
	baseEnv := map[string]string{"K": "V"}

	tests := []struct {
		name     string
		adapter  Adapter
		input    Input
		wantExe  string
		wantArgs []string
		wantEnv  map[string]string
	}{
		{
			name:     "codex basic",
			adapter:  CodexAdapter{},
			input:    Input{Task: "fix tests", WorkspacePath: "/repo", Env: baseEnv},
			wantExe:  "codex",
			wantArgs: []string{"exec", "--sandbox", "workspace-write", "fix tests"},
			wantEnv:  map[string]string{"K": "V"},
		},
		{
			name:     "codex extra args appended",
			adapter:  CodexAdapter{},
			input:    Input{Task: "fix tests", WorkspacePath: "/repo", Env: baseEnv, ExtraArgs: []string{"--dangerously-bypass-approvals-and-sandbox"}},
			wantExe:  "codex",
			wantArgs: []string{"exec", "--sandbox", "workspace-write", "fix tests", "--dangerously-bypass-approvals-and-sandbox"},
			wantEnv:  map[string]string{"K": "V"},
		},
		{
			name:     "gemini basic",
			adapter:  GeminiAdapter{},
			input:    Input{Task: "fix tests", WorkspacePath: "/repo", Env: baseEnv},
			wantExe:  "gemini",
			wantArgs: []string{"--approval-mode", "auto_edit", "-p", "fix tests"},
			wantEnv:  map[string]string{"K": "V"},
		},
		{
			name:     "gemini extra args appended",
			adapter:  GeminiAdapter{},
			input:    Input{Task: "fix tests", WorkspacePath: "/repo", Env: baseEnv, ExtraArgs: []string{"--approval-mode", "yolo"}},
			wantExe:  "gemini",
			wantArgs: []string{"--approval-mode", "auto_edit", "-p", "fix tests", "--approval-mode", "yolo"},
			wantEnv:  map[string]string{"K": "V"},
		},
		{
			name:     "opencode basic",
			adapter:  OpenCodeAdapter{},
			input:    Input{Task: "fix tests", WorkspacePath: "/repo", Env: baseEnv},
			wantExe:  "opencode",
			wantArgs: []string{"run", "fix tests"},
			wantEnv:  map[string]string{"K": "V"},
		},
		{
			name:     "opencode extra args appended",
			adapter:  OpenCodeAdapter{},
			input:    Input{Task: "fix tests", WorkspacePath: "/repo", Env: baseEnv, ExtraArgs: []string{"--dangerously-skip-permissions"}},
			wantExe:  "opencode",
			wantArgs: []string{"run", "fix tests", "--dangerously-skip-permissions"},
			wantEnv:  map[string]string{"K": "V"},
		},
		{
			name:     "goose basic merges GOOSE_MODE",
			adapter:  GooseAdapter{},
			input:    Input{Task: "fix tests", WorkspacePath: "/repo", Env: baseEnv},
			wantExe:  "goose",
			wantArgs: []string{"run", "--no-session", "-t", "fix tests"},
			wantEnv:  map[string]string{"K": "V", "GOOSE_MODE": "auto"},
		},
		{
			name:     "goose extra args appended",
			adapter:  GooseAdapter{},
			input:    Input{Task: "fix tests", WorkspacePath: "/repo", Env: baseEnv, ExtraArgs: []string{"--debug"}},
			wantExe:  "goose",
			wantArgs: []string{"run", "--no-session", "-t", "fix tests", "--debug"},
			wantEnv:  map[string]string{"K": "V", "GOOSE_MODE": "auto"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd, err := tt.adapter.BuildCommand(context.Background(), tt.input)
			if err != nil {
				t.Fatalf("BuildCommand() error = %v", err)
			}
			if cmd.Executable != tt.wantExe {
				t.Errorf("Executable = %q, want %q", cmd.Executable, tt.wantExe)
			}
			if !reflect.DeepEqual(cmd.Args, tt.wantArgs) {
				t.Errorf("Args = %#v, want %#v", cmd.Args, tt.wantArgs)
			}
			if cmd.WorkingDir != tt.input.WorkspacePath {
				t.Errorf("WorkingDir = %q, want %q", cmd.WorkingDir, tt.input.WorkspacePath)
			}
			if !reflect.DeepEqual(cmd.Env, tt.wantEnv) {
				t.Errorf("Env = %#v, want %#v", cmd.Env, tt.wantEnv)
			}
		})
	}
}

func TestGooseAdapterEnvMerge(t *testing.T) {
	t.Run("does not mutate input env", func(t *testing.T) {
		in := Input{Task: "t", Env: map[string]string{"A": "1"}}
		cmd, err := GooseAdapter{}.BuildCommand(context.Background(), in)
		if err != nil {
			t.Fatalf("BuildCommand() error = %v", err)
		}
		if got := cmd.Env["GOOSE_MODE"]; got != "auto" {
			t.Errorf("Env[GOOSE_MODE] = %q, want %q", got, "auto")
		}
		if got := cmd.Env["A"]; got != "1" {
			t.Errorf("Env[A] = %q, want %q (caller env must be carried over)", got, "1")
		}
		if _, ok := in.Env["GOOSE_MODE"]; ok {
			t.Error("input.Env was mutated: GOOSE_MODE leaked into the caller map")
		}
		if !reflect.DeepEqual(in.Env, map[string]string{"A": "1"}) {
			t.Errorf("input.Env changed: got %#v", in.Env)
		}
	})

	t.Run("respects caller-provided GOOSE_MODE", func(t *testing.T) {
		in := Input{Task: "t", Env: map[string]string{"GOOSE_MODE": "chat"}}
		cmd, err := GooseAdapter{}.BuildCommand(context.Background(), in)
		if err != nil {
			t.Fatalf("BuildCommand() error = %v", err)
		}
		if got := cmd.Env["GOOSE_MODE"]; got != "chat" {
			t.Errorf("Env[GOOSE_MODE] = %q, want caller-provided %q", got, "chat")
		}
	})

	t.Run("nil input env still sets GOOSE_MODE", func(t *testing.T) {
		cmd, err := GooseAdapter{}.BuildCommand(context.Background(), Input{Task: "t"})
		if err != nil {
			t.Fatalf("BuildCommand() error = %v", err)
		}
		if got := cmd.Env["GOOSE_MODE"]; got != "auto" {
			t.Errorf("Env[GOOSE_MODE] = %q, want %q", got, "auto")
		}
	})
}

func TestGet(t *testing.T) {
	cfg := config.CustomAgentConfig{Command: "mytool", Args: []string{"a", "{{ task }}"}}

	tests := []struct {
		name      string
		agentName string
		custom    config.CustomAgentConfig
		wantErr   bool
		check     func(t *testing.T, a Adapter)
	}{
		{
			name:      "custom uses provided config",
			agentName: "custom",
			custom:    cfg,
			check: func(t *testing.T, a Adapter) {
				ca, ok := a.(CustomAdapter)
				if !ok {
					t.Fatalf("Get returned %T, want CustomAdapter", a)
				}
				if ca.Command != cfg.Command {
					t.Errorf("Command = %q, want %q", ca.Command, cfg.Command)
				}
				if !reflect.DeepEqual(ca.Args, cfg.Args) {
					t.Errorf("Args = %#v, want %#v", ca.Args, cfg.Args)
				}
			},
		},
		{
			name:      "aider",
			agentName: "aider",
			check: func(t *testing.T, a Adapter) {
				if _, ok := a.(AiderAdapter); !ok {
					t.Fatalf("Get returned %T, want AiderAdapter", a)
				}
			},
		},
		{
			name:      "claude",
			agentName: "claude",
			check: func(t *testing.T, a Adapter) {
				if _, ok := a.(ClaudeAdapter); !ok {
					t.Fatalf("Get returned %T, want ClaudeAdapter", a)
				}
			},
		},
		{
			name:      "codex",
			agentName: "codex",
			check: func(t *testing.T, a Adapter) {
				if _, ok := a.(CodexAdapter); !ok {
					t.Fatalf("Get returned %T, want CodexAdapter", a)
				}
			},
		},
		{
			name:      "gemini",
			agentName: "gemini",
			check: func(t *testing.T, a Adapter) {
				if _, ok := a.(GeminiAdapter); !ok {
					t.Fatalf("Get returned %T, want GeminiAdapter", a)
				}
			},
		},
		{
			name:      "opencode",
			agentName: "opencode",
			check: func(t *testing.T, a Adapter) {
				if _, ok := a.(OpenCodeAdapter); !ok {
					t.Fatalf("Get returned %T, want OpenCodeAdapter", a)
				}
			},
		},
		{
			name:      "goose",
			agentName: "goose",
			check: func(t *testing.T, a Adapter) {
				if _, ok := a.(GooseAdapter); !ok {
					t.Fatalf("Get returned %T, want GooseAdapter", a)
				}
			},
		},
		{
			name:      "unknown errors",
			agentName: "nope",
			wantErr:   true,
		},
		{
			name:      "empty errors",
			agentName: "",
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, err := Get(tt.agentName, tt.custom)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Get(%q) error = nil, want error", tt.agentName)
				}
				return
			}
			if err != nil {
				t.Fatalf("Get(%q) error = %v", tt.agentName, err)
			}
			if a == nil {
				t.Fatalf("Get(%q) returned nil adapter", tt.agentName)
			}
			if tt.check != nil {
				tt.check(t, a)
			}
		})
	}
}

func TestGetResolvesAllSupportedNames(t *testing.T) {
	for _, name := range SupportedNames() {
		t.Run(name, func(t *testing.T) {
			a, err := Get(name, config.CustomAgentConfig{Command: "mytool"})
			if err != nil {
				t.Fatalf("Get(%q) error = %v", name, err)
			}
			if got := a.Name(); got != name {
				t.Errorf("Get(%q).Name() = %q, want %q", name, got, name)
			}
		})
	}
}

func TestSupportedNames(t *testing.T) {
	want := []string{"aider", "claude", "codex", "custom", "gemini", "goose", "opencode"}
	if got := SupportedNames(); !reflect.DeepEqual(got, want) {
		t.Errorf("SupportedNames() = %#v, want %#v", got, want)
	}
}

func TestCustomAdapterCheckMissing(t *testing.T) {
	a := CustomAdapter{Command: "definitely-not-a-real-binary-xyz-123"}
	if err := a.Check(context.Background()); err == nil {
		t.Fatal("Check() error = nil, want error for missing command")
	}
}

func TestCustomAdapterCheckPresent(t *testing.T) {
	// Use a binary that should exist on the host; skip if it is not resolvable
	// (keeps the test offline-safe and environment-independent).
	const present = "echo"
	if _, err := exec.LookPath(present); err != nil {
		t.Skipf("%q not found on PATH; skipping", present)
	}
	a := CustomAdapter{Command: present}
	if err := a.Check(context.Background()); err != nil {
		t.Fatalf("Check() error = %v, want nil for present command", err)
	}
}

func TestCustomAdapterCheckEmptyCommand(t *testing.T) {
	if err := (CustomAdapter{}).Check(context.Background()); err == nil {
		t.Fatal("Check() error = nil, want error for empty command")
	}
}

func TestAiderAdapterCheck(t *testing.T) {
	// Aider is an external binary; only assert the failure path when it is
	// absent, otherwise skip so the suite stays offline and deterministic.
	if _, err := exec.LookPath("aider"); err == nil {
		t.Skip("aider is installed; skipping missing-binary assertion")
	}
	if err := (AiderAdapter{}).Check(context.Background()); err == nil {
		t.Fatal("Check() error = nil, want error when aider is absent")
	}
}

func TestNewAdapterChecks(t *testing.T) {
	// These are external binaries; only assert the failure path (with its
	// install hint) when the binary is absent, otherwise skip so the suite
	// stays offline and deterministic.
	tests := []struct {
		binary  string
		adapter Adapter
		hint    string
	}{
		{"codex", CodexAdapter{}, "npm install -g @openai/codex"},
		{"gemini", GeminiAdapter{}, "npm install -g @google/gemini-cli"},
		{"opencode", OpenCodeAdapter{}, "https://opencode.ai/install"},
		{"goose", GooseAdapter{}, "brew install block-goose-cli"},
	}

	for _, tt := range tests {
		t.Run(tt.binary, func(t *testing.T) {
			if _, err := exec.LookPath(tt.binary); err == nil {
				t.Skipf("%s is installed; skipping missing-binary assertion", tt.binary)
			}
			err := tt.adapter.Check(context.Background())
			if err == nil {
				t.Fatalf("Check() error = nil, want error when %s is absent", tt.binary)
			}
			if !strings.Contains(err.Error(), tt.hint) {
				t.Errorf("Check() error %q does not mention install hint %q", err, tt.hint)
			}
		})
	}
}
