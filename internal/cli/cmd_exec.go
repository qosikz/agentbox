package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/qosikz/andbo/internal/config"
	"github.com/qosikz/andbo/internal/git"
	"github.com/qosikz/andbo/internal/policy"
	"github.com/qosikz/andbo/internal/runtime"
	"github.com/qosikz/andbo/internal/session"
	"github.com/qosikz/andbo/internal/workspace"
)

// cmdExec runs an arbitrary command inside an isolated, policy-controlled
// workspace. Unlike `run`, there is no agent adapter: the CALLER is the agent.
// This is the primitive that agent harnesses (Claude Code, OpenClaw, ...) use
// to safely try risky commands, validate generated code, or test new tools.
//
// Exit-code contract (harness ergonomics): on infrastructure success the
// sandboxed command's own exit code is passed through, so callers can gate on
// it directly. Andbo's own failures keep the standard coded exits
// (invalid config 7, runtime unavailable 3, unsafe required 8, ...).
func (r *Root) cmdExec(ctx context.Context, args []string) error {
	o, command, err := parseExecArgs(args)
	if err != nil {
		return coded(ExitGeneral, err)
	}
	if len(command) == 0 {
		return codedf(ExitGeneral, "missing command.\nExamples:\n  andbo exec \"go test ./...\"\n  andbo exec -- python -m pytest tests/")
	}
	if o.commit || o.openPR {
		return codedf(ExitGeneral, "exec does not support --commit/--open-pr; use 'andbo run' for agent workflows that commit")
	}

	base, err := os.Getwd()
	if err != nil {
		return coded(ExitGeneral, err)
	}

	// Policy: same loading, validation, overrides, and unsafe gating as run.
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
	if o.runtime != "" && o.runtime != "container" && o.runtime != "local" {
		return codedf(ExitInvalidConfig, "--runtime %q is invalid (expected: container, local)", o.runtime)
	}
	ov := policy.Overrides{
		Network: o.network, Runtime: o.runtime, Engine: o.engine, ExtraWrite: o.write,
		Unsafe: o.unsafe, AllowHostHome: o.allowHostHome,
		AllowDockerSocket: o.allowDockerSocket, YesUnsafe: o.yesUnsafe,
	}
	ep := policy.BuildEffectivePolicy(cfg, o.policy, ov)

	// Same refusal as run, in the same place: exec shares the deadline, so it
	// must share the bound on what that deadline can hold.
	if err := checkBudgetMinutes(ep.Budget.MaxRuntimeMinutes, o.policy); err != nil {
		return err
	}

	if ep.RequiresUnsafeConfirmation() {
		if o.dryRun {
			warn("unsafe options are set; a real exec would require confirmation:")
			for _, reason := range ep.UnsafeReasons() {
				warn("  - " + reason)
			}
		} else if err := confirmUnsafe(ep.UnsafeReasons(), o.yesUnsafe); err != nil {
			return err
		}
	}

	if !o.dryRun && ep.Budget.MaxRuntimeMinutes > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, budgetWindow(ep.Budget.MaxRuntimeMinutes))
		defer cancel()
	}

	cmdLine := strings.Join(command, " ")
	rec := session.NewRecorder(base, cmdLine, "exec")
	rec.Session.DryRun = o.dryRun
	rec.Session.Repository = "."
	rec.Session.Policy = session.PolicyRef{Path: o.policy, Hash: config.HashPolicyFile(o.policy)}
	rec.Session.Runtime = session.RuntimeInfo{Engine: engineLabel(ep), Network: ep.EnforcedNetwork(), Image: ep.Runtime.Image}
	rec.Event(session.EvPolicyLoaded)
	red := buildRedactor(ep)

	workDir := filepath.Join(base, ".andbo", "work", rec.Session.ID)
	keep := false
	if !o.dryRun && ep.Runtime.Cleanup {
		defer func() {
			if !keep {
				_ = os.RemoveAll(workDir)
			}
		}()
	}

	plan, err := workspace.BuildPlan(base, ep)
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

	runner, rerr := selectRunner(ep, o)
	if rerr != nil {
		return finishErr(rec, red, rerr)
	}
	if err := runner.Available(ctx); err != nil {
		return finishErr(rec, red, codedf(ExitRuntimeUnavail, "%v", err))
	}

	execRoot := base
	var repo *git.Repo
	var baseline map[string]bool
	if !o.dryRun {
		if err := prepareWorkspace(plan, base, workDir); err != nil {
			return finishErr(rec, red, coded(ExitGeneral, err))
		}
		execRoot = workDir
		if git.IsRepo(execRoot) {
			repo, _ = git.Open(execRoot)
			if repo != nil {
				// Normalize untracked entries to file granularity (matching the
				// post-run diff) so pre-existing files are correctly excluded.
				repo.MarkIntentToAdd(ctx)
				if files, ferr := repo.ChangedFiles(ctx); ferr == nil {
					baseline = toSet(files)
				}
			}
		}
	}

	agentEnv := buildAgentEnv(ep)
	spec := buildRuntimeSpec(ep, plan, execRoot, agentEnv, o)
	rec.Event(session.EvRuntimeStarted)

	cs := runtime.CommandSpec{Env: agentEnv, WorkingDir: execRoot}
	if len(command) == 1 {
		// Single string form: run through the shell.
		cs.Executable, cs.Args = "sh", []string{"-lc", command[0]}
	} else {
		cs.Executable, cs.Args = command[0], command[1:]
	}
	if ep.Budget.MaxRuntimeMinutes > 0 {
		cs.Timeout = budgetWindow(ep.Budget.MaxRuntimeMinutes)
	}
	rec.Session.Commands = append(rec.Session.Commands, session.ExecutedCmd{Cmd: cmdLine})

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

	if ep.Budget.MaxRuntimeMinutes > 0 && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		rec.PolicyBlocked(fmt.Sprintf("exec exceeded budget.max_runtime_minutes (%d); command stopped", ep.Budget.MaxRuntimeMinutes))
		rec.Session.Result = session.Result{Status: "failed", Summary: "budget exceeded: exec hit max_runtime_minutes"}
		return finishErr(rec, red, codedf(ExitAgentFailed,
			"budget exceeded: the command hit budget.max_runtime_minutes (%d) and was stopped.", ep.Budget.MaxRuntimeMinutes))
	}
	if runErr != nil {
		rec.Session.Result = session.Result{Status: "failed", Summary: runErr.Error()}
		return finishErr(rec, red, codedf(ExitRuntimeUnavail, "exec runtime error: %v", runErr))
	}

	if repo != nil && !o.dryRun {
		if diff, derr := repo.Diff(ctx); derr == nil {
			rec.Diff = diff
			rec.Event(session.EvGitDiffGenerated)
		}
		if files, ferr := repo.ChangedFiles(ctx); ferr == nil {
			if changed := subtractSet(files, baseline); len(changed) > 0 {
				rec.Session.ChangedFiles = changed
				rec.Event(session.EvFileChanged)
			}
		}
	}

	status := "success"
	if res.ExitCode != 0 {
		status = "failed"
	}
	summary := fmt.Sprintf("exec completed with exit code %d", res.ExitCode)
	if o.dryRun {
		summary = "dry run completed; command was not executed"
	}
	rec.Session.Result = session.Result{Status: status, Summary: summary}

	dir, serr := rec.Save(red)
	if serr != nil {
		return coded(ExitGeneral, serr)
	}

	if o.json {
		out := map[string]any{
			"exit_code":     res.ExitCode,
			"stdout":        red.Redact(res.Stdout),
			"stderr":        red.Redact(res.Stderr),
			"changed_files": rec.Session.ChangedFiles,
			"policy_events": rec.Session.PolicyEvents,
			"dry_run":       o.dryRun,
			"session_dir":   dir,
		}
		if err := printJSON(os.Stdout, out); err != nil {
			return coded(ExitGeneral, err)
		}
	} else {
		printExecSummary(rec, ep, o, res, dir)
	}

	// Pass the sandboxed command's exit code through to the caller. Codes
	// outside 1-255 (signal kills report -1) collapse to the agent-failed code.
	if !o.dryRun && res.ExitCode != 0 {
		code := res.ExitCode
		if code < 1 || code > 255 {
			code = ExitAgentFailed
		}
		return &CodedError{Code: code, Err: emptyError{}}
	}
	return nil
}

// parseExecArgs splits exec arguments into flags and the command. Everything
// after a literal "--" is the command argv, taken verbatim. Without "--",
// non-flag positionals are joined into a single shell command string.
func parseExecArgs(args []string) (runOptions, []string, error) {
	var flagPart, command []string
	for i, a := range args {
		if a == "--" {
			flagPart = args[:i]
			command = args[i+1:]
			break
		}
	}
	if command == nil {
		flagPart = args
	}

	o, err := parseRunFlags(flagPart)
	if err != nil {
		return o, nil, err
	}
	if command == nil {
		// parseRunFlags put non-flag positionals into task/repo; recombine
		// them in their original roles as the command string.
		var parts []string
		if o.repo != "" {
			parts = append(parts, o.repo)
		}
		if o.task != "" {
			parts = append(parts, o.task)
		}
		if len(parts) > 0 {
			command = []string{strings.Join(parts, " ")}
		}
	}
	return o, command, nil
}

func printExecSummary(rec *session.Recorder, ep policy.EffectivePolicy, o runOptions, res runtime.RunResult, dir string) {
	s := rec.Session
	w := os.Stdout
	fmt.Fprintf(w, "Andbo exec\n\n")
	fmt.Fprintf(w, "Command: %s\n", s.Task)
	runtimeLine := s.Runtime.Engine
	if o.dryRun {
		runtimeLine += " (dry-run)"
	}
	fmt.Fprintf(w, "Runtime: %s\n", runtimeLine)
	fmt.Fprintf(w, "Network: %s\n\n", s.Runtime.Network)

	if o.dryRun && len(res.Description) > 0 {
		fmt.Fprintln(w, "Intended runtime actions (dry-run):")
		for _, line := range res.Description {
			fmt.Fprintf(w, "  %s\n", line)
		}
		fmt.Fprintln(w)
	}

	if out := strings.TrimRight(res.Stdout, "\n"); out != "" {
		fmt.Fprintln(w, out)
	}
	if errOut := strings.TrimRight(res.Stderr, "\n"); errOut != "" {
		fmt.Fprintln(os.Stderr, errOut)
	}

	if len(s.ChangedFiles) > 0 {
		fmt.Fprintln(w, "\nChanged files (in the disposable workspace):")
		for _, f := range s.ChangedFiles {
			fmt.Fprintf(w, "  %s\n", f)
		}
	}

	fmt.Fprintf(w, "\nExit code: %d\n", res.ExitCode)
	fmt.Fprintf(w, "Session: %s\n", dir)
}
