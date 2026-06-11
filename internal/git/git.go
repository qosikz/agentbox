package git

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

// run executes "git" with the given args and returns trimmed stdout. On
// failure it returns an actionable error that includes git's stderr, which
// usually explains the precise cause (e.g. "not a git repository").
func run(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return stdout.Bytes(), nil
}

// IsRepo reports whether dir is inside a git working tree.
func IsRepo(dir string) bool {
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel")
	// Silence output; only the exit status matters here.
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run() == nil
}

// Open resolves the top level of the working tree containing dir and returns a
// Repo rooted there.
func Open(dir string) (*Repo, error) {
	out, err := run(context.Background(), "-C", dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("open git repo at %q: %s; run this inside a git working tree or run 'git init' first", dir, errMsg(err))
	}
	top := strings.TrimSpace(string(out))
	if top == "" {
		return nil, fmt.Errorf("open git repo at %q: git returned an empty toplevel; ensure %q is inside a git working tree", dir, dir)
	}
	return &Repo{Dir: top}, nil
}

// errMsg strips the leading "git ...: " prefix that run() adds so wrapping
// errors read cleanly.
func errMsg(err error) string {
	s := err.Error()
	if i := strings.Index(s, ": "); i >= 0 {
		return s[i+2:]
	}
	return s
}

// Diff returns the working-tree diff against HEAD. If the repository has no
// commits yet (no HEAD), it falls back to a plain diff. Empty output is valid
// and is not an error.
//
// Untracked (e.g. agent-created) files are included: they are marked
// intent-to-add first so they appear in the diff as new files. This mutates the
// index only with intent-to-add markers, which is safe in AgentBox's disposable
// workspace copy. Best-effort: a failure here does not fail the diff.
func (r *Repo) Diff(ctx context.Context) ([]byte, error) {
	_, _ = run(ctx, "-C", r.Dir, "add", "-N", ".")
	out, err := run(ctx, "-C", r.Dir, "diff", "--no-color", "HEAD")
	if err != nil {
		// No HEAD yet (unborn branch / empty repo): fall back to a diff of the
		// index/working tree so the caller still gets something useful.
		out, err = run(ctx, "-C", r.Dir, "diff", "--no-color")
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// ChangedFiles returns a sorted, de-duplicated list of paths reported by
// "git status --porcelain".
func (r *Repo) ChangedFiles(ctx context.Context) ([]string, error) {
	out, err := run(ctx, "-C", r.Dir, "status", "--porcelain")
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{})
	var files []string
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		// Porcelain v1 lines are "XY <path>" where XY is a two-char status.
		// Strip the status field plus its trailing space.
		if len(line) < 4 {
			continue
		}
		path := strings.TrimSpace(line[3:])
		if path == "" {
			continue
		}
		// Renames/copies are reported as "old -> new"; record the new path.
		if i := strings.Index(path, " -> "); i >= 0 {
			path = path[i+len(" -> "):]
		}
		path = unquotePath(path)
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		files = append(files, path)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("parse git status output: %w", err)
	}
	sort.Strings(files)
	return files, nil
}

// unquotePath handles git's C-style quoting of paths that contain unusual
// characters (enabled by core.quotepath). For ordinary paths it is a no-op.
func unquotePath(p string) string {
	if len(p) >= 2 && p[0] == '"' && p[len(p)-1] == '"' {
		if unq, err := strconv.Unquote(p); err == nil {
			return unq
		}
	}
	return p
}

// BranchName builds a deterministic-prefix, collision-resistant branch name of
// the form "agentbox/<slug>-<6 hex>". The random suffix uses crypto/rand so
// concurrent runs on the same task do not collide.
func BranchName(task string) string {
	s := slug(task)
	if s == "" {
		s = "task"
	}
	return "agentbox/" + s + "-" + randHex(3)
}

// slug lowercases task, collapses any run of non-[a-z0-9] characters into a
// single "-", trims leading/trailing "-", and caps the result to 40 runes.
func slug(task string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(task) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevDash = false
			continue
		}
		if !prevDash {
			b.WriteByte('-')
			prevDash = true
		}
	}
	s := strings.Trim(b.String(), "-")
	if len(s) > 40 {
		s = strings.Trim(s[:40], "-")
	}
	return s
}

// randHex returns n random bytes encoded as 2n lowercase hex characters.
func randHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand should never fail; if it does, surface a marker rather
		// than panicking inside a name builder.
		return strings.Repeat("0", n*2)
	}
	return hex.EncodeToString(buf)
}

// CreateBranch creates and checks out a new branch.
func (r *Repo) CreateBranch(ctx context.Context, name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("create branch: empty branch name")
	}
	if _, err := run(ctx, "-C", r.Dir, "checkout", "-b", name); err != nil {
		return err
	}
	return nil
}

// Commit stages all changes and records a commit. If there is nothing to
// commit it returns a clear error so the caller can decide how to react.
//
// When the repository has no committer identity configured (common on fresh
// CI runners), the commit is recorded under a neutral AgentBox identity
// instead of failing — the alternative is silently losing the agent's work.
func (r *Repo) Commit(ctx context.Context, message string) error {
	if _, err := run(ctx, "-C", r.Dir, "add", "-A"); err != nil {
		return err
	}
	// Detect an empty staging state up front so we can return a precise error
	// instead of git's terse "nothing to commit" exit code.
	files, err := r.ChangedFiles(ctx)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("commit %q: nothing to commit; the working tree is clean (stage changes before committing)", message)
	}
	args := []string{"-C", r.Dir}
	if !r.hasIdentity(ctx) {
		args = append(args, "-c", "user.name=AgentBox", "-c", "user.email=agentbox@localhost")
	}
	args = append(args, "commit", "-m", message)
	if _, err := run(ctx, args...); err != nil {
		return err
	}
	return nil
}

// hasIdentity reports whether a committer identity is configured for the
// repository (local, global, or system scope).
func (r *Repo) hasIdentity(ctx context.Context) bool {
	out, err := run(ctx, "-C", r.Dir, "config", "user.email")
	return err == nil && strings.TrimSpace(string(out)) != ""
}

// FetchBranch copies branch from the repository at fromDir into the repository
// at repoDir (creating or updating the same-named local branch). AgentBox uses
// this to propagate a commit made in a disposable workspace copy back into the
// user's real repository before the workspace is cleaned up. Fetching (rather
// than pushing into a non-bare repo) avoids receive-side restrictions.
func FetchBranch(ctx context.Context, repoDir, fromDir, branch string) error {
	if strings.TrimSpace(branch) == "" {
		return errors.New("fetch branch: empty branch name")
	}
	if _, err := run(ctx, "-C", repoDir, "fetch", fromDir, branch+":"+branch); err != nil {
		return fmt.Errorf("copying branch %q from the workspace into %s: %s", branch, repoDir, errMsg(err))
	}
	return nil
}

// PushBranch pushes branch to the named remote (e.g. "origin"). Used after a
// commit in a cloned workspace so the branch survives workspace cleanup and a
// pull request can reference it. Requires push credentials (e.g. gh auth).
func (r *Repo) PushBranch(ctx context.Context, remote, branch string) error {
	if strings.TrimSpace(branch) == "" {
		return errors.New("push branch: empty branch name")
	}
	if _, err := run(ctx, "-C", r.Dir, "push", remote, branch); err != nil {
		return fmt.Errorf("pushing branch %q to %s: %s", branch, remote, errMsg(err))
	}
	return nil
}

// Clone clones repo into dest. The remote is normalized so callers may pass a
// bare "github.com/org/repo" shorthand.
func Clone(ctx context.Context, repo, dest string) error {
	remote := NormalizeRemote(repo)
	if _, err := run(ctx, "clone", remote, dest); err != nil {
		return err
	}
	return nil
}

// NormalizeRemote turns a repository reference into a clonable URL.
//
//   - "github.com/org/repo"          -> "https://github.com/org/repo.git"
//   - "https://..." / "http://..."   -> passed through unchanged
//   - "git@host:org/repo.git"        -> passed through unchanged
//
// A ".git" suffix is added idempotently for the shorthand form only.
func NormalizeRemote(repo string) string {
	r := strings.TrimSpace(repo)
	switch {
	case strings.HasPrefix(r, "https://"), strings.HasPrefix(r, "http://"):
		return r
	case strings.HasPrefix(r, "git@"):
		return r
	default:
		if !strings.HasSuffix(r, ".git") {
			r += ".git"
		}
		return "https://" + r
	}
}
