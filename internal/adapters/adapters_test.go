package adapters

import (
	"context"
	"os/exec"
	"reflect"
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

func TestSupportedNames(t *testing.T) {
	want := []string{"custom", "aider"}
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
