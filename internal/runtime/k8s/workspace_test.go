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
//
// The environment matters: buildAgentEnv sets PATH to a fixed container PATH
// for EVERY non-local isolation and passes LANG/LC_ALL/TERM through from the
// host when they are set, and buildRuntimeSpec then adds HOME. A fixture that
// carried HOME alone would certify a shape the producer never emits.
func realRuntimeSpec() runtime.RuntimeSpec {
	rs := containerSpec()
	rs.Workdir = hostWorkspacePath
	rs.WritePaths = []string{hostWorkspacePath}
	rs.Env = map[string]string{
		"HOME": hostWorkspacePath,
		"PATH": "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"LANG": "en_US.UTF-8",
		"TERM": "xterm-256color",
	}
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
			// Not "/" — every error contains a slash, so that would pass for any
			// rejection at all.
			wantSub: "image root",
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
	// "-R", not "-a". kubelet creates the emptyDir owned by root and fsGroup
	// only changes its GROUP, so the volume root stays uid 0. Any preserve flag
	// makes cp fatal when it cannot set the destination directory's timestamps
	// (utimensat needs ownership, not write permission), so `cp -a` exits 1 on
	// every real cluster and the init container fails the whole Job.
	args, _ := init["args"].([]any)
	want := []any{"-R", "--", imageWorkspaceSrc + "/.", DefaultWorkingDir}
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

	// PATH, LANG, LC_ALL, and TERM are container-runtime hygiene that Kubernetes
	// takes from the image; carrying Andbo's PATH would override an image whose
	// agent lives outside the standard directories.
	for _, dropped := range []string{"PATH", "LANG", "LC_ALL", "TERM"} {
		if v, present := got.Env[dropped]; present {
			t.Errorf("Env[%s] = %q, want it dropped: the image supplies it", dropped, v)
		}
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

// TestSecurity_ContainerHygieneEnvIsDroppedNotCarried covers the case where a
// hygiene name holds a HOST value: policy `secrets.allow: [PATH]` makes
// buildAgentEnv overwrite the fixed container PATH with the host's, which would
// put the host layout in the manifest. Dropping by name is safe whatever the
// value is, which is why these names are dropped rather than allowlisted.
func TestSecurity_ContainerHygieneEnvIsDroppedNotCarried(t *testing.T) {
	rs := realRuntimeSpec()
	rs.Env["PATH"] = "/Users/alice/.local/bin:/usr/bin"

	got, err := FromRuntimeSpec(imageWorkspaceSpec(), rs, realCommandSpec())
	if err != nil {
		t.Fatalf("FromRuntimeSpec() = %v, want nil", err)
	}
	if v, present := got.Env["PATH"]; present {
		t.Fatalf("Env[PATH] = %q, want it dropped; a host PATH must never reach the manifest", v)
	}

	manifest, err := got.Render()
	if err != nil {
		t.Fatalf("Render() = %v, want nil", err)
	}
	if strings.Contains(manifest, "/Users/alice") {
		t.Errorf("manifest leaks the host PATH:\n%s", manifest)
	}
}

// TestSecurity_WorkspacePathCannotSurviveInArgv covers the last channel that
// carries caller text verbatim into the manifest. Command and Args come from
// the adapter (the custom adapter's executable is policy-supplied, and the task
// text becomes an argument), so without this the host workspace path reaches a
// manifest applied to a shared cluster — and names a directory that does not
// exist in the pod.
func TestSecurity_WorkspacePathCannotSurviveInArgv(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*runtime.CommandSpec)
	}{
		{
			name:   "executable under the host workspace",
			mutate: func(cs *runtime.CommandSpec) { cs.Executable = hostWorkspacePath + "/bin/myagent" },
		},
		{
			name: "task text naming the host workspace",
			mutate: func(cs *runtime.CommandSpec) {
				cs.Args = []string{"--task", "port the code under " + hostWorkspacePath + "/legacy"}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cs := realCommandSpec()
			tt.mutate(&cs)

			got, err := FromRuntimeSpec(imageWorkspaceSpec(), realRuntimeSpec(), cs)
			if err == nil {
				manifest, rerr := got.Render()
				if rerr == nil && strings.Contains(manifest, hostWorkspacePath) {
					t.Fatalf("host workspace path reached the manifest:\n%s", manifest)
				}
				t.Fatal("FromRuntimeSpec() accepted argv carrying the host workspace path, want a rejection")
			}
			// The error has to name the pod path, or the fix is not obvious.
			if !strings.Contains(err.Error(), DefaultWorkingDir) {
				t.Errorf("error = %q, want it to name the pod path %q", err, DefaultWorkingDir)
			}
		})
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
			// Not "workspace" — nearly every message in this slice says it, so
			// the case would pass on any rejection.
			wantSub: "command spec sets host working directory",
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

// TestSecurity_WorkspaceCopyCarriesNoPreserveFlag guards the one invariant the
// rest of this suite structurally cannot: nothing here executes cp, so a copy
// that fails on every real cluster still renders and still passes.
//
// kubelet leaves the emptyDir's root owned by uid 0 (fsGroup changes only the
// group), and any preserve flag makes coreutils fatal when it cannot write that
// directory's metadata — which needs ownership, not write permission. `cp -a`
// therefore copies every file and exits 1, and the failed init container takes
// the whole Job down. Reintroducing a preserve flag is the specific regression
// worth failing loudly on.
func TestSecurity_WorkspaceCopyCarriesNoPreserveFlag(t *testing.T) {
	s := validSpec()
	s.WorkspaceTransport = WorkspaceFromImage
	s.ImageWorkspacePath = imageWorkspaceSrc

	inits := s.initContainers()
	if len(inits) != 1 {
		t.Fatalf("initContainers() = %d containers, want 1", len(inits))
	}

	for _, arg := range inits[0].Args {
		if !strings.HasPrefix(arg, "-") || arg == "--" {
			continue // an operand, not a flag
		}
		if strings.Contains(arg, "preserve") || strings.Contains(arg, "a") {
			t.Errorf("workspace copy flag %q preserves metadata; cp then exits non-zero because the emptyDir root is owned by uid 0, and the failed init container fails the Job", arg)
		}
	}
}

// TestPathsOverlap covers the containment test the masking checks rely on,
// including "/" — which the string form gets wrong (b+"/" becomes "//"), and
// which is only latent because Validate rejects a "/" source before reaching
// here. Relying on a guard elsewhere for correctness here is how that guard
// gets removed later as redundant.
func TestPathsOverlap(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{"/work", "/work", true},
		{"/work", "/work/src", true},
		{"/work/src", "/work", true},
		{"/", "/work", true},
		{"/work", "/", true},
		{"/", "/", true},
		{"/work", "/work2", false},
		{"/work2", "/work", false},
		{"/andbo/workspace", "/work", false},
		{"/tmp", "/work", false},
	}

	for _, tt := range tests {
		if got := pathsOverlap(tt.a, tt.b); got != tt.want {
			t.Errorf("pathsOverlap(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}

// TestFromRuntimeSpec_MountRejectionNamesTheActualCause covers two error
// messages that described a different cause than the one that tripped them: a
// spec with no workspace at all was told its mounts were "beyond the workspace
// copy" (naming an empty path), and a spec with one extra mount beside the
// workspace was told that BOTH paths were extra.
func TestFromRuntimeSpec_MountRejectionNamesTheActualCause(t *testing.T) {
	t.Run("no workspace declared", func(t *testing.T) {
		rs := realRuntimeSpec()
		rs.Workdir = ""
		rs.WritePaths = []string{"/var/lib/other"}
		delete(rs.Env, "HOME")

		_, err := FromRuntimeSpec(imageWorkspaceSpec(), rs, runtime.CommandSpec{Executable: "andbo-agent"})
		if err == nil {
			t.Fatal("FromRuntimeSpec() = nil, want a rejection")
		}
		if !strings.Contains(err.Error(), "declares no workspace") {
			t.Errorf("error = %q, want it to say no workspace was declared", err)
		}
		if strings.Contains(err.Error(), `""`) {
			t.Errorf("error names an empty path as the workspace: %q", err)
		}
	})

	t.Run("one extra mount beside the workspace", func(t *testing.T) {
		rs := realRuntimeSpec()
		rs.WritePaths = []string{hostWorkspacePath, "/etc/andbo"}

		_, err := FromRuntimeSpec(imageWorkspaceSpec(), rs, realCommandSpec())
		if err == nil {
			t.Fatal("FromRuntimeSpec() = nil, want a rejection")
		}
		if !strings.Contains(err.Error(), "1 host path") {
			t.Errorf("error = %q, want it to count exactly 1 extra path (not both mounts)", err)
		}
	})
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
		// Not "pull": the pre-existing note about kubelet image pulls already
		// contains it, so this case would pass with the new note deleted.
		{"the image carries the workspace and anyone who can pull it can read it", "registry"},
		{"the copy depends on cp being present in the image", "cp -r"},
		// The transport declaration proves a transport was CHOSEN, not that the
		// image actually holds the intended workspace. A source directory that
		// exists but is empty copies nothing and exits 0.
		{"an empty source copies nothing and still succeeds", "empty"},
		{"the renderer cannot verify the image carries the right workspace", "verify"},
		{"the deadline covers the copy as well as the agent", "activedeadlineseconds"},
	} {
		if !strings.Contains(notes, want.substr) {
			t.Errorf("enforcement notes do not mention %s (looking for %q):\n%s", want.topic, want.substr, notes)
		}
	}
}
