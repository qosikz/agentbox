# Security Policy

Andbo is a security tool, so we take reports seriously — and we try to be
honest about what it does and does not enforce.

## Supported versions

The latest `v0.x` release and `main` receive security fixes. Andbo is
pre-1.0; defaults and APIs may still change.

## Reporting a vulnerability

**Please report privately — do not open a public issue.**

Use GitHub's private vulnerability reporting:
**[→ Report a vulnerability](https://github.com/qosikz/andbo/security/advisories/new)**
(the repository's **Security** tab → **Report a vulnerability**). This keeps the
report confidential while we work on a fix.

Please include:

- A description and the impact (what an attacker gains).
- Reproduction steps and the affected version/commit (`andbo version`).
- Your environment (OS, container engine + version).
- A suggested mitigation, if you have one.

We'll acknowledge the report, confirm the issue, and coordinate a fix and
disclosure with you.

## Scope — what's enforced vs. what isn't

Andbo reduces risk; it does not make an unsafe agent safe. The enforced
boundaries **and the honest limitations** are, in short:

- **Secrets** — the host environment is not forwarded; only allowlisted names
  reach the sandbox; values are redacted from logs, diffs, and sessions.
- **Network** — `deny` by default. `allowlist` is **enforced** for container
  runs via a per-run internal network + egress proxy (allowed domains on ports
  80/443 only; everything else, including DNS and IP-literal/reserved targets,
  fails closed). `network.ports` can permit additional ports — this widens
  egress to arbitrary TCP toward your allowlisted host:port (still domain- and
  anti-SSRF-checked), so it is an explicit, opt-in choice. `open` is an explicit
  unsafe opt-in.
- **Runtime** — non-root, `--cap-drop ALL`, never privileged, never the Docker
  socket. `--security-opt no-new-privileges` is applied on the **docker and
  podman** engines; the `apple` engine has no equivalent flag, so it drops all
  capabilities and runs non-root but does **not** block setuid privilege
  escalation inside the container.
- **Kubernetes (`andbo k8s render`)** — **rendering only, nothing enforced at
  runtime.** Andbo emits a hardened `Job` and a default-deny `NetworkPolicy` and
  never contacts a cluster, so every control in those manifests is enforced by
  *your* cluster, not by Andbo: the NetworkPolicy is inert unless the CNI
  implements it, it is additive and cannot subtract from another policy that
  also selects the pod, and it does nothing at all unless whoever applies the
  manifest applies it too. What Andbo *does* enforce is what it will emit —
  never privileged, no host namespaces, no `hostPath`, no service-account token,
  non-root only, bounded resources and deadline, and exactly one agent container
  (plus the one workspace init container `--workspace image:/path` asks for) —
  and what it refuses to emit:
  `network.mode` `allowlist`/`open`, `runtime.isolation: local`, host mounts,
  and any host environment variable (a manifest is plain text in etcd, so
  secrets never cross). `andbo k8s render` writes the manifest and stops.

**In scope (valuable):** breaking an *enforced* boundary — e.g. egress escape
from a container run, secret leakage into logs/sandbox, sandbox escape, or a way
for a harness to escalate past policy via the `exec`/MCP/skill surfaces. For the
Kubernetes path specifically: any input to `andbo k8s render` that makes it emit
a manifest weaker than the guarantees above, leak a host secret or host path into
the rendered YAML, or contact a cluster at all.

**Not a vulnerability (expected behavior):** that `--runtime local` or
`network: open` are unsafe — these are opt-in modes that require explicit
confirmation and are documented as such.
