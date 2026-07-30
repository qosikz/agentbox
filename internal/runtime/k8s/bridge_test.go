package k8s

import (
	"strings"
	"testing"
	"time"

	"github.com/qosikz/andbo/internal/runtime"
)

func baseSpec() JobSpec {
	s := DefaultJobSpec()
	s.Name = "fix-tests"
	s.Namespace = "andbo-runs"
	return s
}

func containerSpec() runtime.RuntimeSpec {
	return runtime.RuntimeSpec{
		Engine:      "docker",
		Image:       "ghcr.io/qosikz/andbo/runtime:latest",
		NetworkMode: "none",
		User:        "10001:10001",
	}
}

func TestFromRuntimeSpec_MapsSecureDefaults(t *testing.T) {
	cs := runtime.CommandSpec{
		Executable: "andbo-agent",
		Args:       []string{"--task", "fix failing tests"},
		Timeout:    20 * time.Minute,
	}

	got, err := FromRuntimeSpec(baseSpec(), containerSpec(), cs)
	if err != nil {
		t.Fatalf("FromRuntimeSpec() = %v, want nil", err)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("mapped spec failed validation: %v", err)
	}

	if got.Image != containerSpec().Image {
		t.Errorf("Image = %q, want %q", got.Image, containerSpec().Image)
	}
	// The pod working directory comes from the base spec, never from the host.
	if got.WorkingDir != DefaultWorkingDir {
		t.Errorf("WorkingDir = %q, want %q", got.WorkingDir, DefaultWorkingDir)
	}
	if got.RunAsUser != 10001 {
		t.Errorf("RunAsUser = %d, want 10001", got.RunAsUser)
	}
	if got.NetworkMode != NetworkDeny {
		t.Errorf("NetworkMode = %q, want %q", got.NetworkMode, NetworkDeny)
	}
	if len(got.Command) != 1 || got.Command[0] != "andbo-agent" {
		t.Errorf("Command = %v, want [andbo-agent]", got.Command)
	}
	if strings.Join(got.Args, " ") != "--task fix failing tests" {
		t.Errorf("Args = %v, want [--task, fix failing tests]", got.Args)
	}
	if got.ActiveDeadlineSeconds != 1200 {
		t.Errorf("ActiveDeadlineSeconds = %d, want 1200 (from CommandSpec.Timeout)", got.ActiveDeadlineSeconds)
	}
	// Host environment is never bridged: see TestSecurity_HostEnvIsNeverBridged.
	if len(got.Env) != 0 {
		t.Errorf("Env = %v, want empty: host environment must not reach the manifest", got.Env)
	}
}

func TestFromRuntimeSpec_FailsClosed(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*runtime.RuntimeSpec)
		wantSub string
	}{
		{
			name:    "privileged",
			mutate:  func(rs *runtime.RuntimeSpec) { rs.Privileged = true },
			wantSub: "privileged",
		},
		{
			name:    "docker socket",
			mutate:  func(rs *runtime.RuntimeSpec) { rs.MountDockerSocket = true },
			wantSub: "docker socket",
		},
		{
			name:    "host read-only mounts",
			mutate:  func(rs *runtime.RuntimeSpec) { rs.ReadOnlyPaths = []string{"/src"} },
			wantSub: "host path",
		},
		{
			name:    "host write mounts",
			mutate:  func(rs *runtime.RuntimeSpec) { rs.WritePaths = []string{"/src"} },
			wantSub: "host path",
		},
		{
			name:    "network open",
			mutate:  func(rs *runtime.RuntimeSpec) { rs.NetworkMode = "open" },
			wantSub: "not implemented",
		},
		{
			name:    "network bridge",
			mutate:  func(rs *runtime.RuntimeSpec) { rs.NetworkMode = "bridge" },
			wantSub: "not implemented",
		},
		{
			name: "network allowlist",
			mutate: func(rs *runtime.RuntimeSpec) {
				rs.NetworkMode = "allowlist"
				rs.AllowedDomains = []string{"github.com"}
			},
			wantSub: "not implemented",
		},
		{
			name:    "network mode unset is ambiguous",
			mutate:  func(rs *runtime.RuntimeSpec) { rs.NetworkMode = "" },
			wantSub: "network",
		},
		{
			name:    "root user",
			mutate:  func(rs *runtime.RuntimeSpec) { rs.User = "0:0" },
			wantSub: "non-root",
		},
		{
			name:    "root by name",
			mutate:  func(rs *runtime.RuntimeSpec) { rs.User = "root" },
			wantSub: "numeric",
		},
		{
			name:    "unresolvable username",
			mutate:  func(rs *runtime.RuntimeSpec) { rs.User = "agent:agent" },
			wantSub: "numeric",
		},
		{
			// The renderer has one UID field that also becomes the GID and
			// fsGroup, so a differing GID must fail rather than be dropped.
			name:    "gid differs from uid",
			mutate:  func(rs *runtime.RuntimeSpec) { rs.User = "10001:20002" },
			wantSub: "gid",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rs := containerSpec()
			tt.mutate(&rs)

			got, err := FromRuntimeSpec(baseSpec(), rs, runtime.CommandSpec{Executable: "andbo-agent"})
			if err == nil {
				t.Fatalf("FromRuntimeSpec() = %+v, want an error mentioning %q", got, tt.wantSub)
			}
			if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tt.wantSub)) {
				t.Errorf("error = %q, want it to mention %q", err.Error(), tt.wantSub)
			}
		})
	}
}

func TestFromRuntimeSpec_TimeoutBounds(t *testing.T) {
	tests := []struct {
		name    string
		timeout time.Duration
		want    int64
		wantErr bool
	}{
		{name: "unset keeps the base default", timeout: 0, want: DefaultActiveDeadlineSeconds},
		{name: "rounds up to whole seconds", timeout: 1500 * time.Millisecond, want: 2},
		{name: "sub-second still yields at least one second", timeout: time.Millisecond, want: 1},
		{name: "exact minutes", timeout: 30 * time.Minute, want: 1800},
		{name: "over the cap is rejected", timeout: (MaxActiveDeadlineSeconds + 1) * time.Second, wantErr: true},
		{name: "negative is rejected", timeout: -time.Second, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cs := runtime.CommandSpec{Executable: "andbo-agent", Timeout: tt.timeout}

			got, err := FromRuntimeSpec(baseSpec(), containerSpec(), cs)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("FromRuntimeSpec() = %d, want an error", got.ActiveDeadlineSeconds)
				}
				return
			}
			if err != nil {
				t.Fatalf("FromRuntimeSpec() = %v, want nil", err)
			}
			if got.ActiveDeadlineSeconds != tt.want {
				t.Errorf("ActiveDeadlineSeconds = %d, want %d", got.ActiveDeadlineSeconds, tt.want)
			}
		})
	}
}

func TestFromRuntimeSpec_DenyModeAliases(t *testing.T) {
	for _, mode := range []string{"none", "deny"} {
		t.Run(mode, func(t *testing.T) {
			rs := containerSpec()
			rs.NetworkMode = mode

			got, err := FromRuntimeSpec(baseSpec(), rs, runtime.CommandSpec{Executable: "andbo-agent"})
			if err != nil {
				t.Fatalf("FromRuntimeSpec() = %v, want nil", err)
			}
			if got.NetworkMode != NetworkDeny {
				t.Errorf("NetworkMode = %q, want %q", got.NetworkMode, NetworkDeny)
			}
		})
	}
}

func TestFromRuntimeSpec_RendersHardenedManifest(t *testing.T) {
	got, err := FromRuntimeSpec(baseSpec(), containerSpec(), runtime.CommandSpec{
		Executable: "andbo-agent",
		Args:       []string{"--task", "fix failing tests"},
	})
	if err != nil {
		t.Fatalf("FromRuntimeSpec() = %v, want nil", err)
	}

	manifest, err := got.Render()
	if err != nil {
		t.Fatalf("Render() = %v, want nil", err)
	}
	assertHardened(t, manifest)
}
