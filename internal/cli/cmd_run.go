package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/qosikz/andbo/internal/adapters"
	"github.com/qosikz/andbo/internal/config"
	"github.com/qosikz/andbo/internal/git"
	"github.com/qosikz/andbo/internal/policy"
	"github.com/qosikz/andbo/internal/runtime"
	"github.com/qosikz/andbo/internal/secrets"
	"github.com/qosikz/andbo/internal/session"
	"github.com/qosikz/andbo/internal/workspace"
)

// minimalSafeEnv are the only host variables passed to an agent running in
// unsafe LOCAL mode, in addition to explicitly allowlisted secrets. No other
// host environment is forwarded. USER/LOGNAME are included because they are
// non-sensitive and required by OS keychains (e.g. Claude Code's macOS auth)
// and git's identity fallback; without them real agents fail to authenticate.
var minimalSafeEnv = []string{"PATH", "HOME", "USER", "LOGNAME", "LANG", "LC_ALL", "TERM"}

// containerPATH is the standard Linux PATH set inside containers. The host's
// PATH (and HOME) must never leak into a container: they are host-specific,
// can reveal usernames/layout, and would shadow the image's own binaries.
const containerPATH = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

// budgetWindow converts budget.max_runtime_minutes into the run deadline.
// It is a variable so tests can shrink the window to milliseconds.
var budgetWindow = func(minutes int) time.Duration { return time.Duration(minutes) * time.Minute }

func (r *Root) cmdRun(ctx context.Context, args []string) error {
	o, err := parseRunFlags(args)
	if err != nil {
		return coded(ExitGeneral, err)
	}
	if o.task == "" {
		if o.repo != "" {
			return codedf(ExitGeneral, "a repository was given but no task.\nAdd a task: andbo run %s --task \"add tests\"", o.repo)
		}
		return codedf(ExitGeneral, "missing task.\nExample: andbo run \"fix failing tests\"")
	}

	base, err := os.Getwd()
	if err != nil {
		return coded(ExitGeneral, err)
	}

	// 1. Load + validate policy.
	cfg, err := config.LoadPolicy(o.policy)
	if err != nil {
		return coded(ExitInvalidConfig, err)
	}
	if chk := cfg.Check(); !chk.OK() {
		for _, e := range chk.Errors {
			warn(e)
		}
		return codedf(ExitInvalidConfig, "policy %s is invalid; run 'andbo policy check'", o.policy)
	}

	if o.engine != "" && o.engine != "docker" && o.engine != "podman" {
		return codedf(ExitInvalidConfig, "--engine %q is invalid (expected: docker, podman)", o.engine)
	}
	// Security: an unrecognized --runtime value must not silently route the run
	// down an unexpected path (e.g. host-env handling).
	if o.runtime != "" && o.runtime != "container" && o.runtime != "local" {
		return codedf(ExitInvalidConfig, "--runtime %q is invalid (expected: container, local)", o.runtime)
	}
	ov := policy.Overrides{
		Network: o.network, Runtime: o.runtime, Engine: o.engine, Agent: o.agent, ExtraWrite: o.write,
		Unsafe: o.unsafe, AllowHostHome: o.allowHostHome,
		AllowDockerSocket: o.allowDockerSocket, YesUnsafe: o.yesUnsafe,
	}
	ep := policy.BuildEffectivePolicy(cfg, o.policy, ov)

	// 2. Gate unsafe modes. Dry-run never executes, so it only warns.
	if ep.RequiresUnsafeConfirmation() {
		if o.dryRun {
			warn("unsafe options are set; a real run would require confirmation:")
			for _, reason := range ep.UnsafeReasons() {
				warn("  - " + reason)
			}
		} else if err := confirmUnsafe(ep.UnsafeReasons(), o.yesUnsafe); err != nil {
			return err
		}
	}

	// Budget: max_runtime_minutes is a hard deadline for real runs, started
	// AFTER the interactive unsafe prompt so waiting at the prompt never burns
	// budget. The deadline bounds the clone, the agent, tests, and git work.
	if !o.dryRun && ep.Budget.MaxRuntimeMinutes > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, budgetWindow(ep.Budget.MaxRuntimeMinutes))
		defer cancel()
	}

	// 3. Start the session recorder.
	rec := session.NewRecorder(base, o.task, ep.Agent.Default)
	rec.Session.DryRun = o.dryRun
	rec.Session.Repository = repoLabel(o)
	rec.Session.Policy = session.PolicyRef{Path: o.policy, Hash: config.HashPolicyFile(o.policy)}
	rec.Session.Runtime = session.RuntimeInfo{Engine: engineLabel(ep), Network: ep.EnforcedNetwork(), Image: ep.Runtime.Image}
	rec.Event(session.EvPolicyLoaded)

	red := buildRedactor(ep)

	// budgetHit/budgetExceeded centralize budget-kill detection and reporting
	// so every phase (clone, agent, tests) reports it identically.
	budgetHit := func() bool {
		return ep.Budget.MaxRuntimeMinutes > 0 && errors.Is(ctx.Err(), context.DeadlineExceeded)
	}
	budgetExceeded := func() error {
		rec.PolicyBlocked(fmt.Sprintf("run exceeded budget.max_runtime_minutes (%d); agent stopped", ep.Budget.MaxRuntimeMinutes))
		rec.Session.Result = session.Result{Status: "failed", Summary: "budget exceeded: run hit max_runtime_minutes"}
		return finishErr(rec, red, codedf(ExitAgentFailed,
			"budget exceeded: the run hit budget.max_runtime_minutes (%d) and was stopped.\nRaise the budget in %s or simplify the task.",
			ep.Budget.MaxRuntimeMinutes, o.policy))
	}

	// Disposable directories for this run. When runtime.cleanup is true they
	// are removed on every exit path (success or failure) — UNLESS the run
	// produced a branch/commit that could not be propagated back, in which case
	// the workspace is kept so the user's work is never destroyed. Session
	// artifacts under .andbo/sessions/ are always kept.
	workDir := filepath.Join(base, ".andbo", "work", rec.Session.ID)
	cloneDir := filepath.Join(base, ".andbo", "clone", rec.Session.ID)
	keepWorkspace := false
	if !o.dryRun && ep.Runtime.Cleanup {
		defer func() {
			if keepWorkspace {
				return
			}
			_ = os.RemoveAll(workDir)
			_ = os.RemoveAll(cloneDir)
		}()
	}

	// 4. Resolve the working root (clone remote repos).
	root := base
	isRemote := false
	if o.repo != "" && looksLikeRepo(o.repo) {
		if o.dryRun {
			rec.Logf("would clone %s", git.NormalizeRemote(o.repo))
		} else {
			// Clone into a separate dir; the sanitized workspace copy is built
			// from it below so denied files are excluded for remote repos too.
			rec.Logf("cloning %s", git.NormalizeRemote(o.repo))
			if err := git.Clone(ctx, o.repo, cloneDir); err != nil {
				if budgetHit() {
					return budgetExceeded()
				}
				return finishErr(rec, red, coded(ExitGitFailed, err))
			}
			root = cloneDir
			isRemote = true
		}
	}

	// 5. Build the workspace plan and record policy events.
	plan, err := workspace.BuildPlan(root, ep)
	if err != nil {
		return finishErr(rec, red, coded(ExitGeneral, err))
	}
	rec.Event(session.EvWorkspaceCreated)
	for _, w := range plan.Warnings {
		rec.Logf("warning: %s", w)
	}
	for _, ex := range plan.ExcludedMounts {
		rec.PolicyBlocked("excluded sensitive path from workspace: " + ex)
	}
	if ep.EnforcedNetwork() == "deny" {
		rec.PolicyBlocked("outbound network access (network=" + ep.Network.Mode + ")")
		// Honesty note, conditional by design: a baked-in agent that must reach
		// a model API cannot do so under network=deny. Phrased as "if it needs
		// to reach a remote API" because many agents/tasks (and the bundled
		// stub fixture) run fully offline and need no change.
		if !o.dryRun && ep.Runtime.Isolation != "local" && len(ep.Secrets.Allow) > 0 {
			rec.Logf("note: network is denied; if this agent needs to reach a remote API it will fail. Use network: allowlist with your provider's API domains (enforced via egress proxy) — network: open (unsafe) is no longer required. Offline agents are unaffected.")
		}
	}

	// 6. Select the agent adapter and build its command.
	adapter, err := adapters.Get(ep.Agent.Default, cfg.Agent.Custom)
	if err != nil {
		return finishErr(rec, red, coded(ExitAgentFailed, err))
	}

	agentEnv := buildAgentEnv(ep)
	cmd, err := adapter.BuildCommand(ctx, adapters.Input{Task: o.task, WorkspacePath: root, Env: agentEnv})
	if err != nil {
		return finishErr(rec, red, coded(ExitAgentFailed, err))
	}
	rec.Session.Commands = append(rec.Session.Commands, session.ExecutedCmd{Cmd: cmdString(cmd)})
	rec.Event(session.EvAgentStarted)

	// 7. Select the runner and verify availability.
	runner, rerr := selectRunnerFn(ep, o)
	if rerr != nil {
		return finishErr(rec, red, rerr)
	}
	if err := runner.Available(ctx); err != nil {
		return finishErr(rec, red, codedf(ExitRuntimeUnavail, "%v", err))
	}

	// 7b. Preflight the agent binary in the environment that will actually run
	//     it (host PATH for local; the runtime image for container). This
	//     replaces a host-only check that wrongly failed baked-in agents.
	if perr := preflightAgent(ctx, ep, o, runner, adapter, cmd.Executable, rec); perr != nil {
		return finishErr(rec, red, perr)
	}

	// 8. For real runs, materialize a disposable, sanitized workspace copy for
	//    BOTH local and cloned repos (denied files like .env are excluded; .git
	//    is restored so diffs work). The agent never touches the original repo.
	execRoot := root
	var repo *git.Repo
	var baselineChanged map[string]bool
	if !o.dryRun {
		if err := prepareWorkspace(plan, root, workDir); err != nil {
			return finishErr(rec, red, coded(ExitGeneral, err))
		}
		execRoot = workDir
		cmd.WorkingDir = execRoot
		if git.IsRepo(execRoot) {
			repo, _ = git.Open(execRoot)
			// Record the pre-existing dirty state so we attribute only the
			// agent's changes, not files already modified in the source repo.
			// Normalize untracked entries to file granularity (matching the
			// post-run diff) so pre-existing untracked dirs are excluded.
			if repo != nil {
				repo.MarkIntentToAdd(ctx)
				if files, ferr := repo.ChangedFiles(ctx); ferr == nil {
					baselineChanged = toSet(files)
				}
			}
		}
	}

	spec := buildRuntimeSpec(ep, plan, execRoot, agentEnv, o)
	rec.Event(session.EvRuntimeStarted)

	// 9. Run the agent.
	cs := toCommandSpec(cmd)
	if ep.Budget.MaxRuntimeMinutes > 0 {
		cs.Timeout = budgetWindow(ep.Budget.MaxRuntimeMinutes)
	}
	res, runErr := runner.Run(ctx, spec, cs)
	for _, line := range res.Description {
		rec.Log(line)
	}
	if s := strings.TrimRight(res.Stdout, "\n"); s != "" {
		rec.Log(s)
	}
	if s := strings.TrimRight(res.Stderr, "\n"); s != "" {
		rec.Log(s)
	}
	if n := len(rec.Session.Commands); n > 0 {
		rec.Session.Commands[n-1].ExitCode = res.ExitCode
	}
	recordEgress(rec, res.EgressLog)
	rec.Event(session.EvCommandExecuted)
	// Budget kill first: depending on the runner, a deadline kill surfaces as
	// an error (docker wrap) or as a clean exit code -1 (process killed by
	// signal). The context tells the truth either way.
	if budgetHit() {
		return budgetExceeded()
	}
	if runErr != nil {
		rec.Session.Result = session.Result{Status: "failed", Summary: runErr.Error()}
		return finishErr(rec, red, codedf(ExitAgentFailed, "agent runtime error: %v", runErr))
	}
	if res.ExitCode != 0 {
		rec.Session.Result = session.Result{Status: "failed", Summary: fmt.Sprintf("agent exited with code %d", res.ExitCode)}
		dir, _ := rec.Save(red)
		printRunSummary(rec, ep, o, res, dir)
		return &CodedError{Code: ExitAgentFailed, Err: emptyError{}}
	}

	// 10. Diff (real runs only).
	if repo != nil && !o.dryRun {
		if diff, derr := repo.Diff(ctx); derr == nil {
			rec.Diff = diff
			rec.Event(session.EvGitDiffGenerated)
		}
		if files, ferr := repo.ChangedFiles(ctx); ferr == nil {
			changed := subtractSet(files, baselineChanged)
			if len(changed) > 0 {
				rec.Session.ChangedFiles = changed
				rec.Event(session.EvFileChanged)
			}
		}
	}

	// 11. Tests (real runs only).
	if !o.dryRun && len(ep.Tests.Commands) > 0 {
		rec.Event(session.EvTestsStarted)
		testsFailed := false
		for _, tc := range ep.Tests.Commands {
			tr, _ := runner.Run(ctx, spec, runtime.CommandSpec{
				Executable: "sh", Args: []string{"-lc", tc}, Env: agentEnv, WorkingDir: execRoot,
			})
			recordEgress(rec, tr.EgressLog)
			// A budget kill mid-tests is a budget event, not a test failure.
			if budgetHit() {
				return budgetExceeded()
			}
			status := "passed"
			if tr.ExitCode != 0 {
				status, testsFailed = "failed", true
			}
			rec.Session.Tests = append(rec.Session.Tests, session.TestRun{
				Command: tc, Status: status, Output: strings.TrimSpace(tr.Stdout + "\n" + tr.Stderr),
			})
		}
		rec.Event(session.EvTestsCompleted)
		if testsFailed {
			rec.Session.Result = session.Result{Status: "failed", Summary: "one or more test commands failed"}
			dir, _ := rec.Save(red)
			printRunSummary(rec, ep, o, res, dir)
			return &CodedError{Code: ExitTestsFailed, Err: emptyError{}}
		}
	}

	// 12. Commit / open PR (real runs only). The task is redacted before it is
	//     written into the commit message and PR text, in case it carries a
	//     secret value.
	//
	// The commit is created in the DISPOSABLE workspace copy, so it must be
	// propagated back before cleanup or the user's requested work would be
	// destroyed: local runs fetch the branch into the source repository;
	// remote runs push it to origin. If propagation fails, the workspace is
	// kept and its path reported — cleanup never deletes an unpropagated commit.
	if !o.dryRun && repo != nil && (o.commit || o.openPR) {
		safeTask := red.Redact(o.task)
		branch := git.BranchName(o.task)
		committed := false
		if err := repo.CreateBranch(ctx, branch); err != nil {
			rec.Logf("branch: %v", err)
			warn("branch creation failed: " + err.Error())
		} else {
			rec.Session.Branch = branch
			if err := repo.Commit(ctx, commitMessage(safeTask, rec.Session.ID)); err != nil {
				rec.Logf("commit: %v", err)
				warn("commit skipped: " + err.Error())
			} else {
				committed = true
			}
		}

		if committed {
			if isRemote {
				if err := repo.PushBranch(ctx, "origin", branch); err != nil {
					rec.Logf("push: %v", err)
					warn("branch " + branch + " could not be pushed to origin: " + err.Error())
					keepWorkspace = true
					warn("workspace kept at " + execRoot + " to preserve the commit")
				} else {
					rec.Logf("pushed %s to origin", branch)
				}
			} else {
				if err := git.FetchBranch(ctx, root, execRoot, branch); err != nil {
					rec.Logf("propagate branch: %v", err)
					warn("branch " + branch + " could not be copied into your repository: " + err.Error())
					keepWorkspace = true
					warn("workspace kept at " + execRoot + " to preserve the commit")
				} else {
					rec.Logf("branch %s created in %s", branch, root)
				}
			}
		}

		if o.openPR {
			out, perr := repo.OpenPR(ctx, git.PRInput{
				Title: "Andbo: " + safeTask, Body: prBody(safeTask, ep, rec.Session.Tests, rec.Session.ID), Branch: branch, Base: "main",
			})
			switch {
			case perr != nil:
				rec.Logf("open PR: %v", perr)
				warn("open PR failed: " + perr.Error())
			case out.Created:
				rec.Logf("opened PR: %s", out.URL)
			default:
				rec.Logf("PR not created: %s", out.Note)
				warn("PR not created: " + out.Note)
			}
		}
	}

	// 13. Finalize.
	if rec.Session.Result.Summary == "" {
		if o.dryRun {
			rec.Session.Result = session.Result{Status: "success", Summary: "dry run completed; no agent was executed"}
		} else {
			rec.Session.Result = session.Result{Status: "success", Summary: "agent completed"}
		}
	}
	dir, serr := rec.Save(red)
	if serr != nil {
		return coded(ExitGeneral, serr)
	}
	if o.json {
		return printRunJSON(rec, dir)
	}
	printRunSummary(rec, ep, o, res, dir)
	return nil
}

// --- helpers ---

// emptyError is a no-message error used to set an exit code after the command
// has already produced its own output.
type emptyError struct{}

func (emptyError) Error() string { return "" }

func repoLabel(o runOptions) string {
	if o.repo != "" {
		return o.repo
	}
	return "."
}

func engineLabel(ep policy.EffectivePolicy) string {
	if ep.Runtime.Isolation == "local" {
		return "local"
	}
	return ep.Runtime.Engine
}

// buildAgentEnv assembles the environment passed to the agent.
//
// Container runs get a container-appropriate environment: a standard Linux
// PATH (never the host's), LANG/LC_ALL/TERM pass-through, and explicitly
// allowlisted secrets. HOME is set later to the workspace (see
// buildRuntimeSpec) so tools have a writable home.
//
// Local (unsafe) runs need the host PATH/HOME to function, so they get the
// minimal safe host variables instead. Nothing else is ever forwarded.
func buildAgentEnv(ep policy.EffectivePolicy) map[string]string {
	env := map[string]string{}
	// Fail safe: only the explicit "local" isolation gets host variables; any
	// other value (including future/unknown ones) gets the container-hygiene
	// environment.
	if ep.Runtime.Isolation == "local" {
		for _, k := range minimalSafeEnv {
			if v := os.Getenv(k); v != "" {
				env[k] = v
			}
		}
	} else {
		env["PATH"] = containerPATH
		for _, k := range []string{"LANG", "LC_ALL", "TERM"} {
			if v := os.Getenv(k); v != "" {
				env[k] = v
			}
		}
	}
	for _, name := range ep.Secrets.Allow {
		if name == "*" {
			continue
		}
		if v := os.Getenv(name); v != "" {
			env[name] = v
		}
	}
	return env
}

// buildRedactor gathers known secret values (from allow + deny names) so they
// can be scrubbed from logs, and compiles the policy's redact patterns.
func buildRedactor(ep policy.EffectivePolicy) *secrets.Redactor {
	names := append(append([]string{}, ep.Secrets.Allow...), ep.Secrets.Deny...)
	values := secrets.GatherSecretValues(names, os.Getenv)
	red, err := secrets.NewRedactor(values, ep.Secrets.RedactPatterns)
	if err != nil {
		warn(fmt.Sprintf("ignoring invalid redact pattern: %v", err))
		red, _ = secrets.NewRedactor(values, nil)
	}
	return red
}

func buildRuntimeSpec(ep policy.EffectivePolicy, plan workspace.Plan, root string, env map[string]string, o runOptions) runtime.RuntimeSpec {
	netMode := "none"
	var allowedDomains []string
	var allowedPorts []int
	switch ep.EnforcedNetwork() {
	case "open":
		netMode = "bridge"
	case "allowlist":
		// Enforced by the runner: per-run internal network + egress proxy
		// restricted to these domains (and ports). See internal/runtime/allowlist.go.
		netMode = "allowlist"
		allowedDomains = ep.Network.Allow
		allowedPorts = ep.Network.Ports
	}

	// Mount only the resolved write paths (read-write). The repository root is
	// intentionally NOT bind-mounted wholesale, so denied files such as .env are
	// never present in the runtime — they are excluded from the mount set.
	var writeMounts []string
	if o.dryRun {
		writeMounts = plan.WritePaths // show the policy's write list as-is
	} else {
		writeMounts = []string{root} // the sanitized workspace copy
	}

	// Containers get HOME pointed at the writable workspace so tools that
	// need a home directory (git, package managers) work under --user.
	// Fail safe: every isolation except explicit "local" is container-like.
	specEnv := env
	if ep.Runtime.Isolation != "local" {
		specEnv = make(map[string]string, len(env)+1)
		for k, v := range env {
			specEnv[k] = v
		}
		specEnv["HOME"] = root
	}

	return runtime.RuntimeSpec{
		Engine:            engineLabel(ep),
		Image:             ep.Runtime.Image,
		NetworkMode:       netMode,
		AllowedDomains:    allowedDomains,
		AllowedPorts:      allowedPorts,
		Workdir:           root,
		Env:               specEnv,
		User:              "10001:10001",       // non-root
		Privileged:        false,               // never
		MountDockerSocket: o.allowDockerSocket, // only with explicit unsafe flag
		ReadOnlyPaths:     nil,
		WritePaths:        writeMounts,
	}
}

// selectRunnerFn is a seam so tests can inject a runner without a container
// engine. Production always uses selectRunner.
var selectRunnerFn = selectRunner

// recordEgress folds the egress proxy's audit lines into the session: denials
// become policy-blocked events (the security record), allows become log lines.
// Classification is anchored to the verb field (the token right after the
// audit prefix) rather than a free substring, so an allowed request whose
// method/host text happens to contain "DENY"/"ALLOW" cannot be mislabeled.
func recordEgress(rec *session.Recorder, egressLog []string) {
	const prefix = "ANDBO-EGRESS "
	for _, line := range egressLog {
		i := strings.Index(line, prefix)
		if i < 0 {
			continue
		}
		rest := line[i+len(prefix):]
		verb, _, _ := strings.Cut(rest, " ")
		switch verb {
		case "DENY":
			rec.PolicyBlocked("egress " + rest)
		case "ALLOW":
			rec.Logf("%s", strings.TrimSpace(line[i:]))
		}
	}
}

// preflightAgent verifies the agent binary is reachable in the environment that
// will run it. Local isolation executes on the host, so the binary must be on
// the host PATH (adapter.Check). Container isolation runs the binary from
// inside the image (a baked-in agent), so the host PATH is irrelevant — probe
// the image instead. Container dry-runs skip the probe so planning never needs
// a daemon.
func preflightAgent(ctx context.Context, ep policy.EffectivePolicy, o runOptions, runner runtime.Runner, adapter adapters.Adapter, bin string, rec *session.Recorder) error {
	if ep.Runtime.Isolation == "local" {
		if cerr := adapter.Check(ctx); cerr != nil {
			if o.dryRun {
				warn(cerr.Error())
				rec.Logf("agent check: %v", cerr)
				return nil
			}
			return coded(ExitAgentFailed, cerr)
		}
		return nil
	}
	// Container isolation.
	if o.dryRun {
		return nil
	}
	present, perr := runner.ProbeBinary(ctx, ep.Runtime.Image, bin)
	if perr != nil {
		// Inconclusive probe (daemon down, image missing, no shell in image):
		// do NOT block. The real run surfaces a genuine engine/image failure
		// with full, actionable detail (see engineFailureError).
		rec.Logf("agent preflight probe inconclusive for %q in %q: %v", bin, ep.Runtime.Image, perr)
		return nil
	}
	if !present {
		return codedf(ExitAgentFailed,
			"agent %q is not installed in runtime image %q.\nBake the agent into the image (see examples/agents/README.md) or set runtime.image to one that contains it.\nTo run on the host instead, use --runtime local (unsafe).",
			bin, ep.Runtime.Image)
	}
	return nil
}

func selectRunner(ep policy.EffectivePolicy, o runOptions) (runtime.Runner, error) {
	if o.dryRun {
		return runtime.NewDryRunRunner(), nil
	}
	if ep.Runtime.Isolation == "local" {
		return runtime.NewLocalRunner(), nil
	}
	switch ep.Runtime.Engine {
	case "docker":
		return runtime.NewDockerRunner(), nil
	case "podman":
		return runtime.NewPodmanRunner(), nil
	default:
		return nil, codedf(ExitRuntimeUnavail, "unknown runtime engine %q (expected: docker, podman)", ep.Runtime.Engine)
	}
}

func toCommandSpec(c adapters.Command) runtime.CommandSpec {
	return runtime.CommandSpec{Executable: c.Executable, Args: c.Args, Env: c.Env, WorkingDir: c.WorkingDir}
}

func cmdString(c adapters.Command) string {
	return strings.TrimSpace(c.Executable + " " + strings.Join(c.Args, " "))
}

// prepareWorkspace builds a sanitized copy of the repo at workDir and restores
// the .git directory so diff/commit work against the disposable workspace.
func prepareWorkspace(plan workspace.Plan, srcRoot, workDir string) error {
	if err := workspace.Prepare(plan, workDir); err != nil {
		return err
	}
	if git.IsRepo(srcRoot) {
		if err := copyTree(filepath.Join(srcRoot, ".git"), filepath.Join(workDir, ".git")); err != nil {
			return fmt.Errorf("restoring .git into workspace: %w", err)
		}
	}
	return nil
}

func copyTree(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	})
}

func confirmUnsafe(reasons []string, yesUnsafe bool) error {
	fmt.Fprintln(os.Stderr, "WARNING: You are enabling unsafe mode.")
	fmt.Fprintln(os.Stderr, "This may expose local files, credentials, or network access to the agent.")
	for _, reason := range reasons {
		fmt.Fprintln(os.Stderr, "  - "+reason)
	}
	if yesUnsafe {
		fmt.Fprintln(os.Stderr, "Proceeding because --yes-unsafe was provided.")
		return nil
	}
	if !isInteractive() {
		return codedf(ExitUnsafeRequired, "unsafe mode requires confirmation.\nPass --yes-unsafe to proceed in a non-interactive environment (CI).")
	}
	fmt.Fprint(os.Stderr, "\nType \"I understand\" to continue: ")
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	if strings.TrimSpace(line) != "I understand" {
		return codedf(ExitUnsafeRequired, "unsafe mode not confirmed; aborting.")
	}
	return nil
}

func isInteractive() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && (fi.Mode()&os.ModeCharDevice) != 0
}

func commitMessage(task, sessionID string) string {
	return fmt.Sprintf("andbo: %s\n\nAndbo-Session: %s", task, sessionID)
}

func prBody(task string, ep policy.EffectivePolicy, tests []session.TestRun, sessionID string) string {
	testLines := "_No tests run._"
	if len(tests) > 0 {
		var b strings.Builder
		for _, t := range tests {
			fmt.Fprintf(&b, "- %s: %s\n", t.Command, t.Status)
		}
		testLines = strings.TrimRight(b.String(), "\n")
	}
	return fmt.Sprintf(`## Summary

Generated by Andbo.

## Task

%s

## Session

- Agent: %s
- Runtime: %s
- Network: %s
- Policy: %s
- Session: %s

## Tests

%s

## Security notes

- Secrets protected (host environment not forwarded; logs redacted)
- Network: %s
- Sensitive paths excluded from the workspace
`, task, ep.Agent.Default, engineLabel(ep), ep.EnforcedNetwork(), ep.PolicyPath, sessionID, testLines, ep.EnforcedNetwork())
}

// toSet builds a set from a slice of paths.
func toSet(items []string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, it := range items {
		m[it] = true
	}
	return m
}

// subtractSet returns the items not present in base, preserving order.
func subtractSet(items []string, base map[string]bool) []string {
	var out []string
	for _, it := range items {
		if !base[it] {
			out = append(out, it)
		}
	}
	return out
}

// finishErr persists a (failed) session best-effort, then returns err.
func finishErr(rec *session.Recorder, red *secrets.Redactor, err error) error {
	if rec.Session.Result.Summary == "" {
		rec.Session.Result = session.Result{Status: "failed", Summary: errString(err)}
	}
	_, _ = rec.Save(red)
	return err
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func printRunSummary(rec *session.Recorder, ep policy.EffectivePolicy, o runOptions, res runtime.RunResult, dir string) {
	s := rec.Session
	w := os.Stdout
	fmt.Fprintln(w, "Andbo session started")
	fmt.Fprintln(w)
	fmt.Fprintf(w, "Repository: %s\n", s.Repository)
	fmt.Fprintf(w, "Agent: %s\n", s.Agent)
	runtimeLine := s.Runtime.Engine
	if o.dryRun {
		runtimeLine += " (dry-run)"
	}
	fmt.Fprintf(w, "Runtime: %s\n", runtimeLine)
	fmt.Fprintf(w, "Network: %s\n", s.Runtime.Network)
	fmt.Fprintf(w, "Policy: %s\n", s.Policy.Path)
	fmt.Fprintln(w)

	ok(w, "Workspace created")
	ok(w, "Policy applied")
	ok(w, "Secrets protected")
	switch {
	case o.dryRun:
		ok(w, "Agent dry-run (not executed)")
	case s.Result.Status == "failed":
		fmt.Fprintf(w, "✗ %s\n", s.Result.Summary)
	default:
		ok(w, "Agent completed")
	}
	if len(rec.Diff) > 0 {
		ok(w, "Diff generated")
	}
	ok(w, "Session saved")
	if !o.dryRun && ep.Runtime.Cleanup {
		ok(w, "Workspace cleaned")
	}

	if o.dryRun && len(res.Description) > 0 {
		fmt.Fprintln(w, "\nIntended runtime actions (dry-run):")
		for _, line := range res.Description {
			fmt.Fprintf(w, "  %s\n", line)
		}
	}

	if s.Branch != "" {
		fmt.Fprintf(w, "\nBranch: %s\n", s.Branch)
	}
	if len(s.ChangedFiles) > 0 {
		fmt.Fprintln(w, "\nChanged files:")
		for _, f := range s.ChangedFiles {
			fmt.Fprintf(w, "  %s\n", f)
		}
	}
	if len(s.PolicyEvents) > 0 {
		fmt.Fprintln(w, "\nPolicy events:")
		for _, e := range s.PolicyEvents {
			fmt.Fprintf(w, "  %s %s\n", strings.ToUpper(e.Type), e.Detail)
		}
	}
	if len(s.Tests) > 0 {
		fmt.Fprintln(w, "\nTests:")
		for _, t := range s.Tests {
			fmt.Fprintf(w, "  %s: %s\n", t.Command, t.Status)
		}
	}

	fmt.Fprintln(w, "\nResult:")
	fmt.Fprintf(w, "  %s\n", s.Result.Summary)
	fmt.Fprintf(w, "  Session: %s\n", dir)
}

func printRunJSON(rec *session.Recorder, dir string) error {
	return printJSON(os.Stdout, map[string]any{
		"session":     rec.Session,
		"session_dir": dir,
	})
}
