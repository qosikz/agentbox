# AgentBox

**Safe workspaces for AI coding agents — and a sandbox your agent harness can drive.**

AgentBox runs AI coding agents (Claude Code, Codex, Gemini, Goose, OpenCode, or
**any custom shell agent**) in isolated, reproducible, policy-controlled
workspaces — so an agent can edit a real repository without uncontrolled access
to your secrets, network, host filesystem, or MCP tools. Every run leaves an
auditable session record.

It works in **both directions**: point AgentBox at an agent, **or** let an agent
harness — Claude Code, [OpenClaw](#use-agentbox-from-your-agent-harness-integration),
Hermes Agent, or any MCP/skill-capable harness — call AgentBox as its *safety
sandbox* to test risky commands, validate generated code, and vet new tools or
subagents before trusting them. Via `agentbox exec`, an MCP server, or a
cross-harness skill.

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

> **Status: active development (v0.3.0).** Dry-run, sessions, policy, MCP
> scanning, real container execution (Docker or Podman), harness integration
> (`exec` / `mcp serve` / `skill`), and baked-in containerized agents are
> supported. Real runs require a runtime image you provide (see
> [Runtime image](#runtime-image)).

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
| `agentbox exec "<command>"` | Run a command in an isolated workspace (exit code passes through) |
| `agentbox policy check [--json]` | Validate policy; show effective config, unsafe options, honest limitations |
| `agentbox mcp scan <path> [--json]` | Statically scan an MCP server (exit 2 if unsafe) |
| `agentbox mcp serve` | Serve sandbox tools over MCP (stdio) to agent harnesses |
| `agentbox skill install` | Install the AgentBox skill into a harness (Claude Code, OpenClaw, Hermes, …) |
| `agentbox session list / show [id] / replay [id]` | Inspect recorded sessions |
| `agentbox doctor` | Diagnose local setup |
| `agentbox version` | Print version |

Most commands support `--json`. Exit codes follow
[docs/05_cli_ux_specification.md](docs/05_cli_ux_specification.md) §8.

### Useful `run` flags

```text
--dry-run                 Plan only; do not execute the agent (no Docker required)
--agent <name>            custom | claude | codex | gemini | goose | opencode
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

```bash
agentbox run "fix the failing test" --agent claude --runtime local --yes-unsafe --commit
```

Built-in adapters: `custom`, `claude`, `codex`, `gemini`, `goose`, `opencode`.
The `custom` adapter runs **any** CLI agent — point `agent.custom.command` at
your binary and AgentBox substitutes the task into `{{ task }}`, so you are
never limited to the built-ins. AgentBox runs the agent in a disposable
workspace copy, re-runs your test commands, captures the diff, and (with
`--commit`) propagates the branch back into your repository. Local mode
forwards only `PATH`, `HOME`, `USER`, `LOGNAME`, `LANG`, `LC_ALL`, `TERM`, and
explicitly allowlisted secrets. For container runs the agent CLI must exist in
your runtime image and its API key must be allowlisted in `secrets.allow`;
keychain/OAuth logins (like `claude`'s) only work in local mode.

### Run an agent fully containerized (baked-in agents)

To run a coding agent inside the sandbox, **bake its CLI into a runtime image**
and let AgentBox run it under policy. The agent binary lives in the image, not
on your host — AgentBox preflights it by probing the image, so a baked-in agent
you have never installed locally still runs.

```bash
# Build an image with the agent CLI baked in (examples in examples/agents/):
docker build -t agentbox/codex:latest -f examples/agents/codex.Dockerfile examples/agents

# Inject the key at runtime (NEVER baked into the image) and run.
# (Illustrative — Codex auth/sandbox specifics are version-dependent; the stub
# below is the verified path. `codex exec` reads CODEX_API_KEY.)
export CODEX_API_KEY=sk-...
agentbox run "add a test for parseConfig" --policy examples/agentbox.codex.yaml --yes-unsafe
```

- **The API key is never in the image.** Image layers are immutable and would
  leak a baked-in secret via `docker history` / `docker save` / a registry push.
  AgentBox reads the key from your host env, injects it into the container only
  when `secrets.allow` lists it (and `secrets.deny` does not), and redacts its
  value from logs, diffs, and session metadata.
- **Network caveat (honest):** a real agent must reach its model API, but
  `network.mode: allowlist` is **not enforced yet** (no egress proxy) and falls
  back to `deny`. So a real agent run currently needs `network: open` — an
  explicit unsafe mode. Enforced allowlist egress is the next safety milestone.

Prove the whole path for free with the bundled **stub agent** — no key, no
network, no spend:

```bash
docker build -t agentbox/stub-agent:latest -f examples/agents/stub.Dockerfile examples/agents
AGENTBOX_FAKE_API_KEY=dummy-not-a-real-key \
  agentbox run "prove the path" --policy examples/agentbox.stub.yaml
```

The stub confirms the injected key reached the container and writes a file; the
saved session shows the dummy key only as `[REDACTED:AGENTBOX_FAKE_API_KEY]`.
See [examples/agents/README.md](examples/agents/README.md) for the full guide.

## Use AgentBox FROM your agent (harness integration)

This is where AgentBox shines. An agent harness on your machine — **Claude
Code, OpenClaw, Hermes Agent**, Codex CLI, Gemini CLI, Goose, OpenCode, or
anything that speaks MCP or reads markdown skills — can call AgentBox as its
**safety sandbox**. The harness *is* the agent; AgentBox is the blast shield
around whatever it wants to try:

- **Test a new subagent or tool** before trusting it in the real workspace.
- **Run generated code / risky commands** (migrations, installs, `rm`) and read
  the exit code, diff, and output back — without touching the host.
- **Vet an MCP server** for dangerous capabilities before wiring it in.

Every experiment is isolated, policy-controlled, secret-redacted, and recorded
as an auditable session. Unsafe modes are **not** reachable through these
surfaces, so a harness can never escalate past your `agentbox.yaml`. Three ways
to wire it in:

**1. The sandbox primitive** — `agentbox exec` runs any command in an isolated
workspace and passes the command's exit code through:

```bash
agentbox exec "go test ./..." --json     # exit_code, stdout, changed_files, session_dir
agentbox exec --dry-run "rm -rf build"   # preview the sandbox without executing
```

**2. The skill** — teach your harness when to reach for the sandbox:

```bash
agentbox skill install --target claude-project   # ./.claude/skills/ (this repo)
agentbox skill install --target openclaw         # ~/.openclaw/workspace/skills/
agentbox skill install --target hermes           # ~/.hermes/skills/
agentbox skill install --target agents           # ~/.agents/skills/ (cross-agent standard)
```

**3. The MCP server** — structured tools (`sandbox_exec`, `sandbox_run`,
`scan_mcp`, `session_list`, `session_show`) for any MCP-capable harness:

```bash
claude mcp add agentbox -- agentbox mcp serve
openclaw mcp add agentbox --command agentbox --arg mcp --arg serve
codex mcp add agentbox -- agentbox mcp serve
gemini mcp add agentbox agentbox mcp serve
```

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
