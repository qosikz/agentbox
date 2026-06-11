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

> **Status: production preview (v0.1.0).** Dry-run, sessions, policy, MCP
> scanning, and real container execution (Docker or Podman) are supported. Real
> runs require a runtime image you provide (see [Runtime image](#runtime-image)).

## Why

AI coding agents can read code, run commands, install dependencies, open PRs, and
call MCP tools. Run directly on your machine, they can leak `.env` files, SSH
keys, and cloud credentials, reach any network endpoint, and run destructive
commands. AgentBox puts deterministic guardrails around them:

- **Secrets** — the host environment (including `PATH` and `HOME`) is never
  forwarded; containers get a standard `PATH`, `HOME` set to the workspace,
  `LANG`/`TERM`, and explicitly allowlisted secrets only. Secret-shaped values
  are redacted from logs.
- **Filesystem** — sensitive paths (`.env`, `~/.ssh`, `~/.aws`, `~/.kube`, the
  Docker socket) are excluded from the workspace by default.
- **Network** — denied by default; `open` requires explicit unsafe confirmation.
- **Runtime** — containers run as a non-root user with `--cap-drop ALL` and
  `--security-opt no-new-privileges`; never privileged, never the Docker socket.
  Works with Docker or Podman.
- **MCP** — a static scanner flags dangerous MCP server capabilities before you trust them.
- **Audit** — every run is recorded under `.agentbox/sessions/<id>/`.

AgentBox does not make an unsafe agent magically safe. It creates guardrails, and
it is honest about what it does and does not enforce.

## Install

**Prebuilt binaries:**

Download the binary for your platform from
[GitHub Releases](https://github.com/qosi/agentbox/releases) (published for
every `v*` tag; `checksums.txt` carries SHA-256 sums), make it executable, and
put it on your `PATH`.

**From source:**

```bash
git clone https://github.com/qosi/agentbox.git
cd agentbox
make build        # produces ./bin/agentbox with embedded version/commit/date
./bin/agentbox version
```

**With `go install`:**

```bash
go install github.com/qosi/agentbox/cmd/agentbox@latest
```

Building from source requires Go 1.23+. Docker or Podman is optional and only
needed for real (non-dry-run) container execution.

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
--engine docker|podman    Container engine (default: policy runtime.engine)
--write <path>            Add a writable path (repeatable)
--commit                  Commit the agent's changes on a new branch
--open-pr                 Open a pull request (requires the gh CLI)
--runtime local --unsafe  Run on the host without a container (unsafe)
--yes-unsafe              Acknowledge unsafe mode non-interactively (CI)
```

### Example: a real agent (Claude Code)

```yaml
agent:
  default: custom
  custom:
    command: claude
    args: ["-p", "--permission-mode", "acceptEdits", "{{ task }}"]
```

```bash
agentbox run "fix the failing test" --runtime local --yes-unsafe --commit
```

AgentBox runs the agent in a disposable workspace copy, re-runs your test
commands, captures the diff, and (with `--commit`) propagates the branch back
into your repository. Local mode forwards only `PATH`, `HOME`, `USER`,
`LOGNAME`, `LANG`, `LC_ALL`, `TERM`, and explicitly allowlisted secrets.
For container runs the agent CLI must exist in your runtime image and its API
key must be allowlisted in `secrets.allow`; keychain/OAuth logins (like
`claude`'s) only work in local mode.

## Policy

`agentbox init` writes a commented `agentbox.yaml` with secure defaults. See
[docs/04_policy_specification.md](docs/04_policy_specification.md) and the
[examples](examples/) (`agentbox.yaml`, `agentbox.strict.yaml`).

Key rules: deny overrides allow; sensitive paths are always denied unless you
opt in with an explicit unsafe flag; the network defaults to `deny`; no secrets
are passed unless named in `secrets.allow`.

A few runtime knobs worth knowing:

- `runtime.engine: docker | podman` selects the container engine (or use
  `--engine` per run).
- `budget.max_runtime_minutes` is enforced as a hard deadline on real runs —
  the agent is stopped when it expires (dry-run is unaffected).
- `runtime.cleanup` is honored: the disposable workspace copy is removed after
  the run; set `cleanup: false` to keep it for debugging. Session artifacts
  under `.agentbox/sessions/` are always kept.

## Runtime image

Real container runs execute the agent inside a container image (Docker or
Podman). The default policy
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
- `budget` token/USD caps depend on adapter support and are reported as `unknown`
  otherwise (`max_runtime_minutes` is enforced).
- Real container execution requires a runtime image you build; there is no
  published runtime image yet.
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
