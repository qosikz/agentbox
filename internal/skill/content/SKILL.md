---
name: agentbox-sandbox
description: Run commands, tests, or untrusted code in an isolated AgentBox sandbox. Use before executing risky commands, to validate generated code, or to vet MCP servers.
version: 0.2.0
metadata: {"openclaw": {"requires": {"bins": ["agentbox"]}}, "hermes": {"category": "devops", "tags": ["sandbox", "security", "testing"]}}
---

# AgentBox Sandbox

Run anything risky inside an isolated, policy-controlled container instead of the host.

## When to use

- Executing risky or untrusted commands (installers, curl-pipe-sh, unknown scripts).
- Validating generated code, new tools, skills, or subagents before trusting them.
- Testing changes without touching the real workspace.
- Vetting an MCP server before connecting it.
- Auditing past sandboxed runs.

## Core commands

```
agentbox exec "<command>" --json        # sandboxed command; exit code passes through; changed files + diff captured
agentbox exec --dry-run "<command>"     # preview the sandbox (mounts, policy, image) without executing
agentbox run "<task>" --agent <name>    # drive a coding agent in the sandbox: custom, claude, codex, gemini, goose, opencode
agentbox mcp scan <path> --json         # static-scan an MCP server; exit 2 = unsafe
agentbox session list                   # audit recorded runs
agentbox session show latest            # inspect the most recent run; artifacts in .agentbox/sessions/<id>/
```

Prefer `--json` when you will parse the result.

## Reading results

`--json` fields: `exit_code`, `stdout`, `stderr`, `changed_files`, `session_dir`. A non-zero `exec` exit code means the command failed inside the sandbox — report it, do not retry on the host.

## Safety rules

- Runs are policy-governed by `agentbox.yaml`: network denied by default, secrets and `.env` excluded.
- NEVER pass `--unsafe`, `--yes-unsafe`, `--runtime local`, `--allow-host-home`, or `--allow-docker-socket` unless the human explicitly asked.
- If `agentbox` or Docker is missing, say so instead of bypassing the sandbox (`agentbox doctor` diagnoses).

## Requirements

`agentbox` binary on PATH; Docker (or Podman) for container isolation; run `agentbox init` once per project.
