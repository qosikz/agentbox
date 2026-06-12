package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// gitOrSkip skips the test if the git binary is not available, keeping the
// suite runnable offline and in minimal environments.
func gitOrSkip(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not found on PATH; skipping git-dependent test")
	}
}

// gitRun runs git in dir with a per-command identity so tests do not depend on
// (or mutate) the developer's global git config.
func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	full := append([]string{
		"-C", dir,
		"-c", "user.email=a@b.c",
		"-c", "user.name=test",
		"-c", "commit.gpgsign=false",
	}, args...)
	cmd := exec.Command("git", full...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}

// newTempRepo creates an initialized repo with one committed file and returns
// its directory.
func newTempRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitRun(t, dir, "init")
	gitRun(t, dir, "checkout", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-m", "initial")
	return dir
}

func TestIsRepo(t *testing.T) {
	gitOrSkip(t)
	repo := newTempRepo(t)
	if !IsRepo(repo) {
		t.Errorf("IsRepo(%q) = false, want true", repo)
	}
	bare := t.TempDir()
	if IsRepo(bare) {
		t.Errorf("IsRepo(%q) = true, want false for non-repo dir", bare)
	}
}

func TestOpen(t *testing.T) {
	gitOrSkip(t)
	repo := newTempRepo(t)
	r, err := Open(repo)
	if err != nil {
		t.Fatalf("Open(%q) error: %v", repo, err)
	}
	if r.Dir == "" {
		t.Errorf("Open returned empty Dir")
	}

	if _, err := Open(t.TempDir()); err == nil {
		t.Errorf("Open on non-repo dir: want error, got nil")
	}
}

func TestDiffAndChangedFiles(t *testing.T) {
	gitOrSkip(t)
	repo := newTempRepo(t)
	r, err := Open(repo)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ctx := context.Background()

	// Clean tree: diff empty, no changed files.
	diff, err := r.Diff(ctx)
	if err != nil {
		t.Fatalf("Diff (clean): %v", err)
	}
	if len(diff) != 0 {
		t.Errorf("Diff on clean tree = %q, want empty", diff)
	}
	files, err := r.ChangedFiles(ctx)
	if err != nil {
		t.Fatalf("ChangedFiles (clean): %v", err)
	}
	if len(files) != 0 {
		t.Errorf("ChangedFiles on clean tree = %v, want empty", files)
	}

	// Modify a tracked file.
	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("hello world\n"), 0o644); err != nil {
		t.Fatalf("modify file: %v", err)
	}
	diff, err = r.Diff(ctx)
	if err != nil {
		t.Fatalf("Diff (dirty): %v", err)
	}
	if len(diff) == 0 {
		t.Errorf("Diff after modification = empty, want non-empty")
	}
	files, err = r.ChangedFiles(ctx)
	if err != nil {
		t.Fatalf("ChangedFiles (dirty): %v", err)
	}
	if len(files) != 1 || files[0] != "file.txt" {
		t.Errorf("ChangedFiles = %v, want [file.txt]", files)
	}
}

func TestCommit(t *testing.T) {
	gitOrSkip(t)
	repo := newTempRepo(t)
	// Configure identity so Commit (which uses no -c flags) can succeed.
	gitRun(t, repo, "config", "user.email", "a@b.c")
	gitRun(t, repo, "config", "user.name", "test")
	gitRun(t, repo, "config", "commit.gpgsign", "false")

	r, err := Open(repo)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ctx := context.Background()

	// Nothing to commit on a clean tree -> clear error.
	if err := r.Commit(ctx, "noop"); err == nil {
		t.Errorf("Commit on clean tree: want error, got nil")
	}

	if err := os.WriteFile(filepath.Join(repo, "new.txt"), []byte("data\n"), 0o644); err != nil {
		t.Fatalf("write new file: %v", err)
	}
	if err := r.Commit(ctx, "add new.txt"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	files, err := r.ChangedFiles(ctx)
	if err != nil {
		t.Fatalf("ChangedFiles after commit: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("tree not clean after Commit: %v", files)
	}
}

func TestCreateBranch(t *testing.T) {
	gitOrSkip(t)
	repo := newTempRepo(t)
	r, err := Open(repo)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ctx := context.Background()
	name := BranchName("feature work")
	if err := r.CreateBranch(ctx, name); err != nil {
		t.Fatalf("CreateBranch(%q): %v", name, err)
	}
	out, err := exec.Command("git", "-C", repo, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}
	if got := string(regexp.MustCompile(`\s+$`).ReplaceAll(out, nil)); got != name {
		t.Errorf("current branch = %q, want %q", got, name)
	}
}

func TestBranchName(t *testing.T) {
	re := regexp.MustCompile(`^andbo/fix-failing-tests-[0-9a-f]{6}$`)
	got := BranchName("Fix Failing Tests!")
	if !re.MatchString(got) {
		t.Errorf("BranchName = %q, does not match %s", got, re)
	}
}

func TestSlugCapAndEmpty(t *testing.T) {
	tests := []struct {
		name string
		in   string
		re   string
	}{
		{"empty", "", `^andbo/task-[0-9a-f]{6}$`},
		{"only-symbols", "!!!@@@", `^andbo/task-[0-9a-f]{6}$`},
		{"leading-trailing", "  Hello  ", `^andbo/hello-[0-9a-f]{6}$`},
		{"long", "this is a very long task description that should be capped at around forty characters",
			`^andbo/[a-z0-9-]{1,40}-[0-9a-f]{6}$`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BranchName(tt.in)
			if !regexp.MustCompile(tt.re).MatchString(got) {
				t.Errorf("BranchName(%q) = %q, want match %s", tt.in, got, tt.re)
			}
		})
	}
}

func TestNormalizeRemote(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"shorthand", "github.com/org/repo", "https://github.com/org/repo.git"},
		{"shorthand-with-git", "github.com/org/repo.git", "https://github.com/org/repo.git"},
		{"https", "https://github.com/org/repo", "https://github.com/org/repo"},
		{"https-with-git", "https://github.com/org/repo.git", "https://github.com/org/repo.git"},
		{"http", "http://example.com/org/repo.git", "http://example.com/org/repo.git"},
		{"ssh", "git@github.com:org/repo.git", "git@github.com:org/repo.git"},
		{"trim-space", "  github.com/org/repo  ", "https://github.com/org/repo.git"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalizeRemote(tt.in); got != tt.want {
				t.Errorf("NormalizeRemote(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestOpenPRWithoutGH(t *testing.T) {
	// Only meaningful when gh is genuinely absent; otherwise it would try to
	// hit the network/auth. Skip when gh is present.
	if _, err := exec.LookPath("gh"); err == nil {
		t.Skip("gh CLI present; skipping gh-absent OpenPR test")
	}
	r := &Repo{Dir: t.TempDir()}
	out, err := r.OpenPR(context.Background(), PRInput{
		Title: "t", Body: "b", Base: "main", Branch: "andbo/x-abcdef",
	})
	if err != nil {
		t.Fatalf("OpenPR without gh: unexpected error %v", err)
	}
	if out.Created {
		t.Errorf("OpenPR without gh: Created = true, want false")
	}
	if out.Note == "" {
		t.Errorf("OpenPR without gh: empty Note, want explanation")
	}
}

func TestParsePRURL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "https://github.com/org/repo/pull/7\n", "https://github.com/org/repo/pull/7"},
		{"with-noise", "Creating pull request...\nhttps://github.com/org/repo/pull/8\n", "https://github.com/org/repo/pull/8"},
		{"no-url", "something went sideways", "something went sideways"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parsePRURL(tt.in); got != tt.want {
				t.Errorf("parsePRURL(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestFetchBranchPropagatesCommit(t *testing.T) {
	gitOrSkip(t)
	src := newTempRepo(t)

	// Simulate a disposable workspace: clone src, branch + commit there.
	work := filepath.Join(t.TempDir(), "work")
	gitRun(t, src, "clone", src, work) // -C src irrelevant for clone; produces work
	gitRun(t, work, "checkout", "-b", "andbo/test-abc123")
	if err := os.WriteFile(filepath.Join(work, "new.txt"), []byte("agent\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, work, "add", "-A")
	gitRun(t, work, "commit", "-m", "andbo: test")

	if err := FetchBranch(context.Background(), src, work, "andbo/test-abc123"); err != nil {
		t.Fatalf("FetchBranch: %v", err)
	}
	// The branch must now exist in the source repo.
	cmd := exec.Command("git", "-C", src, "rev-parse", "--verify", "andbo/test-abc123")
	if err := cmd.Run(); err != nil {
		t.Errorf("branch not propagated into source repo: %v", err)
	}
}

func TestFetchBranchEmptyName(t *testing.T) {
	if err := FetchBranch(context.Background(), ".", ".", ""); err == nil {
		t.Fatal("empty branch name should error")
	}
}

func TestPushBranchEmptyName(t *testing.T) {
	r := &Repo{Dir: "."}
	if err := r.PushBranch(context.Background(), "origin", ""); err == nil {
		t.Fatal("empty branch name should error")
	}
}

// TestCommitWithoutIdentityFallsBack reproduces a fresh CI runner: no global,
// system, or repo-local git identity. Commit must succeed via the Andbo
// fallback identity rather than fail with "Author identity unknown".
func TestCommitWithoutIdentityFallsBack(t *testing.T) {
	gitOrSkip(t)
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	repo := newTempRepo(t) // helper commits use per-command -c identity flags

	if err := os.WriteFile(filepath.Join(repo, "new.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := &Repo{Dir: repo}
	if err := r.Commit(context.Background(), "andbo: test"); err != nil {
		t.Fatalf("Commit without identity should fall back, got: %v", err)
	}
	out, err := exec.Command("git", "-C", repo, "log", "-1", "--format=%ae").Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := string(out); !strings.Contains(got, "andbo@qosi.kz") {
		t.Errorf("fallback committer = %q, want andbo@qosi.kz", got)
	}
}
