# Changelog

All notable changes to AgentBox are documented here.

## v0.4.1 — 2026-06-12 (zero-friction first run + supply-chain trust)

Adoption and trust packaging: a real `agentbox run` now works with no setup, and
every release is verifiable.

### Added
- **Published default runtime image** `ghcr.io/qosikz/agentbox/runtime:latest`
  (multi-arch linux/amd64+arm64, non-root, minimal Debian + `git` +
  `ca-certificates`). The default policy points at it and Docker/Podman pull it
  automatically — no image to build before the first container run.
- **Signed releases (Sigstore / keyless).** `checksums.txt` is signed with
  `cosign sign-blob`; verify with the published `.sig` + `.pem` and the workflow
  identity. The runtime image is signed by digest.
- **SBOM** (SPDX): a dependency SBOM for the release plus an image SBOM scanned
  from the published runtime image.
- **SLSA build provenance** attestations for the release binaries and the image
  (verifiable with `gh attestation verify`).
- `publish-image.yml` workflow; a "Verifying releases" guide in the README.

### Changed
- Default `runtime.image` repointed from the unpublished `agentbox/default:latest`
  to the published GHCR image, across `agentbox init`, examples, and docs.
- All GitHub Actions are pinned to commit SHAs (release, CI, and the composite
  action) — supply-chain hardening against tag-mutation. Release/publish jobs run
  with least-privilege `permissions` and OIDC (`id-token`/`attestations: write`).

### Fixed
- README status line said `v0.3.1` while the latest release was `v0.4.0`.

## v0.4.0 — 2026-06-12 (enforced network allowlist)

The flagship safety milestone: `network.mode: allowlist` is now **enforced**,
not advisory. A real agent run no longer needs `network: open` — allowlist the
provider's API domains and the agent can reach those and nothing else.

### Added
- **Egress enforcement** for container runs. Two cooperating mechanisms:
  1. The agent container's only interface is a per-run `--internal` container
     network with no default route — direct egress and external DNS fail
     closed, so traffic that ignores the proxy cannot leave at all.
  2. An egress-proxy sidecar (dual-homed onto the external network) is the only
     path out and forwards only HTTP(S) whose target host matches
     `network.allow`. Each entry covers the domain and its subdomains; ports
     80/443 only; IP-literal targets always denied; targets resolving to
     private/loopback/link-local ranges refused (anti-SSRF backstop).
- `internal/netproxy` + `cmd/netproxy`: a stdlib-only filtering forward proxy
  (HTTP CONNECT + absolute-form HTTP) with structured ALLOW/DENY audit lines.
  Static linux builds (amd64/arm64) are **embedded into the agentbox binary**
  by `make build`/`make release` and run in the sidecar from the user's own
  runtime image — no extra image or binary to install.
- Every proxy ALLOW/DENY is harvested into the session: denials become policy
  events (`egress DENY connect host:443: ...`), allows become audit log lines.
- The proxy sidecar carries the same hardening as the agent container:
  non-root 10001, `--cap-drop ALL`, no-new-privileges, `--rm`, and mounts ONLY
  the proxy binary read-only — never the workspace.
- `agentbox doctor` reports `egress-proxy` embed status (allowlist
  enforceability) per architecture.
- `examples/agentbox.codex.yaml` now uses `network: allowlist` with
  `api.openai.com` — **no `--yes-unsafe` required** for a real agent run.

### Changed
- `EnforcedNetwork()` no longer collapses allowlist to deny for container
  isolation; `policy check` shows `allowlist (enforced: allowlist)` with an
  honest enforcement note. Local isolation still collapses (no container
  network to enforce with) and says so.
- Allowlist setup failure fails the run — enforcement never falls open. If the
  internal-network swap is somehow skipped, the container stays on the
  isolated `none` network (fail closed by construction).
- The network=deny honesty note now points to allowlist instead of open.

### Hardening (from adversarial review)
- Anti-SSRF backstop refuses an explicit reserved-range set (loopback, RFC1918,
  CGNAT 100.64/10, link-local/metadata, benchmarking, TEST-NETs, 240/4, ULA,
  NAT64 64:ff9b::/96, 6to4, Teredo, and IPv4-in-IPv6 forms) — not just
  `IsPrivate()`.
- DNS-tunnel exfiltration is closed **structurally**: the agent's resolver is
  sunk (`--dns 0.0.0.0`); the proxy resolves allowlisted names. Independent of
  the daemon's internal-network DNS behavior.
- Proxy egress leg uses a **dedicated per-run external network**, not the
  shared default bridge, so unrelated containers can't use it as an open relay.
- CONNECT tunnels have an idle deadline + a concurrency cap; the HTTP server has
  read/idle timeouts and a header-size cap; the sidecar runs read-only and
  PID-capped — bounding self-inflicted DoS of the egress path.
- Egress audit lines are classified by their verb field (not substring), and
  the dry-run plan only claims enforcement when the proxy is actually embedded.

### Limitations (honest)
- Container isolation only; `--runtime local` runs have no network enforcement.
- HTTP(S) only: SSH and raw TCP cannot leave the sandbox at all (fail closed).
- An allowlisted domain is a permitted channel by definition — keep the list
  minimal (an allowlisted DoH/DNS resolver would re-open a DNS channel).
- Verified end-to-end on Docker; podman uses identical CLI arguments but is
  less tested.

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
