package k8s

import (
	"strings"
	"testing"

	"github.com/qosikz/andbo/internal/runtime"
)

// hostWorkspacePath is the shape buildRuntimeSpec produces: the sanitized
// workspace copy on the HOST, used as the working directory, the single write
// mount, and HOME.
const hostWorkspacePath = "/Users/alice/.andbo/ws/abc123"

// imageWorkspaceSrc is where a run image is expected to carry the workspace.
const imageWorkspaceSrc = "/andbo/workspace"

// realRuntimeSpec is what internal/cli/cmd_run.go actually builds for a
// container run with network.mode=deny and no allowlisted secrets.
func realRuntimeSpec() runtime.RuntimeSpec {
	rs := containerSpec()
	rs.Workdir = hostWorkspacePath
	rs.WritePaths = []string{hostWorkspacePath}
	rs.Env = map[string]string{"HOME": hostWorkspacePath}
	return rs
}

func realCommandSpec() runtime.CommandSpec {
	return runtime.CommandSpec{
		Executable: "andbo-agent",
		Args:       []string{"--task", "fix failing tests"},
		WorkingDir: hostWorkspacePath,
	}
}

// imageWorkspaceSpec is a base JobSpec that declares where the workspace bytes
// come from, which is what lets the bridge accept a spec that has one.
func imageWorkspaceSpec() JobSpec {
	s := baseSpec()
	s.WorkspaceTransport = WorkspaceFromImage
	s.ImageWorkspacePath = imageWorkspaceSrc
	return s
}

func TestDefaultJobSpec_DeclaresAnEmptyWorkspace(t *testing.T) {
	s := DefaultJobSpec()
	if s.WorkspaceTransport != WorkspaceEmpty {
		t.Errorf("WorkspaceTransport = %q, want %q", s.WorkspaceTransport, WorkspaceEmpty)
	}
	if s.ImageWorkspacePath != "" {
		t.Errorf("ImageWorkspacePath = %q, want empty for the %q transport", s.ImageWorkspacePath, WorkspaceEmpty)
	}
}

// TestValidate_WorkspaceTransportMustBeDeclared covers the fail-closed default:
// a hand-built JobSpec that says nothing about the workspace must not render,
// because an unstated transport is indistinguishable from a lost one.
func TestValidate_WorkspaceTransportMustBeDeclared(t *testing.T) {
	s := validSpec()
	s.WorkspaceTransport = ""

	err := s.Validate()
	if err == nil {
		t.Fatal("Validate() = nil, want an error for an undeclared workspace transport")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "workspacetransport") {
		t.Errorf("error = %q, want it to name the workspaceTransport field", err)
	}
}

func TestValidate_WorkspaceTransportRejects(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*JobSpec)
		wantSub string
	}{
		{
			name:    "unknown transport",
			mutate:  func(s *JobSpec) { s.WorkspaceTransport = "rsync" },
			wantSub: "invalid",
		},
		{
			name: "image transport without a source path",
			mutate: func(s *JobSpec) {
				s.WorkspaceTransport = WorkspaceFromImage
				s.ImageWorkspacePath = ""
			},
			wantSub: "imageWorkspacePath",
		},
		{
			name: "empty transport with a source path",
			mutate: func(s *JobSpec) {
				s.WorkspaceTransport = WorkspaceEmpty
				s.ImageWorkspacePath = imageWorkspaceSrc
			},
			wantSub: "imageWorkspacePath",
		},
		{
			name: "relative source path",
			mutate: func(s *JobSpec) {
				s.WorkspaceTransport = WorkspaceFromImage
				s.ImageWorkspacePath = "andbo/workspace"
			},
			wantSub: "absolute",
		},
		{
			name: "non-canonical source path",
			mutate: func(s *JobSpec) {
				s.WorkspaceTransport = WorkspaceFromImage
				s.ImageWorkspacePath = "/andbo/../work/src"
			},
			wantSub: "clean",
		},
		{
			name: "root source path",
			mutate: func(s *JobSpec) {
				s.WorkspaceTransport = WorkspaceFromImage
				s.ImageWorkspacePath = "/"
			},
			wantSub: "/",
		},
		{
			name: "source path with unsafe runes",
			mutate: func(s *JobSpec) {
				s.WorkspaceTransport = WorkspaceFromImage
				s.ImageWorkspacePath = "/andbo/work\u202espace"
			},
			wantSub: "control",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := validSpec()
			tt.mutate(&s)

			err := s.Validate()
			if err == nil {
				t.Fatalf("Validate() = nil, want an error mentioning %q", tt.wantSub)
			}
			if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tt.wantSub)) {
				t.Errorf("error = %q, want it to mention %q", err, tt.wantSub)
			}
		})
	}
}

// TestSecurity_WorkspaceSourceCannotBeMaskedByAWritableVolume covers the
// silent-empty-workspace failure: the writable emptyDirs are mounted at
// WorkingDir and /tmp, so an image source that overlaps either one is hidden
// from the init container. The copy would then succeed while delivering
// nothing, and the agent would run against an empty repository.
func TestSecurity_WorkspaceSourceCannotBeMaskedByAWritableVolume(t *testing.T) {
	masked := []string{
		"/work",           // exactly the working directory
		"/work/src",       // under the working directory
		"/tmp",            // exactly the scratch volume
		"/tmp/workspace",  // under the scratch volume
		"/etc",            // reserved: the image owns it
		"/usr/local/repo", // reserved
	}

	for _, src := range masked {
		t.Run(src, func(t *testing.T) {
			s := validSpec()
			s.WorkingDir = DefaultWorkingDir
			s.WorkspaceTransport = WorkspaceFromImage
			s.ImageWorkspacePath = src

			if _, err := s.Render(); err == nil {
				t.Fatalf("Render() accepted image workspace source %q, which a writable volume or the image itself masks", src)
			}
		})
	}

	// A source directory that CONTAINS the working directory is equally broken:
	// the emptyDir hides part of the tree being copied.
	t.Run("source contains the working directory", func(t *testing.T) {
		s := validSpec()
		s.WorkingDir = "/srv/repo/checkout"
		s.WorkspaceTransport = WorkspaceFromImage
		s.ImageWorkspacePath = "/srv/repo"

		if _, err := s.Render(); err == nil {
			t.Fatal("Render() accepted an image workspace source that contains the working directory")
		}
	})

	t.Run("disjoint source is accepted", func(t *testing.T) {
		s := validSpec()
		s.WorkingDir = DefaultWorkingDir
		s.WorkspaceTransport = WorkspaceFromImage
		s.ImageWorkspacePath = imageWorkspaceSrc

		if err := s.Validate(); err != nil {
			t.Errorf("Validate() = %v, want nil for a disjoint image workspace source", err)
		}
	})
}

func TestRender_EmptyTransportRendersNoInitContainer(t *testing.T) {
	s := validSpec()
	s.WorkspaceTransport = WorkspaceEmpty

	manifest, err := s.Render()
	if err != nil {
		t.Fatalf("Render() = %v, want nil", err)
	}
	job := docs(t, manifest)[1]
	podSpec, ok := dig(t, job, "spec", "template", "spec").(map[string]any)
	if !ok {
		t.Fatal("job pod spec is not a mapping")
	}
	if got, present := podSpec["initContainers"]; present {
		t.Errorf("initContainers = %v, want the key absent for the %q transport", got, WorkspaceEmpty)
	}
}

// TestRender_ImageTransportCopiesIntoTheWritableVolume pins the transport
// mechanism: one init container, same image as the agent, exec form (no shell,
// so a path can never be reinterpreted as a command), writing into the same
// emptyDir the agent later works in.
func TestRender_ImageTransportCopiesIntoTheWritableVolume(t *testing.T) {
	s := validSpec()
	s.WorkingDir = DefaultWorkingDir
	s.WorkspaceTransport = WorkspaceFromImage
	s.ImageWorkspacePath = imageWorkspaceSrc

	manifest, err := s.Render()
	if err != nil {
		t.Fatalf("Render() = %v, want nil", err)
	}
	assertHardened(t, manifest)

	job := docs(t, manifest)[1]
	inits, ok := dig(t, job, "spec", "template", "spec", "initContainers").([]any)
	if !ok || len(inits) != 1 {
		t.Fatalf("initContainers = %v, want exactly one", dig(t, job, "spec", "template", "spec", "initContainers"))
	}
	init, ok := inits[0].(map[string]any)
	if !ok {
		t.Fatalf("initContainers[0] = %T, want a mapping", inits[0])
	}

	if init["image"] != s.Image {
		t.Errorf("initContainers[0].image = %v, want the agent image %q", init["image"], s.Image)
	}
	// A shell would turn the source and destination paths into script text.
	cmd, _ := init["command"].([]any)
	if len(cmd) != 1 || cmd[0] != "cp" {
		t.Errorf("initContainers[0].command = %v, want exec form [cp] with no shell", init["command"])
	}
	// "--" ends option parsing, so a path can never be read as a cp flag. The
	// validator already requires both paths to start with "/", but the separator
	// makes the whole class structurally impossible rather than merely excluded.
	args, _ := init["args"].([]any)
	want := []any{"-a", "--", imageWorkspaceSrc + "/.", DefaultWorkingDir}
	if len(args) != len(want) {
		t.Fatalf("initContainers[0].args = %v, want %v", args, want)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Errorf("initContainers[0].args[%d] = %v, want %v", i, args[i], want[i])
		}
	}
	// It must write into the volume the agent reads, not somewhere else.
	mounts, _ := init["volumeMounts"].([]any)
	var mounted bool
	for _, m := range mounts {
		mm, ok := m.(map[string]any)
		if ok && mm["name"] == workspaceVolume && mm["mountPath"] == DefaultWorkingDir {
			mounted = true
		}
	}
	if !mounted {
		t.Errorf("initContainers[0].volumeMounts = %v, want the %q volume at %q", mounts, workspaceVolume, DefaultWorkingDir)
	}
	// The init container carries no environment: it never needs one, and env is
	// where this codebase's secrets live.
	if env, present := init["env"]; present {
		t.Errorf("initContainers[0].env = %v, want none", env)
	}
}

// TestFromRuntimeSpec_AcceptsTheRealWorkspaceShape is the milestone: the spec
// internal/cli/cmd_run.go builds for a deny-network container run maps cleanly
// once the caller declares how the workspace reaches the pod.
func TestFromRuntimeSpec_AcceptsTheRealWorkspaceShape(t *testing.T) {
	got, err := FromRuntimeSpec(imageWorkspaceSpec(), realRuntimeSpec(), realCommandSpec())
	if err != nil {
		t.Fatalf("FromRuntimeSpec() = %v, want nil for a real runtime spec", err)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("mapped spec failed validation: %v", err)
	}

	// The pod working directory is the POD path, never the host path.
	if got.WorkingDir != DefaultWorkingDir {
		t.Errorf("WorkingDir = %q, want %q", got.WorkingDir, DefaultWorkingDir)
	}
	// HOME is part of the workspace, not a secret: it is rewritten, not dropped
	// (dropping it breaks git and every package manager under a non-root UID).
	// It must track the POD working directory, not merely happen to equal the
	// default — HOME on the read-only root filesystem is the failure this
	// rewrite exists to prevent.
	if got.Env["HOME"] != got.WorkingDir {
		t.Errorf("Env[HOME] = %q, want the pod working directory %q", got.Env["HOME"], got.WorkingDir)
	}

	manifest, err := got.Render()
	if err != nil {
		t.Fatalf("Render() = %v, want nil", err)
	}
	assertHardened(t, manifest)
	// No part of the host layout may survive into a manifest applied to a
	// shared cluster.
	for _, leak := range []string{hostWorkspacePath, "/Users/alice", "alice"} {
		if strings.Contains(manifest, leak) {
			t.Errorf("manifest leaks host path fragment %q:\n%s", leak, manifest)
		}
	}
}

func TestFromRuntimeSpec_WorkspaceFailsClosed(t *testing.T) {
	tests := []struct {
		name    string
		base    func() JobSpec
		mutate  func(*runtime.RuntimeSpec, *runtime.CommandSpec)
		wantSub string
	}{
		{
			name:    "workspace present but no transport declared",
			base:    baseSpec,
			mutate:  func(rs *runtime.RuntimeSpec, cs *runtime.CommandSpec) {},
			wantSub: "transport",
		},
		{
			name: "extra host write mount beside the workspace",
			base: imageWorkspaceSpec,
			mutate: func(rs *runtime.RuntimeSpec, cs *runtime.CommandSpec) {
				rs.WritePaths = append(rs.WritePaths, "/etc/andbo")
			},
			wantSub: "host path",
		},
		{
			name: "write mount that is not the workspace",
			base: imageWorkspaceSpec,
			mutate: func(rs *runtime.RuntimeSpec, cs *runtime.CommandSpec) {
				rs.WritePaths = []string{"/var/lib/other"}
			},
			wantSub: "host path",
		},
		{
			name: "read-only host mount",
			base: imageWorkspaceSpec,
			mutate: func(rs *runtime.RuntimeSpec, cs *runtime.CommandSpec) {
				rs.ReadOnlyPaths = []string{"/usr/share/ca-certificates"}
			},
			wantSub: "host path",
		},
		{
			name: "relative host workspace",
			base: imageWorkspaceSpec,
			mutate: func(rs *runtime.RuntimeSpec, cs *runtime.CommandSpec) {
				rs.Workdir = "ws/abc123"
				rs.WritePaths = []string{"ws/abc123"}
				rs.Env = map[string]string{"HOME": "ws/abc123"}
				cs.WorkingDir = "ws/abc123"
			},
			wantSub: "absolute",
		},
		{
			name: "non-canonical host workspace",
			base: imageWorkspaceSpec,
			mutate: func(rs *runtime.RuntimeSpec, cs *runtime.CommandSpec) {
				rs.Workdir = hostWorkspacePath + "/"
				rs.WritePaths = []string{hostWorkspacePath + "/"}
				rs.Env = map[string]string{"HOME": hostWorkspacePath + "/"}
				cs.WorkingDir = hostWorkspacePath + "/"
			},
			wantSub: "clean",
		},
		{
			name: "command working directory outside the workspace",
			base: imageWorkspaceSpec,
			mutate: func(rs *runtime.RuntimeSpec, cs *runtime.CommandSpec) {
				cs.WorkingDir = "/Users/alice/other-repo"
			},
			wantSub: "workspace",
		},
		{
			name: "HOME pointing somewhere other than the workspace",
			base: imageWorkspaceSpec,
			mutate: func(rs *runtime.RuntimeSpec, cs *runtime.CommandSpec) {
				rs.Env = map[string]string{"HOME": "/Users/alice"}
			},
			wantSub: "environment",
		},
		{
			name: "a secret riding along with HOME",
			base: imageWorkspaceSpec,
			mutate: func(rs *runtime.RuntimeSpec, cs *runtime.CommandSpec) {
				rs.Env["GITHUB_TOKEN"] = "ghp_realtokenvalue"
			},
			wantSub: "environment",
		},
		{
			name: "a secret in the command environment",
			base: imageWorkspaceSpec,
			mutate: func(rs *runtime.RuntimeSpec, cs *runtime.CommandSpec) {
				cs.Env = map[string]string{"ANTHROPIC_API_KEY": "sk-realkeyvalue"}
			},
			wantSub: "environment",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rs, cs := realRuntimeSpec(), realCommandSpec()
			tt.mutate(&rs, &cs)

			got, err := FromRuntimeSpec(tt.base(), rs, cs)
			if err == nil {
				t.Fatalf("FromRuntimeSpec() = %+v, want an error mentioning %q", got, tt.wantSub)
			}
			if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tt.wantSub)) {
				t.Errorf("error = %q, want it to mention %q", err, tt.wantSub)
			}
			for _, secret := range []string{"ghp_realtokenvalue", "sk-realkeyvalue"} {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("error leaks a secret value: %q", err)
				}
			}
			// A caller that ignores the error must not be able to render.
			if verr := got.Validate(); verr == nil {
				t.Error("the spec returned alongside the error validates; a caller ignoring the error would render it")
			}
		})
	}
}

// TestFromRuntimeSpec_DoesNotMutateCallerEnv covers map aliasing: JobSpec is
// copied by value, but its Env map is not, so rewriting HOME in place would
// edit the caller's map behind its back.
func TestFromRuntimeSpec_DoesNotMutateCallerEnv(t *testing.T) {
	base := imageWorkspaceSpec()
	base.Env = map[string]string{"ANDBO_RUN_ID": "01J0"}

	got, err := FromRuntimeSpec(base, realRuntimeSpec(), realCommandSpec())
	if err != nil {
		t.Fatalf("FromRuntimeSpec() = %v, want nil", err)
	}
	if _, mutated := base.Env["HOME"]; mutated {
		t.Errorf("base.Env was mutated: %v", base.Env)
	}
	if got.Env["ANDBO_RUN_ID"] != "01J0" {
		t.Errorf("Env = %v, want the caller's own literals preserved", got.Env)
	}
}

// TestSecurity_WorkspaceTransportIsDocumentedAsLimited keeps the enforcement
// notes honest about what the image transport does and does not do.
func TestSecurity_WorkspaceTransportIsDocumentedAsLimited(t *testing.T) {
	s := validSpec()
	s.WorkspaceTransport = WorkspaceFromImage
	s.ImageWorkspacePath = imageWorkspaceSrc
	notes := strings.ToLower(strings.Join(s.EnforcementNotes(), "\n"))

	for _, want := range []struct{ topic, substr string }{
		{"results are not copied back out", "results"},
		{"the image carries the workspace and anyone who can pull it can read it", "pull"},
		{"the copy depends on cp being present in the image", "cp"},
	} {
		if !strings.Contains(notes, want.substr) {
			t.Errorf("enforcement notes do not mention %s (looking for %q):\n%s", want.topic, want.substr, notes)
		}
	}
}
