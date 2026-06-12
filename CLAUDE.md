# CLAUDE.md — Andbo Project Instructions

You are helping build **Andbo**, an open-source secure runtime for AI coding agents.

## Product summary

Andbo lets developers run AI coding agents in isolated, reproducible, policy-controlled workspaces.

The core command is:

```bash
andbo run "fix failing tests"
```

The product must protect secrets, restrict filesystem/network access, scan MCP tools, record sessions, generate diffs, run tests, and optionally open pull requests.

## Highest-priority rules

1. Build CLI-first.
2. Keep MVP small and working.
3. Do not build a web dashboard.
4. Do not create a proprietary agent.
5. Do not overclaim security enforcement.
6. Write tests with every feature.
7. Keep output clear and developer-friendly.
8. Secure defaults are mandatory.
9. Prefer simple, composable Go code.
10. Every feature must connect to the PRD.

## Implementation language

Use Go for the core CLI/runtime.

## Architecture

Follow this structure:

```text
cmd/andbo/
internal/cli/
internal/config/
internal/policy/
internal/runtime/
internal/workspace/
internal/adapters/
internal/mcpguard/
internal/git/
internal/session/
internal/secrets/
internal/report/
```

## MVP priorities

P0:

- `andbo init`
- `andbo run`
- `andbo policy check`
- `andbo doctor`
- `andbo session list`
- `andbo session show`
- `andbo mcp scan`
- YAML policy loading
- Docker runtime abstraction
- Custom adapter
- Session recorder
- Secret redaction
- Basic Git diff
- MCP static scanner
- Tests

P1:

- Harness integration (exec / MCP server / skill)
- GitHub PR creation
- GitHub Action
- Podman support
- Cost metadata
- Markdown reports

P2:

- Kubernetes runner
- MCP runtime firewall
- Enterprise dashboard

## Security rules

Default behavior must be conservative:

- Do not pass host secrets by default.
- Do not mount `~/.ssh`.
- Do not mount `~/.aws`.
- Do not mount `~/.kube`.
- Do not mount Docker socket.
- Do not run privileged containers.
- Deny `.env` by default.
- Redact secrets from logs.
- Network open mode must require explicit unsafe confirmation.
- Local runtime mode must require explicit unsafe confirmation.

## Coding standards

- Use small packages.
- Keep interfaces narrow.
- Return actionable errors.
- Use table-driven tests.
- Avoid global mutable state.
- Do not introduce unnecessary dependencies.
- Add comments for security-sensitive behavior.
- Do not swallow errors.
- Prefer standard library where practical.

## CLI output rules

Good output:

```text
✓ Workspace created
✓ Policy applied
✓ Secrets protected
✓ Agent completed
✓ Diff generated
✓ Session saved
```

Bad output:

```text
Error: failed
```

Errors must explain:

1. What failed.
2. Why it likely failed.
3. How to fix it.

## Testing rules

Before finishing any task, run or explain:

```bash
make fmt
make lint
make test
make build
```

If a command cannot run, explain why and what remains.

## Documentation rules

When changing behavior, update:

- README if user-facing.
- docs if architecture/security/policy changes.
- examples if config changes.

## Do not do

- Do not add SaaS code.
- Do not add RBAC/SSO in MVP.
- Do not create a huge framework.
- Do not silently weaken security defaults.
- Do not claim network allowlist enforcement unless implemented.
- Do not pass through all environment variables.
- Do not use random shell scripts without tests.

## End-of-task response format

Always summarize:

```text
Changed:
- ...

Tests:
- ...

Security impact:
- ...

Known limitations:
- ...

Next:
- ...
```
