# Security Policy

AgentBox is a security tool, so we take reports seriously — and we try to be
honest about what it does and does not enforce.

## Supported versions

The latest `v0.x` release and `main` receive security fixes. AgentBox is
pre-1.0; defaults and APIs may still change.

## Reporting a vulnerability

**Please report privately — do not open a public issue.**

Use GitHub's private vulnerability reporting:
**[→ Report a vulnerability](https://github.com/qosikz/agentbox/security/advisories/new)**
(the repository's **Security** tab → **Report a vulnerability**). This keeps the
report confidential while we work on a fix.

Please include:

- A description and the impact (what an attacker gains).
- Reproduction steps and the affected version/commit (`agentbox version`).
- Your environment (OS, container engine + version).
- A suggested mitigation, if you have one.

We'll acknowledge the report, confirm the issue, and coordinate a fix and
disclosure with you.

## Scope — what's enforced vs. what isn't

AgentBox reduces risk; it does not make an unsafe agent safe. The enforced
boundaries **and the honest limitations** are, in short:

- **Secrets** — the host environment is not forwarded; only allowlisted names
  reach the sandbox; values are redacted from logs, diffs, and sessions.
- **Network** — `deny` by default. `allowlist` is **enforced** for container
  runs via a per-run internal network + egress proxy (HTTP/HTTPS to allowed
  domains only; everything else, including DNS, fails closed). `open` is an
  explicit unsafe opt-in.
- **Runtime** — non-root, `--cap-drop ALL`, `--security-opt no-new-privileges`,
  never privileged, never the Docker socket.

**In scope (valuable):** breaking an *enforced* boundary — e.g. egress escape
from a container run, secret leakage into logs/sandbox, sandbox escape, or a way
for a harness to escalate past policy via the `exec`/MCP/skill surfaces.

**Not a vulnerability (expected behavior):** that `--runtime local` or
`network: open` are unsafe — these are opt-in modes that require explicit
confirmation and are documented as such.
