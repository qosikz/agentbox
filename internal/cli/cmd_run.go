package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/qosi/agentbox/internal/adapters"
	"github.com/qosi/agentbox/internal/config"
	"github.com/qosi/agentbox/internal/git"
	"github.com/qosi/agentbox/internal/policy"
	"github.com/qosi/agentbox/internal/runtime"
	"github.com/qosi/agentbox/internal/secrets"
	"github.com/qosi/agentbox/internal/session"
	"github.com/qosi/agentbox/internal/workspace"
)

// minimalSafeEnv are the only host variables passed to an agent in addition to
// explicitly allowlisted secrets. No other host environment is forwarded.
var minimalSafeEnv = []string{"PATH", "HOME", "LANG", "LC_ALL", "TERM"}

func (r *Root) cmdRun(ctx context.Context, args []string) error {
	o, err := parseRunFlags(args)
	if err != nil {
		return coded(ExitGeneral, err)
	}
	if o.task == "" {
		if o.repo != "" {
			return codedf(ExitGeneral, "a repository was given but no task.\nAdd a task: agentbox run %s --task \"add tests\"", o.repo)
		}
		return codedf(ExitGeneral, "missing task.\nExample: agentbox run \"fix failing tests\"")
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
		return codedf(ExitInvalidConfig, "policy %s is invalid; run 'agentbox policy check'", o.policy)
	}

	ov := policy.Overrides{
		Network: o.network, Runtime: o.runtime, Agent: o.agent, ExtraWrite: o.write,
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

	// 3. Start the session recorder.
	rec := session.NewRecorder(base, o.task, ep.Agent.Default)
	rec.Session.DryRun = o.dryRun
	rec.Session.Repository = repoLabel(o)
	rec.Session.Policy = session.PolicyRef{Path: o.policy, Hash: config.HashPolicyFile(o.policy)}
	rec.Session.Runtime = session.RuntimeInfo{Engine: engineLabel(ep), Network: ep.EnforcedNetwork(), Image: ep.Runtime.Image}
	rec.Event(session.EvPolicyLoaded)

	red := buildRedactor(ep)

	// 4. Resolve the working root (clone remote repos).
	root := base
	if o.repo != "" && looksLikeRepo(o.repo) {
		if o.dryRun {
			rec.Logf("would clone %s", git.NormalizeRemote(o.repo))
		} else {
			// Clone into a separate dir; the sanitized workspace copy is built
			// from it below so denied files are excluded for remote repos too.
			dest := filepath.Join(base, ".agentbox", "clone", rec.Session.ID)
			rec.Logf("cloning %s", git.NormalizeRemote(o.repo))
			if err := git.Clone(ctx, o.repo, dest); err != nil {
				return finishErr(rec, red, coded(ExitGitFailed, err))
			}
			root = dest
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
	}

	// 6. Select and check the agent adapter.
	adapter, err := adapters.Get(ep.Agent.Default, cfg.Agent.Custom)
	if err != nil {
		return finishErr(rec, red, coded(ExitAgentFailed, err))
	}
	if cerr := adapter.Check(ctx); cerr != nil {
		if o.dryRun {
			warn(cerr.Error())
			rec.Logf("agent check: %v", cerr)
		} else {
			return finishErr(rec, red, coded(ExitAgentFailed, cerr))
		}
	}

	agentEnv := buildAgentEnv(ep)
	cmd, err := adapter.BuildCommand(ctx, adapters.Input{Task: o.task, WorkspacePath: root, Env: agentEnv})
	if err != nil {
		return finishErr(rec, red, coded(ExitAgentFailed, err))
	}
	rec.Session.Commands = append(rec.Session.Commands, session.ExecutedCmd{Cmd: cmdString(cmd)})
	rec.Event(session.EvAgentStarted)

	// 7. Select the runner and verify availability.
	runner, rerr := selectRunner(ep, o)
	if rerr != nil {
		return finishErr(rec, red, rerr)
	}
	if err := runner.Available(ctx); err != nil {
		return finishErr(rec, red, codedf(ExitRuntimeUnavail, "%v", err))
	}

	// 8. For real runs, materialize a disposable, sanitized workspace copy for
	//    BOTH local and cloned repos (denied files like .env are excluded; .git
	//    is restored so diffs work). The agent never touches the original repo.
	execRoot := root
	var repo *git.Repo
	var baselineChanged map[string]bool
	if !o.dryRun {
		workDir := filepath.Join(base, ".agentbox", "work", rec.Session.ID)
		if err := prepareWorkspace(plan, root, workDir); err != nil {
			return finishErr(rec, red, coded(ExitGeneral, err))
		}
		execRoot = workDir
		cmd.WorkingDir = execRoot
		if git.IsRepo(execRoot) {
			repo, _ = git.Open(execRoot)
			// Record the pre-existing dirty state so we attribute only the
			// agent's changes, not files already modified in the source repo.
			if repo != nil {
				if files, ferr := repo.ChangedFiles(ctx); ferr == nil {
					baselineChanged = toSet(files)
				}
			}
		}
	}

	spec := buildRuntimeSpec(ep, plan, execRoot, agentEnv, o)
	rec.Event(session.EvRuntimeStarted)

	// 9. Run the agent.
	res, runErr := runner.Run(ctx, spec, toCommandSpec(cmd))
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
	rec.Event(session.EvCommandExecuted)
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
	if !o.dryRun && repo != nil && (o.commit || o.openPR) {
		safeTask := red.Redact(o.task)
		branch := git.BranchName(o.task)
		if err := repo.CreateBranch(ctx, branch); err != nil {
			rec.Logf("branch: %v", err)
			warn("branch creation failed: " + err.Error())
		} else {
			rec.Session.Branch = branch
		}
		if err := repo.Commit(ctx, commitMessage(safeTask, rec.Session.ID)); err != nil {
			rec.Logf("commit: %v", err)
			warn("commit skipped: " + err.Error())
		}
		if o.openPR {
			out, perr := repo.OpenPR(ctx, git.PRInput{
				Title: "AgentBox: " + safeTask, Body: prBody(safeTask, ep, rec.Session.Tests, rec.Session.ID), Branch: branch, Base: "main",
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

// buildAgentEnv assembles the environment passed to the agent: minimal safe
// host variables plus explicitly allowlisted secrets. Nothing else is exposed.
func buildAgentEnv(ep policy.EffectivePolicy) map[string]string {
	env := map[string]string{}
	for _, k := range minimalSafeEnv {
		if v := os.Getenv(k); v != "" {
			env[k] = v
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
	if ep.EnforcedNetwork() == "open" {
		netMode = "bridge"
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

	return runtime.RuntimeSpec{
		Engine:            engineLabel(ep),
		Image:             ep.Runtime.Image,
		NetworkMode:       netMode,
		Workdir:           root,
		Env:               env,
		User:              "10001:10001",       // non-root
		Privileged:        false,               // never
		MountDockerSocket: o.allowDockerSocket, // only with explicit unsafe flag
		ReadOnlyPaths:     nil,
		WritePaths:        writeMounts,
	}
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
		return nil, codedf(ExitRuntimeUnavail, "podman runtime is not implemented yet.\nUse engine docker, or run with --dry-run.")
	default:
		return nil, codedf(ExitRuntimeUnavail, "unknown runtime engine %q", ep.Runtime.Engine)
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
	return fmt.Sprintf("agentbox: %s\n\nAgentBox-Session: %s", task, sessionID)
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

Generated by AgentBox.

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
	fmt.Fprintln(w, "AgentBox session started")
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

	// Surface an actionable hint when the container failed to even start
	// (exit 125 is Docker's "container could not be created" code, most often a
	// missing runtime image).
	if !o.dryRun && res.ExitCode == 125 {
		fmt.Fprintln(w, "\nThe runtime container failed to start (exit 125).")
		fmt.Fprintf(w, "The image %q may not exist locally. Build or pull it, or use --dry-run.\n", ep.Runtime.Image)
	}

	if o.dryRun && len(res.Description) > 0 {
		fmt.Fprintln(w, "\nIntended runtime actions (dry-run):")
		for _, line := range res.Description {
			fmt.Fprintf(w, "  %s\n", line)
		}
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
