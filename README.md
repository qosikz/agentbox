# AgentBox

**Safe workspaces for AI coding agents.**

AgentBox runs AI coding agents (Aider, Claude Code, Codex, custom shell agents, …)
in isolated, reproducible, policy-controlled workspaces — so an agent can edit a
real repository without uncontrolled access to your secrets, network, host
filesystem, or MCP tools. Every run leaves behind an auditable session record.

```bash
agentbox run "fix failing tests"
```

```text
AgentBox session started

Repository: .
Agent: custom
Runtime: docker
Network: deny
Policy: agentbox.yaml

✓ Workspace created
✓ Policy applied
✓ Secrets protected
✓ Agent completed
✓ Diff generated
✓ Session saved
```

> **Status: developer preview (MVP).** The fully-supported path today is
> `--dry-run` plus the local/CI safety tooling. Real container execution works
> but requires a runtime image you provide (see [Runtime image](#runtime-image)).

## Why

AI coding agents can read code, run commands, install dependencies, open PRs, and
call MCP tools. Run directly on your machine, they can leak `.env` files, SSH
keys, and cloud credentials, reach any network endpoint, and run destructive
commands. AgentBox puts deterministic guardrails around them:

- **Secrets** — the host environment is never forwarded; only explicitly
  allowlisted secrets are passed, and secret-shaped values are redacted from logs.
- **Filesystem** — sensitive paths (`.env`, `~/.ssh`, `~/.aws`, `~/.kube`, the
  Docker socket) are excluded from the workspace by default.
- **Network** — denied by default; `open` requires explicit unsafe confirmation.
- **Runtime** — non-root container, never privileged, never mounts the Docker socket.
- **MCP** — a static scanner flags dangerous MCP server capabilities before you trust them.
- **Audit** — every run is recorded under `.agentbox/sessions/<id>/`.

AgentBox does not make an unsafe agent magically safe. It creates guardrails, and
it is honest about what it does and does not enforce.

## Install

**From source (recommended during the preview):**

```bash
git clone https://github.com/qosi/agentbox.git
cd agentbox
make build        # produces ./bin/agentbox
./bin/agentbox version
```

**With `go install`:**

```bash
go install github.com/qosi/agentbox/cmd/agentbox@latest
```

Requires Go 1.23+. Docker is optional and only needed for real (non-dry-run)
container execution.

## Quickstart

```bash
# 1. Create a policy and session directory
agentbox init

# 2. Validate the policy and view the effective configuration
agentbox policy check

# 3. Plan a run without executing anything (safe, no Docker needed)
agentbox run "fix failing tests" --dry-run

# 4. Inspect the recorded session
agentbox session list
agentbox session show latest

# 5. Scan an MCP server for dangerous capabilities
agentbox mcp scan ./path-to-mcp-server
```

`agentbox doctor` checks your local setup (Docker, git, gh, known agents).

## Commands

| Command | Description |
|---|---|
| `agentbox init` | Create `agentbox.yaml` and `.agentbox/` |
| `agentbox run "<task>"` | Run an agent in an isolated, policy-controlled workspace |
| `agentbox policy check [--json]` | Validate policy; show effective config, unsafe options, honest limitations |
| `agentbox mcp scan <path> [--json]` | Statically scan an MCP server (exit 2 if unsafe) |
| `agentbox session list / show [id] / replay [id]` | Inspect recorded sessions |
| `agentbox doctor` | Diagnose local setup |
| `agentbox version` | Print version |

Most commands support `--json`. Exit codes follow
[docs/05_cli_ux_specification.md](docs/05_cli_ux_specification.md) §8.

### Useful `run` flags

```text
--dry-run                 Plan only; do not execute the agent (no Docker required)
--agent <name>            custom | aider
--policy <file>           Policy file (default: agentbox.yaml)
--network deny|allowlist|open
--write <path>            Add a writable path (repeatable)
--commit                  Commit the agent's changes on a new branch
--open-pr                 Open a pull request (requires the gh CLI)
--runtime local --unsafe  Run on the host without a container (unsafe)
--yes-unsafe              Acknowledge unsafe mode non-interactively (CI)
```

## Policy

`agentbox init` writes a commented `agentbox.yaml` with secure defaults. See
[docs/04_policy_specification.md](docs/04_policy_specification.md) and the
[examples](examples/) (`agentbox.yaml`, `agentbox.strict.yaml`).

Key rules: deny overrides allow; sensitive paths are always denied unless you
opt in with an explicit unsafe flag; the network defaults to `deny`; no secrets
are passed unless named in `secrets.allow`.

## Runtime image

Real container runs execute the agent inside a Docker image. The default policy
references `agentbox/default:latest`, which **you must provide** — there is no
published image yet. Build a minimal one from the example:

```bash
docker build -t agentbox/default:latest -f examples/runtime.Dockerfile examples/
agentbox run "fix tests"        # now uses your image
```

Until you build a runtime image, use `--dry-run` (the supported preview path) or
`--runtime local --unsafe` to run on the host.

## Sessions

Every run is recorded under `.agentbox/sessions/<id>/`:

```text
session.json   report.md   logs.txt   diff.patch
policy-events.json   test-results.txt   metadata.json
```

Logs and reports are passed through secret redaction before being written.

## GitHub Action

A composite action lives in [`.github/actions/agentbox`](.github/actions/agentbox);
a safe example workflow is in
[`examples/github-action-agentbox.yml`](examples/github-action-agentbox.yml). The
example defaults to `--dry-run` and uploads the session as an artifact. For fork
pull requests, keep it dry-run and avoid exposing write tokens.

## Security

See [SECURITY.md](SECURITY.md) and
[docs/03_security_model_and_threat_model.md](docs/03_security_model_and_threat_model.md).
The security acceptance tests (§8) live in `internal/cli/security_test.go`.

## MVP limitations (honest)

- `network: allowlist` is **not** enforced yet (no proxy/firewall); it falls back
  to `deny` and the domain list is advisory.
- `commands.deny` is best-effort and cannot stop an agent that spawns shells indirectly.
- `budget` token/USD caps depend on adapter support and are reported as `unknown` otherwise.
- Real container execution requires a runtime image you build; podman is not yet implemented.
- Secret redaction is best-effort and may miss unknown formats.

## Development

```bash
make fmt
make lint
make test
make build
```

## License

[Apache-2.0](LICENSE).
