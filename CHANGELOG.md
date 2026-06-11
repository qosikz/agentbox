# Changelog

All notable changes to AgentBox are documented here.

## v0.3.2 — 2026-06-12 (public-home prep)

### Changed
- **Module path renamed** `github.com/qosi/agentbox` → `github.com/qosikz/agentbox`
  to match the public home (the QOSI organization at github.com/qosikz). This
  updates `go.mod`, every import, the `ghcr.io/.../agentbox` test fixtures, and
  the README install/clone/`go install` URLs, so `go install
  github.com/qosikz/agentbox/cmd/agentbox@latest` resolves once the repo is
  public. No runtime behavior change.

### Documentation
- README: added an "Add AgentBox to your agent harness" quickstart near the top
  (copy-paste `skill install` + `exec` + the MCP one-liner) so harness users can
  wire in the sandbox in ~30 seconds. Status bumped to v0.3.1.

## v0.3.1 — 2026-06-11 (harness-focused)

### Removed
- The `aider` adapter. Aider's upstream activity has stalled (last release
  2025-08); AgentBox now focuses on actively-maintained harnesses and the
  `custom` adapter, which runs any CLI agent. `agent: aider` is no longer a
  valid adapter name — use `custom` with `agent.custom.command: aider` if you
  still need it.

### Changed
- Documentation and positioning lead with **harness integration** — driving
  AgentBox from Claude Code, OpenClaw, Hermes Agent, or any MCP/skill-capable
  harness (via `exec` / `mcp serve` / `skill`) — and with the `custom` adapter
  as the bring-your-own-agent path. `agentbox doctor` now probes
  claude/codex/gemini/goose/opencode instead of aider.

## v0.3.0 — 2026-06-11 (containerized agents)

Run a coding agent fully inside the sandbox: bake its CLI into a runtime image
and let AgentBox run it under policy, with credentials injected at runtime and
redacted from logs.

### Added
- **Baked-in agent support.** The agent CLI can now live only in the runtime
  image. AgentBox preflights the agent by **probing the image** (a hardened,
  no-network, self-removing throwaway container) instead of the host PATH, so a
  containerized agent you have never installed locally runs correctly. A missing
  agent yields an actionable, image-aware error before anything executes.
- `examples/agents/` — worked runtime images and a guide:
  - `stub.Dockerfile` + `stub-agent.sh` — a zero-cost proof fixture that
    confirms a policy-injected key reaches the container and is redacted, with
    no API key and no network.
  - `codex.Dockerfile` — an illustrative image baking the OpenAI Codex CLI in
    (auth/sandbox setup is provider- and version-specific; the stub is the
    verified zero-cost path). Uses `CODEX_API_KEY`, matching the codex adapter.
  - `README.md` — the baked-in-agent model, runtime secret injection, and the
    honest network limitation.
- Example policies `examples/agentbox.stub.yaml` and `examples/agentbox.codex.yaml`.
- `runtime.Runner` gains `ProbeBinary(ctx, image, bin)` (implemented for
  docker/podman, local, and dry-run runners).

### Changed
- The agent preflight is now **isolation-aware**: local isolation checks the
  host PATH (as before); container isolation probes the image. Container
  dry-runs skip the probe entirely, so planning never needs a daemon — and the
  previous spurious "agent not found on PATH" warning for container dry-runs is
  gone.
- A real container agent run under `network=deny` with allowlisted secrets now
  records a note that the agent cannot reach a remote API (allowlist egress is
  still unenforced).

### Security
- Credentials for a baked-in agent are injected **only at runtime** under
  `secrets.allow` (deny still overrides allow; the secure defaults keep
  `OPENAI_API_KEY`/`ANTHROPIC_API_KEY`/`AWS_SECRET_ACCESS_KEY` out unless you
  remove them from `deny` deliberately) and are redacted from all recorded
  artifacts. No example image contains a key, an `ENV`, or an `ARG` for one.
- The image probe is unprivileged: `--cap-drop ALL`, `--security-opt
  no-new-privileges`, `--network none`, `--rm`, no mounts.

## v0.2.0 — 2026-06-11 (harness integration)

AgentBox now works in BOTH directions: it sandboxes agents, and agent
harnesses use it as their safety sandbox.

### Added
- `agentbox exec "<command>"` — run any command in an isolated, policy-
  controlled workspace with no agent adapter: the calling harness IS the
  agent. The sandboxed command's exit code passes through; `--json` returns
  exit_code, redacted output, changed files, and the session path.
- `agentbox mcp serve` — a stdio MCP server (protocol 2025-11-25, with
  fallbacks) exposing `sandbox_exec`, `sandbox_run`, `scan_mcp`,
  `session_list`, and `session_show` to any MCP-capable harness (Claude Code,
  OpenClaw, Codex CLI, Gemini CLI, Goose, OpenCode). Unsafe modes are not
  reachable through MCP tools.
- `agentbox skill install` — installs a cross-harness SKILL.md
  (agentskills.io-style) teaching the harness when to use the sandbox.
  Targets: claude-project, claude-user, openclaw, hermes, agents
  (~/.agents/skills), or `--dir`.
- Built-in adapters for popular coding agents: `claude` (Claude Code,
  `-p --permission-mode acceptEdits`), `codex` (`codex exec --sandbox
  workspace-write`), `gemini` (`--approval-mode auto_edit -p`), `goose`
  (`goose run --no-session -t` with `GOOSE_MODE=auto`), `opencode`
  (`opencode run`). `aider` remains as a community adapter (upstream activity
  has slowed; last release 2025-08).

### Fixed
- Local (unsafe) runs now forward `USER`/`LOGNAME` to the agent. OS keychains
  (e.g. Claude Code's macOS auth) and git's identity fallback require them;
  without them real agents failed to authenticate ("Not logged in").
  Containers still never receive the host `USER`.

### Verified
- End-to-end run with a real coding agent: Claude Code fixed a failing Go
  test under AgentBox — tests re-run green, diff captured, branch committed
  and propagated, session recorded, workspace cleaned.

## v0.1.0 — 2026-06-11 (production preview)

### Added
- CLI: `init`, `run`, `policy check`, `mcp scan`/`mcp list`, `session list`/`show`/`replay`, `doctor`, `version`.
- YAML policy engine with secure defaults, strict loading, validation, and an
  `EffectivePolicy` (deny-overrides-allow, mandatory sensitive denies, unsafe-mode gating).
- Runtime abstraction: dry-run runner, Docker runner (non-root, never privileged,
  never mounts the Docker socket, `deny` → `--network none`), and an unsafe local runner.
- Agent adapters: `custom` (templated `{{ task }}`) and `aider`, with a registry
  and availability checks.
- Session recorder: full `session.json` schema plus `report.md`, redacted `logs.txt`,
  `diff.patch`, `policy-events.json`, `test-results.txt`, and `metadata.json`.
- Secret redaction: named env values plus built-in/extra regex patterns, applied to
  every persisted artifact.
- MCP Guard static scanner with stable rule IDs (shell, env/secrets, filesystem,
  network, database, git, prompt-injection), human and JSON output, exit 2 on unsafe.
- Git integration: repo detection, diff (including agent-created files), changed-file
  attribution, branch/commit, remote normalization/clone, and `gh`-based PR creation.
- Disposable, sanitized workspace copy (denied files excluded; `.git` preserved).
- Unsafe-mode confirmation flow (interactive prompt / `--yes-unsafe` for CI; exit 8).
- GitHub composite action and a safe, dry-run-by-default example workflow.
- Security acceptance tests mapping to the threat model (§8).

### Production hardening
- Podman support: `runtime.engine: podman` in the policy or the new
  `agentbox run --engine docker|podman` flag.
- Container hardening: containers run as a non-root user with `--cap-drop ALL`
  and `--security-opt no-new-privileges`; never privileged, never the Docker socket.
- Environment hygiene: host `PATH`/`HOME` are never forwarded into containers;
  containers get a standard `PATH`, `HOME` set to the workspace, `LANG`/`TERM`,
  and explicitly allowlisted secrets only.
- `budget.max_runtime_minutes` is enforced as a hard deadline on real runs
  (dry-run is unaffected).
- `runtime.cleanup` is honored: the disposable workspace copy is removed after
  the run; `cleanup: false` keeps it for debugging. Session artifacts are always kept.
- Versioned builds: `make build` embeds version/commit/date via `-ldflags`;
  `make release` cross-compiles darwin/linux × amd64/arm64 with SHA-256 checksums.
- Release workflow: pushing a `v*` tag publishes prebuilt binaries to GitHub Releases.

### Known limitations
- `network: allowlist` is not enforced yet (falls back to `deny`, advisory list).
- `commands.deny` is best-effort; `budget` USD/token caps depend on adapter support.
- Real container execution requires a runtime image you build (none published yet).
- Secret redaction is best-effort and may miss unknown formats.
